package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/paths"
	"tailscale.com/prober"
	"tailscale.com/tstime"
	"tailscale.com/tsweb"
)

// version is set with -ldflags "-X main.version=…". Empty falls back
// to the build info version, then "dev".
var version = ""

const (
	exitUsage         = 2
	httpHeaderTimeout = 5 * time.Second
)

type app struct {
	cfg      *Config
	env      netEnv
	log      *slog.Logger
	clock    tstime.Clock
	pub      *publisher
	stdout   io.Writer
	socket   string
	listen   string
	resolver *tsResolver
	presence *presence
	class    *classifier
	learner  *learner
	alerts   *alerter
	notify   *notifier
	metrics  prometheus.Registerer
	// collector identifies this node in notification payloads.
	collector CollectorInfo
	watch     func(context.Context, ipn.NotifyWatchOpt) (ipnWatch, error)
	probeMu   sync.Mutex
	probes    map[string]*sync.Mutex
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "check-config" {
		return runCheckConfig(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "notify-test" {
		return runNotifyTest(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("tail-watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFlag := fs.String("config", "", "config file path (overrides TAIL_WATCH_CONFIG)")
	listenFlag := fs.String("listen", "", "HTTP listen address (overrides collector.listen)")
	socketFlag := fs.String("socket", "", "tailscaled LocalAPI socket")
	onceFlag := fs.Bool("once", false, "run every check once and exit")
	versionFlag := fs.Bool("version", false, "print version and exit")
	logLevelFlag := fs.String("log-level", "", "log level: debug, info, warn, error (overrides TAIL_WATCH_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	if *versionFlag {
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	}
	log, err := newLogger(stderr, effectiveLogLevel(*logLevelFlag, os.Getenv("TAIL_WATCH_LOG_LEVEL")))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "log level: %v\n", err)
		return exitUsage
	}
	slog.SetDefault(log)
	path := resolveConfigPath(*configFlag, os.Getenv("TAIL_WATCH_CONFIG"))
	cfg, err := loadConfig(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	socket := socketPath(*socketFlag)
	a := &app{
		cfg:    cfg,
		env:    newKernelEnv(socket),
		log:    log,
		clock:  tstime.StdClock{},
		pub:    newPublisher(),
		stdout: stdout,
		socket: socket,
		listen: cfg.ListenAddr(*listenFlag),

		resolver: nil,
		presence: nil,
		class:    nil,
		learner:  nil,
		alerts:   nil,
		notify:   nil,
		metrics:  nil,
		watch:    nil,
		probeMu:  sync.Mutex{},
		probes:   nil,

		collector: CollectorInfo{Name: "", TailscaleIP: ""},
	}
	a.pub.add(debugConsumer{log: log})
	a.bindResolver()
	if preflightErr := a.preflight(context.Background()); preflightErr != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", preflightErr)
		return 1
	}
	if *onceFlag {
		return a.runOnce()
	}
	if notifyErr := a.buildNotifier(); notifyErr != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", notifyErr)
		return 1
	}
	return a.runServer()
}

// buildNotifier resolves every sink secret and opens the notification state.
// It runs before the prober so a bad secret or a corrupt snapshot stops startup.
func (a *app) buildNotifier() error {
	if err := a.cfg.resolveSecrets(); err != nil {
		return err
	}
	for _, sc := range a.cfg.Sinks {
		if sc.Type == sinkWebhook && !sc.Secret.isSet() {
			a.log.Warn("webhook sink is unsigned: no secret_env or secret_file", "sink", sc.Name)
		}
	}
	if len(a.cfg.Sinks) == 0 {
		return nil
	}
	n, err := newNotifier(notifierConfig{
		Clock: a.clock, Log: a.log, Sinks: a.cfg.Sinks, Collector: a.collector,
		StateDir: a.cfg.Collector.StateDir, Alerts: nil, Tags: nil, Registry: a.metricRegisterer(),
		NewSink: nil, Env: a.env, Jitter: nil, IntakeCap: 0,
	})
	if err != nil {
		return err
	}
	a.notify = n
	return nil
}

func runCheckConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFlag := fs.String("config", "", "config file path (overrides TAIL_WATCH_CONFIG)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	path := resolveConfigPath(*configFlag, os.Getenv("TAIL_WATCH_CONFIG"))
	cfg, err := loadConfig(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	for _, t := range cfg.Targets {
		_, _ = fmt.Fprintf(stdout, "%s interval=%s timeout=%s network=%s checks=%s\n",
			t.Name, t.Interval, t.Timeout, t.Network, formatChecks(t.Checks))
	}
	for _, s := range cfg.Sinks {
		_, _ = fmt.Fprintln(stdout, describeSink(s))
	}
	if cfg.Heartbeat != nil {
		_, _ = fmt.Fprintf(stdout, "heartbeat interval=%s\n", cfg.Heartbeat.Interval)
	}
	return 0
}

func (a *app) bindResolver() {
	a.resolver = newTSResolver(a.clock, a.log, func(ctx context.Context) (*ipnstate.Status, error) {
		return a.env.LocalClient().Status(ctx)
	})
	a.resolver.presence = a.presence
	for i := range a.cfg.Targets {
		a.bindChecks(&a.cfg.Targets[i])
	}
}

func (a *app) bindChecks(t *Target) {
	if a == nil || a.resolver == nil || t == nil {
		return
	}
	for j, c := range t.Checks {
		ts, ok := c.(tspingCheck)
		if !ok {
			continue
		}
		ts.resolver = a.resolver
		t.Checks[j] = ts
	}
}

func (a *app) watchBus(ctx context.Context, mask ipn.NotifyWatchOpt) (ipnWatch, error) {
	if a.watch != nil {
		return a.watch(ctx, mask)
	}
	return a.env.LocalClient().WatchIPNBus(ctx, mask)
}

func (a *app) metricRegisterer() prometheus.Registerer {
	if a != nil && a.metrics != nil {
		return a.metrics
	}
	return prometheus.DefaultRegisterer
}

func (a *app) probeLock(target, check string) *sync.Mutex {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	if a.probes == nil {
		a.probes = map[string]*sync.Mutex{}
	}
	key := target + "\x00" + check
	lk := a.probes[key]
	if lk == nil {
		lk = &sync.Mutex{}
		a.probes[key] = lk
	}
	return lk
}

func (a *app) schedule(ctx context.Context, p *prober.Prober) []func() error {
	reg := a.startCollectors(ctx, p)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	var out []func() error
	for _, stops := range reg.probes {
		out = append(out, stops...)
	}
	return out
}

func (a *app) startCollectors(ctx context.Context, p *prober.Prober) *runtimeRegistry {
	a.presence = newPresence(a.log, a.clock, a.watchBus)
	a.bindResolver()
	targets := make([]*Target, len(a.cfg.Targets))
	for i := range a.cfg.Targets {
		targets[i] = &a.cfg.Targets[i]
		a.bindChecks(targets[i])
	}
	reg := newRuntimeRegistry(ctx, a, proberStarter{p: p})
	al, err := newAlerter(alertConfig{
		Clock:      a.clock,
		Targets:    targets,
		Parents:    configuredParents(targets),
		Recheck:    reg.enqueueRecheck,
		Registerer: a.metricRegisterer(),
		QueueCap:   0,
	})
	if err != nil && a.log != nil {
		a.log.ErrorContext(ctx, "parent graph", "err", err)
	}
	a.alerts = al
	context.AfterFunc(ctx, al.stopDrain)
	al.addConsumer(transitionLog{log: a.log})
	if a.notify != nil {
		a.notify.attach(al, a.presence.TagsFor)
		al.addConsumer(a.notify)
	}
	reg.alert = al
	reg.seed(targets)
	a.class = newClassifier(a.presence, a.env, a.log, a.clock, a.metricRegisterer(), targets, reg)
	a.presence.setHandler(a.class.nodeForTarget)
	a.class.addConsumer(classifiedDebug{log: a.log})
	a.class.addConsumer(al)
	a.pub.add(a.class)
	a.learner = newLearner(a.class)
	if a.notify != nil {
		names := make(map[string]bool, len(targets))
		for _, t := range targets {
			names[t.Name] = true
		}
		a.notify.restoreOpen(names, al, a.class)
		a.notify.Start(ctx)
	}
	go a.presence.run(ctx)
	go a.learner.run(ctx)
	for _, t := range targets {
		if addErr := reg.AddTarget(t); addErr != nil && a.log != nil {
			a.log.ErrorContext(ctx, "add target", "target", t.Name, "err", addErr)
		}
	}
	return reg
}

func (a *app) runOnce() int {
	col := new(collectConsumer)

	a.pub.add(col)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	p := prober.New().WithSpread(true).WithMetricNamespace("tailwatch").WithOnce(true)
	probes := a.schedule(ctx, p)
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		for _, stop := range probes {
			_ = stop()
		}
		return 0
	case <-done:
	}
	failed := false
	for _, res := range col.snapshot() {
		_, _ = fmt.Fprintln(a.stdout, formatResult(res))
		if res.Err != nil {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func (a *app) runServer() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	p := prober.New().WithSpread(true).WithMetricNamespace("tailwatch")
	probes := a.schedule(ctx, p)
	if hb := a.cfg.Heartbeat; hb != nil {
		beat := newHeartbeat(*hb, a.clock, a.log, a.env, a.alerts.FirstRoundDone(), a.metricRegisterer())
		go beat.run(ctx)
	}
	mux := http.NewServeMux()
	mux.Handle("/", tsweb.StdHandler(
		p.StatusHandler(prober.WithTitle("tail-watch")),
		tsweb.HandlerOptions{Logf: a.logf,
			QuietLoggingIfSuccessful: false,
			Now:                      nil,
			StatusCodeCounters:       nil,
			StatusCodeCountersFull:   nil,
			BucketedStats:            nil,
			OnStart:                  nil,
			OnError:                  nil,
			OnCompletion:             nil,
		},
	))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	srv := &http.Server{
		Addr:              a.listen,
		Handler:           mux,
		ReadHeaderTimeout: httpHeaderTimeout,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	a.log.Info("listening", "addr", a.listen)
	select {
	case err := <-errCh:
		a.log.Error("http server", "err", err)
		_, _ = fmt.Fprintf(os.Stderr, "http server: %v\n", err)
		for _, stop := range probes {
			_ = stop()
		}
		return 1
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), maxTimeout(a.cfg)+5*time.Second)
	defer cancel()
	for _, stop := range probes {
		_ = stop()
	}
	if a.notify != nil {
		a.notify.Stop()
	}
	if err := srv.Shutdown(shutCtx); err != nil {
		a.log.Error("shutdown", "err", err)
	}
	return 0
}

func (a *app) logf(format string, args ...any) {
	a.log.Info(fmt.Sprintf(format, args...))
}

func (a *app) preflight(ctx context.Context) error {
	st, err := a.env.LocalClient().Status(ctx)
	if err != nil {
		return fmt.Errorf("tailscaled is not reachable at %s: %w", a.socket, err)
	}
	var prefs *ipn.Prefs
	if runtime.GOOS == "linux" && hasRemoteSubnet(a.cfg.Targets) {
		prefs, err = a.env.LocalClient().GetPrefs(ctx)
		if err != nil {
			return fmt.Errorf("tailscaled is not reachable at %s: %w", a.socket, err)
		}
	}
	res, err := evaluatePreflight(preflightInput{
		Status:  st,
		Prefs:   prefs,
		Targets: a.cfg.Targets,
		GOOS:    runtime.GOOS,
		Socket:  a.socket,
	})
	a.collector = collectorFrom(res)
	if res.Hostname != "" || len(res.IPs) > 0 || len(res.Tags) > 0 {
		a.log.InfoContext(ctx, "collector identity", "hostname", res.Hostname, "ips", res.IPs, "tags", res.Tags)
	}
	for _, w := range res.Warnings {
		a.log.WarnContext(ctx, w)
	}
	return err
}

type preflightInput struct {
	Status  *ipnstate.Status
	Prefs   *ipn.Prefs
	Targets []Target
	GOOS    string
	Socket  string
}

type preflightResult struct {
	Hostname string
	IPs      []string
	Tags     []string
	Warnings []string
}

func evaluatePreflight(in preflightInput) (preflightResult, error) {
	var res preflightResult
	if in.Status == nil {
		return res, fmt.Errorf("tailscaled is not reachable at %s", in.Socket)
	}
	if in.Status.BackendState != ipn.Running.String() {
		return res, fmt.Errorf("tailscaled state is %s; run tailscale up", in.Status.BackendState)
	}
	if in.Status.Self != nil {
		res.Hostname = in.Status.Self.HostName
		if in.Status.Self.Tags != nil {
			res.Tags = in.Status.Self.Tags.AsSlice()
		}
	}
	for _, ip := range in.Status.TailscaleIPs {
		res.IPs = append(res.IPs, ip.String())
	}
	if !slices.Contains(res.Tags, "tag:monitor") {
		res.Warnings = append(res.Warnings, "node is missing tag:monitor")
	}
	if in.Status.ExitNodeStatus != nil {
		res.Warnings = append(res.Warnings, "exit node is active; notifications will route through the exit node")
	}
	if !in.Status.TUN {
		return res, errors.New("tailscaled is using userspace networking; tail-watch requires kernel-mode tailscaled")
	}
	if in.GOOS == "linux" && hasRemoteSubnet(in.Targets) {
		if in.Prefs == nil || !in.Prefs.RouteAll {
			return res, errors.New("remote subnet targets require accepted routes; run tailscale set --accept-routes")
		}
	}
	return res, nil
}

func formatResult(res Result) string {
	name := ""
	if res.Target != nil {
		name = res.Target.Name
	}
	if res.Err != nil {
		return fmt.Sprintf("%s %s fail %s", name, res.Check, res.Err)
	}
	return fmt.Sprintf("%s %s ok %s", name, res.Check, res.Latency)
}

func maxTimeout(cfg *Config) time.Duration {
	var m time.Duration
	if cfg != nil {
		for _, t := range cfg.Targets {
			if t.Timeout > m {
				m = t.Timeout
			}
		}
	}
	if m <= 0 {
		m = defaultTimeout
	}
	return m
}

func socketPath(flag string) string {
	if flag != "" {
		return flag
	}
	return paths.DefaultTailscaledSocket()
}

func effectiveLogLevel(flag, env string) string {
	if flag != "" {
		return flag
	}
	if env != "" {
		return env
	}
	return "info"
}

func newLogger(w io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown log level %q", level)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl,
		AddSource:   false,
		ReplaceAttr: nil,
	})), nil
}

// checkStarter schedules one check and returns a function that stops it.
type checkStarter interface {
	Start(t *Target, chk check, run func(context.Context) error) (func() error, error)
}

// runtimeRegistry fans target changes out to alerting and check scheduling.
// Alert state is registered before the check is scheduled.
type runtimeRegistry struct {
	app    *app
	alert  targetRegistry
	start  checkStarter
	mu     sync.Mutex
	probes map[string][]func() error
	byName map[string]*Target
	known  map[string]bool
	pool   *recheckPool
}

func (r *runtimeRegistry) AddTarget(t *Target) error {
	if t == nil {
		return nil
	}
	seeded := r.takeSeed(t.Name)
	if !seeded && r.app != nil {
		r.app.bindChecks(t)
	}
	if r.alert != nil && !seeded {
		if err := r.alert.AddTarget(t); err != nil {
			return err
		}
	}
	r.mu.Lock()
	if r.byName == nil {
		r.byName = map[string]*Target{}
	}
	r.byName[t.Name] = t
	r.mu.Unlock()
	if r.start == nil {
		return nil
	}
	for _, chk := range t.Checks {
		target := t
		probe := chk
		stop, err := r.start.Start(target, probe, func(ctx context.Context) error {
			if r.app == nil {
				return nil
			}
			return r.app.execute(ctx, target, probe).Err
		})
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.probes[target.Name] = append(r.probes[target.Name], stop)
		r.mu.Unlock()
	}
	return nil
}

func (r *runtimeRegistry) RemoveTarget(name string) error {
	if r.alert != nil {
		if err := r.alert.RemoveTarget(name); err != nil {
			return err
		}
		if d, ok := r.alert.(interface{ DiscardPendingRestore(string) }); ok {
			d.DiscardPendingRestore(name)
		}
	}
	r.mu.Lock()
	stops := r.probes[name]
	delete(r.probes, name)
	delete(r.byName, name)
	delete(r.known, name)
	r.mu.Unlock()
	var err error
	for _, stop := range stops {
		err = errors.Join(err, stop())
	}
	return err
}

func (r *runtimeRegistry) IsDown(name string) bool {
	if r.alert == nil {
		return false
	}
	return r.alert.IsDown(name)
}

func (r *runtimeRegistry) UpdateParents(parents map[string]string) error {
	if r.alert == nil {
		return nil
	}
	return r.alert.UpdateParents(parents)
}

type proberStarter struct {
	p *prober.Prober
}

func (s proberStarter) Start(t *Target, chk check, run func(context.Context) error) (func() error, error) {
	if s.p == nil || t == nil {
		return func() error { return nil }, nil
	}
	pr := s.p.Run(t.Name+"/"+chk.String(), t.Interval, prober.Labels{
		labelTarget: t.Name,
		"check":     chk.String(),
		"site":      t.Site,
	}, prober.ProbeClass{
		Class:   chk.Kind(),
		Timeout: t.Timeout,
		Probe:   run,

		Labels:      nil,
		Concurrency: 0,
		Metrics:     nil,
	})
	return pr.Close, nil
}

type classifiedDebug struct {
	log *slog.Logger
}

func (d classifiedDebug) ObserveClassified(cl Classified) {
	if d.log == nil {
		return
	}
	name := ""
	if cl.Target != nil {
		name = cl.Target.Name
	}
	d.log.Debug(
		"classified",
		"target", name,
		"check", cl.Check,
		"layer", string(cl.Layer),
		"parent", cl.Parent,
		"path", cl.Path.Kind,
	)
}

func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}

// collectorFrom picks the payload identity: the node's hostname and its first
// Tailscale IPv4 address, or IPv6 when it has none.
func collectorFrom(res preflightResult) CollectorInfo {
	col := CollectorInfo{Name: res.Hostname, TailscaleIP: ""}
	for _, ip := range res.IPs {
		if addr, err := netip.ParseAddr(ip); err == nil && addr.Is4() {
			col.TailscaleIP = ip
			return col
		}
	}
	if len(res.IPs) > 0 {
		col.TailscaleIP = res.IPs[0]
	}
	return col
}

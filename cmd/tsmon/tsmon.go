package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

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
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "check-config" {
		return runCheckConfig(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("tsmon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFlag := fs.String("config", "", "config file path (overrides TSMON_CONFIG)")
	listenFlag := fs.String("listen", "", "HTTP listen address (overrides collector.listen)")
	socketFlag := fs.String("socket", "", "tailscaled LocalAPI socket")
	onceFlag := fs.Bool("once", false, "run every check once and exit")
	versionFlag := fs.Bool("version", false, "print version and exit")
	logLevelFlag := fs.String("log-level", "", "log level: debug, info, warn, error (overrides TSMON_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *versionFlag {
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	}
	log, err := newLogger(stderr, effectiveLogLevel(*logLevelFlag, os.Getenv("TSMON_LOG_LEVEL")))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "log level: %v\n", err)
		return 2
	}
	slog.SetDefault(log)
	path := resolveConfigPath(*configFlag, os.Getenv("TSMON_CONFIG"))
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
		pub:    newPublisher(1),
		stdout: stdout,
		socket: socket,
		listen: cfg.ListenAddr(*listenFlag),
	}
	a.pub.add(debugConsumer{log: log})
	a.bindResolver()
	if err := a.preflight(context.Background()); err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if *onceFlag {
		return a.runOnce()
	}
	return a.runServer()
}

func runCheckConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFlag := fs.String("config", "", "config file path (overrides TSMON_CONFIG)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	path := resolveConfigPath(*configFlag, os.Getenv("TSMON_CONFIG"))
	cfg, err := loadConfig(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	for _, t := range cfg.Targets {
		_, _ = fmt.Fprintf(stdout, "%s interval=%s timeout=%s network=%s checks=%s\n",
			t.Name, t.Interval, t.Timeout, t.Network, formatChecks(t.Checks))
	}
	return 0
}

func (a *app) bindResolver() {
	a.resolver = newTSResolver(a.clock, a.log, func(ctx context.Context) (*ipnstate.Status, error) {
		return a.env.LocalClient().Status(ctx)
	})
	for i := range a.cfg.Targets {
		t := &a.cfg.Targets[i]
		for j, c := range t.Checks {
			ts, ok := c.(tspingCheck)
			if !ok {
				continue
			}
			ts.resolver = a.resolver
			t.Checks[j] = ts
		}
	}
}

func (a *app) schedule(p *prober.Prober) []*prober.Probe {
	var out []*prober.Probe
	for i := range a.cfg.Targets {
		t := &a.cfg.Targets[i]
		for _, chk := range t.Checks {
			target := t
			check := chk
			pr := p.Run(t.Name+"/"+chk.String(), t.Interval, prober.Labels{
				"target": t.Name,
				"check":  chk.String(),
				"site":   t.Site,
			}, prober.ProbeClass{
				Class:   chk.Kind(),
				Timeout: t.Timeout,
				Probe: func(ctx context.Context) error {
					return a.execute(ctx, target, check).Err
				},
			})
			out = append(out, pr)
		}
	}
	return out
}

func (a *app) runOnce() int {
	col := &collectConsumer{}
	a.pub.add(col)
	p := prober.New().WithSpread(true).WithMetricNamespace("tsmon").WithOnce(true)
	probes := a.schedule(p)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		for _, pr := range probes {
			_ = pr.Close()
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
	p := prober.New().WithSpread(true).WithMetricNamespace("tsmon")
	probes := a.schedule(p)
	mux := http.NewServeMux()
	mux.Handle("/", tsweb.StdHandler(p.StatusHandler(prober.WithTitle("tsmon")), tsweb.HandlerOptions{Logf: a.logf}))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	srv := &http.Server{
		Addr:              a.listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	a.log.Info("listening", "addr", a.listen)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		a.log.Error("http server", "err", err)
		_, _ = fmt.Fprintf(os.Stderr, "http server: %v\n", err)
		for _, pr := range probes {
			_ = pr.Close()
		}
		return 1
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), maxTimeout(a.cfg)+5*time.Second)
	defer cancel()
	for _, pr := range probes {
		_ = pr.Close()
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
	if res.Hostname != "" || len(res.IPs) > 0 || len(res.Tags) > 0 {
		a.log.Info("collector identity", "hostname", res.Hostname, "ips", res.IPs, "tags", res.Tags)
	}
	for _, w := range res.Warnings {
		a.log.Warn(w)
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
		return res, errors.New("tailscaled is using userspace networking; tsmon requires kernel-mode tailscaled")
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
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
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

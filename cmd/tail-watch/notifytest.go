package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"tailscale.com/tstime"
)

const (
	defaultNotifyTimeout = 30 * time.Second
	identityTimeout      = 3 * time.Second
)

// notifyTestDeps are the seams of notify-test. Nil fields use the real thing.
type notifyTestDeps struct {
	identity func(ctx context.Context) (CollectorInfo, error)
	env      netEnv
	clock    tstime.Clock
}

func runNotifyTest(args []string, stdout, stderr io.Writer) int {
	return runNotifyTestWith(args, stdout, stderr, notifyTestDeps{identity: nil, env: nil, clock: nil})
}

// runNotifyTestWith sends one test event through each selected sink's real
// signing, rendering and retry code. It bypasses routing filters, queues and
// state_dir, and works when tailscaled is unreachable.
func runNotifyTestWith(args []string, stdout, stderr io.Writer, deps notifyTestDeps) int {
	fs := flag.NewFlagSet("notify-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sinkFlag := fs.String("sink", "", "send only to the sink with this name (default: every sink)")
	configFlag := fs.String("config", "", "config file path (overrides TAIL_WATCH_CONFIG)")
	socketFlag := fs.String("socket", "", "tailscaled LocalAPI socket")
	timeoutFlag := fs.Duration("timeout", defaultNotifyTimeout, "give up on a sink after this long")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	cfg, err := loadConfig(resolveConfigPath(*configFlag, os.Getenv("TAIL_WATCH_CONFIG")))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	selected, err := selectSinks(cfg.Sinks, *sinkFlag)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	if deps.clock == nil {
		deps.clock = tstime.StdClock{}
	}
	if deps.env == nil {
		deps.env = newKernelEnv(socketPath(*socketFlag))
	}
	if deps.identity == nil {
		deps.identity = func(ctx context.Context) (CollectorInfo, error) { return localIdentity(ctx, deps.env) }
	}
	col := collectorOrHostname(deps.identity)
	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()
	id, err := newID(prefixEvent, deps.clock.Now(), nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	ev := newTestEvent(id, col, deps.clock.Now())
	lines := make([]string, len(selected))
	oks := make([]bool, len(selected))
	var wg sync.WaitGroup
	for i, sc := range selected {
		wg.Go(func() {
			lines[i], oks[i] = sendTest(ctx, sc, col, ev, deps)
		})
	}
	wg.Wait()
	code := 0
	for i, line := range lines {
		_, _ = fmt.Fprintln(stdout, line)
		if !oks[i] {
			code = 1
		}
	}
	return code
}

func selectSinks(all []SinkConfig, name string) ([]SinkConfig, error) {
	if len(all) == 0 {
		return nil, errors.New("no sinks configured")
	}
	if name == "" {
		return all, nil
	}
	names := make([]string, len(all))
	for i, sc := range all {
		if sc.Name == name {
			return []SinkConfig{sc}, nil
		}
		names[i] = sc.Name
	}
	return nil, fmt.Errorf("unknown sink %q; configured sinks: %s", name, strings.Join(names, ", "))
}

// collectorOrHostname asks tailscaled for the collector identity and falls back
// to the OS hostname, without an address, when it is unreachable.
func collectorOrHostname(identity func(context.Context) (CollectorInfo, error)) CollectorInfo {
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()
	if col, err := identity(ctx); err == nil && col.Name != "" {
		return col
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return CollectorInfo{Name: host, TailscaleIP: ""}
}

func localIdentity(ctx context.Context, env netEnv) (CollectorInfo, error) {
	st, err := env.LocalClient().Status(ctx)
	if err != nil {
		return CollectorInfo{}, err
	}
	return identityOf(st.Self.HostName, st.TailscaleIPs), nil
}

func identityOf(host string, ips []netip.Addr) CollectorInfo {
	col := CollectorInfo{Name: host, TailscaleIP: ""}
	for _, ip := range ips {
		if ip.Is4() {
			col.TailscaleIP = ip.String()
			return col
		}
	}
	if len(ips) > 0 {
		col.TailscaleIP = ips[0].String()
	}
	return col
}

// sendTest delivers ev through one real sink and returns its report line.
func sendTest(ctx context.Context, sc SinkConfig, col CollectorInfo, ev Event, deps notifyTestDeps) (string, bool) {
	prefix := fmt.Sprintf("%s %s", sc.Name, sc.Type)
	if err := sc.checkSecrets(); err != nil {
		return fmt.Sprintf("%s error: %v", prefix, err), false
	}
	s, err := newSinkWithEnv(sc, col, deps.env)
	if err != nil {
		return fmt.Sprintf("%s error: %v", prefix, err), false
	}
	id := ev.ID
	if sc.Type == sinkSlack || sc.Batch {
		if id, err = newID(prefixBatch, deps.clock.Now(), nil); err != nil {
			return fmt.Sprintf("%s error: %v", prefix, err), false
		}
	}
	body, err := s.Render([]Event{ev}, id)
	if err != nil {
		return fmt.Sprintf("%s error: %v", prefix, err), false
	}
	loop := retryLoop{clock: deps.clock, jitter: func() float64 { return jitterFloor }, sink: s, last: time.Time{}}
	out := loop.run(ctx, body, id, nil, nil)
	switch out.Kind {
	case outcomeDelivered:
		return fmt.Sprintf("%s %d", prefix, out.Result.Status), true
	case outcomeCancelled:
		return fmt.Sprintf("%s error: timed out after retries (last: %s)", prefix, out.Result), false
	case outcomePermanent, outcomeExpired:
	}
	return fmt.Sprintf("%s %s", prefix, out.Result), false
}

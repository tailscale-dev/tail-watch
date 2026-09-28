package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"tailscale.com/net/tsaddr"
	"tailscale.com/tailcfg"
)

const (
	defaultListen     = "127.0.0.1:9109"
	defaultConfigPath = "/etc/tail-watch/tail-watch.yaml"

	defaultInterval   = 30 * time.Second
	defaultTimeout    = 5 * time.Second
	defaultFail       = 3
	defaultRecover    = 2
	defaultFlapLimit  = 4
	defaultFlapWindow = 15 * time.Minute
)

// Severity is the paging class of a target.
type Severity string

const (
	SeverityPage Severity = "page"
	SeverityWarn Severity = "warn"
)

// Network is the path a target's checks are meant to take.
type Network string

const (
	NetworkTailnet Network = "tailnet"
	NetworkLocal   Network = "local"
)

// Config is a resolved collector configuration.
type Config struct {
	Collector CollectorConfig
	Targets   []Target
}

// CollectorConfig is the process-level section of a config file.
type CollectorConfig struct {
	Listen   string
	StateDir string
}

// Target is one monitored device with its checks and resolved timings.
type Target struct {
	Name             string
	Addr             string
	Site             string
	Severity         Severity
	Parent           string
	InsecureTLS      bool
	Interval         time.Duration
	Timeout          time.Duration
	FailThreshold    int
	RecoverThreshold int
	FlapLimit        int
	FlapWindow       time.Duration
	Network          Network
	Checks           []check
	// Implicit is set for a reachability target created by dependency learning.
	Implicit bool
	// StableID is the tailnet stable node ID retained for an implicit target.
	StableID tailcfg.StableNodeID
	// NodeID is the tailnet node ID of an implicit target.
	NodeID tailcfg.NodeID
}

// ListenAddr returns the HTTP listen address. A non-empty flag wins,
// then collector.listen, then 127.0.0.1:9109.
func (c *Config) ListenAddr(flag string) string {
	if flag != "" {
		return flag
	}
	if c != nil && c.Collector.Listen != "" {
		return c.Collector.Listen
	}
	return defaultListen
}

func resolveConfigPath(flag, env string) string {
	if flag != "" {
		return flag
	}
	if env != "" {
		return env
	}
	return defaultConfigPath
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // The local operator explicitly selects the config path.
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	cfg, err := parseConfig(b)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

type rawConfig struct {
	Collector rawCollector `yaml:"collector"`
	Defaults  rawDefaults  `yaml:"defaults"`
	Targets   []rawTarget  `yaml:"targets"`
}

type rawCollector struct {
	Listen   string `yaml:"listen"`
	StateDir string `yaml:"state_dir"`
}

type rawDefaults struct {
	Interval         yamlDuration `yaml:"interval"`
	Timeout          yamlDuration `yaml:"timeout"`
	FailThreshold    *int         `yaml:"fail_threshold"`
	RecoverThreshold *int         `yaml:"recover_threshold"`
	FlapLimit        *int         `yaml:"flap_limit"`
	FlapWindow       yamlDuration `yaml:"flap_window"`
}

type rawTarget struct {
	Name             string       `yaml:"name"`
	Addr             string       `yaml:"addr"`
	Site             string       `yaml:"site"`
	Severity         string       `yaml:"severity"`
	Parent           string       `yaml:"parent"`
	InsecureTLS      bool         `yaml:"insecure_tls"`
	Interval         yamlDuration `yaml:"interval"`
	Timeout          yamlDuration `yaml:"timeout"`
	FailThreshold    *int         `yaml:"fail_threshold"`
	RecoverThreshold *int         `yaml:"recover_threshold"`
	FlapLimit        *int         `yaml:"flap_limit"`
	FlapWindow       yamlDuration `yaml:"flap_window"`
	Network          string       `yaml:"network"`
	Checks           []string     `yaml:"checks"`
}

// yamlDuration parses Go duration strings. A decode problem is stored
// so one bad value does not hide the rest of the file.
type yamlDuration struct {
	set bool
	d   time.Duration
	err error
}

//nolint:nilerr,unparam // YAML requires an error return; failures are collected in d.err for aggregate validation.
func (d *yamlDuration) UnmarshalYAML(value *yaml.Node) error {
	if value.Tag == "!!null" {
		return nil
	}
	d.set = true
	var s string
	if err := value.Decode(&s); err != nil {
		d.err = errors.New("invalid duration")
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		d.err = fmt.Errorf("invalid duration %q", s)
		return nil
	}
	d.d = parsed
	return nil
}

func parseConfig(data []byte) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("config: empty file")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var raw rawConfig
	decErr := dec.Decode(&raw)
	if decErr != nil && errors.Is(decErr, io.EOF) {
		return nil, errors.New("config: empty file")
	}
	cfg, valErrs := resolveConfig(raw)
	var errs []error
	if decErr != nil {
		errs = append(errs, decErr)
	}
	errs = append(errs, valErrs...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

type timing struct {
	Interval         time.Duration
	Timeout          time.Duration
	FailThreshold    int
	RecoverThreshold int
	FlapLimit        int
	FlapWindow       time.Duration
}

func builtinTiming() timing {
	return timing{
		Interval:         defaultInterval,
		Timeout:          defaultTimeout,
		FailThreshold:    defaultFail,
		RecoverThreshold: defaultRecover,
		FlapLimit:        defaultFlapLimit,
		FlapWindow:       defaultFlapWindow,
	}
}

func resolveConfig(raw rawConfig) (*Config, []error) {
	var errs []error
	base := builtinTiming()
	applyDur(&errs, "defaults.interval", raw.Defaults.Interval, &base.Interval)
	applyDur(&errs, "defaults.timeout", raw.Defaults.Timeout, &base.Timeout)
	applyDur(&errs, "defaults.flap_window", raw.Defaults.FlapWindow, &base.FlapWindow)
	applyInt(raw.Defaults.FailThreshold, &base.FailThreshold)
	applyInt(raw.Defaults.RecoverThreshold, &base.RecoverThreshold)
	applyInt(raw.Defaults.FlapLimit, &base.FlapLimit)

	cfg := &Config{
		Collector: CollectorConfig{
			Listen:   raw.Collector.Listen,
			StateDir: raw.Collector.StateDir,
		},
		Targets: make([]Target, len(raw.Targets)),
	}
	seen := map[string]int{}
	checksOK := make([]bool, len(raw.Targets))
	for i, rt := range raw.Targets {
		t, ok := resolveTarget(&errs, i, rt, base, seen)
		cfg.Targets[i] = t
		checksOK[i] = ok
	}
	byName := map[string]int{}
	for i, t := range cfg.Targets {
		if t.Name == "" {
			continue
		}
		if _, exists := byName[t.Name]; !exists {
			byName[t.Name] = i
		}
	}
	for i, t := range cfg.Targets {
		if t.Parent == "" {
			continue
		}
		pi, ok := byName[t.Parent]
		if !ok || t.Parent == "" {
			errs = append(
				errs,
				fmt.Errorf(
					"targets[%d].parent: target %s: parent %q does not name a target",
					i,
					targetLabel(t),
					t.Parent,
				),
			)
			continue
		}
		if checksOK[pi] && !reachabilityOnly(cfg.Targets[pi]) {
			errs = append(
				errs,
				fmt.Errorf(
					"targets[%d].parent: target %s: parent %q must have exactly one icmp or tsping check",
					i,
					targetLabel(t),
					t.Parent,
				),
			)
		}
	}
	errs = append(errs, parentCycles(cfg.Targets, byName)...)
	return cfg, errs
}

func resolveTarget(errs *[]error, i int, rt rawTarget, base timing, seen map[string]int) (Target, bool) {
	t := Target{
		Name:             rt.Name,
		Addr:             rt.Addr,
		Site:             rt.Site,
		Parent:           rt.Parent,
		InsecureTLS:      rt.InsecureTLS,
		Interval:         base.Interval,
		Timeout:          base.Timeout,
		FailThreshold:    base.FailThreshold,
		RecoverThreshold: base.RecoverThreshold,
		FlapLimit:        base.FlapLimit,
		FlapWindow:       base.FlapWindow,
		Severity:         SeverityPage,
		Network:          NetworkTailnet,

		Checks:   nil,
		Implicit: false,
		StableID: "",
		NodeID:   0,
	}
	loc := fmt.Sprintf("targets[%d]", i)
	if rt.Name == "" {
		*errs = append(*errs, fmt.Errorf("%s.name: name is required", loc))
	} else if !targetNameRE.MatchString(rt.Name) {
		*errs = append(*errs, fmt.Errorf("%s.name: target name %q must match [A-Za-z0-9._-]", loc, rt.Name))
	} else if prev, ok := seen[rt.Name]; ok {
		*errs = append(*errs, fmt.Errorf("%s.name: duplicate target name %q (also targets[%d])", loc, rt.Name, prev))
	} else {
		seen[rt.Name] = i
	}
	if rt.Addr == "" {
		*errs = append(*errs, fmt.Errorf("%s.addr: target %s: addr is required", loc, targetLabel(t)))
	}
	switch rt.Severity {
	case "", string(SeverityPage):
		t.Severity = SeverityPage
	case string(SeverityWarn):
		t.Severity = SeverityWarn
	default:
		*errs = append(
			*errs,
			fmt.Errorf("%s.severity: target %s: severity %q must be page or warn", loc, targetLabel(t), rt.Severity),
		)
	}
	switch rt.Network {
	case "", string(NetworkTailnet):
		t.Network = NetworkTailnet
	case string(NetworkLocal):
		t.Network = NetworkLocal
	default:
		*errs = append(
			*errs,
			fmt.Errorf("%s.network: target %s: network %q must be tailnet or local", loc, targetLabel(t), rt.Network),
		)
	}

	applyDur(errs, loc+".interval", rt.Interval, &t.Interval)
	applyDur(errs, loc+".timeout", rt.Timeout, &t.Timeout)
	applyDur(errs, loc+".flap_window", rt.FlapWindow, &t.FlapWindow)
	applyInt(rt.FailThreshold, &t.FailThreshold)
	applyInt(rt.RecoverThreshold, &t.RecoverThreshold)
	applyInt(rt.FlapLimit, &t.FlapLimit)

	if t.Interval <= 0 {
		*errs = append(*errs, fmt.Errorf("%s.interval: target %s: interval must be positive", loc, targetLabel(t)))
	}
	if t.Timeout <= 0 || (t.Interval > 0 && t.Timeout >= t.Interval) {
		*errs = append(
			*errs,
			fmt.Errorf(
				"%s.timeout: target %s: timeout must be greater than 0 and less than interval %s",
				loc,
				targetLabel(t),
				t.Interval,
			),
		)
	}
	if t.FlapWindow <= 0 {
		*errs = append(
			*errs,
			fmt.Errorf("%s.flap_window: target %s: flap_window must be positive", loc, targetLabel(t)),
		)
	}
	if t.FailThreshold < 1 {
		*errs = append(*errs, fmt.Errorf("%s.fail_threshold: target %s: must be >= 1", loc, targetLabel(t)))
	}
	if t.RecoverThreshold < 1 {
		*errs = append(*errs, fmt.Errorf("%s.recover_threshold: target %s: must be >= 1", loc, targetLabel(t)))
	}
	if t.FlapLimit < 1 {
		*errs = append(*errs, fmt.Errorf("%s.flap_limit: target %s: must be >= 1", loc, targetLabel(t)))
	}

	ok := resolveChecks(errs, loc, rt.Checks, &t)
	return t, ok
}

func resolveChecks(errs *[]error, loc string, rawChecks []string, t *Target) bool {
	if len(rawChecks) == 0 {
		*errs = append(*errs, fmt.Errorf("%s.checks: target %s: checks must not be empty", loc, targetLabel(*t)))
		return false
	}
	ok := true
	seenChk := map[string]struct{}{}
	for j, rawChk := range rawChecks {
		c, err := parseCheck(rawChk)
		if err != nil {
			ok = false
			*errs = append(
				*errs,
				fmt.Errorf("%s.checks[%d]: target %s: check %q: %w", loc, j, targetLabel(*t), rawChk, err),
			)
			continue
		}
		if _, dup := seenChk[c.String()]; dup {
			*errs = append(
				*errs,
				fmt.Errorf("%s.checks[%d]: target %s: duplicate check %q", loc, j, targetLabel(*t), c.String()),
			)
		}
		seenChk[c.String()] = struct{}{}
		if t.Network == NetworkLocal && c.Kind() == checkTSPing {
			*errs = append(
				*errs,
				fmt.Errorf("%s.checks[%d]: target %s: tsping is not valid for network local", loc, j, targetLabel(*t)),
			)
		}
		if h, isHTTP := c.(httpCheck); isHTTP && t.Addr != "" && !sameHost(t.Addr, h.host) {
			*errs = append(
				*errs,
				fmt.Errorf(
					"%s.checks[%d]: target %s: http URL host %q does not match addr %q",
					loc,
					j,
					targetLabel(*t),
					h.host,
					t.Addr,
				),
			)
		}
		t.Checks = append(t.Checks, c)
	}
	return ok
}

func applyDur(errs *[]error, loc string, v yamlDuration, dst *time.Duration) {
	if v.err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", loc, v.err))
		return
	}
	if v.set {
		*dst = v.d
	}
}

func applyInt(v *int, dst *int) {
	if v != nil {
		*dst = *v
	}
}

func reachabilityOnly(t Target) bool {
	if len(t.Checks) != 1 {
		return false
	}
	k := t.Checks[0].Kind()
	return k == checkICMP || k == checkTSPing
}

func parentCycles(targets []Target, byName map[string]int) []error {
	const (
		unseen = 0
		active = 1
		done   = 2
	)
	state := map[string]int{}
	var stack []string
	var errs []error
	var dfs func(name string)
	dfs = func(name string) {
		switch state[name] {
		case done:
			return
		case active:
			i := 0
			for i < len(stack) && stack[i] != name {
				i++
			}
			cyc := append([]string{}, stack[i:]...)
			cyc = append(cyc, name)
			errs = append(errs, fmt.Errorf("targets: parent cycle: %s", strings.Join(cyc, " -> ")))
			return
		}
		state[name] = active
		stack = append(stack, name)
		if i, ok := byName[name]; ok {
			parent := targets[i].Parent
			if parent != "" {
				if _, known := byName[parent]; known {
					dfs(parent)
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
	}
	for _, t := range targets {
		if t.Name != "" {
			dfs(t.Name)
		}
	}
	return errs
}

func targetLabel(t Target) string {
	if t.Name != "" {
		return t.Name
	}
	return "(unnamed)"
}

var targetNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func sameHost(addr, host string) bool {
	addr = strings.TrimSuffix(strings.TrimSpace(addr), ".")
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if strings.EqualFold(addr, host) {
		return true
	}
	a, aerr := netip.ParseAddr(addr)
	h, herr := netip.ParseAddr(host)
	if aerr == nil && herr == nil {
		return a.Unmap() == h.Unmap()
	}
	return false
}

func formatChecks(checks []check) string {
	parts := make([]string, len(checks))
	for i, c := range checks {
		parts[i] = c.String()
	}
	return strings.Join(parts, ",")
}

// remoteSubnet reports whether addr is a literal IP that a tailnet
// check can reach only if the host has accepted a subnet route.
// Loopback is excluded so local probe tests are not treated as remote
// subnets. DNS names are resolved later, not here.
func remoteSubnet(addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return !tsaddr.IsTailscaleIP(ip)
}

func hasRemoteSubnet(targets []Target) bool {
	for _, t := range targets {
		if t.Network != NetworkTailnet {
			continue
		}
		if remoteSubnet(t.Addr) {
			return true
		}
	}
	return false
}

package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	SeverityInfo Severity = "info"

	rankInfo = 0
	rankWarn = 1
	rankPage = 2

	sinkWebhook = "webhook"
	sinkSlack   = "slack"
	sinkTeams   = "teams"

	queueMemory = "memory"
	queueDisk   = "disk"

	defaultSinkMaxAge     = time.Hour
	defaultQueueSize      = 1000
	defaultGroupWait      = 10 * time.Second
	defaultMaxBatch       = 100
	defaultHeartbeatEvery = time.Minute

	sinkIncidents = "incidents"
	sinkIntake    = "intake"
	sinkState     = "state"
)

// EventType is the type field of a notification payload.
type EventType string

const (
	EventDown      EventType = "target.down"
	EventRecovered EventType = "target.recovered"
	EventFlapping  EventType = "target.flapping"
	EventDegraded  EventType = "target.degraded"
	EventStarted   EventType = "collector.started"
	EventTest      EventType = "test"
)

func knownEventTypes() []EventType {
	return []EventType{EventDown, EventRecovered, EventFlapping, EventDegraded, EventStarted, EventTest}
}

// rank orders severities: info < warn < page.
func (s Severity) rank() int {
	switch s {
	case SeverityPage:
		return rankPage
	case SeverityWarn:
		return rankWarn
	case SeverityInfo:
		return rankInfo
	default:
		return rankInfo
	}
}

// secretRef names where a secret value lives. Exactly one of Env, File and
// Setec is set. src is the setec store bound at startup; env and file refs
// ignore it.
type secretRef struct {
	Env   string
	File  string
	Setec string
	src   secretSource
}

func (r secretRef) isSet() bool { return r.Env != "" || r.File != "" || r.Setec != "" }

// setSourcesNote names the keys that are set when more than one source is
// given, as ": url_env and url_file are set". It is empty for zero or one.
func setSourcesNote(prefix, env, file, setec string) string {
	var keys []string
	for _, k := range []struct{ suffix, val string }{{"env", env}, {"file", file}, {"setec", setec}} {
		if k.val != "" {
			keys = append(keys, prefix+"_"+k.suffix)
		}
	}
	if len(keys) <= 1 {
		return ""
	}
	return ": " + strings.Join(keys, " and ") + " are set"
}

func secretSourceCount(env, file, setec string) int {
	n := 0
	if env != "" {
		n++
	}
	if file != "" {
		n++
	}
	if setec != "" {
		n++
	}
	return n
}

// resolve reads the current secret value. File and setec values are
// whitespace trimmed. A setec ref with no bound store is an error.
func (r secretRef) resolve() (string, error) {
	switch {
	case r.Env != "":
		v := os.Getenv(r.Env)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is unset or empty", r.Env)
		}
		return v, nil
	case r.File != "":
		b, err := os.ReadFile(r.File)
		if err != nil {
			return "", fmt.Errorf("secret file: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("secret file %s is empty", r.File)
		}
		return v, nil
	case r.Setec != "":
		if r.src == nil {
			return "", errors.New("setec is not configured")
		}
		return r.src.get(r.Setec)
	default:
		return "", errors.New("no secret source configured")
	}
}

// redactURL keeps only the scheme and host of a URL.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<redacted>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// MatchConfig narrows which targets a sink hears about.
type MatchConfig struct {
	Sites   []string
	Targets []string
	Tags    []string
}

// SinkConfig is one resolved notification sink.
type SinkConfig struct {
	Name   string
	Type   string
	URL    secretRef
	Secret secretRef
	// Headers maps a header name to the secret holding its value.
	Headers       map[string]secretRef
	MinSeverity   Severity
	Events        []EventType
	Match         MatchConfig
	MaxAge        time.Duration
	Queue         string
	QueueSize     int
	Batch         bool
	GroupWait     time.Duration
	MaxBatch      int
	MentionOnPage string
	StatusURL     string
}

// HeartbeatConfig is the dead-man's-switch ping.
type HeartbeatConfig struct {
	URL      secretRef
	Interval time.Duration
}

type rawMatch struct {
	Sites   []string `yaml:"sites"`
	Targets []string `yaml:"targets"`
	Tags    []string `yaml:"tags"`
}

type rawSink struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"`
	URLEnv        string            `yaml:"url_env"`
	URLFile       string            `yaml:"url_file"`
	URLSetec      string            `yaml:"url_setec"`
	SecretEnv     string            `yaml:"secret_env"`
	SecretFile    string            `yaml:"secret_file"`
	SecretSetec   string            `yaml:"secret_setec"`
	HeadersEnv    map[string]string `yaml:"headers_env"`
	HeadersSetec  map[string]string `yaml:"headers_setec"`
	MinSeverity   string            `yaml:"min_severity"`
	Events        []string          `yaml:"events"`
	Match         rawMatch          `yaml:"match"`
	MaxAge        yamlDuration      `yaml:"max_age"`
	Queue         string            `yaml:"queue"`
	QueueSize     *int              `yaml:"queue_size"`
	Batch         *bool             `yaml:"batch"`
	GroupWait     yamlDuration      `yaml:"group_wait"`
	MaxBatch      *int              `yaml:"max_batch"`
	MentionOnPage string            `yaml:"mention_on_page"`
	StatusURL     string            `yaml:"status_url"`
}

type rawHeartbeat struct {
	URLEnv   string       `yaml:"url_env"`
	URLFile  string       `yaml:"url_file"`
	URLSetec string       `yaml:"url_setec"`
	Interval yamlDuration `yaml:"interval"`
}

var sinkNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func resolveSinks(errs *[]error, raws []rawSink, stateDir string) []SinkConfig {
	out := make([]SinkConfig, len(raws))
	seen := map[string]int{}
	for i, rs := range raws {
		out[i] = resolveSink(errs, i, rs, stateDir, seen)
	}
	return out
}

func resolveSink(errs *[]error, i int, rs rawSink, stateDir string, seen map[string]int) SinkConfig {
	loc := fmt.Sprintf("sinks[%d]", i)
	label := rs.Name
	if label == "" {
		label = unnamedLabel
	}
	fail := func(key, format string, args ...any) {
		*errs = append(*errs, fmt.Errorf("%s.%s: sink %s: %s", loc, key, label, fmt.Sprintf(format, args...)))
	}
	s := SinkConfig{
		Name:          rs.Name,
		Type:          rs.Type,
		URL:           secretRef{Env: rs.URLEnv, File: rs.URLFile, Setec: rs.URLSetec, src: nil},
		Secret:        secretRef{Env: rs.SecretEnv, File: rs.SecretFile, Setec: rs.SecretSetec, src: nil},
		Headers:       nil,
		MinSeverity:   SeverityInfo,
		Events:        nil,
		Match:         MatchConfig{Sites: rs.Match.Sites, Targets: rs.Match.Targets, Tags: rs.Match.Tags},
		MaxAge:        defaultSinkMaxAge,
		Queue:         queueMemory,
		QueueSize:     defaultQueueSize,
		Batch:         false,
		GroupWait:     defaultGroupWait,
		MaxBatch:      defaultMaxBatch,
		MentionOnPage: rs.MentionOnPage,
		StatusURL:     rs.StatusURL,
	}
	switch {
	case rs.Name == "":
		fail("name", "name is required")
	case !sinkNameRE.MatchString(rs.Name):
		fail("name", "name %q must match [A-Za-z0-9._-]", rs.Name)
	case rs.Name == sinkIncidents || rs.Name == sinkIntake || rs.Name == sinkState:
		fail("name", "name %q is reserved", rs.Name)
	default:
		if prev, dup := seen[rs.Name]; dup {
			fail("name", "duplicate sink name %q (also sinks[%d])", rs.Name, prev)
		} else {
			seen[rs.Name] = i
		}
	}
	switch rs.Type {
	case sinkWebhook, sinkSlack, sinkTeams:
	default:
		fail("type", "type %q must be webhook, slack or teams", rs.Type)
	}
	if secretSourceCount(rs.URLEnv, rs.URLFile, rs.URLSetec) != 1 {
		fail("url", "exactly one of url_env, url_file or url_setec is required%s",
			setSourcesNote("url", rs.URLEnv, rs.URLFile, rs.URLSetec))
	}
	if secretSourceCount(rs.SecretEnv, rs.SecretFile, rs.SecretSetec) > 1 {
		fail("secret", "set only one of secret_env, secret_file or secret_setec%s",
			setSourcesNote("secret", rs.SecretEnv, rs.SecretFile, rs.SecretSetec))
	}
	s.Headers = resolveHeaders(fail, rs.HeadersEnv, rs.HeadersSetec)
	resolveSinkTypeKeys(fail, rs)
	resolveSinkFilters(fail, rs, &s)
	resolveSinkLimits(errs, fail, loc, rs, &s, stateDir)
	return s
}

func resolveSinkTypeKeys(fail func(key, format string, args ...any), rs rawSink) {
	switch rs.Type {
	case sinkSlack, sinkTeams:
		for _, k := range []struct {
			key string
			set bool
		}{
			{"batch", rs.Batch != nil},
			{"max_batch", rs.MaxBatch != nil},
			{"secret_env", rs.SecretEnv != ""},
			{"secret_file", rs.SecretFile != ""},
			{"secret_setec", rs.SecretSetec != ""},
			{"headers_env", len(rs.HeadersEnv) > 0},
			{"headers_setec", len(rs.HeadersSetec) > 0},
		} {
			if k.set {
				fail(k.key, "%s is not valid for a %s sink", k.key, rs.Type)
			}
		}
		if rs.Type == sinkTeams && rs.MentionOnPage != "" {
			fail("mention_on_page", "mention_on_page is not valid for a teams sink")
		}
	case sinkWebhook:
		if rs.MentionOnPage != "" {
			fail("mention_on_page", "mention_on_page is only valid for a slack sink")
		}
		if rs.StatusURL != "" {
			fail("status_url", "status_url is only valid for a slack sink")
		}
	}
}

// resolveHeaders turns headers_env and headers_setec into secret refs.
// A header name in both maps is an error. Empty names are errors.
func resolveHeaders(
	fail func(key, format string, args ...any),
	envMap, setecMap map[string]string,
) map[string]secretRef {
	if len(envMap) == 0 && len(setecMap) == 0 {
		return nil
	}
	out := make(map[string]secretRef, len(envMap)+len(setecMap))
	for name, envName := range envMap {
		switch {
		case name == "":
			fail("headers_env", "header name is empty")
		case envName == "":
			fail("headers_env", "header %s has an empty environment variable", name)
		default:
			out[name] = secretRef{Env: envName, File: "", Setec: "", src: nil}
		}
	}
	for name, secretName := range setecMap {
		if _, inEnv := envMap[name]; inEnv {
			fail("headers_setec", "header %s is set in both headers_env and headers_setec", name)
			continue
		}
		switch {
		case name == "":
			fail("headers_setec", "header name is empty")
		case secretName == "":
			fail("headers_setec", "header %s has an empty secret name", name)
		default:
			out[name] = secretRef{Env: "", File: "", Setec: secretName, src: nil}
		}
	}
	return out
}

func resolveSinkFilters(fail func(key, format string, args ...any), rs rawSink, s *SinkConfig) {
	switch Severity(rs.MinSeverity) {
	case "":
	case SeverityInfo, SeverityWarn, SeverityPage:
		s.MinSeverity = Severity(rs.MinSeverity)
	default:
		fail("min_severity", "min_severity %q must be info, warn or page", rs.MinSeverity)
	}
	for _, e := range rs.Events {
		if !slices.Contains(knownEventTypes(), EventType(e)) {
			fail("events", "unknown event type %q", e)
			continue
		}
		s.Events = append(s.Events, EventType(e))
	}
	for _, g := range rs.Match.Targets {
		if _, err := path.Match(g, "x"); err != nil {
			fail("match.targets", "invalid glob %q", g)
		}
	}
}

func resolveSinkLimits(
	errs *[]error,
	fail func(key, format string, args ...any),
	loc string,
	rs rawSink,
	s *SinkConfig,
	stateDir string,
) {
	switch rs.Queue {
	case "":
	case queueMemory:
	case queueDisk:
		s.Queue = queueDisk
		if stateDir == "" {
			fail("queue", "queue: disk requires collector.state_dir")
		}
	default:
		fail("queue", "queue %q must be memory or disk", rs.Queue)
	}
	applyDur(errs, loc+".max_age", rs.MaxAge, &s.MaxAge)
	applyDur(errs, loc+".group_wait", rs.GroupWait, &s.GroupWait)
	applyInt(rs.QueueSize, &s.QueueSize)
	applyInt(rs.MaxBatch, &s.MaxBatch)
	if rs.Batch != nil {
		s.Batch = *rs.Batch
	}
	if s.MaxAge <= 0 {
		fail("max_age", "max_age must be positive")
	}
	if s.GroupWait <= 0 {
		fail("group_wait", "group_wait must be positive")
	}
	if s.QueueSize < 1 {
		fail("queue_size", "queue_size must be positive")
	}
	if s.MaxBatch < 1 {
		fail("max_batch", "max_batch must be positive")
	}
}

func resolveHeartbeat(errs *[]error, rh *rawHeartbeat) *HeartbeatConfig {
	if rh == nil {
		return nil
	}
	hb := &HeartbeatConfig{
		URL:      secretRef{Env: rh.URLEnv, File: rh.URLFile, Setec: rh.URLSetec, src: nil},
		Interval: defaultHeartbeatEvery,
	}
	if secretSourceCount(rh.URLEnv, rh.URLFile, rh.URLSetec) != 1 {
		*errs = append(*errs, fmt.Errorf("heartbeat.url: exactly one of url_env, url_file or url_setec is required%s",
			setSourcesNote("url", rh.URLEnv, rh.URLFile, rh.URLSetec)))
	}
	applyDur(errs, "heartbeat.interval", rh.Interval, &hb.Interval)
	if hb.Interval <= 0 {
		*errs = append(*errs, errors.New("heartbeat.interval: interval must be positive"))
	}
	return hb
}

// bindSecrets attaches src to every URL, HMAC secret, header and heartbeat
// ref. Env and file refs ignore src. Header map entries are copied because
// ranging a map yields values, not references.
func (c *Config) bindSecrets(src secretSource) {
	if c == nil || src == nil {
		return
	}
	for i := range c.Sinks {
		c.Sinks[i].URL.src = src
		c.Sinks[i].Secret.src = src
		for name, ref := range c.Sinks[i].Headers {
			ref.src = src
			c.Sinks[i].Headers[name] = ref
		}
	}
	if c.Heartbeat != nil {
		c.Heartbeat.URL.src = src
	}
}

// resolveSecrets fails fast when any referenced secret cannot be read now.
func (c *Config) resolveSecrets() error {
	var errs []error
	for _, s := range c.Sinks {
		if err := s.checkSecrets(); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Heartbeat != nil {
		if _, err := c.Heartbeat.URL.resolve(); err != nil {
			errs = append(errs, fmt.Errorf("heartbeat: %w", err))
		}
	}
	return errors.Join(errs...)
}

// messageSink reports whether the sink posts chat messages (Slack, Teams), whose
// group is capped by queue_size rather than max_batch.
func (s SinkConfig) messageSink() bool {
	return s.Type == sinkSlack || s.Type == sinkTeams
}

// groupsMessages reports whether the sink folds events inside group_wait into
// one request: message sinks always do, a webhook does when batching.
func (s SinkConfig) groupsMessages() bool {
	return s.messageSink() || s.Batch
}

func (s SinkConfig) checkSecrets() error {
	var errs []error
	if _, err := s.URL.resolve(); err != nil {
		errs = append(errs, fmt.Errorf("sink %s: url: %w", s.Name, err))
	}
	if s.Secret.isSet() {
		if _, err := s.Secret.resolve(); err != nil {
			errs = append(errs, fmt.Errorf("sink %s: secret: %w", s.Name, err))
		}
	}
	for header, ref := range s.Headers {
		if _, err := ref.resolve(); err != nil {
			errs = append(errs, fmt.Errorf("sink %s: header %s: %w", s.Name, header, err))
		}
	}
	return errors.Join(errs...)
}

// unnamedLabel is the text used when a target or route has no name.
const unnamedLabel = "(unnamed)"

// Route is one resolved notification route. Indexes are positions in the
// config's sink list for names that exist. Names keeps the configured order,
// including names that no longer have a sink.
type Route struct {
	Name     string
	Severity Severity
	Names    []string
	Indexes  []int
	Missing  []string
	Mention  string
	Source   string
}

// rawRoute is a route as written in the config file or routes.json.
type rawRoute struct {
	Name     string   `yaml:"name"               json:"name"`
	Severity string   `yaml:"severity,omitempty" json:"severity,omitempty"`
	Sinks    []string `yaml:"sinks"              json:"sinks"`
	Mention  string   `yaml:"mention,omitempty"  json:"mention,omitempty"`
	unknown  []string
	err      error
}

//nolint:nilerr,unparam // YAML requires an error return; failures are collected in r.err for aggregate validation.
func (r *rawRoute) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		r.err = errors.New("must be a mapping")
		return nil
	}
	known := map[string]struct{}{"name": {}, "severity": {}, "sinks": {}, "mention": {}}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if _, ok := known[key]; !ok {
			r.unknown = append(r.unknown, key)
		}
	}
	var plain struct {
		Name     string   `yaml:"name"`
		Severity string   `yaml:"severity"`
		Sinks    []string `yaml:"sinks"`
		Mention  string   `yaml:"mention"`
	}
	if err := value.Decode(&plain); err != nil {
		r.err = err
		return nil
	}
	r.Name = plain.Name
	r.Severity = plain.Severity
	r.Sinks = append([]string(nil), plain.Sinks...)
	r.Mention = plain.Mention
	return nil
}

// resolveRoutes validates every route. A route that fails is left out of the
// result; its problems are in errs. Unknown sink names are rejected.
func resolveRoutes(errs *[]error, raws []rawRoute, sinks []SinkConfig) []Route {
	out := make([]Route, 0, len(raws))
	seen := map[string]int{}
	for i, rr := range raws {
		reportRouteDecode(errs, i, rr)
		rt, ok := resolveOneRoute(errs, i, rr, sinks, seen, true)
		if ok {
			out = append(out, rt)
		}
	}
	return out
}

func reportRouteDecode(errs *[]error, i int, rr rawRoute) {
	if rr.err != nil {
		*errs = append(*errs, fmt.Errorf("routes[%d]: %w", i, rr.err))
	}
	for _, key := range rr.unknown {
		*errs = append(*errs, fmt.Errorf("routes[%d].%s: unknown key", i, key))
	}
}

// resolveOneRoute checks one route. strict rejects a sink name that no sink
// defines; otherwise that name is recorded on Missing and the route still loads.
func resolveOneRoute(
	errs *[]error, i int, rr rawRoute, sinks []SinkConfig, seen map[string]int, strict bool,
) (Route, bool) {
	loc := fmt.Sprintf("routes[%d]", i)
	label := rr.Name
	if label == "" {
		label = unnamedLabel
	}
	ok := true
	fail := func(key, format string, args ...any) {
		ok = false
		*errs = append(*errs, fmt.Errorf("%s.%s: route %s: %s", loc, key, label, fmt.Sprintf(format, args...)))
	}
	if !routeNameOK(fail, rr.Name, seen, i) {
		ok = false
	}
	sev, sevOK := routeSeverity(rr.Severity)
	if !sevOK {
		fail("severity", "severity %q must be page or warn", rr.Severity)
	}
	if len(rr.Sinks) == 0 {
		fail("sinks", "sinks must not be empty")
	}
	indexes, missing := routeSinkIndexes(fail, rr.Sinks, sinks, strict)
	if !ok {
		return Route{}, false
	}
	return Route{
		Name: rr.Name, Severity: sev, Names: append([]string(nil), rr.Sinks...),
		Indexes: indexes, Missing: missing, Mention: rr.Mention, Source: sourceConfig,
	}, true
}

func routeNameOK(fail func(key, format string, args ...any), name string, seen map[string]int, i int) bool {
	switch {
	case name == "":
		fail("name", "name is required")
		return false
	case !targetNameRE.MatchString(name):
		fail("name", "name %q must match [A-Za-z0-9._-]", name)
		return false
	}
	if prev, dup := seen[name]; dup {
		fail("name", "duplicate route name %q (also routes[%d])", name, prev)
		return false
	}
	seen[name] = i
	return true
}

func routeSeverity(raw string) (Severity, bool) {
	switch raw {
	case "", string(SeverityPage):
		return SeverityPage, true
	case string(SeverityWarn):
		return SeverityWarn, true
	default:
		return SeverityPage, false
	}
}

func routeSinkIndexes(
	fail func(key, format string, args ...any), names []string, sinks []SinkConfig, strict bool,
) ([]int, []string) {
	index := make(map[string]int, len(sinks))
	for i, s := range sinks {
		index[s.Name] = i
	}
	var indexes []int
	var missing []string
	seen := map[string]int{}
	for j, name := range names {
		if prev, dup := seen[name]; dup {
			fail(fmt.Sprintf("sinks[%d]", j), "sink %q is listed twice (also sinks[%d])", name, prev)
			continue
		}
		seen[name] = j
		if idx, ok := index[name]; ok {
			indexes = append(indexes, idx)
			continue
		}
		if strict {
			fail(fmt.Sprintf("sinks[%d]", j), "unknown sink %q", name)
			continue
		}
		missing = append(missing, name)
	}
	return indexes, missing
}

// describeRoute is the check-config line for a route.
func describeRoute(r Route) string {
	names := "none"
	if len(r.Names) > 0 {
		names = strings.Join(r.Names, ",")
	}
	return fmt.Sprintf("route %s severity=%s sinks=%s", r.Name, r.Severity, names)
}

// targetRouteLine is the route and candidate-sink tail of a check-config target line.
func targetRouteLine(t Target, routes []Route, sinks []SinkConfig) string {
	if t.Route == "" {
		return "route=none sinks=" + sinkNameList(sinks)
	}
	for _, r := range routes {
		if r.Name != t.Route {
			continue
		}
		candidates := make([]string, 0, len(r.Indexes))
		for _, idx := range r.Indexes {
			if idx >= 0 && idx < len(sinks) {
				candidates = append(candidates, sinks[idx].Name)
			}
		}
		if len(candidates) == 0 {
			return "route=" + r.Name + " sinks=none"
		}
		return "route=" + r.Name + " sinks=" + strings.Join(candidates, ",")
	}
	return "route=" + t.Route + " sinks=none"
}

func sinkNameList(sinks []SinkConfig) string {
	if len(sinks) == 0 {
		return "none"
	}
	names := make([]string, len(sinks))
	for i, s := range sinks {
		names[i] = s.Name
	}
	return strings.Join(names, ",")
}

// describeSink is the check-config line for a sink. It never prints secrets.
func describeSink(s SinkConfig) string {
	events := "all"
	if len(s.Events) > 0 {
		parts := make([]string, len(s.Events))
		for i, e := range s.Events {
			parts[i] = string(e)
		}
		events = strings.Join(parts, ",")
	}
	line := fmt.Sprintf("sink %s type=%s min_severity=%s events=%s queue=%s",
		s.Name, s.Type, s.MinSeverity, events, s.Queue)
	var match []string
	for _, kv := range []struct {
		key  string
		vals []string
	}{{"sites", s.Match.Sites}, {"targets", s.Match.Targets}, {"tags", s.Match.Tags}} {
		if len(kv.vals) > 0 {
			match = append(match, kv.key+":"+strings.Join(kv.vals, ","))
		}
	}
	if len(match) > 0 {
		line += " match=" + strings.Join(match, " ")
	}
	if s.URL.Setec != "" {
		line += " url=setec:" + s.URL.Setec
	}
	return line
}

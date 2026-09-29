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
)

const (
	SeverityInfo Severity = "info"

	rankInfo = 0
	rankWarn = 1
	rankPage = 2

	sinkWebhook = "webhook"
	sinkSlack   = "slack"

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

// secretRef names where a secret value lives. Exactly one field is set.
type secretRef struct {
	Env  string
	File string
}

func (r secretRef) isSet() bool { return r.Env != "" || r.File != "" }

// resolve reads the current secret value. File values are whitespace trimmed.
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
	Name          string
	Type          string
	URL           secretRef
	Secret        secretRef
	HeadersEnv    map[string]string
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
	SecretEnv     string            `yaml:"secret_env"`
	SecretFile    string            `yaml:"secret_file"`
	HeadersEnv    map[string]string `yaml:"headers_env"`
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
		label = "(unnamed)"
	}
	fail := func(key, format string, args ...any) {
		*errs = append(*errs, fmt.Errorf("%s.%s: sink %s: %s", loc, key, label, fmt.Sprintf(format, args...)))
	}
	s := SinkConfig{
		Name:          rs.Name,
		Type:          rs.Type,
		URL:           secretRef{Env: rs.URLEnv, File: rs.URLFile},
		Secret:        secretRef{Env: rs.SecretEnv, File: rs.SecretFile},
		HeadersEnv:    rs.HeadersEnv,
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
	case sinkWebhook, sinkSlack:
	default:
		fail("type", "type %q must be webhook or slack", rs.Type)
	}
	if (rs.URLEnv == "") == (rs.URLFile == "") {
		fail("url", "exactly one of url_env or url_file is required")
	}
	if rs.SecretEnv != "" && rs.SecretFile != "" {
		fail("secret", "set only one of secret_env or secret_file")
	}
	resolveSinkTypeKeys(fail, rs)
	resolveSinkFilters(fail, rs, &s)
	resolveSinkLimits(errs, fail, loc, rs, &s, stateDir)
	return s
}

func resolveSinkTypeKeys(fail func(key, format string, args ...any), rs rawSink) {
	switch rs.Type {
	case sinkSlack:
		for _, k := range []struct {
			key string
			set bool
		}{
			{"batch", rs.Batch != nil},
			{"max_batch", rs.MaxBatch != nil},
			{"secret_env", rs.SecretEnv != ""},
			{"secret_file", rs.SecretFile != ""},
			{"headers_env", len(rs.HeadersEnv) > 0},
		} {
			if k.set {
				fail(k.key, "%s is not valid for a slack sink", k.key)
			}
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
		URL:      secretRef{Env: rh.URLEnv, File: rh.URLFile},
		Interval: defaultHeartbeatEvery,
	}
	if (rh.URLEnv == "") == (rh.URLFile == "") {
		*errs = append(*errs, errors.New("heartbeat.url: exactly one of url_env or url_file is required"))
	}
	applyDur(errs, "heartbeat.interval", rh.Interval, &hb.Interval)
	if hb.Interval <= 0 {
		*errs = append(*errs, errors.New("heartbeat.interval: interval must be positive"))
	}
	return hb
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
	for header, env := range s.HeadersEnv {
		if _, err := (secretRef{Env: env, File: ""}).resolve(); err != nil {
			errs = append(errs, fmt.Errorf("sink %s: header %s: %w", s.Name, header, err))
		}
	}
	return errors.Join(errs...)
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
	return line
}

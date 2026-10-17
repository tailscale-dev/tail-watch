package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/setec/client/setec"
	"github.com/tailscale/setec/types/api"
)

const (
	defaultSetecPoll = 5 * time.Minute
	minSetecPoll     = time.Minute
)

// Tests replace these seams. setecStartupTimeout bounds the initial fetch.
// setecHTTPClient is how the store dials. openSetecStore is the constructor
// run and notify-test call.
//
//nolint:gochecknoglobals // Replaced by tests; production always uses the defaults.
var (
	setecStartupTimeout = 30 * time.Second
	setecHTTPClient     = newSinkClient
	openSetecStore      = newSetecStore
)

// secretSource returns the current value of a named secret.
// An unknown or empty name is an error. The value is not logged.
type secretSource interface {
	get(name string) (string, error)
}

// setecSettings is the validated collector.setec section.
// An empty Server means the section is absent or was rejected.
type setecSettings struct {
	Server       string
	PollInterval time.Duration
	// pollTicker is set only by tests. When non-nil, the store polls on it
	// instead of a time.Ticker, so a test can advance one poll.
	pollTicker setec.Ticker
}

// setecStore is the one in-memory store for the process.
type setecStore struct {
	store *setec.Store
	host  string
}

func (s *setecStore) get(name string) (string, error) {
	v := strings.TrimSpace(s.store.Secret(name).GetString())
	if v == "" {
		return "", fmt.Errorf("setec: secret %s is empty", name)
	}
	return v, nil
}

// Close stops the store's poller.
func (s *setecStore) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

// newSetecStore fetches every name before returning. Secrets stay in the
// store's memory cache: there is no Cache and no FileCache. A failed later
// poll keeps the last good value and is logged at warn.
func newSetecStore(
	ctx context.Context,
	env netEnv,
	cfg setecSettings,
	names []string,
	logger *slog.Logger,
) (*setecStore, error) {
	if len(names) == 0 {
		return nil, errors.New("setec: no secrets are listed")
	}
	host := setecHost(cfg.Server)
	rec := &recordingSetecClient{
		setec.Client{Server: cfg.Server, DoHTTP: setecHTTPClient(env).Do},
		sync.Mutex{},
		"",
		"",
		nil,
	}
	st, err := setec.NewStore(ctx, setec.StoreConfig{
		Client:            rec,
		Secrets:           names,
		Structs:           nil,
		AllowLookup:       false,
		Cache:             nil,
		PollInterval:      cfg.PollInterval,
		ExpiryAge:         0,
		Logf:              setecLogf(logger),
		PollTicker:        cfg.pollTicker,
		TimeNow:           nil,
		BackgroundContext: nil,
	})
	if err != nil {
		return nil, setecStartupError(err, rec, names, host)
	}
	out := &setecStore{store: st, host: host}
	for _, name := range names {
		if _, getErr := out.get(name); getErr != nil {
			_ = out.Close()
			return nil, &setecSecretError{secret: name, err: fmt.Errorf("%w (server %s)", getErr, host)}
		}
	}
	return out, nil
}

func setecLogf(logger *slog.Logger) func(string, ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		// The store uses one Logf for lifecycle lines and for failures.
		// Failures are warnings. A healthy poll is not.
		if setecLineIsFailure(msg) {
			logger.Warn(msg)
			return
		}
		logger.Debug(msg)
	}
}

func setecLineIsFailure(msg string) bool {
	for _, marker := range []string{"WARNING", "error", "continuing", "retrying"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func setecHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func validateSetecServer(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("must be an https URL with a host")
	}
	return nil
}

// recordingSetecClient remembers the first not-found and the first other
// fetch error. NewStore retries until the context ends and then often
// returns only the last error, which may be a deadline with no secret name.
type recordingSetecClient struct {
	setec.Client

	mu      sync.Mutex
	missing string
	failed  string
	failErr error
}

func (r *recordingSetecClient) Get(ctx context.Context, name string) (*api.SecretValue, error) {
	v, err := r.Client.Get(ctx, name)
	r.note(name, err)
	return v, err
}

func (r *recordingSetecClient) note(name string, err error) {
	if err == nil || errors.Is(err, api.ErrValueNotChanged) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if errors.Is(err, api.ErrNotFound) {
		if r.missing == "" {
			r.missing = name
		}
		return
	}
	if r.failed != "" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	r.failed = name
	r.failErr = err
}

func setecStartupError(err error, rec *recordingSetecClient, names []string, host string) error {
	rec.mu.Lock()
	missing, failed, failErr := rec.missing, rec.failed, rec.failErr
	rec.mu.Unlock()
	if missing != "" {
		return &setecSecretError{
			secret: missing,
			err:    fmt.Errorf("setec: secret not found: %s (server %s)", missing, host),
		}
	}
	cause := failErr
	if cause == nil {
		cause = err
	}
	if failed == "" {
		return &setecSecretError{
			secret: "",
			err: fmt.Errorf(
				"setec: server unreachable: %s (secrets %s): %w", host, strings.Join(names, ", "), cause),
		}
	}
	return &setecSecretError{
		secret: failed,
		err:    fmt.Errorf("setec: server unreachable: %s (secret %s): %w", host, failed, cause),
	}
}

// setecSecretError is a startup error tied to one secret, so the caller can name
// the sinks that use it. secret is empty when no single secret is to blame.
type setecSecretError struct {
	secret string
	err    error
}

func (f *setecSecretError) Error() string { return f.err.Error() }
func (f *setecSecretError) Unwrap() error { return f.err }

// setecUsers lists the sinks and the heartbeat that name secret.
func setecUsers(cfg *Config, secret string) string {
	var users []string
	for _, sink := range cfg.Sinks {
		if sinkNamesSetec(sink, secret) {
			users = append(users, "sink "+sink.Name)
		}
	}
	if cfg.Heartbeat != nil && cfg.Heartbeat.URL.Setec == secret {
		users = append(users, "heartbeat")
	}
	return strings.Join(users, ", ")
}

func sinkNamesSetec(s SinkConfig, secret string) bool {
	if s.URL.Setec == secret || s.Secret.Setec == secret {
		return true
	}
	for _, ref := range s.Headers {
		if ref.Setec == secret {
			return true
		}
	}
	return false
}

// applySetec builds the store when the config names a setec secret and binds
// it onto every secret ref. No names means no client and a nil error.
// The caller closes the store when the process exits.
func applySetec(ctx context.Context, cfg *Config, env netEnv, logger *slog.Logger) (*setecStore, error) {
	names := setecSecretNames(cfg)
	if len(names) == 0 {
		return nil, nil //nolint:nilnil // no *_setec names is success and no store
	}
	ctx, cancel := context.WithTimeout(ctx, setecStartupTimeout)
	defer cancel()
	st, err := openSetecStore(ctx, env, cfg.Collector.Setec, names, logger)
	if err != nil {
		var failure *setecSecretError
		if errors.As(err, &failure) && failure.secret != "" {
			if users := setecUsers(cfg, failure.secret); users != "" {
				return nil, fmt.Errorf("%w (used by %s)", err, users)
			}
		}
		return nil, err
	}
	cfg.bindSecrets(st)
	return st, nil
}

func setecSecretNames(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	var names []string
	add := func(ref secretRef) {
		if ref.Setec != "" {
			names = append(names, ref.Setec)
		}
	}
	for _, sink := range cfg.Sinks {
		add(sink.URL)
		add(sink.Secret)
		for _, ref := range sink.Headers {
			add(ref)
		}
	}
	if cfg.Heartbeat != nil {
		add(cfg.Heartbeat.URL)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func sinkUsesSetec(s SinkConfig) bool {
	if s.URL.Setec != "" || s.Secret.Setec != "" {
		return true
	}
	for _, ref := range s.Headers {
		if ref.Setec != "" {
			return true
		}
	}
	return false
}

// resolveSetec validates collector.setec. An empty section is absent.
// local-only mode rejects the section without also judging the URL.
func resolveSetec(errs *[]error, raw rawCollector) setecSettings {
	sec := raw.Setec
	if sec == nil || (sec.Server == "" && !sec.PollInterval.set && sec.PollInterval.err == nil) {
		return setecSettings{Server: "", PollInterval: 0, pollTicker: nil}
	}
	if raw.LocalOnly != nil && *raw.LocalOnly {
		*errs = append(*errs, errors.New("collector.setec: setec is unavailable in local-only mode"))
		return setecSettings{Server: "", PollInterval: 0, pollTicker: nil}
	}
	out := setecSettings{Server: "", PollInterval: defaultSetecPoll, pollTicker: nil}
	switch {
	case sec.PollInterval.err != nil:
		*errs = append(*errs, fmt.Errorf("collector.setec.poll_interval: %w", sec.PollInterval.err))
	case sec.PollInterval.set && sec.PollInterval.d < minSetecPoll:
		*errs = append(*errs, errors.New("collector.setec.poll_interval: poll_interval must be at least 1m"))
	case sec.PollInterval.set:
		out.PollInterval = sec.PollInterval.d
	}
	if sec.Server == "" {
		return out
	}
	if err := validateSetecServer(sec.Server); err != nil {
		*errs = append(*errs, fmt.Errorf("collector.setec.server: %w", err))
		return out
	}
	out.Server = sec.Server
	return out
}

// rejectSetecKeys reports every *_setec key that local-only mode forbids,
// and every *_setec key that has no collector.setec.server.
func rejectSetecKeys(errs *[]error, raw rawConfig, localOnly, serverSet bool) {
	if !localOnly && serverSet {
		return
	}
	for i, s := range raw.Sinks {
		label := s.Name
		if label == "" {
			label = unnamedLabel
		}
		loc := fmt.Sprintf("sinks[%d]", i)
		for _, key := range setecKeysOnSink(s) {
			rejectOneSetecKey(errs, localOnly, loc+"."+key, label)
		}
	}
	if raw.Heartbeat != nil && raw.Heartbeat.URLSetec != "" {
		rejectOneSetecKey(errs, localOnly, "heartbeat.url_setec", "")
	}
}

func setecKeysOnSink(s rawSink) []string {
	var keys []string
	if s.URLSetec != "" {
		keys = append(keys, "url_setec")
	}
	if s.SecretSetec != "" {
		keys = append(keys, "secret_setec")
	}
	if len(s.HeadersSetec) > 0 {
		keys = append(keys, "headers_setec")
	}
	return keys
}

func rejectOneSetecKey(errs *[]error, localOnly bool, field, sink string) {
	key := field
	if _, after, ok := strings.CutLast(field, "."); ok {
		key = after
	}
	var err error
	switch {
	case localOnly && sink != "":
		err = fmt.Errorf("%s: sink %s: %s is unavailable in local-only mode", field, sink, key)
	case localOnly:
		err = fmt.Errorf("%s: %s is unavailable in local-only mode", field, key)
	case sink != "":
		err = fmt.Errorf("%s: sink %s: %s requires collector.setec.server", field, sink, key)
	default:
		err = fmt.Errorf("%s: %s requires collector.setec.server", field, key)
	}
	*errs = append(*errs, err)
}

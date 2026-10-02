package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	dynamicFileName = "targets.json"
	dynamicFileMode = 0o600
)

// dynamicTarget is one UI-managed monitor as stored in targets.json and
// exchanged with the monitor API. Field names match a YAML target.
type dynamicTarget struct {
	Name             string   `json:"name"`
	Addr             string   `json:"addr"`
	Site             string   `json:"site,omitempty"`
	Severity         string   `json:"severity,omitempty"`
	Route            string   `json:"route,omitempty"`
	Parent           string   `json:"parent,omitempty"`
	InsecureTLS      bool     `json:"insecure_tls,omitempty"`
	Interval         string   `json:"interval,omitempty"`
	Timeout          string   `json:"timeout,omitempty"`
	FailThreshold    *int     `json:"fail_threshold,omitempty"`
	RecoverThreshold *int     `json:"recover_threshold,omitempty"`
	FlapLimit        *int     `json:"flap_limit,omitempty"`
	FlapWindow       string   `json:"flap_window,omitempty"`
	Network          string   `json:"network,omitempty"`
	Checks           []string `json:"checks"`
}

// raw converts to the YAML-shaped target the config loader validates.
func (d dynamicTarget) raw() rawTarget {
	return rawTarget{
		Name:             d.Name,
		Addr:             d.Addr,
		Site:             d.Site,
		Severity:         d.Severity,
		Route:            d.Route,
		Parent:           d.Parent,
		InsecureTLS:      d.InsecureTLS,
		Interval:         durationFromString(d.Interval),
		Timeout:          durationFromString(d.Timeout),
		FailThreshold:    d.FailThreshold,
		RecoverThreshold: d.RecoverThreshold,
		FlapLimit:        d.FlapLimit,
		FlapWindow:       durationFromString(d.FlapWindow),
		Network:          d.Network,
		Checks:           d.Checks,
	}
}

func durationFromString(s string) yamlDuration {
	s = strings.TrimSpace(s)
	if s == "" {
		return yamlDuration{set: false, d: 0, err: nil}
	}
	var d yamlDuration
	d.set = true
	parsed, err := time.ParseDuration(s)
	if err != nil {
		d.err = fmt.Errorf("invalid duration %q", s)
		return d
	}
	d.d = parsed
	return d
}

// loadDynamic reads targets.json from dir. A missing file is an empty list.
func loadDynamic(dir string) ([]dynamicTarget, error) {
	path := filepath.Join(dir, dynamicFileName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []dynamicTarget
	if decodeErr := json.NewDecoder(bytes.NewReader(b)).Decode(&out); decodeErr != nil {
		return nil, fmt.Errorf("%s: %w", path, decodeErr)
	}
	return out, nil
}

// saveDynamic atomically replaces targets.json in dir.
func saveDynamic(dir string, targets []dynamicTarget) error {
	if targets == nil {
		targets = []dynamicTarget{}
	}
	return writeJSONAtomic(dir, dynamicFileName, targets)
}

// writeJSONAtomic replaces file in dir, mode 0600: write a temporary file,
// fsync it, rename it over the store, then fsync the directory.
func writeJSONAtomic(dir, file string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, file+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if chmodErr := os.Chmod(name, dynamicFileMode); chmodErr != nil {
		return chmodErr
	}
	if _, writeErr := tmp.Write(append(data, '\n')); writeErr != nil {
		return writeErr
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		return syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	if renameErr := os.Rename(name, filepath.Join(dir, file)); renameErr != nil {
		return renameErr
	}
	renamed = true
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// mergeDynamic appends the entries that validate to c.Targets, after the YAML
// targets. An entry whose name clashes with an earlier target, or that fails
// validation, is skipped with a warning. It returns the raw form of the
// entries it kept.
func (c *Config) mergeDynamic(entries []dynamicTarget, log *slog.Logger) []rawTarget {
	kept := slices.Clone(c.RawTargets)
	var added []rawTarget
	for _, e := range entries {
		raw := e.raw()
		if slices.ContainsFunc(kept, func(r rawTarget) bool { return r.Name == raw.Name }) {
			warnSkipped(log, e.Name, "name is already used by a configured or earlier monitor")
			continue
		}
		t, errs := c.validateCandidate(kept, raw, c.Routes)
		if len(errs) > 0 {
			warnSkipped(log, e.Name, errors.Join(errs...).Error())
			continue
		}
		kept = append(kept, raw)
		added = append(added, raw)
		c.Targets = append(c.Targets, t)
	}
	return added
}

func warnSkipped(log *slog.Logger, name, reason string) {
	if log != nil {
		log.Warn("skipping dynamic monitor in "+dynamicFileName, "target", name, "reason", reason)
	}
}

// loadDynamicTargets reads targets.json when the UI is on. When the UI is off
// it leaves the file alone and warns how many monitors are inactive.
func (a *app) loadDynamicTargets(once bool) error {
	dir := a.cfg.Collector.StateDir
	if dir == "" || once {
		return nil
	}
	entries, err := loadDynamic(dir)
	if !a.ui {
		switch {
		case err != nil:
			a.log.Warn("dynamic monitors are inactive: the UI is off and "+dynamicFileName+" is unreadable", "err", err)
		case len(entries) > 0:
			a.log.Warn("dynamic monitors are inactive: the UI is off",
				"file", filepath.Join(dir, dynamicFileName), "count", len(entries))
		}
		return nil
	}
	if err != nil {
		return err
	}
	a.dynamic = entries
	return nil
}

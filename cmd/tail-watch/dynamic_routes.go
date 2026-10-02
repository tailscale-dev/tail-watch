package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const routesFileName = "routes.json"

// dynamicRoute is one UI-managed route as stored in routes.json. Field names
// match a YAML route. Unknown JSON fields are ignored.
type dynamicRoute struct {
	Name     string   `json:"name"`
	Severity string   `json:"severity,omitempty"`
	Sinks    []string `json:"sinks"`
	Mention  string   `json:"mention,omitempty"`
}

// loadRoutes reads routes.json from dir. A missing file is an empty list.
// A file that cannot be decoded returns an error that names the file.
func loadRoutes(dir string) ([]dynamicRoute, error) {
	path := filepath.Join(dir, routesFileName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []dynamicRoute
	if decodeErr := json.NewDecoder(bytes.NewReader(b)).Decode(&out); decodeErr != nil {
		return nil, fmt.Errorf("%s: %w", path, decodeErr)
	}
	return out, nil
}

// saveRoutes atomically replaces routes.json in dir, mode 0600.
func saveRoutes(dir string, routes []dynamicRoute) error {
	if routes == nil {
		routes = []dynamicRoute{}
	}
	return writeJSONAtomic(dir, routesFileName, routes)
}

// mergeRoutes appends UI routes that validate. A name that matches a YAML
// route, or an entry that fails for any reason other than a missing sink, is
// skipped with a warning and left in the file. A missing sink is recorded and
// the route still loads.
func (c *Config) mergeRoutes(entries []dynamicRoute, log *slog.Logger) {
	seen := map[string]int{}
	for i, r := range c.Routes {
		seen[r.Name] = i
	}
	for i, e := range entries {
		rr := rawRoute{
			Name: e.Name, Severity: e.Severity, Sinks: append([]string(nil), e.Sinks...), Mention: e.Mention,
			unknown: nil, err: nil,
		}
		_, existed := seen[e.Name]
		var errs []error
		rt, ok := resolveOneRoute(&errs, i, rr, c.Sinks, seen, false)
		if !ok {
			if e.Name != "" && !existed {
				delete(seen, e.Name)
			}
			warnRoute(log, e.Name, errors.Join(errs...).Error())
			continue
		}
		if len(rt.Missing) > 0 && log != nil {
			log.Warn("route names a sink that is not configured",
				"route", rt.Name, "sinks", strings.Join(rt.Missing, ","))
		}
		rt.Source = sourceUI
		c.Routes = append(c.Routes, rt)
	}
}

func warnRoute(log *slog.Logger, name, reason string) {
	if log != nil {
		log.Warn("skipping route in "+routesFileName, "route", name, "reason", reason)
	}
}

// loadRouteFile reads routes.json when the UI is on. --once does not look at
// the file. When the UI is off the file is left unread, with a warning if it
// exists.
func (a *app) loadRouteFile(once bool) error {
	dir := a.cfg.Collector.StateDir
	if dir == "" || once {
		return nil
	}
	path := filepath.Join(dir, routesFileName)
	if !a.ui {
		if _, err := os.Stat(path); err == nil {
			a.log.Warn(routesFileName+" is present but the UI is off; leaving it unread", "file", path)
		}
		return nil
	}
	entries, err := loadRoutes(dir)
	if err != nil {
		return err
	}
	a.uiRoutes = entries
	return nil
}

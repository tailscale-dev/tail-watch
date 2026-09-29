package main

import (
	"log/slog"
	"time"
)

func rawT(name, addr string, checks ...string) rawTarget {
	return rawTarget{
		Name: name, Addr: addr, Site: "", Severity: "", Parent: "", InsecureTLS: false,
		Interval: yamlDuration{set: false, d: 0, err: nil}, Timeout: yamlDuration{set: false, d: 0, err: nil},
		FailThreshold: nil, RecoverThreshold: nil, FlapLimit: nil,
		FlapWindow: yamlDuration{set: false, d: 0, err: nil}, Network: "", Checks: checks,
	}
}

func rawWith(r rawTarget, mutate func(*rawTarget)) rawTarget {
	mutate(&r)
	return r
}

func dur(d time.Duration) yamlDuration { return yamlDuration{set: true, d: d, err: nil} }

func dynT(name, addr string, checks ...string) dynamicTarget {
	return dynamicTarget{
		Name: name, Addr: addr, Site: "", Severity: "", Parent: "", InsecureTLS: false, Interval: "",
		Timeout: "", FailThreshold: nil, RecoverThreshold: nil, FlapLimit: nil, FlapWindow: "",
		Network: "", Checks: checks,
	}
}

func dynWith(d dynamicTarget, mutate func(*dynamicTarget)) dynamicTarget {
	mutate(&d)
	return d
}

func appWithState(dir string, log *slog.Logger, ui bool) *app {
	cfg := new(Config)
	cfg.Collector.StateDir = dir
	a := new(app)
	a.cfg, a.log, a.ui = cfg, log, ui
	return a
}

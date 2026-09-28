package main

import "log/slog"

// transitionLog makes state changes observable before notification sinks exist.
type transitionLog struct {
	log *slog.Logger
}

func (l transitionLog) ObserveTransition(tr Transition) {
	if l.log == nil {
		return
	}
	l.log.Info("alert transition",
		"sequence", tr.Sequence, "target", tr.Target.Name,
		"from", tr.From.String(), "to", tr.To.String(), "flapping", tr.Flapping,
		"check", tr.Check, "layer", string(tr.Layer), "since", tr.Since,
		"down_since", tr.DownSince, "dependents", tr.Dependents)
}

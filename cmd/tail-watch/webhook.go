package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// webhookSink posts JSON events to a generic receiver, signed when it has a
// secret.
type webhookSink struct {
	cfg    SinkConfig
	client *http.Client
}

func newWebhookSink(cfg SinkConfig, client *http.Client) *webhookSink {
	return &webhookSink{cfg: cfg, client: client}
}

func (w *webhookSink) Name() string { return w.cfg.Name }

func (w *webhookSink) MinInterval() time.Duration { return 0 }

type batchBody struct {
	Version int     `json:"version"`
	BatchID string  `json:"batch_id"`
	Events  []Event `json:"events"`
}

// Render is one event payload, or a batch envelope when batching.
func (w *webhookSink) Render(evs []Event, unitID string) ([]byte, error) {
	if w.cfg.Batch {
		return marshalJSON(batchBody{Version: eventVersion, BatchID: unitID, Events: evs})
	}
	if len(evs) != 1 {
		return nil, fmt.Errorf("webhook sink %s: %d events in a single-event request", w.cfg.Name, len(evs))
	}
	return marshalJSON(evs[0])
}

func (w *webhookSink) Send(ctx context.Context, body []byte, m sendMeta) deliveryResult {
	url, err := w.cfg.URL.resolve()
	if err != nil {
		return secretUnavailable(fmt.Errorf("sink %s: url: %w", w.cfg.Name, err))
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "tail-watch/" + versionString(),
	}
	if w.cfg.Batch {
		headers["Tail-Watch-Batch-Id"] = m.ID
	} else {
		headers["Tail-Watch-Event-Id"] = m.ID
	}
	for name, env := range w.cfg.HeadersEnv {
		v, headerErr := (secretRef{Env: env, File: ""}).resolve()
		if headerErr != nil {
			return secretUnavailable(fmt.Errorf("sink %s: header %s: %w", w.cfg.Name, name, headerErr))
		}
		headers[name] = v
	}
	if w.cfg.Secret.isSet() {
		secret, secretErr := w.cfg.Secret.resolve()
		if secretErr != nil {
			return secretUnavailable(fmt.Errorf("sink %s: secret: %w", w.cfg.Name, secretErr))
		}
		headers[signatureHeader] = signBody(secret, m.At.Unix(), body)
	}
	return httpPost(ctx, w.client, url, body, headers, m.At)
}

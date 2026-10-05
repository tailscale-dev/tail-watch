package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	teamsMaxEvents   = 10
	teamsMaxDeps     = 10
	teamsMaxErrorLen = 300

	teamsEnvelopeType = "message"
	teamsContentType  = "application/vnd.microsoft.card.adaptive"
	teamsCardSchema   = "http://adaptivecards.io/schemas/adaptive-card.json"
	teamsCardType     = "AdaptiveCard"
	teamsCardVersion  = "1.4"

	teamsColorDown      = "Attention"
	teamsColorRecovered = "Good"
	teamsColorFlapping  = "Warning"

	teamsBlockType = "TextBlock"
)

// teamsSink posts Adaptive Cards to a Teams Workflows webhook.
type teamsSink struct {
	cfg    SinkConfig
	col    CollectorInfo
	client *http.Client
}

func newTeamsSink(cfg SinkConfig, col CollectorInfo, client *http.Client) *teamsSink {
	return &teamsSink{cfg: cfg, col: col, client: client}
}

func (s *teamsSink) Name() string { return s.cfg.Name }

func (s *teamsSink) MinInterval() time.Duration { return messageMinInterval }

type teamsTextBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Wrap  bool   `json:"wrap"`
	Color string `json:"color,omitempty"`
}

type teamsCard struct {
	Schema  string           `json:"$schema"`
	Type    string           `json:"type"`
	Version string           `json:"version"`
	Body    []teamsTextBlock `json:"body"`
}

type teamsAttachment struct {
	ContentType string    `json:"contentType"`
	Content     teamsCard `json:"content"`
}

// teamsMessage is the Workflows envelope: one Adaptive Card attachment.
type teamsMessage struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

// Render builds the envelope from the events. A route mention never reaches
// the card: teamsSink does not implement mentionRenderer.
func (s *teamsSink) Render(evs []Event, _ string) ([]byte, error) {
	return marshalJSON(renderTeams(evs, s.cfg))
}

func renderTeams(evs []Event, cfg SinkConfig) teamsMessage {
	shown := evs
	if len(shown) > teamsMaxEvents {
		shown = shown[:teamsMaxEvents]
	}
	var body []teamsTextBlock
	for _, ev := range shown {
		head, color := teamsHeadline(ev, cfg.StatusURL)
		body = append(body,
			teamsTextBlock{Type: teamsBlockType, Text: head, Wrap: true, Color: color},
			teamsTextBlock{Type: teamsBlockType, Text: teamsDetail(ev), Wrap: true, Color: ""},
		)
	}
	if more := len(evs) - len(shown); more > 0 {
		body = append(body, teamsTextBlock{
			Type: teamsBlockType, Text: fmt.Sprintf("and %d more", more), Wrap: true, Color: "",
		})
	}
	return teamsMessage{
		Type: teamsEnvelopeType,
		Attachments: []teamsAttachment{{
			ContentType: teamsContentType,
			Content: teamsCard{
				Schema: teamsCardSchema, Type: teamsCardType, Version: teamsCardVersion, Body: body,
			},
		}},
	}
}

// teamsHeadline is the first TextBlock of an event and its Adaptive Card
// color, "" when the event has no severity color.
func teamsHeadline(ev Event, statusURL string) (string, string) {
	name := ""
	if ev.Target != nil {
		name = ev.Target.Name
		if ev.Target.Implicit {
			name += " (learned)"
		}
	}
	link := name
	if statusURL != "" && ev.Target != nil {
		link = fmt.Sprintf("[%s](%s/#%s)", name, strings.TrimRight(statusURL, "/"), ev.Target.Name)
	}
	check := teamsEscape(ev.Check)
	switch ev.Type {
	case EventDown:
		return fmt.Sprintf("DOWN  %s  %s%s", link, check, layerNote(ev)), teamsColorDown
	case EventRecovered:
		after := humanDuration(secondsOf(ev.DurationSeconds))
		return fmt.Sprintf("RECOVERED %s %s after %s%s", link, check, after, removedNote(ev)), teamsColorRecovered
	case EventFlapping:
		return fmt.Sprintf("FLAPPING %s %s%s", link, check, layerNote(ev)), teamsColorFlapping
	case EventStarted:
		return fmt.Sprintf("tail-watch started on %s", teamsEscape(ev.Collector.Name)), ""
	case EventTest:
		return fmt.Sprintf("tail-watch test notification from %s", teamsEscape(ev.Collector.Name)), ""
	case EventDegraded:
		fallthrough
	default:
		return teamsEscape(string(ev.Type)), ""
	}
}

// teamsDetail is the second TextBlock of an event: the same fact lines the
// Slack sink prints, with untrusted text escaped for markdown.
func teamsDetail(ev Event) string {
	var details []string
	if t := ev.Target; t != nil {
		details = append(details, "addr "+teamsEscape(t.Addr))
		if t.Site != "" {
			details = append(details, "site "+teamsEscape(t.Site))
		}
		if t.Parent != "" {
			details = append(details, "parent "+teamsEscape(t.Parent))
		}
	}
	if p := ev.Path; p != nil && p.Kind != "" {
		path := "path " + p.Kind
		if p.LatencyMS != nil {
			path += fmt.Sprintf(" %d ms", *p.LatencyMS)
		}
		details = append(details, path)
	}
	var lines []string
	if len(details) > 0 {
		lines = append(lines, strings.Join(details, " · "))
	}
	if ev.Error != "" {
		lines = append(lines, "error: "+teamsEscape(truncate(ev.Error, teamsMaxErrorLen)))
	}
	var tail []string
	if ev.Since != nil {
		tail = append(tail, "down since "+ev.Since.Time().UTC().Format(slackClockLayout))
	}
	if ev.Failures > 0 {
		tail = append(tail, fmt.Sprintf("%d failures", ev.Failures))
	}
	tail = append(tail, "collector "+teamsEscape(ev.Collector.Name))
	lines = append(lines, strings.Join(tail, " · "))
	if ev.Dependents != nil && len(*ev.Dependents) > 0 {
		deps := *ev.Dependents
		shown := deps
		if len(shown) > teamsMaxDeps {
			shown = shown[:teamsMaxDeps]
		}
		line := fmt.Sprintf(slackTargetsBehind, len(deps)) + ": " + teamsEscape(strings.Join(shown, ", "))
		if len(deps) > len(shown) {
			line += ", …"
		}
		lines = append(lines, line)
	}
	// Teams markdown renders a single "\n" as a space; a blank line breaks.
	return strings.Join(lines, "\n\n")
}

// teamsEscaper escapes the characters Teams renders as markdown control
// syntax. Untrusted event text passes through it before entering a card.
//
//nolint:gochecknoglobals // A replacer is immutable and built once.
var teamsEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"*", `\*`,
	"_", `\_`,
	"[", `\[`,
	"]", `\]`,
	"(", `\(`,
	")", `\)`,
	"<", `\<`,
	">", `\>`,
)

func teamsEscape(s string) string { return teamsEscaper.Replace(s) }

func (s *teamsSink) Send(ctx context.Context, body []byte, m sendMeta) deliveryResult {
	url, err := s.cfg.URL.resolve()
	if err != nil {
		return secretUnavailable(fmt.Errorf("sink %s: url: %w", s.cfg.Name, err))
	}
	headers := map[string]string{
		headerContentType: contentTypeJSON,
		headerUserAgent:   "tail-watch/" + versionString(),
	}
	return httpPost(ctx, s.client, url, body, headers, m.At)
}

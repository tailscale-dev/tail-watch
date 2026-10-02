package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	slackMaxLines      = 10
	slackMaxDeps       = 10
	slackMaxErrorLen   = 300
	slackMinInterval   = time.Second
	slackTimeLayout    = "2006-01-02 15:04 UTC"
	slackClockLayout   = "15:04 UTC"
	slackRedCircle     = ":red_circle:"
	slackGreenCircle   = ":large_green_circle:"
	slackWarnMarker    = ":warning:"
	slackInfoMarker    = ":information_source:"
	slackTestMarker    = ":test_tube:"
	slackTargetsBehind = "and %d targets behind it"
)

// slackSink posts Block Kit messages to an incoming webhook.
type slackSink struct {
	cfg    SinkConfig
	col    CollectorInfo
	client *http.Client
}

func newSlackSink(cfg SinkConfig, col CollectorInfo, client *http.Client) *slackSink {
	return &slackSink{cfg: cfg, col: col, client: client}
}

func (s *slackSink) Name() string { return s.cfg.Name }

func (s *slackSink) MinInterval() time.Duration { return slackMinInterval }

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

type slackMessage struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

// Render builds the body from the events and the sink's own mention.
func (s *slackSink) Render(evs []Event, unitID string) ([]byte, error) {
	return s.RenderWithMentions(evs, unitID, nil)
}

// RenderWithMentions uses mentions, parallel to evs, for page-severity downs.
// An empty entry falls back to the sink's mention_on_page.
func (s *slackSink) RenderWithMentions(evs []Event, _ string, mentions []string) ([]byte, error) {
	return marshalJSON(renderSlack(evs, s.cfg, s.col, mentions))
}

func renderSlack(evs []Event, cfg SinkConfig, col CollectorInfo, mentions []string) slackMessage {
	var blocks []slackBlock
	var plain []string
	for _, text := range pageMentions(evs, cfg, mentions) {
		blocks = append(blocks, mrkdwnBlock(text))
		plain = append(plain, text)
	}
	shown := evs
	if len(shown) > slackMaxLines {
		shown = shown[:slackMaxLines]
	}
	for _, ev := range shown {
		rich, flat := slackLine(ev, cfg.StatusURL)
		blocks = append(blocks, mrkdwnBlock(rich))
		plain = append(plain, flat)
	}
	if more := len(evs) - len(shown); more > 0 {
		line := fmt.Sprintf("and %d more", more)
		blocks = append(blocks, mrkdwnBlock(line))
		plain = append(plain, line)
	}
	name := col.Name
	if name == "" {
		name = "tail-watch"
	}
	when := ""
	if len(evs) > 0 {
		when = " · " + evs[0].Time.Time().UTC().Format(slackTimeLayout)
	}
	blocks = append(blocks, slackBlock{
		Type: "context", Text: nil,
		Elements: []slackText{{Type: "mrkdwn", Text: slackEscape(name) + when}},
	})
	return slackMessage{Text: strings.Join(plain, "\n"), Blocks: blocks}
}

// pageMentions returns the distinct page-down mentions in queue order. A down
// with an empty route mention uses the sink's mention_on_page.
func pageMentions(evs []Event, cfg SinkConfig, mentions []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for i, ev := range evs {
		if ev.Type != EventDown || ev.Severity != SeverityPage {
			continue
		}
		text := ""
		if i < len(mentions) {
			text = mentions[i]
		}
		if text == "" {
			text = cfg.MentionOnPage
		}
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func mrkdwnBlock(text string) slackBlock {
	return slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: text}, Elements: nil}
}

// slackEscape escapes the three characters Slack treats as control syntax.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// slackLine renders one event as mrkdwn with links and as plain fallback text.
func slackLine(ev Event, statusURL string) (string, string) {
	name := ""
	if ev.Target != nil {
		name = slackEscape(ev.Target.Name)
		if ev.Target.Implicit {
			name += " (learned)"
		}
	}
	link := name
	if statusURL != "" && ev.Target != nil {
		link = fmt.Sprintf("<%s/#%s|%s>", strings.TrimRight(statusURL, "/"), ev.Target.Name, name)
	}
	switch ev.Type {
	case EventDown:
		return slackDown(ev, link, name)
	case EventRecovered:
		tail := "%s RECOVERED %%s %s after %s%s"
		after := humanDuration(secondsOf(ev.DurationSeconds))
		return fmt.Sprintf(fmt.Sprintf(tail, slackGreenCircle, slackCheckRich(ev.Check), after, removedNote(ev)), link),
			fmt.Sprintf(fmt.Sprintf(tail, slackGreenCircle, slackEscape(ev.Check), after, removedNote(ev)), name)
	case EventFlapping:
		head := "%s FLAPPING %%s %s%s"
		return fmt.Sprintf(fmt.Sprintf(head, slackWarnMarker, slackCheckRich(ev.Check), layerNote(ev)), link),
			fmt.Sprintf(fmt.Sprintf(head, slackWarnMarker, slackEscape(ev.Check), layerNote(ev)), name)
	case EventStarted:
		line := fmt.Sprintf("%s tail-watch started on %s", slackInfoMarker, slackEscape(ev.Collector.Name))
		return line, line
	case EventTest:
		line := fmt.Sprintf("%s tail-watch test notification from %s", slackTestMarker, slackEscape(ev.Collector.Name))
		return line, line
	case EventDegraded:
		fallthrough
	default:
		line := fmt.Sprintf("%s %s", slackInfoMarker, slackEscape(string(ev.Type)))
		return line, line
	}
}

// slackCheckRich escapes a check for mrkdwn. A check holding a URL, such as
// "http:http://host/|text", is a code span: Slack would otherwise auto-link it
// and encode the "|" as "%7C".
func slackCheckRich(check string) string {
	if strings.Contains(check, "://") {
		return "`" + strings.ReplaceAll(check, "`", "'") + "`"
	}
	return slackEscape(check)
}

func layerNote(ev Event) string {
	if ev.Layer == "" {
		return ""
	}
	return "  (" + ev.Layer + ")"
}

func removedNote(ev Event) string {
	if ev.Reason == reasonRemoved {
		return " (target removed)"
	}
	return ""
}

func secondsOf(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func slackDown(ev Event, link, name string) (string, string) {
	const headFmt = "%s DOWN  %%s  %s%s"
	richHead := fmt.Sprintf(headFmt, slackRedCircle, slackCheckRich(ev.Check), layerNote(ev))
	flatHead := fmt.Sprintf(headFmt, slackRedCircle, slackEscape(ev.Check), layerNote(ev))
	var details []string
	if t := ev.Target; t != nil {
		details = append(details, "addr "+slackEscape(t.Addr))
		if t.Site != "" {
			details = append(details, "site "+slackEscape(t.Site))
		}
		if t.Parent != "" {
			details = append(details, "parent "+slackEscape(t.Parent))
		}
	}
	if p := ev.Path; p != nil && p.Kind != "" {
		path := "path " + p.Kind
		if p.LatencyMS != nil {
			path += fmt.Sprintf(" %d ms", *p.LatencyMS)
		}
		details = append(details, path)
	}
	lines := []string{""}
	if len(details) > 0 {
		lines = append(lines, strings.Join(details, " · "))
	}
	if ev.Error != "" {
		lines = append(lines, "error: "+slackEscape(truncate(ev.Error, slackMaxErrorLen)))
	}
	var tail []string
	if ev.Since != nil {
		tail = append(tail, "down since "+ev.Since.Time().UTC().Format(slackClockLayout))
	}
	if ev.Failures > 0 {
		tail = append(tail, fmt.Sprintf("%d failures", ev.Failures))
	}
	tail = append(tail, "collector "+slackEscape(ev.Collector.Name))
	lines = append(lines, strings.Join(tail, " · "))
	if ev.Dependents != nil && len(*ev.Dependents) > 0 {
		deps := *ev.Dependents
		shown := deps
		if len(shown) > slackMaxDeps {
			shown = shown[:slackMaxDeps]
		}
		line := fmt.Sprintf(slackTargetsBehind, len(deps)) + ": " + slackEscape(strings.Join(shown, ", "))
		if len(deps) > len(shown) {
			line += ", …"
		}
		lines = append(lines, line)
	}
	rest := strings.Join(lines, "\n")
	return fmt.Sprintf(richHead, link) + rest, fmt.Sprintf(flatHead, name) + rest
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// humanDuration renders whole seconds compactly: 45s, 12m, 1h5m.
func humanDuration(secs int64) string {
	s := (time.Duration(secs) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (s *slackSink) Send(ctx context.Context, body []byte, m sendMeta) deliveryResult {
	url, err := s.cfg.URL.resolve()
	if err != nil {
		return secretUnavailable(fmt.Errorf("sink %s: url: %w", s.cfg.Name, err))
	}
	headers := map[string]string{
		"Content-Type": contentTypeJSON,
		"User-Agent":   "tail-watch/" + versionString(),
	}
	return httpPost(ctx, s.client, url, body, headers, m.At)
}

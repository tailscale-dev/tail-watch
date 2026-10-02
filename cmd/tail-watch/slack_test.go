package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func slackSinkCfg(mut func(*SinkConfig)) SinkConfig {
	sc := testSink("slack")
	sc.Type = sinkSlack
	if mut != nil {
		mut(&sc)
	}
	return sc
}

func renderJSON(t *testing.T, evs []Event, cfg SinkConfig) []byte {
	t.Helper()
	return renderMentions(t, evs, cfg, nil)
}

func renderMentions(t *testing.T, evs []Event, cfg SinkConfig, mentions []string) []byte {
	t.Helper()
	b, err := marshalJSON(renderSlack(evs, cfg, goldenCollector(), mentions))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSlackGolden(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	down := newDownEvent("evt_D1", goldenCollector(), tr)
	rec := newRecoveredEvent(
		"evt_R1",
		goldenCollector(),
		&incident{
			Down:   down,
			ID:     "",
			Status: incidentOpen,
			Target: targetDef{
				Name:     "",
				Addr:     "",
				Site:     "",
				Parent:   "",
				Severity: SeverityPage,
				Implicit: false,
				StableID: "",
				NodeID:   0,
			},
			DownSince: time.Time{},
			Recovery:  nil,
			Sinks:     nil,
		},
		tr.At.Add(12*time.Minute),
		nil,
		"",
	)

	router := goldenTransition()
	router.Target = Target{
		Dynamic:          false,
		Name:             "nyc-router",
		Addr:             "100.64.0.9",
		Site:             "nyc",
		Severity:         SeverityPage,
		Implicit:         true,
		Parent:           "",
		InsecureTLS:      false,
		Interval:         0,
		Timeout:          0,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Network:          NetworkTailnet,
		Checks:           nil,
		StableID:         "",
		NodeID:           0, Route: "",
	}
	router.Layer = layerUnrouted
	for i := range 14 {
		router.Dependents = append(router.Dependents, fmt.Sprintf("nyc-host-%02d", i+1))
	}
	routerDown := newDownEvent("evt_D2", goldenCollector(), router)

	warn := goldenTransition()
	warn.Target.Severity = SeverityWarn
	warnDown := newDownEvent("evt_D3", goldenCollector(), warn)

	mention := slackSinkCfg(func(sc *SinkConfig) { sc.MentionOnPage = "<!subteam^S0123ABC>" })
	linked := slackSinkCfg(func(sc *SinkConfig) { sc.StatusURL = "https://monitor-nyc.example.ts.net:9109" })

	cases := map[string][]byte{
		"down":               renderJSON(t, []Event{down}, slackSinkCfg(nil)),
		"down-mention":       renderJSON(t, []Event{down}, mention),
		"recovered":          renderJSON(t, []Event{rec}, mention),
		"router-14-deps":     renderJSON(t, []Event{routerDown}, slackSinkCfg(nil)),
		"warn-with-mention":  renderJSON(t, []Event{warnDown}, mention),
		"down-status-link":   renderJSON(t, []Event{down}, linked),
		"down-route-mention": renderMentions(t, []Event{down, warnDown}, mention, []string{"<!subteam^S0DB>", ""}),
		"recovered-removed": renderJSON(
			t,
			[]Event{
				newRecoveredEvent(
					"evt_R2",
					goldenCollector(),
					&incident{
						Down:   down,
						ID:     "",
						Status: incidentOpen,
						Target: targetDef{
							Name:     "",
							Addr:     "",
							Site:     "",
							Parent:   "",
							Severity: SeverityPage,
							Implicit: false,
							StableID: "",
							NodeID:   0,
						},
						DownSince: time.Time{},
						Recovery:  nil,
						Sinks:     nil,
					},
					tr.At.Add(time.Hour+5*time.Minute),
					nil,
					reasonRemoved,
				),
			},
			slackSinkCfg(nil),
		),
	}
	for name, raw := range cases {
		assertGolden(t, filepath.Join("testdata", "slack", name+".json"), raw)
	}
}

func TestSlackMessageContent(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	down := newDownEvent("evt_D1", goldenCollector(), tr)
	msg := renderSlack([]Event{down}, slackSinkCfg(nil), goldenCollector(), nil)
	if !strings.HasPrefix(msg.Text, ":red_circle: DOWN  nyc-nas  tcp:445  (service)") {
		t.Fatalf("text starts %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "connection refused") {
		t.Fatalf("error missing from text %q", msg.Text)
	}
	if len(msg.Blocks) == 0 {
		t.Fatal("no blocks")
	}

	rec := newRecoveredEvent(
		"evt_R1",
		goldenCollector(),
		&incident{
			Down:   down,
			ID:     "",
			Status: incidentOpen,
			Target: targetDef{
				Name:     "",
				Addr:     "",
				Site:     "",
				Parent:   "",
				Severity: SeverityPage,
				Implicit: false,
				StableID: "",
				NodeID:   0,
			},
			DownSince: time.Time{},
			Recovery:  nil,
			Sinks:     nil,
		},
		tr.At.Add(12*time.Minute+30*time.Second),
		nil,
		"",
	)
	got := renderSlack([]Event{rec}, slackSinkCfg(nil), goldenCollector(), nil).Text
	want := ":large_green_circle: RECOVERED nyc-nas tcp:445 after 14m"
	if got != want {
		t.Fatalf("recovered %q want %q", got, want)
	}

	routerTr := goldenTransition()
	for i := range 14 {
		routerTr.Dependents = append(routerTr.Dependents, fmt.Sprintf("dep-%02d", i))
	}
	text := renderSlack(
		[]Event{newDownEvent("evt_D2", goldenCollector(), routerTr)},
		slackSinkCfg(nil),
		goldenCollector(),
		nil,
	).Text
	if !strings.Contains(text, "and 14 targets behind it") || !strings.Contains(text, "dep-09") ||
		strings.Contains(text, "dep-10") {
		t.Fatalf("dependents line wrong: %q", text)
	}

	implicit := goldenTransition()
	implicit.Target.Implicit = true
	text = renderSlack(
		[]Event{newDownEvent("evt_D3", goldenCollector(), implicit)},
		slackSinkCfg(nil),
		goldenCollector(),
		nil,
	).Text
	if !strings.Contains(text, "nyc-nas (learned)") {
		t.Fatalf("implicit target not marked: %q", text)
	}
}

func TestSlackMentionsOnlyPageDowns(t *testing.T) {
	t.Parallel()
	const mention = "<!here>"
	cfg := slackSinkCfg(func(sc *SinkConfig) { sc.MentionOnPage = mention })
	tr := goldenTransition()
	pageDown := newDownEvent("evt_D1", goldenCollector(), tr)
	warnTr := goldenTransition()
	warnTr.Target.Severity = SeverityWarn
	warnDown := newDownEvent("evt_D2", goldenCollector(), warnTr)
	rec := newRecoveredEvent(
		"evt_R1",
		goldenCollector(),
		&incident{
			Down:   pageDown,
			ID:     "",
			Status: incidentOpen,
			Target: targetDef{
				Name:     "",
				Addr:     "",
				Site:     "",
				Parent:   "",
				Severity: SeverityPage,
				Implicit: false,
				StableID: "",
				NodeID:   0,
			},
			DownSince: time.Time{},
			Recovery:  nil,
			Sinks:     nil,
		},
		tr.At.Add(time.Minute),
		nil,
		"",
	)
	flap := newFlappingEvent("evt_F1", goldenCollector(), tr)
	started := newStartedEvent("evt_S1", goldenCollector(), tr.At)
	for name, ev := range map[string]Event{"warn": warnDown, "recovery": rec, "flapping": flap, "info": started} {
		if strings.Contains(renderSlack([]Event{ev}, cfg, goldenCollector(), nil).Text, mention) {
			t.Fatalf("%s must not carry the mention", name)
		}
	}
	msg := renderSlack([]Event{pageDown}, cfg, goldenCollector(), nil)
	if !strings.HasPrefix(msg.Text, mention+"\n") {
		t.Fatalf("page down must start with the mention: %q", msg.Text)
	}
	mixed := renderSlack([]Event{warnDown, pageDown}, cfg, goldenCollector(), nil)
	if !strings.HasPrefix(mixed.Text, mention) {
		t.Fatal("a group holding any page down starts with the mention")
	}
}

func TestSlackStatusLinks(t *testing.T) {
	t.Parallel()
	cfg := slackSinkCfg(func(sc *SinkConfig) { sc.StatusURL = "https://monitor-nyc.example.ts.net:9109/" })
	blocks := renderSlack(
		[]Event{newDownEvent("evt_D1", goldenCollector(), goldenTransition())},
		cfg,
		goldenCollector(),
		nil,
	).Blocks
	if !strings.Contains(blocks[0].Text.Text, "<https://monitor-nyc.example.ts.net:9109/#nyc-nas|nyc-nas>") {
		t.Fatalf("no status link: %q", blocks[0].Text.Text)
	}
}

func TestSlackEscapesUserText(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	tr.Err = fmt.Errorf("bad <@U123> & <!channel>")
	text := renderSlack(
		[]Event{newDownEvent("evt_D1", goldenCollector(), tr)},
		slackSinkCfg(nil),
		goldenCollector(),
		nil,
	).Text
	if strings.Contains(text, "<@U123>") || strings.Contains(text, "<!channel>") {
		t.Fatalf("unescaped control syntax: %q", text)
	}
}

func TestSlackGroupsBurstIntoOneMessage(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := slackSinkCfg(func(sc *SinkConfig) { sc.URL = secretRef{Env: "TW_TEST_URL", File: ""} })
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.n.mu.Lock()
	for i := range 25 {
		tr := goldenTransition()
		tr.Target.Name = fmt.Sprintf("host-%02d", i)
		h.n.enqueueLocked(newFlappingEvent(fmt.Sprintf("evt_%02d", i), goldenCollector(), tr))
	}
	h.n.mu.Unlock()
	h.start(t)
	h.clock.waitTimers(t, 0)
	if rc.count() != 0 {
		t.Fatal("nothing should be sent before group_wait elapses")
	}
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool { return rc.count() == 1 })
	var msg slackMessage
	if err := json.Unmarshal(rc.request(0).Body, &msg); err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range msg.Blocks {
		if b.Type == "section" && strings.Contains(b.Text.Text, "FLAPPING") {
			lines++
		}
	}
	if lines != 10 || !strings.Contains(msg.Text, "and 15 more") {
		t.Fatalf("lines=%d text tail=%q", lines, msg.Text[max(0, len(msg.Text)-40):])
	}
	time.Sleep(20 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("burst should be a single message, got %d", rc.count())
	}
}

func TestSlackSendsAtMostOnePerSecond(t *testing.T) {
	t.Parallel()
	sc := slackSinkCfg(nil)
	sc.Type = sinkWebhook
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.sinks["slack"].interval = time.Second
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	f := h.sinks["slack"]
	eventually(t, func() bool { return f.callCount() == 1 })
	h.n.ObserveTransition(upTransition(2, h.clock.Now()))
	h.clock.waitTimers(t, 0)
	h.clock.Advance(999 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if f.callCount() != 1 {
		t.Fatal("second message sent inside one second")
	}
	h.clock.Advance(time.Millisecond)
	eventually(t, func() bool { return f.callCount() == 2 })
}

func TestSlackRetryAfterThroughRealSink(t *testing.T) {
	rc := newReceiver(t, 429, 200)
	rc.extra[1] = func(w http.ResponseWriter) { w.Header().Set("Retry-After", "2") }
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := slackSinkCfg(func(sc *SinkConfig) { sc.URL = secretRef{Env: "TW_TEST_URL", File: ""} })
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	h.clock.waitTimers(t, 0)
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool { return rc.count() == 1 })
	h.clock.waitTimers(t, 1)
	h.clock.Advance(time.Second + 500*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatal("resent before Retry-After: 2")
	}
	h.clock.Advance(500 * time.Millisecond)
	eventually(t, func() bool { return rc.count() == 2 })
	if string(rc.request(0).Body) != string(rc.request(1).Body) {
		t.Fatal("Slack retry must resend the same message")
	}
}

func TestSlackURLCheckIsNotAutoLinked(t *testing.T) {
	t.Parallel()
	const check = "http:http://100.76.167.117:8080/|tail-watch-ok"
	tr := goldenTransition()
	tr.Check = check
	down := newDownEvent("evt_D1", goldenCollector(), tr)
	msg := renderSlack([]Event{down}, slackSinkCfg(nil), goldenCollector(), nil)
	rich := msg.Blocks[0].Text.Text
	if !strings.Contains(rich, "`"+check+"`") {
		t.Fatalf("a URL check must be a code span so Slack does not encode the pipe: %q", rich)
	}
	if !strings.HasPrefix(msg.Text, ":red_circle: DOWN  nyc-nas  "+check+"  (service)") ||
		strings.Contains(msg.Text, "`") {
		t.Fatalf("the plain text fallback must keep the exact check: %q", msg.Text)
	}
	rec := newRecoveredEvent("evt_R1", goldenCollector(), &incident{
		ID: "", Status: incidentOpen, Target: targetDef{
			Name: "", Addr: "", Site: "", Parent: "", Severity: SeverityPage, Implicit: false, StableID: "", NodeID: 0,
		},
		Down: down, DownSince: time.Time{}, Recovery: nil, Sinks: nil,
	}, tr.At.Add(time.Minute), nil, "")
	if got := renderSlack([]Event{rec}, slackSinkCfg(nil), goldenCollector(), nil).Blocks[0].Text.Text; !strings.Contains(
		got,
		"`"+check+"`",
	) {
		t.Fatalf("recovery lost the code span: %q", got)
	}
	plain := renderSlack(
		[]Event{newDownEvent("evt_D2", goldenCollector(), goldenTransition())},
		slackSinkCfg(nil),
		goldenCollector(),
		nil,
	)
	if strings.Contains(plain.Blocks[0].Text.Text, "`") {
		t.Fatalf("a non-URL check must stay plain: %q", plain.Blocks[0].Text.Text)
	}
}

func TestSlackRouteMentions(t *testing.T) {
	t.Parallel()
	cfg := slackSinkCfg(func(sc *SinkConfig) { sc.MentionOnPage = "<!here>" })
	col := goldenCollector()
	tr := goldenTransition()
	page := newDownEvent("evt_D1", col, tr)
	warnTr := goldenTransition()
	warnTr.Target.Severity = SeverityWarn
	warn := newDownEvent("evt_D2", col, warnTr)
	rec := newRecoveredEvent("evt_R1", col, &incident{
		ID: "", Status: incidentOpen, Target: targetDef{
			Name: "", Addr: "", Site: "", Parent: "", Severity: SeverityPage, Implicit: false, StableID: "", NodeID: 0,
		},
		Down: page, DownSince: time.Time{}, Recovery: nil, Sinks: nil,
	}, tr.At.Add(time.Minute), nil, "")
	flap := newFlappingEvent("evt_F1", col, tr)

	replaced := renderSlack([]Event{page}, cfg, col, []string{"<!subteam^S0DB>"}).Text
	if !strings.HasPrefix(replaced, "<!subteam^S0DB>\n") || strings.Contains(replaced, "<!here>") {
		t.Fatalf("route mention should replace the sink mention: %q", replaced)
	}
	fallback := renderSlack([]Event{page}, cfg, col, []string{""}).Text
	if !strings.HasPrefix(fallback, "<!here>\n") {
		t.Fatalf("empty route mention should fall back: %q", fallback)
	}
	for name, ev := range map[string]Event{"warn": warn, "recovery": rec, "flapping": flap} {
		text := renderSlack([]Event{ev}, cfg, col, []string{"<!subteam^S0DB>"}).Text
		if strings.Contains(text, "<!subteam^S0DB>") || strings.Contains(text, "<!here>") {
			t.Fatalf("%s carried a mention: %q", name, text)
		}
	}
	page2 := newDownEvent("evt_D3", col, tr)
	group := renderSlack([]Event{page, warn, page2, page}, cfg, col, []string{"<!a>", "<!a>", "<!b>", "<!a>"}).Text
	if !strings.HasPrefix(group, "<!a>\n<!b>\n") || strings.Count(group, "<!a>") != 1 ||
		strings.Count(group, "<!b>") != 1 {
		t.Fatalf("group mentions %q", group)
	}
}

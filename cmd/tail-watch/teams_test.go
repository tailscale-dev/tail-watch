package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func teamsSinkCfg(mut func(*SinkConfig)) SinkConfig {
	sc := testSink("teams")
	sc.Type = sinkTeams
	if mut != nil {
		mut(&sc)
	}
	return sc
}

func renderTeamsJSON(t *testing.T, evs []Event, cfg SinkConfig) []byte {
	t.Helper()
	b, err := marshalJSON(renderTeams(evs, cfg))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// teamsSamples are the events behind the golden cards and the live card.
type teamsSamples struct {
	down, recovered, router, flapping, escape, longError Event
}

func teamsSampleEvents() teamsSamples {
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

	flap := newFlappingEvent("evt_F1", goldenCollector(), tr)

	phish := goldenTransition()
	phish.Err = errors.New("see [phish](https://evil.example)")
	phishDown := newDownEvent("evt_D3", goldenCollector(), phish)

	longTr := goldenTransition()
	longTr.Err = errors.New(strings.Repeat("e", 301))
	longDown := newDownEvent("evt_D4", goldenCollector(), longTr)

	return teamsSamples{
		down: down, recovered: rec, router: routerDown, flapping: flap, escape: phishDown, longError: longDown,
	}
}

func TestTeamsGolden(t *testing.T) {
	t.Parallel()
	ev := teamsSampleEvents()
	down := ev.down
	linked := teamsSinkCfg(func(sc *SinkConfig) { sc.StatusURL = "https://monitor-nyc.example.ts.net:9109/" })

	// A route mention goes through renderBody; teamsSink is not a
	// mentionRenderer, so the mention must never reach the card.
	mentionBody, err := renderBody(
		newTeamsSink(teamsSinkCfg(nil), goldenCollector(), nil),
		[]Event{down},
		"evt_D1",
		[]string{"<at>Jane</at>"},
	)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"down":           renderTeamsJSON(t, []Event{down}, teamsSinkCfg(nil)),
		"recovered":      renderTeamsJSON(t, []Event{ev.recovered}, teamsSinkCfg(nil)),
		"router-14-deps": renderTeamsJSON(t, []Event{ev.router}, teamsSinkCfg(nil)),
		"flapping":       renderTeamsJSON(t, []Event{ev.flapping}, teamsSinkCfg(nil)),
		"escape":         renderTeamsJSON(t, []Event{ev.escape}, teamsSinkCfg(nil)),
		"long-error":     renderTeamsJSON(t, []Event{ev.longError}, teamsSinkCfg(nil)),
		"status-link":    renderTeamsJSON(t, []Event{down}, linked),
		"route-mention":  mentionBody,
	}
	for name, raw := range cases {
		assertGolden(t, filepath.Join("testdata", "teams", name+".json"), raw)
	}

	if got := cardText(t, cases["route-mention"]); strings.Contains(got, "<at>Jane</at>") {
		t.Fatalf("route mention reached the card: %s", got)
	}
	escape := cardText(t, cases["escape"])
	if strings.Contains(escape, "[phish](https://evil.example)") || !strings.Contains(escape, `\[phish\]`) {
		t.Fatalf("phishing error not escaped: %s", escape)
	}
	long := cardText(t, cases["long-error"])
	if !strings.Contains(long, strings.Repeat("e", 300)+"…") || strings.Contains(long, strings.Repeat("e", 301)) {
		t.Fatalf("error not cut to 300 bytes: %s", long)
	}
	status := cardText(t, cases["status-link"])
	if !strings.Contains(status, "[nyc-nas](https://monitor-nyc.example.ts.net:9109/#nyc-nas)") {
		t.Fatalf("no status link: %s", status)
	}
	downCard := cardText(t, cases["down"])
	for _, want := range []string{"DOWN", "nyc-nas", "tcp:445", "(service)", "connection refused"} {
		if !strings.Contains(downCard, want) {
			t.Fatalf("down card missing %q: %s", want, downCard)
		}
	}
	if got := firstBlock(t, cases["down"]).Color; got != teamsColorDown {
		t.Fatalf("down headline color %q", got)
	}
	if got := firstBlock(t, cases["recovered"]).Color; got != teamsColorRecovered {
		t.Fatalf("recovered headline color %q", got)
	}
	if got := firstBlock(t, cases["flapping"]).Color; got != teamsColorFlapping {
		t.Fatalf("flapping headline color %q", got)
	}
	routerCard := cardText(t, cases["router-14-deps"])
	learned := strings.Contains(routerCard, "nyc-router (learned)")
	behind := strings.Contains(routerCard, "and 14 targets behind it")
	if !learned || !behind ||
		!strings.Contains(routerCard, "nyc-host-10") ||
		strings.Contains(routerCard, "nyc-host-11") {
		t.Fatalf("router card wrong: %s", routerCard)
	}
	recCard := cardText(t, cases["recovered"])
	if !strings.Contains(recCard, "RECOVERED") || !strings.Contains(recCard, "after") {
		t.Fatalf("recovered card wrong: %s", recCard)
	}
}

func TestTeamsGoldenInfoCards(t *testing.T) {
	t.Parallel()
	at := goldenTransition().At
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{"started", newStartedEvent("evt_S1", goldenCollector(), at), "tail-watch started on monitor-nyc"},
		{"test", newTestEvent("evt_T1", goldenCollector(), at), "tail-watch test notification from monitor-nyc"},
	}
	for _, tc := range tests {
		raw := renderTeamsJSON(t, []Event{tc.ev}, teamsSinkCfg(nil))
		assertGolden(t, filepath.Join("testdata", "teams", tc.name+".json"), raw)
		if got := firstBlock(t, raw); got.Text != tc.want || got.Color != "" {
			t.Fatalf("%s headline %+v, want %q with no color", tc.name, got, tc.want)
		}
	}
}

func TestSinkGroupsMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ            string
		batch, message bool
		grouped        bool
	}{
		{sinkSlack, false, true, true},
		{sinkTeams, false, true, true},
		{sinkWebhook, true, false, true},
		{sinkWebhook, false, false, false},
	}
	for _, tc := range tests {
		sc := testSink("s")
		sc.Type, sc.Batch = tc.typ, tc.batch
		if got := sc.messageSink(); got != tc.message {
			t.Errorf("type=%s batch=%v: messageSink() = %v, want %v", tc.typ, tc.batch, got, tc.message)
		}
		if got := sc.groupsMessages(); got != tc.grouped {
			t.Errorf("type=%s batch=%v: groupsMessages() = %v, want %v", tc.typ, tc.batch, got, tc.grouped)
		}
	}
}

func TestNotifyTestTeamsSink(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_NT_TEAMS", rc.srv.URL)
	path := notifyTestConfig(t, "\nsinks:\n  - {name: ops-teams, type: teams, url_env: TW_NT_TEAMS}\n")
	var out, errOut strings.Builder
	code := runNotifyTestWith(
		[]string{"--config", path},
		&out,
		&errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil},
	)
	if code != 0 || !strings.Contains(out.String(), "ops-teams teams 200") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if rc.count() != 1 {
		t.Fatalf("requests %d", rc.count())
	}
	if got := firstBlock(t, rc.request(0).Body).Text; got != "tail-watch test notification from monitor-nyc" {
		t.Fatalf("test card headline %q", got)
	}
}

func decodeCard(t *testing.T, raw []byte) teamsCard {
	t.Helper()
	var msg teamsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments %d", len(msg.Attachments))
	}
	return msg.Attachments[0].Content
}

func cardText(t *testing.T, raw []byte) string {
	t.Helper()
	var b strings.Builder
	for _, blk := range decodeCard(t, raw).Body {
		b.WriteString(blk.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func firstBlock(t *testing.T, raw []byte) teamsTextBlock {
	t.Helper()
	body := decodeCard(t, raw).Body
	if len(body) == 0 {
		t.Fatal("card has no body")
	}
	return body[0]
}

func TestTeamsEnvelope(t *testing.T) {
	t.Parallel()
	raw := renderTeamsJSON(t, []Event{newDownEvent("evt_D1", goldenCollector(), goldenTransition())}, teamsSinkCfg(nil))
	var msg teamsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "message" || len(msg.Attachments) != 1 {
		t.Fatalf("envelope: %+v", msg)
	}
	card := msg.Attachments[0].Content
	if msg.Attachments[0].ContentType != "application/vnd.microsoft.card.adaptive" ||
		card.Type != "AdaptiveCard" || card.Schema != "http://adaptivecards.io/schemas/adaptive-card.json" ||
		card.Version != "1.4" {
		t.Fatalf("card: %+v", card)
	}
}

func TestTeamsBadRequestIsPermanent(t *testing.T) {
	rc := newReceiver(t, 400)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := teamsSinkCfg(func(sc *SinkConfig) { sc.URL = secretRef{Env: "TW_TEST_URL", File: "", Setec: "", src: nil} })
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	h.clock.waitTimers(t, 0)
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool {
		return testutil.ToFloat64(h.n.m.droppedC.WithLabelValues("teams", reasonPermanent)) == 1
	})
	time.Sleep(20 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("a 400 must not be retried, got %d requests", rc.count())
	}
}

func TestTeamsRetryAfterThroughRealSink(t *testing.T) {
	rc := newReceiver(t, 429, 200)
	rc.extra[1] = func(w http.ResponseWriter) { w.Header().Set("Retry-After", "2") }
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := teamsSinkCfg(func(sc *SinkConfig) { sc.URL = secretRef{Env: "TW_TEST_URL", File: "", Setec: "", src: nil} })
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
		t.Fatal("Teams retry must resend the same card")
	}
}

func TestTeamsErrorRedactsURL(t *testing.T) {
	t.Parallel()
	sc := teamsSinkCfg(func(sc *SinkConfig) {
		sc.URL = urlFile(t, "http://127.0.0.1:1/workflows/abc?sig=sekret")
	})
	s := newTeamsSink(sc, goldenCollector(), newSinkClient(nil))
	res := s.Send(context.Background(), []byte("{}"), sendMeta{ID: "evt_1", At: time.Now()})
	if res.Err == nil {
		t.Fatal("expected a delivery error")
	}
	if strings.Contains(res.Err.Error(), "sekret") {
		t.Fatalf("error leaks the sig query: %v", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "http://127.0.0.1:1") {
		t.Fatalf("error lost the scheme and host: %v", res.Err)
	}
}

func TestTeamsGroupsBurstIntoOneCard(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := teamsSinkCfg(func(sc *SinkConfig) { sc.URL = secretRef{Env: "TW_TEST_URL", File: "", Setec: "", src: nil} })
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
	req := rc.request(0)
	if got := req.Header.Get("Content-Type"); got != contentTypeJSON {
		t.Fatalf("Content-Type %q", got)
	}
	if got := req.Header.Get("User-Agent"); !strings.HasPrefix(got, "tail-watch/") {
		t.Fatalf("User-Agent %q", got)
	}
	if got := req.Header.Get(signatureHeader); got != "" {
		t.Fatalf("Teams requests carry no signature, got %q", got)
	}
	var msg teamsMessage
	if err := json.Unmarshal(req.Body, &msg); err != nil {
		t.Fatal(err)
	}
	lines := 0
	more := false
	for _, b := range msg.Attachments[0].Content.Body {
		if strings.Contains(b.Text, "FLAPPING") {
			lines++
		}
		if b.Text == "and 15 more" {
			more = true
		}
	}
	if lines != 10 || !more {
		t.Fatalf("lines=%d more=%v", lines, more)
	}
	time.Sleep(20 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("burst should be a single card, got %d", rc.count())
	}
}

func TestTeamsSendsAtMostOnePerSecond(t *testing.T) {
	rc := newReceiver(t, 200, 200)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := teamsSinkCfg(func(sc *SinkConfig) {
		sc.URL = secretRef{Env: "TW_TEST_URL", File: "", Setec: "", src: nil}
		sc.GroupWait = 600 * time.Millisecond
	})
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.n.mu.Lock()
	h.n.enqueueLocked(newFlappingEvent("evt_1", goldenCollector(), goldenTransition()))
	h.n.mu.Unlock()
	h.start(t)
	h.clock.waitTimers(t, 0)
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool { return rc.count() == 1 })
	h.n.mu.Lock()
	h.n.enqueueLocked(newFlappingEvent("evt_2", goldenCollector(), goldenTransition()))
	h.n.wakeLocked()
	h.n.mu.Unlock()
	h.clock.waitTimers(t, 1)
	h.clock.Advance(sc.GroupWait)
	h.clock.waitTimers(t, 2)
	time.Sleep(20 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatal("second card sent inside one second")
	}
	h.clock.Advance(time.Second)
	eventually(t, func() bool { return rc.count() == 2 })
}

func TestTeamsSetecURL(t *testing.T) {
	t.Parallel()
	rc := newReceiver(t)
	sc := teamsSinkCfg(func(sc *SinkConfig) {
		sc.URL = secretRef{
			Env: "", File: "", Setec: "teams/url",
			src: mapSource{values: map[string]string{"teams/url": rc.srv.URL}},
		}
	})
	s := newTeamsSink(sc, goldenCollector(), newSinkClient(nil))
	body := renderTeamsJSON(t, []Event{newDownEvent("evt_D1", goldenCollector(), goldenTransition())}, sc)
	res := s.Send(context.Background(), body, sendMeta{ID: "evt_D1", At: time.Now()})
	if res.Class != resultOK || rc.count() != 1 {
		t.Fatalf("result %+v requests %d", res, rc.count())
	}
}

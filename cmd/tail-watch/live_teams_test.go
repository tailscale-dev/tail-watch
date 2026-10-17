package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var teamsDeliveredRE = regexp.MustCompile(`\bops-teams teams 2\d\d\b`)

// teamsDelivered reports whether notify-test output shows the ops-teams sink
// answered 2xx. A Workflows trigger answers 202 Accepted, not 200.
func teamsDelivered(out string) bool { return teamsDeliveredRE.MatchString(out) }

// TestLiveTeams posts real cards to a Teams Workflows webhook, without a
// tailnet. It is off unless TAIL_WATCH_LIVE_TEAMS=1, and reads the URL from
// TEAMS_WEBHOOK_URL or the pulumi env named by TAIL_WATCH_TEAMS_PULUMI_ENV.
// It sends a notify-test card, a grouped card with every sample event, and an
// overflow card ending in "and 2 more". The workflow only answers 202, so look
// in the channel to check that the three cards render.
func TestLiveTeams(t *testing.T) {
	if os.Getenv("TAIL_WATCH_LIVE_TEAMS") == "" {
		t.Skip("set TAIL_WATCH_LIVE_TEAMS=1 to post cards to a Teams workflow")
	}
	url := liveTeamsURL(t)
	if url == "" {
		t.Fatal("TAIL_WATCH_LIVE_TEAMS=1 needs TEAMS_WEBHOOK_URL or TAIL_WATCH_TEAMS_PULUMI_ENV")
	}
	t.Setenv(liveTeamsEnvVar, url)

	path := notifyTestConfig(t, "\nsinks:\n  - {name: ops-teams, type: teams, url_env: "+liveTeamsEnvVar+"}\n")
	var out, errOut strings.Builder
	code := runNotifyTestWith([]string{"--config", path}, &out, &errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil})
	t.Logf("notify-test: %s", strings.TrimSpace(redactSecrets(out.String())))
	if code != 0 || !teamsDelivered(out.String()) {
		t.Fatalf("notify-test exit %d:\n%s\n%s", code, redactSecrets(out.String()), redactSecrets(errOut.String()))
	}

	sc := teamsSinkCfg(func(sc *SinkConfig) {
		sc.URL = secretRef{Env: liveTeamsEnvVar, File: "", Setec: "", src: nil}
		sc.StatusURL = "https://monitor-nyc.example.ts.net:9109"
	})
	s := newTeamsSink(sc, goldenCollector(), newSinkClient(nil))
	ev := teamsSampleEvents()
	overflow := make([]Event, 0, teamsMaxEvents+2)
	for i := range teamsMaxEvents + 2 {
		tr := goldenTransition()
		tr.Target.Name = fmt.Sprintf("host-%02d", i)
		overflow = append(overflow, newFlappingEvent(fmt.Sprintf("evt_O%02d", i), goldenCollector(), tr))
	}
	cards := map[string][]Event{
		"grouped":  {ev.down, ev.router, ev.escape, ev.longError, ev.flapping, ev.recovered},
		"overflow": overflow,
	}
	for _, name := range []string{"grouped", "overflow"} {
		time.Sleep(s.MinInterval())
		body, err := s.Render(cards[name], "bat_live_"+name)
		if err != nil {
			t.Fatal(err)
		}
		res := s.Send(t.Context(), body, sendMeta{ID: "bat_live_" + name, At: time.Now()})
		if res.Class != resultOK {
			t.Fatalf("%s card: %s", name, redactSecrets(res.String()))
		}
		t.Logf("%s card: %d events, status %d", name, len(cards[name]), res.Status)
	}
}

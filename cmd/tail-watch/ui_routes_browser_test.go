package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const routesUIConfig = `
collector: {state_dir: /unused, ui: true}
sinks:
  - name: pagerduty
    type: webhook
    url_env: PAGER_URL
    min_severity: page
    match: {sites: [nyc]}
  - name: ops-slack
    type: slack
    url_env: SLACK_URL
    min_severity: warn
    mention_on_page: "<!here>"
routes:
  - {name: db-team, severity: page, sinks: [pagerduty, ops-slack]}
  - {name: nowhere, severity: page, sinks: [pagerduty]}
targets:
  - {name: nyc-router, addr: 10.0.0.1, checks: [icmp]}
`

func routesPage(t *testing.T) playwright.Page {
	t.Helper()
	return uiPageOn(t, newAPIEnvFrom(t, routesUIConfig), uiPeers())
}

func chooseRoute(t *testing.T, page playwright.Page, value string) {
	t.Helper()
	opt := page.Locator(fmt.Sprintf(`#f-route option[value=%q]`, value))
	if err := opt.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateAttached,
		Timeout: nil,
	}); err != nil {
		t.Fatalf("option %s: %v", value, err)
	}
	choose(t, page.Locator("#f-route"), value)
}

func waitDown(t *testing.T, page playwright.Page, sink, outcome string) {
	t.Helper()
	sel := fmt.Sprintf(`#preview-down [data-sink=%q][data-outcome=%q]`, sink, outcome)
	if err := page.Locator(sel).WaitFor(); err != nil {
		body, _ := page.Locator("#route-preview").TextContent()
		t.Fatalf("%s: %v\npreview: %s", sel, err, body)
	}
}

func targetItem(t *testing.T, page playwright.Page, name string) map[string]any {
	t.Helper()
	raw, err := page.Evaluate(`fetch("/api/v1/targets").then((r) => r.text())`)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := raw.(string)
	if !ok {
		t.Fatalf("targets response %T", raw)
	}
	var list []map[string]any
	if err = json.Unmarshal([]byte(text), &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item["name"] == name {
			return item
		}
	}
	t.Fatalf("monitor %s not stored", name)
	return nil
}

func watchPosts(t *testing.T, page playwright.Page) *atomic.Value {
	t.Helper()
	var posted atomic.Value
	err := page.Route("**/api/v1/targets", func(r playwright.Route) {
		if r.Request().Method() == http.MethodPost {
			body, bodyErr := r.Request().PostData()
			if bodyErr == nil {
				posted.Store(body)
			}
		}
		_ = r.Continue()
	})
	if err != nil {
		t.Fatal(err)
	}
	return &posted
}

func previewBody(outcome, reason string) string {
	reasonJSON := ""
	if reason != "" {
		reasonJSON = `,"reason":"` + reason + `"`
	}
	row := fmt.Sprintf(`{"sink":"pagerduty","outcome":"%s"%s}`, outcome, reasonJSON)
	return `{"severity":"page","route":"db-team","recovery":"A recovery goes to the sinks that received its down.",` +
		`"down":{"type":"target.down","severity":"page","sinks":[` + row + `]},` +
		`"flapping":{"type":"target.flapping","severity":"warn","sinks":[` + row + `]}}`
}

func fulfillPreview(body string) playwright.RouteFulfillOptions {
	return playwright.RouteFulfillOptions{
		Status: new(200), ContentType: new("application/json"), Body: body,
		Headers: nil, Path: nil, Response: nil,
	}
}

func TestUIBrowserRouteChoiceAndList(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	posted := watchPosts(t, page)
	if err := page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	explain, err := page.Locator("#routing-explain").TextContent()
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"must act on now", "never wake anyone", "Flapping is always sent as warn", "recovery goes wherever its down went"} {
		if !strings.Contains(explain, part) {
			t.Errorf("explanation %q missing %q", explain, part)
		}
	}
	label, err := page.Locator(`#f-route option[value="route:db-team"]`).TextContent()
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"db-team", "page", "pagerduty", "ops-slack"} {
		if !strings.Contains(label, part) {
			t.Errorf("option %q missing %q", label, part)
		}
	}
	fill(t, page, "#f-name", "pg")
	fill(t, page, "#f-addr", "10.9.0.9")
	fill(t, page, "#f-site", "lab")
	addCheck(t, page, "icmp")
	chooseRoute(t, page, "route:db-team")
	save(t, page)
	if err = page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	raw, _ := posted.Load().(string)
	var sent map[string]any
	if err = json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatalf("post %q: %v", raw, err)
	}
	if sent["route"] != "db-team" {
		t.Errorf("route = %v", sent["route"])
	}
	if _, ok := sent["severity"]; ok {
		t.Errorf("severity was sent: %v", sent["severity"])
	}
	cell, err := page.Locator(`tr[data-name="pg"] td[data-label="Routing"]`).TextContent()
	if err != nil {
		t.Fatal(err)
	}
	if cell != "db-team · page" {
		t.Errorf("routing column = %q", cell)
	}
	if err = page.Locator(`button[aria-label="Edit pg"]`).Click(); err != nil {
		t.Fatal(err)
	}
	if _, err = page.WaitForFunction(
		`() => document.querySelector("#f-route").value === "route:db-team"`,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestUIBrowserUnroutedWarnPreselects(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "quiet", "10.9.0.8")
	addCheck(t, page, "icmp")
	chooseRoute(t, page, "severity:warn")
	save(t, page)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	item := targetItem(t, page, "quiet")
	def, _ := item["definition"].(map[string]any)
	if def["severity"] != "warn" {
		t.Errorf("severity = %v", def["severity"])
	}
	if _, ok := def["route"]; ok {
		t.Errorf("route was stored: %v", def["route"])
	}
	cell, err := page.Locator(`tr[data-name="quiet"] td[data-label="Routing"]`).TextContent()
	if err != nil || cell != "warn · all sinks" {
		t.Errorf("routing column = %q (%v)", cell, err)
	}
	if err = page.Locator(`button[aria-label="Edit quiet"]`).Click(); err != nil {
		t.Fatal(err)
	}
	if _, err = page.WaitForFunction(
		`() => document.querySelector("#f-route").value === "severity:warn"`,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestUIBrowserRoutesUnavailableStillSaves(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	if err := page.Route("**/api/v1/routes", func(r playwright.Route) {
		_ = r.Abort("failed")
	}); err != nil {
		t.Fatal(err)
	}
	startAdd(t, page, "typed", "10.9.0.7")
	time.Sleep(500 * time.Millisecond)
	if n := countOf(t, page.Locator(`#f-route option[value^="route:"]`)); n != 0 {
		t.Errorf("%d route options after a failed fetch", n)
	}
	for _, value := range []string{"severity:page", "severity:warn"} {
		if n := countOf(t, page.Locator(fmt.Sprintf(`#f-route option[value=%q]`, value))); n != 1 {
			t.Errorf("%s count = %d", value, n)
		}
	}
	addCheck(t, page, "icmp")
	save(t, page)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if txt, _ := page.Locator("#banner").TextContent(); txt != "Saved typed." {
		t.Errorf("banner = %q", txt)
	}
	def, _ := targetItem(t, page, "typed")["definition"].(map[string]any)
	if def["severity"] != "page" {
		t.Errorf("severity = %v", def["severity"])
	}
	if _, ok := def["route"]; ok {
		t.Errorf("route was stored: %v", def["route"])
	}
}

func TestUIBrowserPreviewDropAndSite(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	startAdd(t, page, "pg", "10.9.0.9")
	fill(t, page, "#f-site", "lab")
	addCheck(t, page, "icmp")
	chooseRoute(t, page, "route:db-team")
	waitDown(t, page, "pagerduty", "dropped")
	waitDown(t, page, "ops-slack", "sent")
	if txt, _ := page.Locator(`#preview-down [data-sink="pagerduty"]`).
		TextContent(); !strings.Contains(
		txt,
		"filtered by site",
	) {
		t.Errorf("pagerduty text = %q", txt)
	}
	fill(t, page, "#f-site", "nyc")
	waitDown(t, page, "pagerduty", "sent")
}

func TestUIBrowserPreviewNobodyStillSaves(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "quiet", "10.9.0.8")
	fill(t, page, "#f-site", "lab")
	addCheck(t, page, "icmp")
	chooseRoute(t, page, "route:nowhere")
	if err := page.Locator("#preview-warn").WaitFor(); err != nil {
		body, _ := page.Locator("#route-preview").TextContent()
		t.Fatalf("warning: %v\npreview: %s", err, body)
	}
	txt, _ := page.Locator("#preview-warn").TextContent()
	if txt != "This monitor's outage would notify no one." {
		t.Errorf("warning = %q", txt)
	}
	save(t, page)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if banner, _ := page.Locator("#banner").TextContent(); banner != "Saved quiet." {
		t.Errorf("banner = %q", banner)
	}
}

func TestUIBrowserFailedPreviewStillSaves(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	if err := page.Route("**/api/v1/route-preview", func(r playwright.Route) {
		_ = r.Abort("failed")
	}); err != nil {
		t.Fatal(err)
	}
	startAdd(t, page, "typed", "10.9.0.7")
	addCheck(t, page, "icmp")
	if err := page.Locator("#preview-status").WaitFor(); err != nil {
		t.Fatal(err)
	}
	save(t, page)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if txt, _ := page.Locator("#banner").TextContent(); txt != "Saved typed." {
		t.Errorf("banner = %q", txt)
	}
}

func TestUIBrowserPreviewIgnoresStale(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	var mu sync.Mutex
	type heldRoute struct {
		route playwright.Route
		site  string
	}
	var held []heldRoute
	if err := page.Route("**/api/v1/route-preview", func(r playwright.Route) {
		var body struct {
			Site string `json:"site"`
		}
		raw, rawErr := r.Request().PostData()
		if rawErr == nil {
			_ = json.Unmarshal([]byte(raw), &body)
		}
		if body.Site == "nyc" {
			_ = r.Fulfill(fulfillPreview(previewBody("sent", "")))
			return
		}
		mu.Lock()
		held = append(held, heldRoute{route: r, site: body.Site})
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	startAdd(t, page, "pg", "10.9.0.9")
	fill(t, page, "#f-site", "lab")
	addCheck(t, page, "icmp")
	chooseRoute(t, page, "route:db-team")
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		var lab bool
		for _, item := range held {
			lab = lab || item.site == "lab"
		}
		mu.Unlock()
		if lab {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lab preview was not sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
	fill(t, page, "#f-site", "nyc")
	waitDown(t, page, "pagerduty", "sent")
	mu.Lock()
	late := append([]heldRoute(nil), held...)
	mu.Unlock()
	for _, item := range late {
		_ = item.route.Fulfill(fulfillPreview(previewBody("dropped", "match.sites")))
	}
	time.Sleep(250 * time.Millisecond)
	outcome, err := page.Locator(`#preview-down [data-sink="pagerduty"]`).GetAttribute("data-outcome")
	if err != nil || outcome != "sent" {
		t.Errorf("outcome after the late response = %q (%v)", outcome, err)
	}
}

func openRoutes(t *testing.T, page playwright.Page) {
	t.Helper()
	if err := page.Locator("#nav-routes").Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(`tr[data-route="db-team"]`).WaitFor(); err != nil {
		t.Fatal(err)
	}
}

func addUIRoute(t *testing.T, page playwright.Page, name, severity, sink string) {
	t.Helper()
	if err := page.Locator("#add-route").Click(); err != nil {
		t.Fatal(err)
	}
	fill(t, page, "#r-name", name)
	choose(t, page.Locator("#r-severity"), severity)
	box := page.Locator(fmt.Sprintf(`#r-sinks input[value=%q]`, sink))
	if err := box.WaitFor(); err != nil {
		t.Fatalf("sink choice %s: %v", sink, err)
	}
	if err := box.Check(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("#route-save").Click(); err != nil {
		t.Fatal(err)
	}
	row := page.Locator(fmt.Sprintf(`tr[data-route=%q]`, name))
	if err := row.WaitFor(); err != nil {
		body, _ := page.Locator("#route-form").TextContent()
		t.Fatalf("route %s: %v\nform: %s", name, err, body)
	}
}

func jsBool(t *testing.T, page playwright.Page, expr string) bool {
	t.Helper()
	raw, err := page.Evaluate(expr)
	if err != nil {
		t.Fatal(err)
	}
	on, ok := raw.(bool)
	if !ok {
		t.Fatalf("%s returned %T (%v)", expr, raw, raw)
	}
	return on
}

func TestUIBrowserRoutesPageListsConfig(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	openRoutes(t, page)
	note, err := page.Locator("#view-routes .hint").TextContent()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "Sinks are defined in the config file") {
		t.Errorf("note = %q", note)
	}
	pager, err := page.Locator(`tr[data-sink="pagerduty"]`).TextContent()
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"pagerduty", "webhook", "all events", "page", "sites: nyc"} {
		if !strings.Contains(pager, part) {
			t.Errorf("pagerduty row %q missing %q", pager, part)
		}
	}
	if strings.Contains(pager, "PAGER_URL") || strings.Contains(pager, "http") {
		t.Errorf("pagerduty row leaks a secret: %q", pager)
	}
	slack, err := page.Locator(`tr[data-sink="ops-slack"]`).TextContent()
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"ops-slack", "slack", "warn", "all monitors"} {
		if !strings.Contains(slack, part) {
			t.Errorf("ops-slack row %q missing %q", slack, part)
		}
	}
	for sel, want := range map[string]string{
		`tr[data-sink="ops-slack"] td[data-label="Minimum severity"]`: "warn",
		`tr[data-sink="ops-slack"] td[data-label="Mention"]`:          "<!here>",
		`tr[data-sink="pagerduty"] td[data-label="Mention"]`:          "",
	} {
		if got, cellErr := page.Locator(sel).TextContent(); cellErr != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", sel, got, cellErr, want)
		}
	}
	if n := countOf(t, page.Locator(`tr[data-route="db-team"] button`)); n != 0 {
		t.Errorf("db-team has %d controls", n)
	}
	source, err := page.Locator(`tr[data-route="db-team"] td[data-label="Source"]`).TextContent()
	if err != nil || source != "Config file" {
		t.Errorf("db-team source = %q (%v)", source, err)
	}
}

func TestUIBrowserAddRouteAppearsInPicker(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	openRoutes(t, page)
	addUIRoute(t, page, "cameras", "warn", "ops-slack")
	if err := page.Locator(`button[aria-label="Edit route cameras"]`).Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("#r-name").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if !jsBool(t, page, `() => document.querySelector("#r-name").readOnly`) {
		t.Error("route name is editable")
	}
	if got := fieldValue(t, page, "#r-name"); got != "cameras" {
		t.Errorf("name = %q", got)
	}
	fill(t, page, "#r-mention", "@oncall")
	if err := page.Locator("#route-save").Click(); err != nil {
		t.Fatal(err)
	}
	mention := page.Locator(`tr[data-route="cameras"] td[data-label="Mention"]`)
	if _, err := page.WaitForFunction(
		`() => document.querySelector('tr[data-route="cameras"] td[data-label="Mention"]').textContent === "@oncall"`,
		nil,
	); err != nil {
		got, _ := mention.TextContent()
		t.Fatalf("mention: %v (%q)", err, got)
	}
	source, err := page.Locator(`tr[data-route="cameras"] td[data-label="Source"]`).TextContent()
	if err != nil || source != "UI" {
		t.Errorf("source = %q (%v)", source, err)
	}
	if err = page.Locator("#nav-monitors").Click(); err != nil {
		t.Fatal(err)
	}
	if err = page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	opt := page.Locator(`#f-route option[value="route:cameras"]`)
	if err = opt.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateAttached,
		Timeout: nil,
	}); err != nil {
		t.Fatal(err)
	}
	label, err := opt.TextContent()
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"cameras", "warn", "ops-slack"} {
		if !strings.Contains(label, part) {
			t.Errorf("option %q missing %q", label, part)
		}
	}
}

func TestUIBrowserDeleteRouteNamesMonitor(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	openRoutes(t, page)
	addUIRoute(t, page, "cameras", "page", "ops-slack")
	if err := page.Locator("#nav-monitors").Click(); err != nil {
		t.Fatal(err)
	}
	startAdd(t, page, "pg-primary", "10.9.0.4")
	addCheck(t, page, "icmp")
	chooseRoute(t, page, "route:cameras")
	save(t, page)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	openRoutes(t, page)
	if _, err := page.WaitForFunction(
		`() => document.querySelector('tr[data-route="cameras"] td[data-label="Used by"]')?.textContent.includes("pg-primary")`,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(`button[aria-label="Delete route cameras"]`).Click(); err != nil {
		t.Fatal(err)
	}
	text, err := page.Locator("#route-confirm-text").TextContent()
	if err != nil || !strings.Contains(text, "pg-primary") {
		t.Fatalf("confirm text = %q (%v)", text, err)
	}
	if err = page.Locator("#route-confirm-ok").Click(); err != nil {
		t.Fatal(err)
	}
	if err = page.Locator("#route-confirm-warning").WaitFor(); err != nil {
		t.Fatal(err)
	}
	warning, err := page.Locator("#route-confirm-warning").TextContent()
	if err != nil || !strings.Contains(warning, "pg-primary") {
		t.Errorf("warning = %q (%v)", warning, err)
	}
	if !jsBool(t, page, `() => document.querySelector("#route-confirm").open`) {
		t.Error("confirm dialog closed")
	}
	if n := countOf(t, page.Locator(`tr[data-route="cameras"]`)); n != 1 {
		t.Errorf("cameras rows = %d", n)
	}
}

func TestUIBrowserMissingSinkMarked(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	openRoutes(t, page)
	raw, err := page.Evaluate(`async () => {
		const res = await fetch("/api/v1/routes", {
			method: "POST",
			headers: {"Content-Type": "application/json"},
			body: JSON.stringify({name: "old", severity: "page", sinks: ["db-hook"]}),
		});
		return res.status + " " + await res.text();
	}`)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := raw.(string)
	if !strings.HasPrefix(status, "201") {
		t.Fatalf("create = %q", status)
	}
	if err = page.Locator("#nav-monitors").Click(); err != nil {
		t.Fatal(err)
	}
	if err = page.Locator("#nav-routes").Click(); err != nil {
		t.Fatal(err)
	}
	chip := page.Locator(`tr[data-route="old"] code.missing`)
	if err = chip.WaitFor(); err != nil {
		t.Fatal(err)
	}
	if text, _ := chip.TextContent(); text != "db-hook (missing)" {
		t.Errorf("chip = %q", text)
	}
}

func TestUIBrowserEmptyRouteSinksShowsError(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	openRoutes(t, page)
	if err := page.Locator("#add-route").Click(); err != nil {
		t.Fatal(err)
	}
	fill(t, page, "#r-name", "empty")
	if err := page.Locator("#route-save").Click(); err != nil {
		t.Fatal(err)
	}
	if _, err := page.WaitForFunction(
		`() => document.querySelector('#route-form .field[data-field="sinks"] .error')?.textContent.includes("sinks must not be empty")`,
		nil,
	); err != nil {
		body, _ := page.Locator("#route-form").TextContent()
		t.Fatalf("field error: %v\nform: %s", err, body)
	}
	if !jsBool(t, page, `() => document.querySelector("#route-editor").open`) {
		t.Error("editor closed")
	}
	if n := countOf(t, page.Locator(`tr[data-route="empty"]`)); n != 0 {
		t.Errorf("empty route was saved (%d rows)", n)
	}
}

func TestUIBrowserPreviewFollowsRouteOnEdit(t *testing.T) {
	t.Parallel()
	page := routesPage(t)
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "moving", "10.9.0.9")
	addCheck(t, page, "icmp")
	save(t, page)
	edit := page.Locator(`button[aria-label="Edit moving"]`)
	if err := edit.WaitFor(); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	err := page.Route("**/api/v1/routes", func(r playwright.Route) {
		<-release
		_ = r.Continue()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = edit.Click(); err != nil {
		t.Fatal(err)
	}
	chooseRoute(t, page, "route:nowhere")
	close(release)
	sel := `#preview-down [data-sink="ops-slack"][data-outcome="dropped"]`
	wait := playwright.LocatorWaitForOptions{State: nil, Timeout: new(1500.0)}
	if err = page.Locator(sel).WaitFor(wait); err != nil {
		body, _ := page.Locator("#route-preview").TextContent()
		t.Fatalf("preview did not follow the route within 1.5s: %v\npreview: %s", err, body)
	}
	if v, _ := page.Locator("#f-route").InputValue(); v != "route:nowhere" {
		t.Errorf("routing field = %q after routes loaded, want route:nowhere", v)
	}
}

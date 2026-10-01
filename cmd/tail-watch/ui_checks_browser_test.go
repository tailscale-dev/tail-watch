package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

func choose(t *testing.T, sel playwright.Locator, value string) {
	t.Helper()
	values := []string{value}
	if _, err := sel.SelectOption(playwright.SelectOptionValues{
		ValuesOrLabels: nil, Values: &values, Indexes: nil, Labels: nil, Elements: nil,
	}); err != nil {
		t.Fatal(err)
	}
}

func checkRowLoc(page playwright.Page, i int) playwright.Locator {
	return page.Locator(".check-row").Nth(i)
}

func countOf(t *testing.T, loc playwright.Locator) int {
	t.Helper()
	n, err := loc.Count()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// addCheck appends a row of the given type and returns its index.
func addCheck(t *testing.T, page playwright.Page, kind string) int {
	t.Helper()
	if err := page.Locator("#add-check").Click(); err != nil {
		t.Fatal(err)
	}
	i := countOf(t, page.Locator(".check-row")) - 1
	if kind != "icmp" {
		choose(t, checkRowLoc(page, i).Locator(".check-type"), kind)
	}
	return i
}

func fillRow(t *testing.T, page playwright.Page, i int, field, value string) {
	t.Helper()
	if err := checkRowLoc(page, i).Locator(".check-" + field).Fill(value); err != nil {
		t.Fatal(err)
	}
}

func startAdd(t *testing.T, page playwright.Page, name, addr string) {
	t.Helper()
	if err := page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	fill(t, page, "#f-name", name)
	fill(t, page, "#f-addr", addr)
}

func save(t *testing.T, page playwright.Page) {
	t.Helper()
	if err := page.Locator("#save").Click(); err != nil {
		t.Fatal(err)
	}
}

// stubProbe answers POST /api/v1/probe with body as the results list, so a
// save is gated on a scripted probe instead of real network checks. body is
// a pointer so a test can change the answer between saves.
func stubProbe(t *testing.T, page playwright.Page, body *string) {
	t.Helper()
	if err := page.Route("**/api/v1/probe", func(r playwright.Route) {
		_ = r.Fulfill(playwright.RouteFulfillOptions{
			Status:      new(200),
			ContentType: new("application/json"),
			Body:        `{"results":` + *body + `}`,
			Headers:     nil,
			Path:        nil,
			Response:    nil,
		})
	}); err != nil {
		t.Fatal(err)
	}
}

const probeOK = `[{"check":"icmp","ok":true,"latency_ms":1}]`

// savedChecks reads a monitor's stored checks from the API, or nil if absent.
func savedChecks(t *testing.T, page playwright.Page, name string) []string {
	t.Helper()
	raw, err := page.Evaluate(`fetch("/api/v1/targets").then((r) => r.text())`)
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Name   string   `json:"name"`
		Checks []string `json:"checks"`
	}
	if err = json.Unmarshal([]byte(raw.(string)), &list); err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.Name == name {
			return m.Checks
		}
	}
	return nil
}

func wantSaved(t *testing.T, page playwright.Page, name string, want ...string) {
	t.Helper()
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if got := savedChecks(t, page, name); !slices.Equal(got, want) {
		t.Errorf("checks of %s = %v, want %v", name, got, want)
	}
}

func fieldError(t *testing.T, page playwright.Page) string {
	t.Helper()
	loc := page.Locator(`.field[data-field="checks"] > .error`)
	if err := loc.WaitFor(); err != nil {
		t.Fatal(err)
	}
	txt, err := loc.TextContent()
	if err != nil {
		t.Fatal(err)
	}
	return txt
}

func TestUIBrowserChecksStartEmpty(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	if err := page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, page.Locator(".check-row")); n != 0 {
		t.Errorf("%d check rows on a new form", n)
	}
	if visible, _ := page.Locator("#add-check").IsVisible(); !visible {
		t.Error("Add check is not visible")
	}
}

func TestUIBrowserEmptyChecksFailThenCheckSucceeds(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	startAdd(t, page, "lab-nas", "lab-nas")

	save(t, page)
	if msg := fieldError(t, page); !strings.Contains(msg, "checks must not be empty") {
		t.Errorf("empty save error = %q", msg)
	}
	if got := savedChecks(t, page, "lab-nas"); got != nil {
		t.Fatalf("empty save created a monitor with checks %v", got)
	}

	probe := probeOK
	stubProbe(t, page, &probe)
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "445")
	save(t, page)
	wantSaved(t, page, "lab-nas", "tcp:445")
}

func TestUIBrowserRemoveCheckRow(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "lab-nas")
	addCheck(t, page, "icmp")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "445")

	if err := page.Locator(`button[aria-label="Remove check 1"]`).Click(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, page.Locator(".check-row")); n != 1 {
		t.Fatalf("%d rows after removing one of two", n)
	}
	save(t, page)
	wantSaved(t, page, "lab-nas", "tcp:445")
}

func TestUIBrowserIncompleteRowReportedOnItsRow(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	startAdd(t, page, "lab-nas", "lab-nas")
	addCheck(t, page, "icmp")
	addCheck(t, page, "tcp")
	save(t, page)

	if err := checkRowLoc(page, 1).Locator(".check-error").WaitFor(); err != nil {
		t.Fatal(err)
	}
	msg, _ := checkRowLoc(page, 1).Locator(".check-error").TextContent()
	if !strings.Contains(msg, "invalid TCP port") {
		t.Errorf("row 2 error = %q", msg)
	}
	if n := countOf(t, page.Locator(".check-row.invalid")); n != 1 {
		t.Errorf("%d rows marked invalid, want 1", n)
	}
	if cls, _ := checkRowLoc(page, 0).GetAttribute("class"); strings.Contains(cls, "invalid") {
		t.Error("the ICMP row is marked invalid")
	}
	if got := savedChecks(t, page, "lab-nas"); got != nil {
		t.Errorf("rejected save created %v", got)
	}
}

func TestUIBrowserHTTPRowBuildsURLFromAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, addr string
		scheme     string
		port, path string
		text       string
		want       string
	}{
		{"full", "10.20.0.50", "https", "5001", "/", "DSM", "http:https://10.20.0.50:5001/|DSM"},
		{"ipv6", "fd7a:115c:a1e0::1", "http", "", "/", "", "http:http://[fd7a:115c:a1e0::1]/"},
		{"path without slash", "10.20.0.50", "http", "", "status", "", "http:http://10.20.0.50/status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := uiPage(t, uiPeers())
			probe := probeOK
			stubProbe(t, page, &probe)
			startAdd(t, page, "web", tc.addr)
			i := addCheck(t, page, "http")
			row := checkRowLoc(page, i)
			choose(t, row.Locator(".check-scheme"), tc.scheme)
			fillRow(t, page, i, "port", tc.port)
			fillRow(t, page, i, "path", tc.path)
			fillRow(t, page, i, "text", tc.text)
			save(t, page)
			wantSaved(t, page, "web", tc.want)
		})
	}
}

func TestUIBrowserHTTPHostFollowsAddressChange(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "web", "nas")
	addCheck(t, page, "http")
	fill(t, page, "#f-addr", "nas2")
	save(t, page)
	wantSaved(t, page, "web", "http:http://nas2/")
}

func TestUIBrowserEditShowsRowsAndKeepsThem(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "web", "10.20.0.50")
	addCheck(t, page, "icmp")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "445")
	j := addCheck(t, page, "http")
	choose(t, checkRowLoc(page, j).Locator(".check-scheme"), "https")
	fillRow(t, page, j, "port", "5001")
	fillRow(t, page, j, "text", "DSM")
	save(t, page)
	wantSaved(t, page, "web", "icmp", "tcp:445", "http:https://10.20.0.50:5001/|DSM")

	if err := page.Locator(`button[aria-label="Edit web"]`).Click(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, page.Locator(".check-row")); n != 3 {
		t.Fatalf("%d rows on edit, want 3", n)
	}
	for i, want := range []string{"icmp", "tcp", "http"} {
		got, err := checkRowLoc(page, i).Locator(".check-type").InputValue()
		if err != nil || got != want {
			t.Errorf("row %d type = %q (%v), want %q", i+1, got, err, want)
		}
	}
	if got, _ := checkRowLoc(page, 1).Locator(".check-port").InputValue(); got != "445" {
		t.Errorf("TCP port = %q", got)
	}
	if got, _ := checkRowLoc(page, 2).Locator(".check-scheme").InputValue(); got != "https" {
		t.Errorf("HTTP scheme = %q", got)
	}
	if got, _ := checkRowLoc(page, 2).Locator(".check-text").InputValue(); got != "DSM" {
		t.Errorf("HTTP text = %q", got)
	}
}

// A stored check the rows cannot express comes from a hand-edited
// targets.json, which the API would refuse to create, so the list is stubbed.
func TestUIBrowserCustomRowKeepsUnparseableCheck(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	stored := `[{"name":"old","addr":"nas.lab","site":"","severity":"page","parent":"","insecure_tls":false,
"interval":"30s","timeout":"5s","fail_threshold":3,"recover_threshold":2,"flap_limit":4,"flap_window":"15m0s",
"network":"tailnet","checks":["tls:443","http:http://other.lab/"],"source":"ui","readonly":false,"state":"UP",
"definition":{"name":"old","addr":"nas.lab","checks":["tls:443","http:http://other.lab/"]}}]`
	if err := page.Route("**/api/v1/targets", func(r playwright.Route) {
		_ = r.Fulfill(playwright.RouteFulfillOptions{
			Status: new(200), ContentType: new("application/json"), Body: stored,
			Headers: nil, Path: nil, Response: nil,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(`button[aria-label="Edit old"]`).Click(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"tls:443", "http:http://other.lab/"} {
		row := checkRowLoc(page, i)
		if typ, _ := row.Locator(".check-type").InputValue(); typ != "custom" {
			t.Errorf("row %d type = %q, want custom", i+1, typ)
		}
		if raw, _ := row.Locator(".check-raw").InputValue(); raw != want {
			t.Errorf("row %d raw = %q, want %q", i+1, raw, want)
		}
	}
	if err := page.Locator("#check").Click(); err != nil {
		t.Fatal(err)
	}
	if err := checkRowLoc(page, 0).Locator(".check-error").WaitFor(); err != nil {
		t.Fatal(err)
	}
	msg, _ := checkRowLoc(page, 0).Locator(".check-error").TextContent()
	if !strings.Contains(msg, "tls checks are not supported") {
		t.Errorf("row 1 error = %q", msg)
	}
}

func TestUIBrowserCheckRowsAreNamedForAssistiveTech(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	startAdd(t, page, "lab-nas", "lab-nas")
	addCheck(t, page, "tcp")
	for _, label := range []string{"Check 1", "Check 1 type", "Check 1 port", "Remove check 1"} {
		if n := countOf(
			t,
			page.GetByLabel(label, playwright.PageGetByLabelOptions{Exact: new(true)}),
		); n != 1 {
			t.Errorf("%d elements labelled %q, want 1", n, label)
		}
	}
}

func TestUIBrowserDuplicateRowReportedOnSecondRow(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	startAdd(t, page, "lab-nas", "lab-nas")
	addCheck(t, page, "icmp")
	addCheck(t, page, "icmp")
	save(t, page)

	if err := checkRowLoc(page, 1).Locator(".check-error").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if msg, _ := checkRowLoc(page, 1).Locator(".check-error").TextContent(); !strings.Contains(msg, "duplicate check") {
		t.Errorf("row 2 error = %q", msg)
	}
	if n := countOf(t, page.Locator(".check-row.invalid")); n != 1 {
		t.Errorf("%d rows marked invalid, want 1", n)
	}
}

// probeTCPRefused is a probe answer whose only check was refused.
const probeTCPRefused = `[{"check":"tcp:443","ok":false,"latency_ms":3,` +
	`"error":"dial tcp 10.0.0.9:443: connect: connection refused"}]`

// probeDialogWait waits for the probe confirmation dialog to open.
func probeDialogWait(t *testing.T, page playwright.Page) {
	t.Helper()
	if err := page.Locator("#probe-confirm[open]").WaitFor(); err != nil {
		t.Fatal(err)
	}
}

func TestUIBrowserProbeGateSavesOnPass(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	var consoleErrs []string
	page.OnConsole(func(m playwright.ConsoleMessage) {
		if m.Type() == "error" {
			consoleErrs = append(consoleErrs, m.Text())
		}
	})
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "lab-nas")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "445")
	save(t, page)
	wantSaved(t, page, "lab-nas", "tcp:445")
	if n := countOf(t, page.Locator("#probe-confirm[open]")); n != 0 {
		t.Error("the confirmation opened for a passing probe")
	}
	if len(consoleErrs) > 0 {
		t.Errorf("console errors: %v", consoleErrs)
	}
}

func TestUIBrowserProbeGateConfirmSavesAnyway(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeTCPRefused
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "10.0.0.9")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "443")
	save(t, page)

	probeDialogWait(t, page)
	listed, listErr := page.Locator("#probe-failures").TextContent()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if !strings.Contains(listed, "tcp:443") || !strings.Contains(listed, "connection refused") {
		t.Errorf("failures = %q", listed)
	}
	if got := savedChecks(t, page, "lab-nas"); got != nil {
		t.Fatalf("an unconfirmed save created %v", got)
	}
	if err := page.Locator("#probe-confirm-yes").Click(); err != nil {
		t.Fatal(err)
	}
	wantSaved(t, page, "lab-nas", "tcp:443")
}

func TestUIBrowserProbeGateDeclineKeepsForm(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeTCPRefused
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "10.0.0.9")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "443")
	save(t, page)

	probeDialogWait(t, page)
	if err := page.Locator("#probe-confirm-no").Click(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, page.Locator("#probe-confirm[open]")); n != 0 {
		t.Fatal("the confirmation stayed open after declining")
	}
	wantField(t, page, "#f-name", "lab-nas")
	wantField(t, page, "#f-addr", "10.0.0.9")
	if got, _ := checkRowLoc(page, 0).Locator(".check-port").InputValue(); got != "443" {
		t.Errorf("TCP port = %q, want 443", got)
	}
	if n := countOf(t, page.Locator(".check-row.invalid")); n != 1 {
		t.Fatalf("%d rows marked invalid, want 1", n)
	}
	if msg, _ := checkRowLoc(
		page,
		0,
	).Locator(".check-error").
		TextContent(); !strings.Contains(
		msg,
		"connection refused",
	) {
		t.Errorf("row error = %q", msg)
	}
	if got := savedChecks(t, page, "lab-nas"); got != nil {
		t.Fatalf("a declined save created %v", got)
	}
}

func TestUIBrowserProbeGateEditIsGated(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeOK
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "10.0.0.9")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "445")
	save(t, page)
	wantSaved(t, page, "lab-nas", "tcp:445")

	if err := page.Locator(`button[aria-label="Edit lab-nas"]`).Click(); err != nil {
		t.Fatal(err)
	}
	fillRow(t, page, 0, "port", "443")
	probe = probeTCPRefused
	save(t, page)

	probeDialogWait(t, page)
	if err := page.Locator("#probe-confirm-yes").Click(); err != nil {
		t.Fatal(err)
	}
	wantSaved(t, page, "lab-nas", "tcp:443")
}

func TestUIBrowserCheckShowsProbeResults(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := `[{"check":"icmp","ok":true,"latency_ms":12},` +
		`{"check":"tcp:443","ok":false,"latency_ms":3,"error":"connection refused"}]`
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "10.0.0.9")
	addCheck(t, page, "icmp")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "443")

	if err := page.Locator("#check").Click(); err != nil {
		t.Fatal(err)
	}
	result := page.Locator("#check-result")
	if err := result.WaitFor(); err != nil {
		t.Fatal(err)
	}
	text, err := result.TextContent()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "icmp: ok (12 ms)") {
		t.Errorf("passing check missing its latency: %q", text)
	}
	if !strings.Contains(text, "tcp:443: failed (connection refused)") {
		t.Errorf("failing check missing its error: %q", text)
	}
	for line, want := range map[string]string{"icmp: ok": "ok", "tcp:443: failed": "failed"} {
		loc := result.Locator("span", playwright.LocatorLocatorOptions{
			Has: nil, HasNot: nil, HasNotText: nil, HasText: line,
		})
		if cls, _ := loc.GetAttribute("class"); cls != want {
			t.Errorf("%q line class = %q, want %q", line, cls, want)
		}
	}
	if got := savedChecks(t, page, "lab-nas"); got != nil {
		t.Fatalf("the check control saved %v", got)
	}
}

// recordTargetWrites counts the create and replace requests the page sends.
func recordTargetWrites(page playwright.Page) *atomic.Int32 {
	var n atomic.Int32
	page.OnRequest(func(r playwright.Request) {
		if strings.Contains(r.URL(), "/api/v1/targets") && r.Method() != http.MethodGet {
			n.Add(1)
		}
	})
	return &n
}

func TestUIBrowserProbeUnreachableSavesNothing(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	if err := page.Route("**/api/v1/probe", func(r playwright.Route) { _ = r.Abort() }); err != nil {
		t.Fatal(err)
	}
	writes := recordTargetWrites(page)
	startAdd(t, page, "lab-nas", "10.0.0.9")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "443")
	save(t, page)

	formError := page.Locator("#form-error")
	if err := formError.WaitFor(); err != nil {
		t.Fatal(err)
	}
	if msg, _ := formError.TextContent(); !strings.Contains(msg, "Could not reach the collector") {
		t.Errorf("form error = %q", msg)
	}
	if n := countOf(t, page.Locator("#probe-confirm[open]")); n != 0 {
		t.Error("the confirmation opened for a failed probe request")
	}
	if n := writes.Load(); n != 0 {
		t.Errorf("%d target writes after a failed probe request, want 0", n)
	}
}

// The probe is not stubbed: the real handler answers 422 before any check runs.
func TestUIBrowserProbeInvalidShowsRowError(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	writes := recordTargetWrites(page)
	probed := make(chan int, 1)
	page.OnResponse(func(r playwright.Response) {
		if strings.HasSuffix(r.URL(), "/api/v1/probe") {
			select {
			case probed <- r.Status():
			default:
			}
		}
	})
	startAdd(t, page, "lab-nas", "10.0.0.9")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "70000")
	save(t, page)

	select {
	case status := <-probed:
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("probe status = %d, want 422", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no probe response")
	}
	row := checkRowLoc(page, i)
	if err := row.Locator(".check-error").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if msg, _ := row.Locator(".check-error").TextContent(); !strings.Contains(msg, "invalid TCP port") {
		t.Errorf("row error = %q", msg)
	}
	if n := countOf(t, page.Locator("#probe-confirm[open]")); n != 0 {
		t.Error("the confirmation opened for an invalid definition")
	}
	if n := writes.Load(); n != 0 {
		t.Errorf("%d target writes after a 422 probe, want 0", n)
	}
}

func TestUIBrowserProbeEscapeDeclines(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeTCPRefused
	stubProbe(t, page, &probe)
	startAdd(t, page, "lab-nas", "10.0.0.9")
	i := addCheck(t, page, "tcp")
	fillRow(t, page, i, "port", "443")
	save(t, page)

	probeDialogWait(t, page)
	if err := page.Keyboard().Press("Escape"); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(".check-row.invalid").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, page.Locator("#probe-confirm[open]")); n != 0 {
		t.Fatal("the confirmation stayed open after Escape")
	}
	msg, _ := checkRowLoc(page, i).Locator(".check-error").TextContent()
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("row error = %q", msg)
	}
	if got := savedChecks(t, page, "lab-nas"); got != nil {
		t.Fatalf("an escaped confirmation created %v", got)
	}
}

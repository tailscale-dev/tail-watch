package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
	"tailscale.com/types/opt"
)

// uiPage serves the embedded UI over a real manager with the given peers and
// returns a page on it. The test skips when the Playwright driver or its
// browser is not installed; see docs/contributing/ui-browser-test.md.
func uiPage(t *testing.T, peers peerSource) playwright.Page {
	t.Helper()
	return uiPageOn(t, newAPIEnv(t), peers)
}

func uiPageOn(t *testing.T, e *apiEnv, peers peerSource) playwright.Page {
	t.Helper()
	e.mgr.peers = peers
	mux := http.NewServeMux()
	e.mgr.routes(mux)
	mux.Handle("/ui/", uiHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	page := browserPage(t)
	if _, err := page.Goto(srv.URL + "/ui/"); err != nil {
		t.Fatal(err)
	}
	return page
}

// browserPage returns a blank Chromium page, or skips the test when the
// Playwright driver or its browser is not installed.
func browserPage(t *testing.T) playwright.Page {
	t.Helper()
	pw, err := playwright.Run()
	if err != nil {
		t.Skipf("playwright driver not installed: %v", err)
	}
	t.Cleanup(func() { _ = pw.Stop() })
	browser, err := pw.Chromium.Launch()
	if err != nil {
		t.Skipf("chromium not installed: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func uiPeers() peerSource {
	peers := peerListFixture()
	peers = append(peers, peerInfo{
		NodeID:     3,
		StableID:   "",
		Name:       "<img src=x onerror=window.pwned=1>",
		DNSName:    "",
		HostName:   "",
		IPs:        []netip.Addr{netip.MustParseAddr("100.64.0.9")},
		Routes:     nil,
		AllowedIPs: nil,
		Tags:       []string{"tag:<b>x</b>"},
		Online:     opt.Bool(""),
	})
	return &fakePeerSource{peers: peers, seeded: true}
}

// switchingIdentity is a fake tailscaled whose answer the test can change
// after the page has loaded.
type switchingIdentity struct {
	mu  sync.Mutex
	who *apitype.WhoIsResponse
	err error
}

func (s *switchingIdentity) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.who, s.err
}

func (s *switchingIdentity) WhoIsForService(
	context.Context, string, tailcfg.ServiceName,
) (*apitype.WhoIsResponse, error) {
	return s.WhoIs(context.Background(), "")
}

func (s *switchingIdentity) set(who *apitype.WhoIsResponse) {
	s.mu.Lock()
	s.who = who
	s.err = nil
	s.mu.Unlock()
}

func roleWho(grant string) *apitype.WhoIsResponse {
	return whoisCaller("alice@example.com", "phone", nil, grant)
}

// uiRolePage serves the UI with authorization on. Requests without
// X-Forwarded-For get one, which is what tailscale serve adds and what the
// resolver requires on a loopback connection.
func uiRolePage(t *testing.T, e *apiEnv, src *switchingIdentity) playwright.Page {
	t.Helper()
	e.mgr.authz = roleResolver(src, "", true)
	e.mgr.authorize = e.mgr.authorizeRequest
	e.mgr.peers = uiPeers()
	mux := http.NewServeMux()
	e.mgr.routes(mux)
	mux.Handle("/ui/", uiHandler())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") == "" {
			r.Header.Set("X-Forwarded-For", "100.64.0.7")
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	page := browserPage(t)
	if _, err := page.Goto(srv.URL + "/ui/"); err != nil {
		t.Fatal(err)
	}
	return page
}

const uiPollInterval = 5 * time.Second

func waitVisibleWithin(t *testing.T, page playwright.Page, sel string, d time.Duration) {
	t.Helper()
	if err := page.Locator(sel).First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: new(float64(d.Milliseconds())),
	}); err != nil {
		t.Fatalf("%s not visible within %s: %v", sel, d, err)
	}
}

func waitHidden(t *testing.T, page playwright.Page, sel string) {
	t.Helper()
	if err := page.Locator(sel).WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: nil,
	}); err != nil {
		t.Fatalf("%s stayed visible: %v", sel, err)
	}
}

func TestUIBrowserViewerIsReadOnly(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"editor"}`), err: nil}
	e.mgr.authz = roleResolver(src, "", true)
	e.mgr.authorize = e.mgr.authorizeRequest
	if status, body := e.do(t, http.MethodPost, "/api/v1/targets", labNAS,
		withHeader("X-Forwarded-For", "100.64.0.7")); status != http.StatusCreated {
		t.Fatalf("seed monitor %d %s", status, body)
	}
	const routeBody = `{"name":"ui-team","sinks":["hook"]}`
	if status, body := e.do(t, http.MethodPost, "/api/v1/routes", routeBody,
		withHeader("X-Forwarded-For", "100.64.0.7")); status != http.StatusCreated {
		t.Fatalf("seed route %d %s", status, body)
	}
	src.set(roleWho(`{"role":"viewer"}`))
	page := uiRolePage(t, e, src)
	waitHidden(t, page, "#add")
	if err := page.Locator("[data-name=lab-nas]").WaitFor(); err != nil {
		t.Fatal(err)
	}
	n, err := page.Locator("[aria-label^='Edit']").Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("viewer sees %d edit buttons", n)
	}
	who, whoErr := page.Locator("#who").TextContent()
	if whoErr != nil || who != "alice@example.com · viewer" {
		t.Fatalf("who = %q, %v", who, whoErr)
	}
	if _, navErr := page.Goto(page.URL() + "#routes"); navErr != nil {
		t.Fatal(navErr)
	}
	if waitErr := page.Locator("[data-route=ui-team]").WaitFor(); waitErr != nil {
		t.Fatal(waitErr)
	}
	waitHidden(t, page, "#add-route")
	n, err = page.Locator("[aria-label^='Edit']").Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("viewer routes page sees %d edit buttons", n)
	}
}

func TestUIBrowserEmptyRoutesHintFollowsRole(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		role     string
		wantHint bool
	}{
		{"viewer", false},
		{"editor", true},
	} {
		t.Run(tt.role, func(t *testing.T) {
			t.Parallel()
			e := newAPIEnv(t)
			src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"` + tt.role + `"}`), err: nil}
			page := uiRolePage(t, e, src)
			if err := page.Locator("#who").WaitFor(); err != nil {
				t.Fatal(err)
			}
			if _, err := page.Goto(page.URL() + "#routes"); err != nil {
				t.Fatal(err)
			}
			if err := page.Locator("#routes-empty").WaitFor(); err != nil {
				t.Fatal(err)
			}
			visible, err := page.Locator("#routes-empty .write-hint").IsVisible()
			if err != nil || visible != tt.wantHint {
				t.Fatalf("add hint visible = %v (err %v), want %v", visible, err, tt.wantHint)
			}
		})
	}
}

func TestUIBrowserEditorCanAdd(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"editor"}`), err: nil}
	page := uiRolePage(t, e, src)
	if err := page.Locator("#who").WaitFor(); err != nil {
		t.Fatal(err)
	}
	openAddForm(t, page)
	who, err := page.Locator("#who").TextContent()
	if err != nil || who != "alice@example.com · editor" {
		t.Fatalf("who = %q, %v", who, err)
	}
}

func TestUIBrowserNoRoleShowsMessage(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"owner"}`), err: nil}
	page := uiRolePage(t, e, src)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	text, err := page.Locator("#banner").TextContent()
	if err != nil || !strings.Contains(text, "has not granted this user a role") {
		t.Fatalf("banner %q, %v", text, err)
	}
	n, err := page.Locator("[data-name]").Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("no-role page lists %d monitors", n)
	}
	waitHidden(t, page, "#add")
}

func TestUIBrowserBlockedPageRecoversWhenRoleIsGranted(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"owner"}`), err: nil}
	page := uiRolePage(t, e, src)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	src.set(roleWho(`{"role":"editor"}`))
	waitVisibleWithin(t, page, "#add", 3*uiPollInterval)
	waitHidden(t, page, "#banner")
	if err := page.Locator("[data-name]").First().WaitFor(); err != nil {
		t.Fatalf("monitors did not appear after the grant: %v", err)
	}
}

func TestUIBrowserUnidentifiedCallerIsToldWhy(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: nil, err: errors.New("tailscaled down")}
	page := uiRolePage(t, e, src)
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	text, err := page.Locator("#banner").TextContent()
	if err != nil || !strings.Contains(text, "could not identify you") || strings.Contains(text, "has not granted") {
		t.Fatalf("banner %q, %v", text, err)
	}
	src.set(roleWho(`{"role":"viewer"}`))
	waitVisibleWithin(t, page, "[data-name]", 3*uiPollInterval)
	waitHidden(t, page, "#banner")
}

func TestUIBrowserFailedMeRequestDoesNotBlock(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"editor"}`), err: nil}
	page := uiRolePage(t, e, src)
	if err := page.Locator("#add").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if err := page.Route("**/api/v1/me", func(r playwright.Route) { _ = r.Abort() }); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("[data-name]").First().WaitFor(); err != nil {
		t.Fatalf("a failed /me request hid the monitors: %v", err)
	}
	text, _ := page.Locator("main").TextContent()
	if strings.Contains(text, "has not granted") {
		t.Fatalf("a failed /me request reported no role: %q", text)
	}
	if err := page.Unroute("**/api/v1/me"); err != nil {
		t.Fatal(err)
	}
	waitVisibleWithin(t, page, "#add", 3*uiPollInterval)
}

func TestUIBrowserRefusedSaveShowsRole(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	src := &switchingIdentity{mu: sync.Mutex{}, who: roleWho(`{"role":"editor"}`), err: nil}
	page := uiRolePage(t, e, src)
	openAddForm(t, page)
	fill(t, page, "#f-name", "lab-refused")
	fill(t, page, "#f-addr", "10.9.0.8")
	src.set(roleWho(`{"role":"viewer"}`))
	if err := page.Locator("#save").Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("#form-error").WaitFor(); err != nil {
		t.Fatal(err)
	}
	text, err := page.Locator("#form-error").TextContent()
	if err != nil || !strings.Contains(text, "role editor is required") {
		t.Fatalf("form error %q, %v", text, err)
	}
	e.mgr.mu.Lock()
	created := false
	for _, ent := range e.mgr.entries {
		if ent.Name == "lab-refused" {
			created = true
		}
	}
	e.mgr.mu.Unlock()
	if created {
		t.Fatal("a refused save created a monitor")
	}
}

func openAddForm(t *testing.T, page playwright.Page) {
	t.Helper()
	if err := page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	// The suggestions arrive after the form opens.
	if err := page.Locator("#peers option").First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateAttached,
		Timeout: nil,
	}); err != nil {
		t.Fatalf("no suggestions: %v", err)
	}
}

func fieldValue(t *testing.T, page playwright.Page, sel string) string {
	t.Helper()
	v, err := page.Locator(sel).InputValue()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func fill(t *testing.T, page playwright.Page, sel, value string) {
	t.Helper()
	if err := page.Locator(sel).Fill(value); err != nil {
		t.Fatal(err)
	}
}

func wantField(t *testing.T, page playwright.Page, sel, want string) {
	t.Helper()
	if got := fieldValue(t, page, sel); got != want {
		t.Errorf("%s = %q, want %q", sel, got, want)
	}
}

// Filling the address with a suggestion's value fires the same input event as
// choosing it from the native popup, which Playwright cannot drive.
func TestUIBrowserPickFillsNameAndAddress(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	openAddForm(t, page)

	fill(t, page, "#f-addr", "lab-nas")
	wantField(t, page, "#f-addr", "lab-nas")
	wantField(t, page, "#f-name", "lab-nas")

	fill(t, page, "#f-addr", "nyc-router")
	wantField(t, page, "#f-name", "nyc-router")
}

func TestUIBrowserTypedNameKept(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	openAddForm(t, page)

	fill(t, page, "#f-name", "nas")
	fill(t, page, "#f-addr", "lab-nas")
	wantField(t, page, "#f-name", "nas")
	wantField(t, page, "#f-addr", "lab-nas")
}

func TestUIBrowserFreeTextAddressLeavesName(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	openAddForm(t, page)

	fill(t, page, "#f-addr", "nas.lab")
	wantField(t, page, "#f-name", "")
}

func TestUIBrowserReopenedFormForgetsPickedName(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	openAddForm(t, page)
	fill(t, page, "#f-addr", "lab-nas")
	if err := page.Locator("#cancel").Click(); err != nil {
		t.Fatal(err)
	}
	openAddForm(t, page)
	wantField(t, page, "#f-name", "")
	fill(t, page, "#f-name", "mine")
	fill(t, page, "#f-addr", "nyc-router")
	wantField(t, page, "#f-name", "mine")
}

func TestUIBrowserEditKeepsName(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeOK
	stubProbe(t, page, &probe)
	openAddForm(t, page)
	fill(t, page, "#f-addr", "lab-nas")
	addCheck(t, page, "icmp")
	if err := page.Locator("#save").Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(`button[aria-label="Edit lab-nas"]`).Click(); err != nil {
		t.Fatal(err)
	}
	fill(t, page, "#f-addr", "nyc-router")
	wantField(t, page, "#f-name", "lab-nas")
}

func TestUIBrowserSuggestionLabelsAndRouteHint(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	openAddForm(t, page)

	label, err := page.Locator(`#peers option[value="nyc-router"]`).GetAttribute("label")
	if err != nil {
		t.Fatal(err)
	}
	if want := "100.64.0.1 · tag:router · online"; label != want {
		t.Errorf("label = %q, want %q", label, want)
	}

	hint := page.Locator("#addr-route")
	fill(t, page, "#f-addr", "10.20.0.50")
	if txt, _ := hint.TextContent(); txt != "Handled by nyc-router" {
		t.Errorf("hint = %q", txt)
	}
	fill(t, page, "#f-addr", "192.168.99.1")
	if visible, _ := hint.IsVisible(); visible {
		t.Error("hint visible for an address no peer routes")
	}
	wantField(t, page, "#f-parent", "")
}

func TestUIBrowserUnseededStillSubmits(t *testing.T) {
	t.Parallel()
	page := uiPage(t, &fakePeerSource{peers: peerListFixture(), seeded: false})
	probe := probeOK
	stubProbe(t, page, &probe)
	if err := page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	fill(t, page, "#f-name", "typed")
	fill(t, page, "#f-addr", "10.0.0.9")
	addCheck(t, page, "icmp")
	if n, _ := page.Locator("#peers option").Count(); n != 0 {
		t.Errorf("%d suggestions while unseeded", n)
	}
	if err := page.Locator("#save").Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if txt, _ := page.Locator("#banner").TextContent(); txt != "Saved typed." {
		t.Errorf("banner = %q", txt)
	}
}

func TestUIBrowserFailedFetchStillSubmits(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	probe := probeOK
	stubProbe(t, page, &probe)
	if err := page.Route("**/api/v1/peers", func(r playwright.Route) { _ = r.Abort() }); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("#add").Click(); err != nil {
		t.Fatal(err)
	}
	fill(t, page, "#f-name", "typed")
	fill(t, page, "#f-addr", "10.0.0.9")
	addCheck(t, page, "icmp")
	if err := page.Locator("#save").Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator("#banner").WaitFor(); err != nil {
		t.Fatal(err)
	}
	if txt, _ := page.Locator("#banner").TextContent(); txt != "Saved typed." {
		t.Errorf("banner = %q", txt)
	}
}

func TestUIBrowserHostilePeerNameIsText(t *testing.T) {
	t.Parallel()
	page := uiPage(t, uiPeers())
	openAddForm(t, page)
	fill(t, page, "#f-addr", "<img src=x onerror=window.pwned=1>")
	wantField(t, page, "#f-name", "<img src=x onerror=window.pwned=1>")
	if n, _ := page.Locator("img").Count(); n != 0 {
		t.Errorf("%d img elements created from a peer name", n)
	}
	pwned, err := page.Evaluate("window.pwned")
	if err != nil {
		t.Fatal(err)
	}
	if pwned != nil {
		t.Errorf("peer name ran script: %v", pwned)
	}
}

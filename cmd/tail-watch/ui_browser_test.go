package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/mxschmitt/playwright-go"
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

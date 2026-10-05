package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// renderCardJS renders one Adaptive Card the way the Adaptive Cards Designer's
// Teams host does: Microsoft's renderer, the Teams light host config, and
// markdown-it for TextBlock markdown. It returns the parse and validation
// messages, each TextBlock's text and color, and every link.
const renderCardJS = `([card, hostConfig]) => {
	AdaptiveCards.AdaptiveCard.onProcessMarkdown = (text, result) => {
		result.outputHtml = markdownit().render(text);
		result.didProcess = true;
	};
	const ac = new AdaptiveCards.AdaptiveCard();
	ac.hostConfig = new AdaptiveCards.HostConfig(hostConfig);
	const ctx = new AdaptiveCards.SerializationContext();
	ac.parse(card, ctx);
	const events = [];
	for (let i = 0; i < ctx.eventCount; i++) events.push(ctx.getEventAt(i).message);
	for (const e of ac.validateProperties().validationEvents) events.push(e.message);
	const el = ac.render();
	const root = document.getElementById("card");
	root.replaceChildren(el);
	return {
		events,
		blocks: [...el.querySelectorAll(".ac-textBlock")].map(b => ({
			text: b.innerText, color: getComputedStyle(b).color,
		})),
		links: [...el.querySelectorAll("a")].map(a => ({text: a.innerText, href: a.getAttribute("href")})),
	};
}`

type renderedCard struct {
	Events []string `json:"events"`
	Blocks []struct {
		Text  string `json:"text"`
		Color string `json:"color"`
	} `json:"blocks"`
	Links []struct {
		Text string `json:"text"`
		Href string `json:"href"`
	} `json:"links"`
}

func cardRenderPage(t *testing.T) (playwright.Page, map[string]any) {
	t.Helper()
	page := browserPage(t)
	if err := page.SetViewportSize(520, 900); err != nil {
		t.Fatal(err)
	}
	if err := page.SetContent(`<!doctype html><body style="margin:0;background:#f5f5f5">` +
		`<div id="card" style="width:500px;background:#fff"></div>`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("testdata", "teams", "render")
	for _, js := range []string{"adaptivecards.min.js", "markdown-it.umd.min.js"} {
		src, err := os.ReadFile(filepath.Join(dir, js))
		if err != nil {
			t.Fatal(err)
		}
		content := string(src)
		if _, err = page.AddScriptTag(playwright.PageAddScriptTagOptions{
			Content: &content, Path: nil, Type: nil, URL: nil,
		}); err != nil {
			t.Fatalf("load %s: %v", js, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "microsoft-teams-light.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hostConfig map[string]any
	if err = json.Unmarshal(raw, &hostConfig); err != nil {
		t.Fatal(err)
	}
	return page, hostConfig
}

func renderCard(t *testing.T, page playwright.Page, hostConfig map[string]any, golden []byte) renderedCard {
	t.Helper()
	var msg struct {
		Attachments []struct {
			Content map[string]any `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(golden, &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments %d", len(msg.Attachments))
	}
	out, err := page.Evaluate(renderCardJS, []any{msg.Attachments[0].Content, hostConfig})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var rc renderedCard
	if err = json.Unmarshal(b, &rc); err != nil {
		t.Fatal(err)
	}
	return rc
}

// hostColor is the CSS rgb() Chromium computes for a host config foreground
// color, which is #RRGGBB or #AARRGGBB.
func hostColor(t *testing.T, hostConfig map[string]any, name string) string {
	t.Helper()
	styles, _ := hostConfig["containerStyles"].(map[string]any)
	def, _ := styles["default"].(map[string]any)
	fg, _ := def["foregroundColors"].(map[string]any)
	c, _ := fg[name].(map[string]any)
	hex, _ := c["default"].(string)
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 8 {
		hex = hex[2:]
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil || len(hex) != 6 {
		t.Fatalf("host color %s: %q", name, hex)
	}
	return fmt.Sprintf("rgb(%d, %d, %d)", v>>16, v>>8&0xff, v&0xff)
}

// teamsHeadlineColors maps each golden card to the host config color of its
// headline.
//
//nolint:gochecknoglobals // A fixed test table.
var teamsHeadlineColors = map[string]string{
	"down": "attention", "escape": "attention", "long-error": "attention", "route-mention": "attention",
	"router-14-deps": "attention", "status-link": "attention",
	"recovered": "good", "flapping": "warning", "started": "default", "test": "default",
}

// TestTeamsCardRender renders every golden card in Chromium the way the
// Adaptive Cards Designer's Teams host does. It fails on a parse or validation
// message, a markdown escape that shows, a link other than the status link, a
// detail line merged into its neighbour, or a wrong headline color. Set
// TAIL_WATCH_CARD_SHOTS to a directory to save a PNG of each card.
func TestTeamsCardRender(t *testing.T) {
	t.Parallel()
	page, hostConfig := cardRenderPage(t)
	shots := os.Getenv("TAIL_WATCH_CARD_SHOTS")
	goldens, err := filepath.Glob(filepath.Join("testdata", "teams", "*.json"))
	if err != nil || len(goldens) != len(teamsHeadlineColors) {
		t.Fatalf("found %d Teams golden cards, want %d: %v", len(goldens), len(teamsHeadlineColors), err)
	}
	for _, path := range goldens {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		rc := renderCard(t, page, hostConfig, raw)
		checkRenderedCard(t, name, raw, rc, hostColor(t, hostConfig, teamsHeadlineColors[name]))
		if shots != "" {
			out := filepath.Join(shots, name+".png")
			if _, err = page.Locator("#card").Screenshot(playwright.LocatorScreenshotOptions{
				Animations: nil, Caret: nil, Mask: nil, MaskColor: nil, OmitBackground: nil, Path: &out,
				Quality: nil, Scale: nil, Style: nil, Timeout: nil, Type: nil,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func checkRenderedCard(t *testing.T, name string, raw []byte, rc renderedCard, headColor string) {
	t.Helper()
	if len(rc.Events) > 0 {
		t.Errorf("%s: renderer reported %q", name, rc.Events)
	}
	if len(rc.Blocks) < 2 {
		t.Fatalf("%s: %d text blocks", name, len(rc.Blocks))
	}
	if rc.Blocks[0].Color != headColor {
		t.Errorf("%s: headline color %s, want %s", name, rc.Blocks[0].Color, headColor)
	}
	var text strings.Builder
	for _, b := range rc.Blocks {
		text.WriteString(b.Text + "\n")
	}
	if strings.ContainsAny(text.String(), `\<>`) {
		t.Errorf("%s: markdown escape or markup shows:\n%s", name, text.String())
	}
	card := decodeCard(t, raw)
	if got, want := lineCount(rc.Blocks[1].Text), lineCount(card.Body[1].Text); got != want {
		t.Errorf("%s: detail shows %d lines, want %d:\n%s", name, got, want, rc.Blocks[1].Text)
	}
	wantLinks := 0
	if name == "status-link" {
		wantLinks = 1
		if len(rc.Links) != 1 || rc.Links[0].Text != "nyc-nas" ||
			rc.Links[0].Href != "https://monitor-nyc.example.ts.net:9109/#nyc-nas" {
			t.Errorf("%s: links %+v", name, rc.Links)
		}
	}
	if len(rc.Links) != wantLinks {
		t.Errorf("%s: links %+v, want %d", name, rc.Links, wantLinks)
	}
	if name == "escape" && !strings.Contains(text.String(), "see [phish](https://evil.example)") {
		t.Errorf("escape: the error does not show as typed:\n%s", text.String())
	}
}

func lineCount(s string) int {
	n := 0
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

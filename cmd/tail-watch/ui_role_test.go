package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runRoleBlock evaluates the role block of app.js followed by tail under bun
// or node. It skips when neither runtime is installed.
func runRoleBlock(t *testing.T, tail string) []byte {
	t.Helper()
	runtime, err := exec.LookPath("bun")
	if err != nil {
		if runtime, err = exec.LookPath("node"); err != nil {
			t.Skip("neither bun nor node is installed")
		}
	}
	src, err := os.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "// --- role:")
	end := strings.Index(text, "// --- end role ---")
	if start < 0 || end < start {
		t.Fatal("role block not found in app.js")
	}
	out, err := exec.CommandContext(t.Context(), runtime, "-e", text[start:end]+"\n"+tail+"\n").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

func TestUIRoleHelpers(t *testing.T) {
	t.Parallel()
	cases := []map[string]any{
		{"fn": "canWrite", "me": map[string]any{"authorization": true, "role": "editor"}, "want": true},
		{"fn": "canWrite", "me": map[string]any{"authorization": true, "role": "viewer"}, "want": false},
		{"fn": "canWrite", "me": map[string]any{"authorization": true, "role": ""}, "want": false},
		{"fn": "canWrite", "me": map[string]any{"authorization": false, "role": "editor"}, "want": true},
		{
			"fn":   "label",
			"me":   map[string]any{"authorization": true, "user": "alice@example.com", "role": "editor"},
			"want": "alice@example.com · editor",
		},
		{
			"fn":   "label",
			"me":   map[string]any{"authorization": true, "user": "alice@example.com", "role": "viewer"},
			"want": "alice@example.com · viewer",
		},
		{
			"fn":   "label",
			"me":   map[string]any{"authorization": true, "node": "db", "role": "viewer"},
			"want": "db · viewer",
		},
		{
			"fn":   "label",
			"me":   map[string]any{"authorization": true, "user": "alice@example.com", "role": ""},
			"want": "alice@example.com",
		},
		{
			"fn":   "label",
			"me":   map[string]any{"authorization": false, "role": "editor"},
			"want": "editor",
		},
		{
			"fn": "denial", "status": 403,
			"body": map[string]any{"errors": []any{"role editor is required"}},
			"want": "role editor is required",
		},
		{
			"fn": "denial", "status": 403,
			"body": map[string]any{"errors": []any{"no role was granted"}},
			"want": "no role was granted",
		},
		{"fn": "denial", "status": 403, "body": map[string]any{}, "want": "The tailnet policy refused this request."},
		{
			"fn": "blocked", "me": map[string]any{"authorization": true, "identified": false, "role": ""},
			"want": "tail-watch could not identify you. Open it through its Tailscale Service address, " +
				"not the collector's IP and port.",
		},
		{
			"fn": "blocked", "me": map[string]any{"authorization": true, "identified": true, "role": ""},
			"want": "The tailnet policy has not granted this user a role.",
		},
		{"fn": "denial", "status": 422, "body": map[string]any{"errors": []any{"name: required"}}, "want": ""},
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	tail := `
const cases = ` + string(raw) + `;
for (const c of cases) {
  let got;
  if (c.fn === "canWrite") got = canWrite(c.me);
  else if (c.fn === "label") got = roleLabel(c.me);
  else if (c.fn === "blocked") got = blockedMessage(c.me);
  else got = denialMessage(c.status, c.body);
  if (JSON.stringify(got) !== JSON.stringify(c.want)) {
    console.error(JSON.stringify({fn: c.fn, got, want: c.want}));
    process.exitCode = 1;
  }
}
`
	out := runRoleBlock(t, tail)
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("role helpers:\n%s", out)
	}
}

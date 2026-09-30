package main

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func checkRow(typ string, overrides map[string]string) map[string]string {
	row := map[string]string{"type": typ, "scheme": "http", "port": "", "path": "/", "text": "", "raw": ""}
	maps.Copy(row, overrides)
	return row
}

// runCheckConversion evaluates the check conversion block of app.js followed
// by tail under bun or node and returns what tail printed. It skips when
// neither runtime is installed.
func runCheckConversion(t *testing.T, tail string) []byte {
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
	start := strings.Index(text, "// --- check conversion")
	end := strings.Index(text, "// --- end check conversion ---")
	if start < 0 || end < start {
		t.Fatal("check conversion block not found in app.js")
	}
	out, err := exec.CommandContext(t.Context(), runtime, "-e", text[start:end]+"\n"+tail+"\n").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

type testCase struct {
	Fn   string            `json:"fn"`
	Addr string            `json:"addr"`
	Row  map[string]string `json:"row,omitempty"`
	S    string            `json:"s,omitempty"`
	want any
}

func toCase(addr string, row map[string]string, want string) testCase {
	return testCase{Fn: "to", Addr: addr, Row: row, S: "", want: want}
}

func fromCase(addr, s string, want map[string]string) testCase {
	return testCase{Fn: "from", Addr: addr, Row: nil, S: s, want: want}
}

// TestUICheckConversion runs checkToString and checkFromString from app.js
// under bun or node, so the row-to-string rules are checked without a browser.
func TestUICheckConversion(t *testing.T) {
	t.Parallel()
	tests := []testCase{
		toCase("", checkRow("icmp", nil), "icmp"),
		toCase("", checkRow("tsping", nil), "tsping"),
		toCase("", checkRow("tcp", map[string]string{"port": " 445 "}), "tcp:445"),
		toCase("", checkRow("tcp", nil), "tcp:"),
		toCase(
			"10.20.0.50",
			checkRow("http", map[string]string{"scheme": "https", "port": "5001", "text": "DSM"}),
			"http:https://10.20.0.50:5001/|DSM",
		),
		toCase("fd7a:115c:a1e0::1", checkRow("http", nil), "http:http://[fd7a:115c:a1e0::1]/"),
		toCase("[fd7a::1]", checkRow("http", nil), "http:http://[fd7a::1]/"),
		toCase("nas2", checkRow("http", map[string]string{"path": "status"}), "http:http://nas2/status"),
		toCase("nas", checkRow("http", map[string]string{"path": ""}), "http:http://nas/"),
		toCase("nas", checkRow("http", map[string]string{"text": "a|b"}), "http:http://nas/|a|b"),
		toCase("", checkRow("custom", map[string]string{"raw": " tls:443 "}), "tls:443"),

		fromCase("", "icmp", checkRow("icmp", nil)),
		fromCase("", "tsping", checkRow("tsping", nil)),
		fromCase("", "tcp:445", checkRow("tcp", map[string]string{"port": "445"})),
		fromCase("", "tcp:70000", checkRow("tcp", map[string]string{"port": "70000"})),
		fromCase("", "tcp:x", checkRow("custom", map[string]string{"raw": "tcp:x"})),
		fromCase("", "tls:443", checkRow("custom", map[string]string{"raw": "tls:443"})),
		fromCase(
			"10.20.0.50",
			"http:https://10.20.0.50:5001/|DSM",
			checkRow("http", map[string]string{"scheme": "https", "port": "5001", "text": "DSM"}),
		),
		fromCase("nas", "http:http://nas:80/", checkRow("http", map[string]string{"port": "80"})),
		fromCase("nas", "http:http://NAS./x", checkRow("http", map[string]string{"path": "/x"})),
		fromCase("nas", "http:http://nas", checkRow("http", map[string]string{"path": ""})),
		fromCase("fd7a:115c:a1e0::1", "http:http://[fd7a:115c:a1e0::1]/", checkRow("http", nil)),
		fromCase(
			"nas.lab",
			"http:http://other.lab/",
			checkRow("custom", map[string]string{"raw": "http:http://other.lab/"}),
		),
		fromCase("nas", "http:http://nas/#frag", checkRow("custom", map[string]string{"raw": "http:http://nas/#frag"})),
		fromCase("nas", "ftp:nas", checkRow("custom", map[string]string{"raw": "ftp:nas"})),
	}

	input, err := json.Marshal(tests)
	if err != nil {
		t.Fatal(err)
	}
	out := runCheckConversion(t, "const cases = "+string(input)+
		";\nconsole.log(JSON.stringify(cases.map((c) => c.fn === \"to\" ? checkToString(c.row, c.addr ?? \"\") : checkFromString(c.s, c.addr ?? \"\"))));")
	var got []any
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	if len(got) != len(tests) {
		t.Fatalf("got %d results, want %d", len(got), len(tests))
	}
	for i, tc := range tests {
		want := tc.want
		if row, ok := want.(map[string]string); ok {
			m := map[string]any{}
			for k, v := range row {
				m[k] = v
			}
			want = m
		}
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("%s(%q, %q, %v) = %v, want %v", tc.Fn, tc.Addr, tc.S, tc.Row, got[i], want)
		}
	}
}

// Every string a row can express, and every one it cannot, must come back unchanged.
func TestUICheckRoundTrip(t *testing.T) {
	t.Parallel()
	strs := []string{
		"icmp", "tsping", "tcp:445", "tcp:70000", "tls:443", "tcp:x",
		"http:https://10.20.0.50:5001/|DSM", "http:http://10.20.0.50/", "http:http://10.20.0.50:80/status?x=1|ok",
		"http:http://other.lab/", "http:http://10.20.0.50/#f",
	}
	input, _ := json.Marshal(strs)
	out := runCheckConversion(t, "const s = "+string(input)+
		";\nconsole.log(JSON.stringify(s.map((x) => checkToString(checkFromString(x, \"10.20.0.50\"), \"10.20.0.50\"))));")
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	for i, want := range strs {
		if got[i] != want {
			t.Errorf("round trip of %q = %q", want, got[i])
		}
	}
}

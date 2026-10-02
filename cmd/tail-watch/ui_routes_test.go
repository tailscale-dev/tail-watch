package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// runRouteForm evaluates the route-form block of app.js followed by tail
// under bun or node and returns what tail printed. It skips when neither
// runtime is installed.
func runRouteForm(t *testing.T, tail string) []byte {
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
	start := strings.Index(text, "// --- route form")
	end := strings.Index(text, "// --- end route form ---")
	if start < 0 || end < start {
		t.Fatal("route form block not found in app.js")
	}
	out, err := exec.CommandContext(t.Context(), runtime, "-e", text[start:end]+"\n"+tail+"\n").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

type routeJSCase struct {
	Fn      string         `json:"fn"`
	Route   map[string]any `json:"route,omitempty"`
	Reason  string         `json:"reason,omitempty"`
	Row     map[string]any `json:"row,omitempty"`
	Sinks   []any          `json:"sinks,omitempty"`
	Def     map[string]any `json:"def,omitempty"`
	Value   string         `json:"value,omitempty"`
	Monitor map[string]any `json:"monitor,omitempty"`
	Routes  []any          `json:"routes,omitempty"`
	want    any
}

func routeCase(
	fn string, route map[string]any, reason string, row map[string]any, sinks []any,
	def map[string]any, value string, monitor map[string]any, routes []any, want any,
) routeJSCase {
	return routeJSCase{
		Fn: fn, Route: route, Reason: reason, Row: row, Sinks: sinks, Def: def,
		Value: value, Monitor: monitor, Routes: routes, want: want,
	}
}

func TestUIRouteFormHelpers(t *testing.T) {
	t.Parallel()
	db := map[string]any{"name": "db-team", "severity": "page", "sinks": []any{"pagerduty", "ops-slack"}}
	tests := []routeJSCase{
		routeCase("label", db, "", nil, nil, nil, "", nil, nil, "db-team · page · pagerduty, ops-slack"),
		routeCase(
			"label",
			map[string]any{"name": "solo", "severity": "warn"},
			"",
			nil,
			nil,
			nil,
			"",
			nil,
			nil,
			"solo · warn",
		),
		routeCase("reason", nil, "not_in_route", nil, nil, nil, "", nil, nil, "not in route"),
		routeCase("reason", nil, "events", nil, nil, nil, "", nil, nil, "filtered by event"),
		routeCase("reason", nil, "min_severity", nil, nil, nil, "", nil, nil, "below this sink's minimum severity"),
		routeCase("reason", nil, "match.sites", nil, nil, nil, "", nil, nil, "filtered by site"),
		routeCase("reason", nil, "match.targets", nil, nil, nil, "", nil, nil, "filtered by target"),
		routeCase("reason", nil, "match.tags", nil, nil, nil, "", nil, nil, "filtered by tag"),
		routeCase("reason", nil, "missing_sink", nil, nil, nil, "", nil, nil, "missing from the config"),
		routeCase("words", nil, "", map[string]any{"outcome": "sent"}, nil, nil, "", nil, nil, "sent"),
		routeCase(
			"words",
			nil,
			"",
			map[string]any{"outcome": "dropped", "reason": "match.sites"},
			nil,
			nil,
			"",
			nil,
			nil,
			"filtered by site",
		),
		routeCase(
			"words",
			nil,
			"",
			map[string]any{"outcome": "unknown", "reason": "match.tags"},
			nil,
			nil,
			"",
			nil,
			nil,
			"unknown, filtered by tag",
		),
		routeCase("none", nil, "", nil, []any{}, nil, "", nil, nil, true),
		routeCase("none", nil, "", nil, []any{
			map[string]any{"outcome": "dropped"}, map[string]any{"outcome": "dropped"},
		}, nil, "", nil, nil, true),
		routeCase("none", nil, "", nil, []any{
			map[string]any{"outcome": "dropped"}, map[string]any{"outcome": "unknown"},
		}, nil, "", nil, nil, false),
		routeCase("none", nil, "", nil, []any{map[string]any{"outcome": "sent"}}, nil, "", nil, nil, false),
		routeCase("choice", nil, "", nil, nil, nil, "", nil, nil, "severity:page"),
		routeCase("choice", nil, "", nil, nil, map[string]any{}, "", nil, nil, "severity:page"),
		routeCase("choice", nil, "", nil, nil, map[string]any{"severity": "warn"}, "", nil, nil, "severity:warn"),
		routeCase(
			"choice",
			nil,
			"",
			nil,
			nil,
			map[string]any{"route": "db-team", "severity": "page"},
			"",
			nil,
			nil,
			"route:db-team",
		),
		routeCase("payload", nil, "", nil, nil, nil, "route:db-team", nil, nil, map[string]any{"route": "db-team"}),
		routeCase("payload", nil, "", nil, nil, nil, "severity:warn", nil, nil, map[string]any{"severity": "warn"}),
		routeCase("payload", nil, "", nil, nil, nil, "severity:page", nil, nil, map[string]any{"severity": "page"}),
		routeCase("text", nil, "", nil, nil, nil, "", map[string]any{"severity": "warn"}, nil, "warn · all sinks"),
		routeCase("text", nil, "", nil, nil, nil, "", map[string]any{"route": "db-team", "severity": "page"},
			[]any{map[string]any{"name": "db-team", "severity": "warn"}}, "db-team · warn"),
		routeCase(
			"text",
			nil,
			"",
			nil,
			nil,
			nil,
			"",
			map[string]any{"route": "db-team", "severity": "page"},
			nil,
			"db-team · page",
		),
	}
	input, err := json.Marshal(tests)
	if err != nil {
		t.Fatal(err)
	}
	script := `const cases = ` + string(input) + `;
console.log(JSON.stringify(cases.map((c) => {
  switch (c.fn) {
    case "label": return routeOptionLabel(c.route);
    case "reason": return reasonWords(c.reason);
    case "words": return previewWords(c.row);
    case "none": return downNotifiesNoOne(c.sinks);
    case "choice": return routingChoice(c.def);
    case "payload": return routingPayload(c.value);
    case "text": return routingText(c.monitor, c.routes);
    default: return null;
  }
})));`
	out := runRouteForm(t, script)
	var got []any
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	if len(got) != len(tests) {
		t.Fatalf("got %d results, want %d", len(got), len(tests))
	}
	for i, tc := range tests {
		if !reflect.DeepEqual(got[i], tc.want) {
			t.Errorf("%s = %#v, want %#v", tc.Fn, got[i], tc.want)
		}
	}
}

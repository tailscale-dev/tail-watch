package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestSidecarCopiesAgree keeps every embedded copy of the sidecar in step with
// examples/kubernetes/sidecar.yaml. The Kyverno policy and freeze-and-patch are
// compared field by field. The MutatingAdmissionPolicy's CEL and the how-to's
// Go handler cannot be parsed here, so they must contain every key and value of
// the reference. Each copy must also build the ConfigMap and Secret name from
// the ProxyGroup rather than hard-code it.
func TestSidecarCopiesAgree(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	var ref podFragment
	if err := yaml.Unmarshal([]byte(read("examples/kubernetes/sidecar.yaml")), &ref); err != nil {
		t.Fatal(err)
	}
	want := ref.withoutName(t, "tail-watch-ingress")

	var released map[string]string
	if err := json.Unmarshal([]byte(read(".release-please-manifest.json")), &released); err != nil {
		t.Fatal(err)
	}
	if got, wantImage := ref.Containers[0]["image"], "ghcr.io/tailscale-dev/tail-watch:"+released["."]; got != wantImage {
		t.Errorf("sidecar.yaml image %v, want %s (the version in .release-please-manifest.json)", got, wantImage)
	}

	structural := []struct {
		name, nameExpr string
		fragment       func(*testing.T) podFragment
	}{
		{
			name:     "kyverno",
			nameExpr: `tail-watch-{{ request.object.metadata.labels."tailscale.com/parent-resource" }}`,
			fragment: func(t *testing.T) podFragment {
				t.Helper()
				return kyvernoFragment(t, read("examples/kubernetes/kyverno-clusterpolicy.yaml"))
			},
		},
		{
			name:     "freeze-and-patch",
			nameExpr: "tail-watch-${PG}",
			fragment: func(t *testing.T) podFragment {
				t.Helper()
				return freezeFragment(t, read("examples/kubernetes/freeze-and-patch.sh"))
			},
		},
	}
	for _, tc := range structural {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.fragment(t).withoutName(t, tc.nameExpr); !reflect.DeepEqual(got, want) {
				t.Fatalf("sidecar differs from sidecar.yaml\n got %v\nwant %v", got, want)
			}
		})
	}

	textual := []struct {
		name, src, nameExpr string
		nameUses            int
	}{
		{
			name:     "mutatingadmissionpolicy",
			src:      read("examples/kubernetes/mutatingadmissionpolicy.yaml"),
			nameExpr: `"tail-watch-" + object.metadata.labels["tailscale.com/parent-resource"]`,
			nameUses: 3,
		},
		{
			name:     "webhook handler",
			src:      howToGoBlock(t, read("docs/how-to/kubernetes-proxygroup.md")),
			nameExpr: `"tail-watch-" + pod.Labels["tailscale.com/parent-resource"]`,
			nameUses: 1,
		},
	}
	for _, tc := range textual {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if n := strings.Count(tc.src, tc.nameExpr); n != tc.nameUses {
				t.Errorf("%s appears %d times, want %d", tc.nameExpr, n, tc.nameUses)
			}
			for _, tok := range fragmentTokens(want["container"], want["volume"]) {
				if !slices.ContainsFunc(tok.forms, func(f string) bool { return strings.Contains(tc.src, f) }) {
					t.Errorf("missing %s from sidecar.yaml", tok.label)
				}
			}
		})
	}
}

func kyvernoFragment(t *testing.T, src string) podFragment {
	t.Helper()
	var policy struct {
		Spec struct {
			Rules []struct {
				Mutate struct {
					PatchStrategicMerge struct {
						Spec podFragment `yaml:"spec"`
					} `yaml:"patchStrategicMerge"`
				} `yaml:"mutate"`
			} `yaml:"rules"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(src), &policy); err != nil {
		t.Fatal(err)
	}
	return policy.Spec.Rules[0].Mutate.PatchStrategicMerge.Spec
}

func freezeFragment(t *testing.T, src string) podFragment {
	t.Helper()
	m := regexp.MustCompile(`(?s)read -r -d '' PATCH <<EOF \|\| true\n(.*?)\nEOF\n`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("freeze-and-patch.sh has no PATCH heredoc")
	}
	var patch struct {
		Spec struct {
			Template struct {
				Spec podFragment `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	image := regexp.MustCompile(`(?m)^IMAGE="([^"]+)"`).FindStringSubmatch(src)
	if image == nil {
		t.Fatal("freeze-and-patch.sh has no IMAGE assignment")
	}
	patchJSON := strings.ReplaceAll(m[1], "${IMAGE}", image[1])
	if err := json.Unmarshal([]byte(patchJSON), &patch); err != nil {
		t.Fatal(err)
	}
	return patch.Spec.Template.Spec
}

type podFragment struct {
	Containers []map[string]any `yaml:"containers" json:"containers"`
	Volumes    []map[string]any `yaml:"volumes"    json:"volumes"`
}

// withoutName checks that the Secret and ConfigMap references are name, then
// blanks them and returns the fragment in JSON's types so YAML and JSON
// sources compare equal.
func (f podFragment) withoutName(t *testing.T, name string) map[string]any {
	t.Helper()
	if len(f.Containers) != 1 || len(f.Volumes) != 1 {
		t.Fatalf("want one container and one volume, got %d and %d", len(f.Containers), len(f.Volumes))
	}
	b, err := json.Marshal(map[string]any{"container": f.Containers[0], "volume": f.Volumes[0]})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if unmarshalErr := json.Unmarshal(b, &out); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	secretRef := out["container"].(map[string]any)["envFrom"].([]any)[0].(map[string]any)["secretRef"].(map[string]any)
	configMap := out["volume"].(map[string]any)["configMap"].(map[string]any)
	for what, ref := range map[string]map[string]any{"Secret": secretRef, "ConfigMap": configMap} {
		if ref["name"] != name {
			t.Errorf("%s name %v, want %s", what, ref["name"], name)
		}
		ref["name"] = ""
	}
	return out
}

type fragmentToken struct {
	label string
	forms []string
}

// fragmentTokens lists every key and every string or number value in parts,
// each with the spellings CEL and Go use for it.
func fragmentTokens(parts ...any) []fragmentToken {
	var out []fragmentToken
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				out = append(out, fragmentToken{label: "key " + k, forms: []string{k + ":", `"` + k + `"`}})
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			if x != "" {
				out = append(out, fragmentToken{label: "value " + x, forms: []string{`"` + x + `"`}})
			}
		case float64:
			s := fmt.Sprint(x)
			out = append(out, fragmentToken{label: "value " + s, forms: []string{s}})
		}
	}
	for _, p := range parts {
		walk(p)
	}
	return out
}

func howToGoBlock(t *testing.T, doc string) string {
	t.Helper()
	m := regexp.MustCompile("(?s)```go\n(.*?)```").FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("docs/how-to/kubernetes-proxygroup.md has no go block")
	}
	return m[1]
}

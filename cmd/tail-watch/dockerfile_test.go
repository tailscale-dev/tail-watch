package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestDockerfilesAgree keeps the runtime stage of Dockerfile (the from-source
// build) and Dockerfile.release (the GoReleaser image) the same: base image,
// user, state directory and entrypoint. Only the way the binary arrives differs.
func TestDockerfilesAgree(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	scratch := runtimeInstructions(t, filepath.Join(root, "Dockerfile"))
	release := runtimeInstructions(t, filepath.Join(root, "Dockerfile.release"))

	if len(scratch) == 0 {
		t.Fatal("Dockerfile has no runtime instructions to compare")
	}
	if !slices.Equal(scratch, release) {
		t.Errorf("runtime stage differs\nDockerfile:\n  %s\nDockerfile.release:\n  %s",
			strings.Join(scratch, "\n  "), strings.Join(release, "\n  "))
	}
}

func runtimeInstructions(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stages [][]string
	var current string
	flush := func() {
		if current != "" {
			stages[len(stages)-1] = append(stages[len(stages)-1], strings.Join(strings.Fields(current), " "))
			current = ""
		}
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if current == "" && (line == "" || strings.HasPrefix(line, "#")) {
			continue
		}
		cont := strings.HasSuffix(line, `\`)
		current += " " + strings.TrimSuffix(line, `\`)
		if cont {
			continue
		}
		if fields := strings.Fields(current); strings.EqualFold(fields[0], "FROM") {
			stages = append(stages, nil)
		}
		flush()
	}
	if len(stages) == 0 {
		t.Fatalf("%s has no FROM", path)
	}
	var out []string
	for _, ins := range stages[len(stages)-1] {
		switch strings.ToUpper(strings.Fields(ins)[0]) {
		case "FROM", "RUN", "USER", "ENTRYPOINT":
			out = append(out, ins)
		}
	}
	return out
}

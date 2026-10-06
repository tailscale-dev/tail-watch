package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleUnitStateDirectoryMode keeps examples/tail-watch.service creating
// the state directory with the mode newStateStore expects. Without it systemd
// uses 0755 and the collector warns that state_dir is readable by others.
func TestExampleUnitStateDirectoryMode(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "examples", "tail-watch.service"))
	if err != nil {
		t.Fatal(err)
	}

	want := fmt.Sprintf("%04o", stateDirMode)
	var got string
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "StateDirectoryMode="); ok {
			got = v
		}
	}
	if got != want {
		t.Errorf("StateDirectoryMode = %q, want %q (stateDirMode)", got, want)
	}
}

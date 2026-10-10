package agent

import (
	"path/filepath"
	"sort"
	"testing"
)

func TestCoreToolNamesAreSortedNonEmptyAndKnown(t *testing.T) {
	t.Parallel()
	names, err := CoreToolNames()
	if err != nil {
		t.Fatalf("CoreToolNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("expected at least one core tool name")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("names not sorted: %v", names)
	}
	// read_file is a stable core tool with a schema; its presence guards against
	// the standup silently registering nothing.
	found := false
	for _, n := range names {
		if n == "read_file" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected read_file among core tools, got %v", names)
	}
}

// CoreToolNames stands up a throwaway session no hub will ever archive, so it
// removes the session's scratch tree itself rather than leaving one per call.
func TestCoreToolNamesLeavesNoScratchTree(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := CoreToolNames(); err != nil {
		t.Fatalf("CoreToolNames: %v", err)
	}
	left, err := filepath.Glob(filepath.Join(tmp, "evener-scratch-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("CoreToolNames left scratch trees %v", left)
	}
}

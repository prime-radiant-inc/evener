package agent

import (
	"sort"
	"testing"
)

func TestCoreToolNamesAreSortedNonEmptyAndKnown(t *testing.T) {
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

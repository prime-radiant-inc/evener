package main

import (
	"strings"
	"testing"
)

// TestLibrariesHaveNoInternalLeaks is the regression guard: the agent, llm, and
// llm/providercfg libraries must not name any serf-internal type in their
// exported surface, so they remain externally importable. (The walk itself is
// exercised continuously by CI running the binary on the real code.)
func TestLibrariesHaveNoInternalLeaks(t *testing.T) {
	leaks, err := findLeaks()
	if err != nil {
		t.Fatalf("findLeaks: %v", err)
	}
	if len(leaks) != 0 {
		t.Fatalf("exported library API leaks %d serf-internal type(s):\n  %s",
			len(leaks), strings.Join(leaks, "\n  "))
	}
}

package dev

import (
	"strings"
	"testing"
)

// TestDevUsage covers the usage function.
func TestDevUsage(t *testing.T) {
	var sb strings.Builder
	usage(&sb)
	out := sb.String()
	if !strings.Contains(out, "usage: evener dev") {
		t.Fatalf("usage missing 'usage: evener dev': %q", out)
	}
	if !strings.Contains(out, "agent-shards") {
		t.Fatalf("usage missing 'agent-shards': %q", out)
	}
	if !strings.Contains(out, "module-lint") {
		t.Fatalf("usage missing 'module-lint': %q", out)
	}
}

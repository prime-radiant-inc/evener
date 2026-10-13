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
	if out == "" {
		t.Fatal("usage wrote nothing to the writer it was given")
	}
}

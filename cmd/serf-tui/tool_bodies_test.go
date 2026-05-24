package main

import (
	"strings"
	"testing"
)

func TestDiffBodyTintsAddLines(t *testing.T) {
	withTestColorProfile(t)
	diff := strings.Join([]string{
		"@@ -1,3 +1,3 @@",
		" context line",
		"-removed",
		"+added",
	}, "\n")
	got := diffBody(ToolArgs{}, diff, 60)
	// Each + line should carry a background tint; we can detect via ANSI bg escape.
	// Lipgloss may combine fg+bg in one sequence (e.g. \x1b[38;2;...;48;2;...m)
	// or emit separate \x1b[48;...m sequences.
	hasBg := strings.Contains(got, "\x1b[48") ||
		strings.Contains(got, ";48;") ||
		strings.Contains(got, ";48m")
	if !hasBg {
		t.Errorf("diffBody should set background on +/− lines: %q", got)
	}
}

func TestDiffBodyHandlesEmptyInput(t *testing.T) {
	got := diffBody(ToolArgs{}, "", 60)
	if got != "" {
		t.Errorf("diffBody on empty input should be empty; got %q", got)
	}
}

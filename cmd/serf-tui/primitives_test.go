package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestStateBarReturnsSingleGlyph(t *testing.T) {
	bar := StateBar(lipgloss.Color("#7aa2f7"))
	if !strings.Contains(bar, "▍") {
		t.Errorf("StateBar missing left-bar glyph: %q", bar)
	}
}

func TestFocusedStateBarReturnsDoubleGlyph(t *testing.T) {
	bar := FocusedStateBar(lipgloss.Color("#7aa2f7"))
	if strings.Count(bar, "▍") != 2 {
		t.Errorf("FocusedStateBar should contain two ▍ glyphs; got %q", bar)
	}
}

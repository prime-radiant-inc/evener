package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

func TestStatusBadgeContainsLabelAndDot(t *testing.T) {
	out := StatusBadge(lipgloss.Color("#f7768e"), "AWAITING")
	if !strings.Contains(out, "●") {
		t.Errorf("StatusBadge missing dot: %q", out)
	}
	if !strings.Contains(out, "AWAITING") {
		t.Errorf("StatusBadge missing label: %q", out)
	}
}

func TestStatusBadgeIsBoldUppercase(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	out := StatusBadge(lipgloss.Color("#f7768e"), "awaiting")
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("StatusBadge should have ANSI styling: %q", out)
	}
	if !strings.Contains(out, "AWAITING") {
		t.Errorf("StatusBadge should upper-case label; got %q", out)
	}
}

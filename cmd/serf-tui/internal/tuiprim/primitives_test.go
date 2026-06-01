package tuiprim

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

func TestSectionDividerEmitsLeftRight(t *testing.T) {
	out := SectionDivider(60, "SERF / SESSION", "12 turns")
	if !strings.Contains(out, "SERF / SESSION") {
		t.Errorf("SectionDivider missing left label: %q", out)
	}
	if !strings.Contains(out, "12 turns") {
		t.Errorf("SectionDivider missing right label: %q", out)
	}
}

func TestSectionDividerUsesRuleGlyphs(t *testing.T) {
	out := SectionDivider(60, "X", "Y")
	if !strings.Contains(out, "─") {
		t.Errorf("SectionDivider missing fill glyph ─: %q", out)
	}
	if !strings.Contains(out, "┄") {
		t.Errorf("SectionDivider missing trailing ┄ glyph: %q", out)
	}
}

func TestSectionDividerTruncatesAtNarrowWidth(t *testing.T) {
	out := SectionDivider(20, "VERY LONG LEFT", "VERY LONG RIGHT")
	visible := lipgloss.Width(out)
	if visible > 25 {
		t.Errorf("SectionDivider too wide at narrow width; got width %d", visible)
	}
}

func TestKbdHintFormatsKeyAndAction(t *testing.T) {
	out := KbdHint("enter", "send")
	if !strings.Contains(out, "enter") {
		t.Errorf("KbdHint missing key: %q", out)
	}
	if !strings.Contains(out, "send") {
		t.Errorf("KbdHint missing action: %q", out)
	}
}

func TestDotLeaderFillsMiddle(t *testing.T) {
	out := DotLeader("read", "12 lines", 50)
	if !strings.Contains(out, "·") {
		t.Errorf("DotLeader missing fill char ·: %q", out)
	}
	if !strings.Contains(out, "read") || !strings.Contains(out, "12 lines") {
		t.Errorf("DotLeader missing label or result: %q", out)
	}
	if lipgloss.Width(out) != 50 {
		t.Errorf("DotLeader should equal target width 50, got %d", lipgloss.Width(out))
	}
}

func TestDotLeaderHandlesOverflow(t *testing.T) {
	out := DotLeader("verylongverb", "and result text here", 10)
	if lipgloss.Width(out) > 10 {
		t.Errorf("DotLeader exceeded width on overflow: width=%d", lipgloss.Width(out))
	}
}

func TestOverlayContainsTitleBodyFooter(t *testing.T) {
	out := Overlay(OverlayOpts{
		Title:  "Select model",
		Width:  60,
		Body:   "the body content",
		Footer: "enter select  esc cancel",
	})
	for _, want := range []string{"Select model", "the body content", "enter select  esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overlay missing %q in output:\n%s", want, out)
		}
	}
}

func TestOverlayDrawsRoundedBorder(t *testing.T) {
	out := Overlay(OverlayOpts{Title: "X", Width: 40, Body: "body"})
	for _, glyph := range []string{"╭", "╮", "╰", "╯"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("Overlay missing border glyph %q", glyph)
		}
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

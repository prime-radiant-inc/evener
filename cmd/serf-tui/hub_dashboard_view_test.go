package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestErroredLane_TUI(t *testing.T) {
	if got := stateLabel("systemError"); got != "errored" {
		t.Fatalf("stateLabel(systemError) = %q, want errored", got)
	}
	if got := stateLabel("errored"); got != "errored" {
		t.Fatalf("stateLabel(errored) = %q", got)
	}
	if attentionRankLabel("systemError") <= attentionRankLabel("awaiting") {
		t.Fatal("errored must outrank awaiting in the TUI")
	}
	if stateColor("errored") == tuitheme.ActiveTheme().TextDim {
		t.Fatal("errored must not fall through to TextDim")
	}
}

// TestStateColorNormalizesRawWireStatus pins stateColor to the label-based
// dispatch (like statusDot/attentionRankLabel) rather than a raw-value
// switch: hubRow.state carries the raw wire status directly for session
// rows (see hubNodeFromThread), so "systemError" must resolve to the same
// color as the hub-normalized "errored", not fall through to TextDim.
func TestStateColorNormalizesRawWireStatus(t *testing.T) {
	if got, want := stateColor("systemError"), stateColor("errored"); got != want {
		t.Fatalf("stateColor(systemError) = %v, want %v (same as stateColor(errored))", got, want)
	}
}

func TestNeedsYouCount(t *testing.T) {
	rows := []hubRow{
		// Project rollup row carries the same attention state as its child
		// sessions; it must not be double-counted alongside them.
		{kind: hubRowProject, project: "serf", state: "errored"},
		{kind: hubRowSession, project: "serf", state: "awaiting", live: true},
		{kind: hubRowSession, project: "serf", state: "systemError", live: true},
		{kind: hubRowSession, project: "serf", state: "warning", live: true},
		{kind: hubRowSession, project: "serf", state: "active", live: true},
		{kind: hubRowSession, project: "serf", state: "idle", live: true},
		{kind: hubRowSession, project: "serf", state: "ended", live: false},
	}
	if got := needsYouCount(rows); got != 3 {
		t.Fatalf("needsYouCount = %d, want 3 (awaiting + raw systemError + warning)", got)
	}
}

func TestNeedsYouBadge(t *testing.T) {
	if got := needsYouBadge(0); got != "" {
		t.Fatalf("needsYouBadge(0) = %q, want empty", got)
	}
	if got := needsYouBadge(2); !strings.Contains(got, "◆2") {
		t.Fatalf("needsYouBadge(2) = %q, want to contain ◆2", got)
	}
}

// TestDashboardHeaderBadgeKeepsExactWidth pins the single-line TopBar
// invariant: SectionDivider fills to exactly the requested width, so the
// needs-you badge must ride inside the divider's right-side content — a
// badge appended after the divider would push the line to width+3 and wrap,
// breaking AppShell's height math (ShellSectionLineCount counts it as one
// line regardless of visual wrap).
func TestDashboardHeaderBadgeKeepsExactWidth(t *testing.T) {
	withTestColorProfile(t)
	const width = 100
	got := dashboardHeader("http://hub.test", 3, width, needsYouBadge(2))
	if strings.Contains(got, "\n") {
		t.Fatalf("header with badge must stay a single line; got %q", got)
	}
	if w := lipgloss.Width(got); w != width {
		t.Fatalf("header with badge is %d columns, want exactly %d", w, width)
	}
	if !strings.Contains(got, "◆2") {
		t.Fatalf("header should carry the needs-you badge ◆2: %q", got)
	}
	// The badge keeps its amber (StateAwaiting) foreground inside the
	// divider's ghost-styled right content. Derive the escape from the live
	// theme so the assertion tracks palette changes.
	sample := lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateAwaiting).Render("X")
	amberEscape, _, _ := strings.Cut(sample, "X")
	if !strings.Contains(got, amberEscape) {
		t.Fatalf("badge should be styled with StateAwaiting %q inside header: %q", amberEscape, got)
	}
	// Zero badge: byte-identical to the pre-badge header so quiet dashboards
	// render exactly as before.
	want := tuiprim.SectionDivider(width, "SERF LIVE", "http://hub.test · 3 live")
	if gotZero := dashboardHeader("http://hub.test", 3, width, needsYouBadge(0)); gotZero != want {
		t.Fatalf("zero-badge header changed:\ngot  %q\nwant %q", gotZero, want)
	}
}

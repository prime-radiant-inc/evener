package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestSessionRowAwaitingHasStateColor(t *testing.T) {
	withTestColorProfile(t)
	row := hubRow{kind: hubRowSession, project: "serf", title: "X", state: "awaiting"}
	got := renderDashboardSessionRow(row, false, 80, false, "")

	// Plain text must contain the state label.
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "awaiting") {
		t.Errorf("awaiting row should contain state label 'awaiting' in plain text; got %q", plain)
	}

	// The row must be wrapped in the StateAwaiting theme color, not just any ANSI escape.
	// Derive the expected ANSI foreground escape from the live theme so the test
	// stays correct if the palette changes — and fails if stateColor("awaiting")
	// returns a different color (e.g. TextDim, the named mutation-that-escapes).
	th := tuitheme.ActiveTheme()
	sample := lipgloss.NewStyle().Foreground(th.StateAwaiting).Render("X")
	wantPrefix, _, _ := strings.Cut(sample, "X")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("awaiting row should start with StateAwaiting color escape %q; got prefix %q",
			wantPrefix, got[:min(len(got), 30)])
	}
}

func TestSessionRowsHaveNoTreeConnectors(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, project: "serf", state: "active"},
		{kind: hubRowSession, project: "serf", title: "Test session", state: "active", projectKey: "serf"},
		{kind: hubRowSession, project: "serf", title: "Second session", state: "idle", projectKey: "serf"},
	}
	got := renderDashboardRowsWindow(rows, 1, 80, false, 0)
	for _, bad := range []string{"├─", "└─"} {
		if strings.Contains(got, bad) {
			t.Errorf("renderDashboardRowsWindow should not emit tree connector %q: %q", bad, got)
		}
	}
}

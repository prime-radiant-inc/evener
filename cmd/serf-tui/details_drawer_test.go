package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestDetailsDrawerHasSectionLabels(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		Title: "Test",
		State: "awaiting",
		Model: "openai/gpt-5.5",
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "AWAITING") {
		t.Errorf("details drawer should show state badge AWAITING: %q", plain)
	}
}

// TestDetailsDrawerShowsMCPServerStatusAndError verifies the details drawer's
// MCP Servers section surfaces each server's status and, when present, its
// last error — and that a server with no reported status (older daemons)
// renders without a dangling separator.
func TestDetailsDrawerShowsMCPServerStatusAndError(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		Title: "Test",
		Diagnostics: &appwire.SerfDiagnostics{
			MCP: []appwire.SerfMCPServerInfo{
				{Name: "linear", Tools: []string{"linear_create"}},
				{Name: "slack", Tools: []string{"slack_post"}, Status: "connected"},
				{Name: "github", Tools: []string{"repo_search"}, Status: "degraded", Error: "connection refused"},
			},
		},
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")

	for _, want := range []string{
		"linear (1 tools)",
		"slack (1 tools) — connected",
		"github (1 tools) — degraded",
		"last error: connection refused",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("details drawer missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "linear (1 tools) —") {
		t.Errorf("details drawer should omit dash suffix when status is empty:\n%s", plain)
	}
}

// TestDetailsDrawerShowsWorkTimeAndTokens verifies the details drawer renders
// a Work: line and a Tokens: line when the session detail carries WS2
// work-time/usage metrics — mirroring hub_status.go's status-bar formatting.
func TestDetailsDrawerShowsWorkTimeAndTokens(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		WorkMillis: 4200,
		Usage: &appwire.SerfUsage{
			InputTokens:     100,
			OutputTokens:    50,
			CacheReadTokens: 10,
			TotalTokens:     160,
		},
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")

	if !strings.Contains(plain, "Work:") {
		t.Errorf("details drawer missing Work: line:\n%s", plain)
	}
	if !strings.Contains(plain, "↑100") || !strings.Contains(plain, "↓50") {
		t.Errorf("details drawer missing token line with ↑100/↓50:\n%s", plain)
	}
}

// TestDetailsDrawerHidesWorkTimeAndTokensWhenAbsent verifies the drawer omits
// the Work: and Tokens: lines entirely when the session carries no WS2
// metrics (old daemon, Codex thread, or a session with zero usage).
func TestDetailsDrawerHidesWorkTimeAndTokensWhenAbsent(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")

	if strings.Contains(plain, "Work:") {
		t.Errorf("details drawer should omit Work: line when absent:\n%s", plain)
	}
	if strings.Contains(plain, "Tokens:") {
		t.Errorf("details drawer should omit Tokens: line when absent:\n%s", plain)
	}
}

// TestDetailsDrawerBandsContextPressure verifies the drawer's Context line
// escalates at the same thresholds as the session header's meta strip: the
// drawer's own ghost tone below warnThreshold, StateWarning from 0.75,
// StateError from compactThreshold's 0.95. A source that reports pressure but
// no window gives no ratio to band on and stays ghost.
func TestDetailsDrawerBandsContextPressure(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()

	const fullWindow = 200000
	cases := []struct {
		name     string
		used     int
		window   int
		pressure float64
		want     lipgloss.Color
	}{
		{name: "below-warn-stays-ghost", used: 148000, window: fullWindow, pressure: 0.74, want: th.TextGhost},
		{name: "at-warn-renders-amber", used: 150000, window: fullWindow, pressure: 0.75, want: th.StateWarning},
		{name: "below-compact-stays-amber", used: 188000, window: fullWindow, pressure: 0.94, want: th.StateWarning},
		{name: "at-compact-renders-red", used: 190000, window: fullWindow, pressure: 0.95, want: th.StateError},
		{name: "no-window-stays-ghost", pressure: 0.98, want: th.TextGhost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := hubSessionDetail{
				Title:           "Pressured",
				SourceLabel:     "serf",
				ContextUsed:     tc.used,
				ContextWindow:   tc.window,
				ContextPressure: tc.pressure,
			}
			got := detailsDrawer{Detail: detail}.View()
			value := fmt.Sprintf("%.0f%% used", tc.pressure*100)
			if plain := ansiPattern.ReplaceAllString(got, ""); !strings.Contains(plain, "Context:  "+value) {
				t.Fatalf("case renders no Context line, so it proves nothing about banding:\n%s", plain)
			}
			want := lipgloss.NewStyle().Foreground(tc.want).Render(value)
			if !strings.Contains(got, want) {
				t.Errorf("context value %q not rendered in %s:\n%q", value, tc.want, got)
			}
		})
	}
}

// TestDetailsDrawerShowsFailedToolCalls verifies the details drawer answers
// "did anything go wrong" (kata md4g): a positive count renders a Failed:
// line, an absent or measured-zero count renders nothing.
func TestDetailsDrawerShowsFailedToolCalls(t *testing.T) {
	withTestColorProfile(t)
	three := 3
	d := detailsDrawer{Detail: hubSessionDetail{FailedToolCalls: &three}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Failed:   3") {
		t.Errorf("details drawer missing Failed: line:\n%s", plain)
	}
}

// TestDetailsDrawerHidesFailedToolCallsWhenZeroOrAbsent verifies a measured
// zero and an uncounted session both render nothing — neither is news, and
// an absent count must never be shown as a fabricated zero.
func TestDetailsDrawerHidesFailedToolCallsWhenZeroOrAbsent(t *testing.T) {
	withTestColorProfile(t)
	zero := 0
	for _, detail := range []hubSessionDetail{
		{FailedToolCalls: &zero},
		{},
	} {
		got := detailsDrawer{Detail: detail}.View()
		plain := ansiPattern.ReplaceAllString(got, "")
		if strings.Contains(plain, "Failed:") {
			t.Errorf("details drawer should omit Failed: line:\n%s", plain)
		}
	}
}

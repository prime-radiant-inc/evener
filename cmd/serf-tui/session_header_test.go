package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestSessionHeaderHasThreeMainSections(t *testing.T) {
	m := hubModel{
		detail: hubSessionDetail{
			Title:       "Restore hub TUI widgets",
			SessionID:   "01SERF",
			State:       "awaiting",
			SourceLabel: "serf",
			Branch:      "feat/widget",
			Model:       "anthropic/claude-haiku-4-5",
			WorkingDir:  "/home/jesse/git/serf",
			TurnCount:   12,
		},
		hubURL: "http://hub.test",
		width:  100,
	}
	got := strings.Join(m.sessionHeaderLines(), "\n")
	// 1. rule with breadcrumb + turn count
	if !strings.Contains(got, "SERF / SESSION") {
		t.Errorf("missing breadcrumb: %q", got)
	}
	if !strings.Contains(got, "12 turns") {
		t.Errorf("missing turn count: %q", got)
	}
	// 2. title + state badge
	if !strings.Contains(got, "Restore hub TUI widgets") {
		t.Errorf("missing title: %q", got)
	}
	if !strings.Contains(got, "YOUR MOVE") {
		t.Errorf("missing state badge: %q", got)
	}
	// 3. meta strip
	if !strings.Contains(got, "src serf") || !strings.Contains(got, "branch feat/widget") {
		t.Errorf("missing meta strip cells: %q", got)
	}
}

// TestSessionHeaderShowsRichContextWhenWindowKnown asserts that when the
// source supplies ContextUsed/Window, the meta strip's "ctx" fragment
// includes the absolute token count and "to compact" hint (kata: ctx-meter).
// When only ContextPressure is known, the strip falls back to the percent.
func TestSessionHeaderShowsRichContextWhenWindowKnown(t *testing.T) {
	rich := hubModel{
		detail: hubSessionDetail{
			Title:           "Rich",
			SourceLabel:     "serf",
			Model:           "openai/gpt-5",
			TurnCount:       1,
			ContextPressure: 0.23,
			ContextUsed:     46000,
			ContextWindow:   200000,
		},
		width: 200,
	}
	got := strings.Join(rich.sessionHeaderLines(), "\n")
	if !strings.Contains(got, "ctx 46k/200k (23%, 144k to compact)") {
		t.Errorf("rich ctx fragment missing in meta strip:\n%s", got)
	}

	thin := hubModel{
		detail: hubSessionDetail{
			Title:           "Thin",
			SourceLabel:     "serf",
			Model:           "openai/gpt-5",
			TurnCount:       1,
			ContextPressure: 0.42,
		},
		width: 200,
	}
	got = strings.Join(thin.sessionHeaderLines(), "\n")
	if !strings.Contains(got, "ctx 42%") {
		t.Errorf("thin ctx fragment missing in meta strip:\n%s", got)
	}
	if strings.Contains(got, "to compact") {
		t.Errorf("thin ctx should not include compact hint when window unknown:\n%s", got)
	}
}

// TestSessionHeaderBandsContextPressure asserts the meta strip's ctx value
// escalates color as the window fills (spec §7.5): plain below warnThreshold,
// StateWarning from 0.75, StateError from compactThreshold's 0.95 — the same
// threshold the "N to compact" figure is derived from, so the color and the
// number can never tell different stories. A session with no known window has
// no ratio to band on and renders plain.
func TestSessionHeaderBandsContextPressure(t *testing.T) {
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
		{name: "below-warn-renders-plain", used: 148000, window: fullWindow, want: th.Text},          // 0.74
		{name: "at-warn-renders-amber", used: 150000, window: fullWindow, want: th.StateWarning},     // 0.75
		{name: "below-compact-stays-amber", used: 188000, window: fullWindow, want: th.StateWarning}, // 0.94
		{name: "at-compact-renders-red", used: 190000, window: fullWindow, want: th.StateError},      // 0.95
		{name: "no-window-renders-plain", used: 190000, window: 0, pressure: 0.95, want: th.Text},
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
			fragment := formatContextFragment(detail)
			if fragment == "" {
				t.Fatalf("case renders no ctx fragment, so it proves nothing about banding: %+v", detail)
			}
			want := lipgloss.NewStyle().Foreground(tc.want).Render(fragment)
			got := strings.Join((hubModel{detail: detail, width: 200}).sessionHeaderLines(), "\n")
			if !strings.Contains(got, want) {
				t.Errorf("ctx fragment %q not rendered in %s:\n%q", fragment, tc.want, got)
			}
		})
	}
}

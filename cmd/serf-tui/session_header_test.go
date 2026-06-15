package main

import (
	"strings"
	"testing"
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
	if !strings.Contains(got, "AWAITING") {
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

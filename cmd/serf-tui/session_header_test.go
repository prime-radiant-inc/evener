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

package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
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

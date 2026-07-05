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

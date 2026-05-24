package main

import (
	"strings"
	"testing"
)

func TestSessionRowAwaitingHasStateColor(t *testing.T) {
	withTestColorProfile(t)
	row := hubRow{kind: hubRowSession, project: "serf", title: "X", state: "awaiting"}
	got := renderDashboardSessionRow(row, false, 80, false, "")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("awaiting row should carry color; got plain: %q", got)
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

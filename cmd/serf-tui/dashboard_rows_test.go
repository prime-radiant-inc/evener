package main

import (
	"strings"
	"testing"
)

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

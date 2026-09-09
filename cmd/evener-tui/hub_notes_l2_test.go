package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestHubNotesUsageWarnsOnIdleLiveSession verifies L2 (TUI half): the /notes
// usage output names the idle-wake cost ("Saving will wake the agent.") when
// the session is live AND idle, and omits it otherwise.
func TestHubNotesUsageWarnsOnIdleLiveSession(t *testing.T) {
	idleLive := hubSessionDetail{Live: true, State: appwire.ThreadStatusIdle}
	got := hubNotesUsage(idleLive)
	if !strings.Contains(got, "Usage: /notes <text> or /notes clear") {
		t.Fatalf("usage missing base line: %q", got)
	}
	if !strings.Contains(got, hubNotesIdleWakeWarning) {
		t.Fatalf("idle live usage missing wake warning: %q", got)
	}
	for name, detail := range map[string]hubSessionDetail{
		"live active": {Live: true, State: appwire.ThreadStatusActive},
		"ended idle":  {Live: false, State: appwire.ThreadStatusIdle},
	} {
		if got := hubNotesUsage(detail); strings.Contains(got, hubNotesIdleWakeWarning) {
			t.Fatalf("%s usage shows wake warning, want none: %q", name, got)
		}
	}
}

// TestRunHubNotesEmptyArgsShowsIdleWarning verifies the warning surfaces
// through the actual empty-/notes dispatch path a palette invocation takes,
// and stays absent when the session is busy.
func TestRunHubNotesEmptyArgsShowsIdleWarning(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Live = true
	m.detail.State = appwire.ThreadStatusIdle
	if cmd := m.runHubNotes(""); cmd != nil {
		t.Fatal("empty args should produce no cmd")
	}
	got := m.session.messages[len(m.session.messages)-1].Text
	if !strings.Contains(got, hubNotesIdleWakeWarning) {
		t.Fatalf("idle usage = %q, want wake warning", got)
	}

	m = newSessionHubModel(nil)
	m.detail.Live = true
	m.detail.State = appwire.ThreadStatusActive
	if cmd := m.runHubNotes(""); cmd != nil {
		t.Fatal("empty args should produce no cmd")
	}
	got = m.session.messages[len(m.session.messages)-1].Text
	if strings.Contains(got, hubNotesIdleWakeWarning) {
		t.Fatalf("active usage = %q, want no wake warning", got)
	}
}

package server

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

// appStatus maps the daemon's raw state string (plus a live "processing" flag)
// to the appwire thread status, defaulting unknown states to idle.
func TestAppStatus(t *testing.T) {
	// A processing session is always Active regardless of the recorded state.
	if got := appStatus(appwire.ThreadStatusIdle, true); got != appwire.ThreadStatusActive {
		t.Fatalf("processing session status=%q, want active", got)
	}

	cases := map[string]string{
		appwire.ThreadStatusIdle:        appwire.ThreadStatusIdle,
		appwire.ThreadStatusActive:      appwire.ThreadStatusActive,
		appwire.ThreadStatusAwaiting:    appwire.ThreadStatusAwaiting,
		appwire.ThreadStatusWarning:     appwire.ThreadStatusWarning,
		appwire.ThreadStatusSystemError: appwire.ThreadStatusSystemError,
		appwire.ThreadStatusClosed:      appwire.ThreadStatusClosed,
		appwire.ThreadStatusNotLoaded:   appwire.ThreadStatusNotLoaded,
		"  idle  ":                      appwire.ThreadStatusIdle, // trimmed
		"something-unknown":             appwire.ThreadStatusIdle, // default
		"":                              appwire.ThreadStatusIdle, // default
	}
	for state, want := range cases {
		if got := appStatus(state, false); got != want {
			t.Errorf("appStatus(%q,false)=%q, want %q", state, got, want)
		}
	}
}

// mergeAppThreadItem fills the incoming item's empty fields from the existing
// one so an update notification never clobbers earlier-observed data.
func TestMergeAppThreadItem(t *testing.T) {
	existing := appwire.ThreadItem{
		Type:          "commandExecution",
		TurnID:        "tn_1",
		Text:          "old text",
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"cmd":"ls"}`,
		Output:        "old output",
		Status:        appwire.TurnStatusInProgress,
	}
	// An incoming item that carries only a new status keeps everything else.
	incoming := appwire.ThreadItem{Status: appwire.TurnStatusCompleted}
	merged := mergeAppThreadItem(existing, incoming)
	if merged.Type != "commandExecution" || merged.TurnID != "tn_1" {
		t.Fatalf("type/turn lost: %+v", merged)
	}
	if merged.Text != "old text" || merged.ToolName != "shell" || merged.CallID != "call_1" {
		t.Fatalf("text/tool/call lost: %+v", merged)
	}
	if merged.ArgumentsJSON != `{"cmd":"ls"}` || merged.Output != "old output" {
		t.Fatalf("args/output lost: %+v", merged)
	}
	if merged.Status != appwire.TurnStatusCompleted {
		t.Fatalf("status should take the incoming value, got %q", merged.Status)
	}

	// A fully-populated incoming item wins over the existing one.
	fresh := appwire.ThreadItem{Type: "agentMessage", Text: "new", Status: appwire.TurnStatusCompleted}
	merged = mergeAppThreadItem(existing, fresh)
	if merged.Type != "agentMessage" || merged.Text != "new" {
		t.Fatalf("incoming values should win: %+v", merged)
	}
}

// TestMergeAppThreadItemKeepsOutputImages holds OutputImages to the same
// never-clobber rule as every other field. A tool-result image is described
// once, on the frame that settles the call; a later update carrying no images
// is silent about them, not a report that there are none.
func TestMergeAppThreadItemKeepsOutputImages(t *testing.T) {
	described := []appwire.OutputImage{{Source: "tool-result", SHA: "abc"}}
	existing := appwire.ThreadItem{Type: "commandExecution", CallID: "call_1", OutputImages: described}

	merged := mergeAppThreadItem(existing, appwire.ThreadItem{Status: appwire.TurnStatusCompleted})
	if len(merged.OutputImages) != 1 || merged.OutputImages[0] != described[0] {
		t.Fatalf("OutputImages=%+v, want the earlier description kept", merged.OutputImages)
	}

	replacement := []appwire.OutputImage{{Source: "written-file", Path: "plot.png"}}
	merged = mergeAppThreadItem(existing, appwire.ThreadItem{OutputImages: replacement})
	if len(merged.OutputImages) != 1 || merged.OutputImages[0] != replacement[0] {
		t.Fatalf("OutputImages=%+v, want the incoming description to win", merged.OutputImages)
	}
}

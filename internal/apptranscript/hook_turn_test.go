package apptranscript

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// kata qm9y: a persisted HOOK_COMPLETED entry projects to the same
// systemMessage item the live projector emits, so Settings -> Transcript's
// hook-exit toggles have something to act on after a reload.
func TestProjectTurnRendersHookCompleted(t *testing.T) {
	out := ProjectTurn("turn_7", 7, schema.Turn{
		Kind:    schema.TurnHookCompleted,
		Message: llm.System("PreToolUse hook my-plugin Bash command exit 0"),
		Hook:    &schema.HookInfo{Event: "PreToolUse", HookType: "command", Matcher: "Bash", PluginName: "my-plugin"},
	}, nil, nil, nil)

	if len(out) != 1 {
		t.Fatalf("items = %+v, want exactly one", out)
	}
	item := out[0]
	if item.Type != "systemMessage" {
		t.Errorf("Type = %q, want systemMessage", item.Type)
	}
	if item.TurnID != "turn_7" || item.TranscriptEntryIndex != 7 {
		t.Errorf("TurnID/TranscriptEntryIndex = %q/%d, want turn_7/7", item.TurnID, item.TranscriptEntryIndex)
	}
	if item.EventKind != appwire.ThreadItemEventKindHookCompleted {
		t.Errorf("EventKind = %q, want %q", item.EventKind, appwire.ThreadItemEventKindHookCompleted)
	}
	if item.Text != "PreToolUse hook my-plugin Bash command exit 0" {
		t.Errorf("Text = %q, want the hook announcement", item.Text)
	}
	// The typed exit code is what the toggles split on. A clean exit must be
	// a present zero, never an absent one: absent means "this daemon did not
	// record a code", which the normal-only toggle deliberately hides.
	if item.ExitCode == nil {
		t.Fatal("ExitCode is nil for a hook that exited 0; the normal-only toggle would hide a clean hook")
	}
	if *item.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", *item.ExitCode)
	}
}

// A non-zero exit must survive the round trip verbatim, or a reloaded broken
// hook renders as a clean one.
func TestProjectTurnCarriesNonZeroHookExit(t *testing.T) {
	out := ProjectTurn("turn_1", 1, schema.Turn{
		Kind:    schema.TurnHookCompleted,
		Message: llm.System("Stop hook exit 3"),
		Hook:    &schema.HookInfo{Event: "Stop", ExitCode: 3},
	}, nil, nil, nil)

	if len(out) != 1 {
		t.Fatalf("items = %+v, want exactly one", out)
	}
	if out[0].ExitCode == nil || *out[0].ExitCode != 3 {
		t.Fatalf("ExitCode = %v, want 3", out[0].ExitCode)
	}
}

// An entry written by a daemon that recorded no hook detail still renders its
// line rather than vanishing, and reports no exit code rather than a
// fabricated zero.
func TestProjectTurnRendersHookCompletedWithoutDetail(t *testing.T) {
	out := ProjectTurn("turn_2", 2, schema.Turn{
		Kind:    schema.TurnHookCompleted,
		Message: llm.System("SessionStart hook"),
	}, nil, nil, nil)

	if len(out) != 1 {
		t.Fatalf("items = %+v, want exactly one", out)
	}
	if out[0].Text != "SessionStart hook" {
		t.Errorf("Text = %q, want the announcement", out[0].Text)
	}
	if out[0].ExitCode != nil {
		t.Errorf("ExitCode = %d, want nil (never fabricate a zero)", *out[0].ExitCode)
	}
}

// A HOOK_COMPLETED entry is presentational; it must not claim the wrapping
// turn failed, whatever the hook's exit code was. A non-zero hook exit is not
// a turn failure.
func TestHookCompletedDoesNotStampTurnFailure(t *testing.T) {
	turn := appwire.Turn{Status: appwire.TurnStatusCompleted}
	StampTurnFailure(&turn, schema.Turn{
		Kind: schema.TurnHookCompleted,
		Hook: &schema.HookInfo{Event: "Stop", ExitCode: 9},
	})
	if turn.Status != appwire.TurnStatusCompleted {
		t.Errorf("Status = %q, want it left as %q", turn.Status, appwire.TurnStatusCompleted)
	}
}

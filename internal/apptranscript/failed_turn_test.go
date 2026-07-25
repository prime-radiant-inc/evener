package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// kata mcgh: a persisted TurnFailure projects to a systemMessage item that
// says the turn failed and carries the diagnostic — the reload counterpart of
// the live projector's failed NotifyTurnCompleted.
func TestProjectTurnRendersFailedTurn(t *testing.T) {
	out := ProjectTurn("turn_4", 4, schema.Turn{
		Kind:    schema.TurnFailure,
		Message: llm.System("provider error: access denied"),
		Error: &schema.TurnFailureInfo{
			Message: "provider error: access denied",
			Source:  "provider",
			Title:   "Access denied",
			Hint:    "check the API key",
			Cause:   &schema.TurnFailureCause{Kind: "provider", Provider: "openai", Model: "gpt-5.2", Status: 403},
		},
	}, nil, nil, nil)

	if len(out) != 1 {
		t.Fatalf("items = %+v, want exactly one", out)
	}
	item := out[0]
	if item.Type != "systemMessage" {
		t.Errorf("Type = %q, want systemMessage", item.Type)
	}
	if item.TurnID != "turn_4" || item.TranscriptEntryIndex != 4 {
		t.Errorf("TurnID/TranscriptEntryIndex = %q/%d, want turn_4/4", item.TurnID, item.TranscriptEntryIndex)
	}
	if item.Status != appwire.TurnStatusFailed {
		t.Errorf("Status = %q, want %q", item.Status, appwire.TurnStatusFailed)
	}
	if item.EventKind != appwire.ThreadItemEventKindError {
		t.Errorf("EventKind = %q, want %q", item.EventKind, appwire.ThreadItemEventKindError)
	}
	if item.Error != "provider error: access denied" {
		t.Errorf("Error = %q, want the diagnostic message", item.Error)
	}
	if item.Text != "provider error: access denied" {
		t.Errorf("Text = %q, want the diagnostic message", item.Text)
	}
}

// A TurnFailure with no diagnostic at all still renders: a failure the reader
// cannot see is the bug this fix exists to close.
func TestProjectTurnRendersFailedTurnWithoutDiagnostic(t *testing.T) {
	out := ProjectTurn("turn_1", 1, schema.Turn{
		Kind:    schema.TurnFailure,
		Message: llm.System("something broke"),
	}, nil, nil, nil)

	if len(out) != 1 {
		t.Fatalf("items = %+v, want exactly one", out)
	}
	if out[0].Status != appwire.TurnStatusFailed {
		t.Errorf("Status = %q, want %q", out[0].Status, appwire.TurnStatusFailed)
	}
	if out[0].Error != "something broke" {
		t.Errorf("Error = %q, want the turn text as the fallback diagnostic", out[0].Error)
	}
}

// A failure carrying no text anywhere still produces a visible item rather
// than vanishing the way a blank model-switch marker does.
func TestProjectTurnRendersFailedTurnWithNoText(t *testing.T) {
	out := ProjectTurn("turn_1", 1, schema.Turn{Kind: schema.TurnFailure}, nil, nil, nil)
	if len(out) != 1 {
		t.Fatalf("items = %+v, want exactly one", out)
	}
	if out[0].Status != appwire.TurnStatusFailed {
		t.Errorf("Status = %q, want %q", out[0].Status, appwire.TurnStatusFailed)
	}
	if out[0].Text == "" {
		t.Error("Text is empty; a textless failure must still say the turn failed")
	}
}

func writeFailureTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		Kind:          "header",
		FormatVersion: transcript.FormatVersion,
		SessionID:     "s",
		ProfileID:     "openai",
		Model:         "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hi"))); err != nil {
		t.Fatalf("append user input: %v", err)
	}
	failure := schema.NewTurn(schema.TurnFailure, llm.System("provider error: access denied"))
	failure.Error = &schema.TurnFailureInfo{
		Message: "provider error: access denied",
		Source:  "provider",
		Title:   "Access denied",
		Hint:    "check the API key",
		Cause:   &schema.TurnFailureCause{Kind: "provider", Provider: "openai", Model: "gpt-5.2", Status: 403},
	}
	if err := w.Append(failure); err != nil {
		t.Fatalf("append failure: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func failureProjector(raw json.RawMessage, turnID string, entryIndex int) []appwire.ThreadItem {
	entry, err := transcript.DecodeEntry(raw)
	if err != nil {
		return nil
	}
	return ProjectTurn(turnID, entryIndex, entry.Turn, map[string]string{}, nil, nil)
}

// The reloaded turn wrapping a persisted failure reports the same
// status/error the live NotifyTurnCompleted did, instead of the blanket
// "completed" every reloaded turn used to claim.
func TestTurnsFromFileStampsFailedTurnStatus(t *testing.T) {
	turns, err := TurnsFromFile(writeFailureTranscript(t), 1<<20, failureProjector)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	// By value with a found flag, not a pointer: staticcheck cannot see that
	// t.Fatalf terminates, so a nil-check-then-dereference here reads to it as
	// a possible nil deref (SA5011). A zero Turn fails the assertions below
	// loudly rather than panicking, so nothing is lost.
	var failed appwire.Turn
	var found bool
	for _, turn := range turns {
		if turn.Status == appwire.TurnStatusFailed {
			failed, found = turn, true
		}
	}
	if !found {
		t.Fatalf("no failed turn in %d reloaded turns; a failed turn must not read as completed", len(turns))
	}
	if failed.Error == nil {
		t.Fatal("failed turn carries no Error; the live wire shape carries one")
	}
	if failed.Error.Message != "provider error: access denied" {
		t.Errorf("Error.Message = %q", failed.Error.Message)
	}
	if failed.Error.Title != "Access denied" || failed.Error.Hint != "check the API key" || failed.Error.Source != "provider" {
		t.Errorf("Error source/title/hint = %q/%q/%q, want the persisted diagnostic", failed.Error.Source, failed.Error.Title, failed.Error.Hint)
	}
	if failed.Error.Cause == nil {
		t.Fatal("Error.Cause is nil; the persisted structured cause was dropped")
	}
	if failed.Error.Cause.Kind != "provider" || failed.Error.Cause.Provider != "openai" || failed.Error.Cause.Status != 403 {
		t.Errorf("Error.Cause = %+v", failed.Error.Cause)
	}
}

// The bounded/indexed read path is what a reloading web client actually uses,
// so it must agree with the whole-file read.
func TestIndexedReadStampsFailedTurnStatus(t *testing.T) {
	path := writeFailureTranscript(t)
	page, err := NewTurnCache().PageFromFile(path, 1<<20, "", 50, func(raw json.RawMessage, turnID string, entryIndex int, _ map[string]string) []appwire.ThreadItem {
		return failureProjector(raw, turnID, entryIndex)
	})
	if err != nil {
		t.Fatalf("PageFromFile: %v", err)
	}
	found := false
	for _, turn := range page.Turns {
		if turn.Status == appwire.TurnStatusFailed {
			found = true
			if turn.Error == nil || turn.Error.Message != "provider error: access denied" {
				t.Errorf("failed turn Error = %+v", turn.Error)
			}
		}
	}
	if !found {
		t.Fatalf("indexed read produced no failed turn: %+v", page.Turns)
	}
	// The index sidecar the read just wrote must not be mistaken for the
	// transcript itself by a later assertion.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
}

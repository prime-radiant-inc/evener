package tui

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// TestRunHubNotesEmptyArgsShowsUsage verifies K2: a palette-invoked /notes
// with empty args must not clear the note (nor issue any mutation). Only the
// explicit `/notes clear` clears.
func TestRunHubNotesEmptyArgsShowsUsage(t *testing.T) {
	var calls []appwire.NotesHumanSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodNotesHumanSet, func(_ context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
			calls = append(calls, params)
			return appwire.NotesHumanSetResponse{Note: params.Note}, nil
		})
	})
	defer cleanup()
	m := newSessionHubModel(client)
	m.detail.Live = true
	m.detail.HumanNote = "existing note"

	if cmd := m.runHubNotes(""); cmd != nil {
		t.Fatal("empty args should produce no cmd")
	}
	if len(calls) != 0 {
		t.Fatalf("empty /notes issued %d mutations, want none", len(calls))
	}
	if got := m.session.messages[len(m.session.messages)-1].Text; got != "Usage: /notes <text> or /notes clear" {
		t.Fatalf("usage message = %q", got)
	}

	if cmd := m.runHubNotes("   "); cmd != nil {
		t.Fatal("whitespace-only args should produce no cmd")
	}
	if len(calls) != 0 {
		t.Fatalf("whitespace /notes issued %d mutations, want none", len(calls))
	}
}

// TestRunHubNotesClearClears verifies the K2 companion: `/notes clear` still
// clears via an empty-note mutation.
func TestRunHubNotesClearClears(t *testing.T) {
	var calls []appwire.NotesHumanSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodNotesHumanSet, func(_ context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
			calls = append(calls, params)
			return appwire.NotesHumanSetResponse{Note: params.Note}, nil
		})
	})
	defer cleanup()
	m := newSessionHubModel(client)
	m.detail.Live = true
	m.detail.HumanNote = "existing note"

	cmd := m.runHubNotes("clear")
	if cmd == nil {
		t.Fatal("clear should produce a cmd")
	}
	msg, ok := cmd().(hubNotesMsg)
	if !ok || msg.err != nil || !msg.cleared {
		t.Fatalf("clear result = %#v, want successful cleared note", msg)
	}
	if len(calls) != 1 || calls[0].Note != "" {
		t.Fatalf("clear calls = %#v, want one empty-note mutation", calls)
	}
}

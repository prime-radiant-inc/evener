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
	m.detail.Capabilities.SharedNotes = true
	// Busy session: the usage line carries no idle-wake warning (covered
	// separately in hub_notes_l2_test.go's idle/busy pair), so the exact
	// usage text pins here.
	m.detail.State = appwire.ThreadStatusActive
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

// TestRunHubNotesQuotedClearSetsLiteral verifies the bare-`clear`
// reservation keeps an escape path: `/notes "clear"` (or single-quoted)
// sets the literal word instead of wiping the note. Only the exact quoted
// word unquotes — quoting is an opt-in escape, not a string syntax.
func TestRunHubNotesQuotedClearSetsLiteral(t *testing.T) {
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
	m.detail.Capabilities.SharedNotes = true
	m.detail.HumanNote = "existing note"

	for _, quoted := range []string{`"clear"`, `'clear'`, `  "clear"  `, `"CLEAR"`, `'Clear'`} {
		calls = nil
		cmd := m.runHubNotes(quoted)
		if cmd == nil {
			t.Fatalf("%q should produce a cmd", quoted)
		}
		msg, ok := cmd().(hubNotesMsg)
		if !ok || msg.err != nil || msg.cleared {
			t.Fatalf("%q result = %#v, want a non-clear set", quoted, msg)
		}
		want := "clear"
		switch quoted {
		case `"CLEAR"`:
			want = "CLEAR"
		case `'Clear'`:
			want = "Clear"
		}
		if len(calls) != 1 || calls[0].Note != want {
			t.Fatalf("%q calls = %#v, want one literal-word mutation", quoted, calls)
		}
	}
	// Quoting anything else is verbatim: no string syntax is implemented.
	calls = nil
	cmd := m.runHubNotes(`"hello world"`)
	if cmd == nil {
		t.Fatal(`quoted phrase should produce a cmd`)
	}
	if msg, ok := cmd().(hubNotesMsg); !ok || msg.err != nil {
		t.Fatalf("quoted phrase result = %#v, want success", msg)
	}
	if len(calls) != 1 || calls[0].Note != `"hello world"` {
		t.Fatalf("quoted phrase calls = %#v, want the quotes verbatim", calls)
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
	m.detail.Capabilities.SharedNotes = true
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
	// Bare case variants still clear: the reservation is case-insensitive.
	for _, variant := range []string{"CLEAR", "Clear", "  cLeAr  "} {
		calls = nil
		cmd := m.runHubNotes(variant)
		if cmd == nil {
			t.Fatalf("%q should produce a cmd", variant)
		}
		if msg, ok := cmd().(hubNotesMsg); !ok || msg.err != nil || !msg.cleared {
			t.Fatalf("%q result = %#v, want successful cleared note", variant, msg)
		}
		if len(calls) != 1 || calls[0].Note != "" {
			t.Fatalf("%q calls = %#v, want one empty-note mutation", variant, calls)
		}
	}
}

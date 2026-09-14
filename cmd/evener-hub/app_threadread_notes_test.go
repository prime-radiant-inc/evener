package hub

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
)

// TestPastThreadReadProjectsPersistedAgentNotesAndURLs mirrors
// TestPastThreadReadProjectsPersistedGoal (app_threadread_tasks_test.go):
// stored human/agent notes and the URL list project into the past thread
// snapshot; an empty stored state reads as absent, never invented.
func TestPastThreadReadProjectsPersistedAgentNotesAndURLs(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	entry.Meta.HumanNote = "human hello"
	entry.Meta.AgentNote = "agent hello"
	entry.Meta.SessionURLs = []schema.SessionURL{
		{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent", AddedAt: 7},
	}

	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	// Metadata is only a human-note projection, never a cutover import source.
	if thread.Evener.HumanNote != "" || thread.Evener.AgentNote != "agent hello" {
		t.Fatalf("persisted notes = %q/%q, want empty/agent hello", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
	if len(thread.Evener.SessionURLs) != 1 {
		t.Fatalf("persisted urls = %+v, want one entry", thread.Evener.SessionURLs)
	}
	got := thread.Evener.SessionURLs[0]
	if got.ID != "u1" || got.URL != "https://x.test/y" || got.Label != "x" || got.AddedBy != "agent" || got.AddedAt != 7 {
		t.Fatalf("persisted url = %+v, want u1/https://x.test/y/x/agent/7", got)
	}
	if !thread.Evener.Capabilities.SharedNotes {
		t.Fatal("past thread capabilities miss SharedNotes")
	}
	// The projection must not alias the stored meta: mutating the served
	// snapshot must leave the past index's SessionMeta unchanged.
	thread.Evener.SessionURLs[0].URL = "mutated"
	if entry.Meta.SessionURLs[0].URL != "https://x.test/y" {
		t.Fatal("past notes projection aliases stored meta")
	}
}

// TestPastThreadForListToleratesCorruptMutationJournal guards the roster path:
// pastEntryThreadForList runs once per past entry and every sibling per-entry
// read in it degrades instead of aborting, but the canonical human-note read
// used to hard-fail on any error. One session with an undecodable
// mutations/<id>.json must therefore read as "no canonical note" (and keep its
// other metadata) rather than failing the entire session list.
func TestPastThreadForListToleratesCorruptMutationJournal(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	entry.Meta.AgentNote = "agent hello"
	journal := filepath.Join(stateDir, "mutations", sessionID+".json")
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("{ this is not a decodable snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}

	thread, err := pastEntryThreadForList(context.Background(), cfg, entry)
	if err != nil {
		t.Fatalf("pastEntryThreadForList with a corrupt journal: %v", err)
	}
	if thread.Evener.HumanNote != "" {
		t.Fatalf("corrupt journal invented human note %q", thread.Evener.HumanNote)
	}
	if thread.Evener.AgentNote != "agent hello" {
		t.Fatalf("entry metadata dropped: agent note = %q", thread.Evener.AgentNote)
	}
}

// TestPastThreadForListReadsNoteFromStrictRejectedSnapshot pins the roster's
// use of the lightweight projection read: a well-formed document the strict
// authority decode refuses (an unknown top-level field) still contributes its
// top-level human note to the read-only list. That is what proves the
// per-entry roster read no longer strict-decodes the mutations journal; under
// the strict reader the roster would degrade to "no canonical note" and this
// would fail.
func TestPastThreadForListReadsNoteFromStrictRejectedSnapshot(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	dir := filepath.Join(stateDir, "mutations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(
		`{"unknown_notes_state":true,"version":1,"session_id":%q,"human_note":"roster light","accepted_turns":0,"journal":{},"input_queue":[],"queue_revision":0,"next_turn_sequence":0,"next_queue_entry_sequence":0,"budget_reservations":{},"pending_executions":{}}`,
		sessionID)
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := agent.ReadCanonicalHumanNote(stateDir, sessionID); err == nil {
		t.Fatal("strict reader accepted the fixture; the roster contrast needs a document it rejects")
	}

	thread, err := pastEntryThreadForList(context.Background(), cfg, entry)
	if err != nil {
		t.Fatalf("pastEntryThreadForList: %v", err)
	}
	if thread.Evener.HumanNote != "roster light" {
		t.Fatalf("roster human note = %q, want %q", thread.Evener.HumanNote, "roster light")
	}
}

func TestPastThreadReadNotesAbsentWhenUnset(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if thread.Evener.HumanNote != "" || thread.Evener.AgentNote != "" {
		t.Fatalf("unset notes = %q/%q, want empty", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
	if thread.Evener.SessionURLs != nil {
		t.Fatalf("unset urls = %+v, want nil", thread.Evener.SessionURLs)
	}
}

func TestPastThreadReadCanonicalHumanNote(t *testing.T) {
	for _, note := range []string{"saved", ""} {
		t.Run(note, func(t *testing.T) {
			cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
			entry, ok := cfg.Past.Find(sessionID)
			if !ok {
				t.Fatal("past entry not found")
			}
			entry.Meta.HumanNote = "stale metadata"
			dir := filepath.Join(entry.StateDir, "mutations")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			data := []byte(fmt.Sprintf(`{"version":1,"session_id":%q,"human_note":%q,"accepted_turns":0,"journal":{},"input_queue":[],"queue_revision":0,"next_turn_sequence":0,"next_queue_entry_sequence":0,"budget_reservations":{},"pending_executions":{}}`, sessionID, note))
			path := filepath.Join(dir, sessionID+".json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			thread, err := pastEntryThread(context.Background(), cfg, entry, false)
			if err != nil {
				t.Fatal(err)
			}
			if thread.Evener.HumanNote != note {
				t.Fatalf("past note = %q, want committed %q", thread.Evener.HumanNote, note)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, data) {
				t.Fatal("past read rewrote journal")
			}
		})
	}
}

// A legacy session's hub-side metadata predates the write-path strip: the agent
// note and the URL fields are projected straight onto the wire for the roster,
// which the TUI prints directly, so the projection sanitizes them rather than
// trusting what was persisted (roborev's fifth round).
func TestPastThreadListSanitizesLegacyAgentNoteAndURLs(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	const payload = "\x1b]0;owned\x07note\u009b31m\u0085"
	entry.Meta.AgentNote = payload
	entry.Meta.SessionURLs = []schema.SessionURL{{
		ID: "u\x1b1", URL: "https://x.test/a\u0085b", Label: payload, AddedBy: "agent", AddedAt: 7,
	}}

	thread, err := pastEntryThreadForList(context.Background(), cfg, entry)
	if err != nil {
		t.Fatalf("pastEntryThreadForList: %v", err)
	}
	assertPlain := func(surface, text string) {
		t.Helper()
		for _, r := range text {
			if unicode.IsControl(r) && r != '\n' {
				t.Fatalf("%s = %q carries control rune %U", surface, text, r)
			}
		}
	}
	assertPlain("projected agent note", thread.Evener.AgentNote)
	if len(thread.Evener.SessionURLs) != 1 {
		t.Fatalf("projected urls = %+v, want one entry", thread.Evener.SessionURLs)
	}
	assertPlain("projected URL", thread.Evener.SessionURLs[0].URL)
	assertPlain("projected URL id", thread.Evener.SessionURLs[0].ID)
	assertPlain("projected URL label", thread.Evener.SessionURLs[0].Label)
	if !strings.Contains(thread.Evener.AgentNote, "note") {
		t.Fatalf("projected agent note %q lost its text", thread.Evener.AgentNote)
	}
}

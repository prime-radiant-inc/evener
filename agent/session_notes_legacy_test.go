package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// Values persisted before the write-path control strip can still carry terminal
// controls: a note or label saved then reaches the TUI drawer, the model copy
// and the tool output when the session is resumed. Roborev asked for the legacy
// case to be closed at the load boundary rather than only at write time, so
// restore normalizes what it reads and the next metadata save persists the
// cleaned values.
func TestRestoredLegacyNotesCarryNoTerminalControls(t *testing.T) {
	const payload = "\x1b]0;owned\x07note\u009b31m\x7f"
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()

	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "test",
		Model:     "gpt-5.2",
		AgentNote: payload,
		SessionURLs: []schema.SessionURL{{
			ID:    "u1",
			URL:   "https://x.test/\u009b31m",
			Label: payload,
		}},
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	// The canonical human note is the mutation snapshot's, so the legacy value
	// has to be planted there too. The document is built with encoding/json
	// rather than the %q shortcut the ASCII fixtures use: a control rune has to
	// appear as a JSON escape, and %q writes a Go escape JSON rejects.
	humanJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal legacy note: %v", err)
	}
	writeMutationSnapshotFile(t, stateDir, sessionID, fmt.Sprintf(
		`{"version":1,"session_id":%q,"human_note":%s,"accepted_turns":0,"journal":{},"input_queue":[],"queue_revision":0,"next_turn_sequence":0,"next_queue_entry_sequence":0,"budget_reservations":{},"pending_executions":{}}`,
		sessionID, humanJSON))

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}})
	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer sess.Close()

	assertNoControlText := func(surface, text string) {
		t.Helper()
		for _, r := range text {
			if unicode.IsControl(r) {
				t.Fatalf("%s = %q carries control rune %U", surface, text, r)
			}
		}
	}

	restored := sess.Meta()
	assertNoControlText("restored agent note", restored.AgentNote)
	assertNoControlText("restored human note", restored.HumanNote)
	if !strings.Contains(restored.AgentNote, "note") {
		t.Fatalf("restored agent note %q lost its printable text", restored.AgentNote)
	}
	if len(restored.SessionURLs) != 1 {
		t.Fatalf("restored URL list = %d entries, want 1", len(restored.SessionURLs))
	}
	assertNoControlText("restored URL", restored.SessionURLs[0].URL)
	assertNoControlText("restored URL label", restored.SessionURLs[0].Label)

	human, agentNote, urls, _ := sess.notesProjectionSnapshot()
	assertNoControlText("projection human note", human)
	assertNoControlText("projection agent note", agentNote)
	for _, entry := range urls {
		assertNoControlText("projection URL", entry.URL)
		assertNoControlText("projection URL label", entry.Label)
	}

	// The two disk readers the hub uses must hand back clean text as well: the
	// roster reads the file directly and never goes through a Session.
	if note, _, err := ReadCanonicalHumanNote(stateDir, sessionID); err != nil {
		t.Fatalf("ReadCanonicalHumanNote: %v", err)
	} else {
		assertNoControlText("strict reader note", note)
	}
	if note, present, err := ReadPersistedHumanNote(stateDir, sessionID); err != nil || !present {
		t.Fatalf("ReadPersistedHumanNote = (%q, %v, %v), want a present note", note, present, err)
	} else {
		assertNoControlText("light reader note", note)
	}
}

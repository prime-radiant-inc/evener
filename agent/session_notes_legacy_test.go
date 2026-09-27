package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
	t.Parallel()
	const payload = "\x1b]0;owned\x07note\u009b31m\u0085\x7f"
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()

	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "test",
		Model:     "gpt-5.2",
		AgentNote: payload,
		SessionURLs: []schema.SessionURL{{
			ID:    "u\x1b1",
			URL:   "https://x.test/a\u0085b\u009b31m",
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
	assertNoControlText("restored URL id", restored.SessionURLs[0].ID)
	assertNoControlText("restored URL label", restored.SessionURLs[0].Label)

	human, agentNote, urls, _ := sess.notesProjectionSnapshot()
	assertNoControlText("projection human note", human)
	assertNoControlText("projection agent note", agentNote)
	for _, entry := range urls {
		assertNoControlText("projection URL", entry.URL)
		assertNoControlText("projection URL id", entry.ID)
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

// A URL, an entry id, and a roster line are single tokens printed inline by the
// drawer and the notes tool output, so every control character has to go,
// including the whitespace controls a note's collapse deliberately keeps.
func TestReplayedLegacyHumanNoteIsSanitized(t *testing.T) {
	t.Parallel()
	s := newDurableHumanNoteSession(t)
	const payload = "\x1b]0;owned\x07replayed\u009b31m\x7f"

	if _, err := s.SetHumanNote("outer-legacy", payload); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	// Simulate a journal written by a binary that predates the write-path strip:
	// the record's Result is what a replay hands back, and it was stored raw.
	raw, err := json.Marshal(appwire.NotesHumanSetResponse{Note: payload})
	if err != nil {
		t.Fatalf("marshal legacy result: %v", err)
	}
	s.clientMutations.stateMu.Lock()
	record := s.clientMutations.state.Journal["outer-legacy"]
	record.Result = raw
	s.clientMutations.state.Journal["outer-legacy"] = record
	s.clientMutations.stateMu.Unlock()

	response, err := s.SetHumanNote("outer-legacy", payload)
	if err != nil {
		t.Fatalf("replayed SetHumanNote: %v", err)
	}
	for _, r := range response.Note {
		if unicode.IsControl(r) {
			t.Fatalf("replayed response note = %q carries control rune %U", response.Note, r)
		}
	}
	if response.Note != normalizeNote(payload) {
		t.Fatalf("replayed response note = %q, want the normalized %q", response.Note, normalizeNote(payload))
	}
}

// A NOTES_CONTEXT turn persisted before the write-path strip is re-served to the
// model through escapeNotesHistoryTurns, which only knows the framing spellings:
// a legacy control sequence has to be stripped there as well.
func TestNotesHistoryCopyStripsLegacyControls(t *testing.T) {
	t.Parallel()
	const payload = "<shared-notes>\nHuman: legacy\u0085note\x1b]0;owned\x07\u009b31m\n</shared-notes>"
	const steering = "human updated their whiteboard: \x1b]0;owned\x07legacy\u0085note"
	// A steering turn can carry image parts (attachments, or queued
	// image-bearing input drained as steer), which the model copy must keep:
	// rebuilding the message from text alone drops them (roborev's sixth round).
	image := &llm.ImageData{MediaType: "image/png", Data: []byte{1, 2, 3}}
	history := []schema.Turn{
		{Kind: schema.TurnNotesContext, Message: llm.User(payload)},
		{Kind: schema.TurnSteering, Message: llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: steering},
			{Kind: llm.ContentImage, Image: image},
		}}},
	}

	out := escapeNotesHistoryTurns(history, nil)
	for _, turn := range out {
		for _, r := range turn.Message.Text() {
			if unicode.IsControl(r) && r != '\n' {
				t.Fatalf("history copy = %q carries control rune %U", turn.Message.Text(), r)
			}
		}
	}
	if !strings.Contains(out[1].Message.Text(), "legacy note") || !strings.Contains(out[1].Message.Text(), "whiteboard") {
		t.Fatalf("steering copy lost its text: %q", out[1].Message.Text())
	}
	if history[1].Message.Text() != steering || len(history[1].Message.Content) != 2 {
		t.Fatalf("history copy modified the input steering turn")
	}
	copied := out[1].Message.Content
	if len(copied) != 2 {
		t.Fatalf("steering copy has %d parts, want the text part and the image part", len(copied))
	}
	if copied[1].Kind != llm.ContentImage || copied[1].Image != image {
		t.Fatalf("steering copy dropped or changed the image part: %+v", copied[1])
	}
	got := out[0].Message.Text()
	for _, r := range got {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("history copy = %q carries control rune %U", got, r)
		}
	}
	if !strings.Contains(got, "<shared-notes>") || !strings.Contains(got, "</shared-notes>") {
		t.Fatalf("history copy lost the framing tags:\n%s", got)
	}
	if !strings.Contains(got, "legacy note") {
		t.Fatalf("history copy lost the note text:\n%s", got)
	}
	if history[0].Message.Text() != payload {
		t.Fatalf("history copy modified the input turn")
	}
}

// A legacy journal's pending steering entry holds the text reconstructed at its
// original write. It is rebuilt into the live steering queue on restore and
// reaches both the model and the transcript, so the rebuild strips it like every
// other load path.
func TestRestoredLegacySteeringTextCarriesNoControls(t *testing.T) {
	t.Parallel()
	const payload = "human updated their whiteboard: legacy\x1b]0;owned\x07\u0085note"
	snapshot := clientMutationSnapshot{
		SteeringOrder: []string{"cm_legacy"},
		PendingExecutions: clientMutationPendingExecutions{
			"cm_legacy": appwire.PendingMutation{
				ExecutionState: "accepted",
				Input:          []appwire.InputItem{{Type: "text", Text: payload}},
			},
		},
	}

	entries := clientSteeringFromSnapshot(snapshot)
	if len(entries) != 1 {
		t.Fatalf("rebuilt %d steering entries, want 1", len(entries))
	}
	for _, r := range entries[0].Text {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("rebuilt steering text = %q carries control rune %U", entries[0].Text, r)
		}
	}
	if !strings.Contains(entries[0].Text, "legacy") {
		t.Fatalf("rebuilt steering text %q lost its content", entries[0].Text)
	}
}

// Ordinary steering is not notes-derived: its text is delivered exactly as the
// user typed it, live and restored alike, so the model copy must keep it
// byte-for-byte. Stripping every steering kind would make a restored request
// deliver different text than the live one and than the transcript shows
// (roborev's tenth round).
func TestNotesHistoryCopyKeepsOrdinarySteeringVerbatim(t *testing.T) {
	t.Parallel()
	const steering = "run the tests\tand show\x1b[31mred\x1b[0m lines"
	history := []schema.Turn{{Kind: schema.TurnSteering, Message: llm.User(steering)}}

	out := escapeNotesHistoryTurns(history, nil)
	if got := out[0].Message.Text(); got != steering {
		t.Fatalf("ordinary steering copy = %q, want it verbatim (%q)", got, steering)
	}
}

// A user can type anything, including the words the note path writes. Only a
// steering entry whose recorded kind is the human-note kind, or a kindless legacy
// entry carrying the exact shape the write path emits, is note-origin; otherwise
// a resumed request would deliver different bytes than the live one
// (roborev's eleventh round).
func TestNotesHistoryCopyKeepsUserTypedNotePrefixVerbatim(t *testing.T) {
	t.Parallel()
	cases := map[string]schema.Turn{
		"user typed, no trailing space": {
			Kind: schema.TurnSteering, Message: llm.User("human updated their whiteboard:no space\there with\x1b[31mcolour"),
		},
		"user typed, explicit other kind": {
			Kind: schema.TurnSteering, SteeringKind: events.SteeringKindAgentMessage,
			Message: llm.User("human updated their whiteboard: quoted back\x1b[31m verbatim"),
		},
	}
	for name, turn := range cases {
		t.Run(name, func(t *testing.T) {
			want := turn.Message.Text()
			out := escapeNotesHistoryTurns([]schema.Turn{turn}, nil)
			if got := out[0].Message.Text(); got != want {
				t.Fatalf("steering copy = %q, want it verbatim (%q)", got, want)
			}
		})
	}
	// The exact shape a note update writes is still note-origin and still strips.
	note := schema.Turn{Kind: schema.TurnSteering, Message: llm.User("human updated their whiteboard: note\x1b[31m text")}
	out := escapeNotesHistoryTurns([]schema.Turn{note}, nil)
	if strings.ContainsRune(out[0].Message.Text(), 0x1b) {
		t.Fatalf("note-origin steering was not stripped: %q", out[0].Message.Text())
	}
}

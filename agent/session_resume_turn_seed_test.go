package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// TestRestoreSessionStartCarriesTranscriptEntryCount (kata eptj) verifies that
// RestoreSessionFromMeta stamps SessionStartData.TranscriptEntries with the
// exact number of entries transcript.OpenWriterForSession validated on this
// resume — the same count internal/apptranscript's reload path would reach
// scanning the identical file (it numbers turn ids by entry index). A live
// event consumer (internal/appprojector) seeds its own turn-id counter from
// this field so a resumed session's first live turn never mints an id the
// reload path already assigned to a persisted entry.
//
// s.modelResponses (the SessionStartData.Turns field) is deliberately NOT
// this count: it tracks LLM round-trips only, and undercounts whenever a
// non-model-response entry (e.g. a durable steering reminder,
// session_lifecycle.go's appendTurnDurably) was appended to the transcript.
// This test's transcript has exactly 2 entries, both ordinary turns, so a
// regression that quietly swapped the field back to s.modelResponses would
// still pass here — TestRestoreSessionStartTranscriptEntryCountExceedsTurns
// below is the one that forces the distinction.
func TestRestoreSessionStartCarriesTranscriptEntryCount(t *testing.T) {
	dir := t.TempDir()
	id := "01RESUMETURNSEED0000001"

	tpath := filepath.Join(dir, sessionsSubdir, id+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("append user turn: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("hi"))); err != nil {
		t.Fatalf("append assistant turn: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}

	data, ok := resumeTurnSeedFindSessionStart(t, restored)
	restored.Close()
	if !ok {
		t.Fatal("no SESSION_START event found on the restored session's stream")
	}
	if data.TranscriptEntries != 2 {
		t.Fatalf("SessionStartData.TranscriptEntries = %d, want 2 (the transcript's two persisted entries)", data.TranscriptEntries)
	}
}

// TestRestoreSessionStartTranscriptEntryCountExceedsTurns (kata eptj) proves
// SessionStartData.TranscriptEntries is not just an alias for the pre-existing
// Turns field (s.modelResponses). The transcript here has 2 persisted entries,
// but the persisted meta claims only 1 model round-trip (TurnCount: 1) — the
// gap a real session leaves whenever it durably appends a non-model-response
// entry, e.g. session_lifecycle.go's reconnect steering reminder
// (appendTurnDurably(schema.TurnSteering, ...)). If a future edit collapsed
// TranscriptEntries back to reading s.modelResponses, this test would still
// see 1, not the 2 the reload path's entry-index numbering would actually
// reach for this same file.
func TestRestoreSessionStartTranscriptEntryCountExceedsTurns(t *testing.T) {
	dir := t.TempDir()
	id := "01RESUMETURNSEED0000002"

	tpath := filepath.Join(dir, sessionsSubdir, id+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("append user turn: %v", err)
	}
	// A durable non-model-response entry (schema.TurnSteering), mirroring what
	// session_lifecycle.go's reconnect steering reminder persists via
	// appendTurnDurably — a second transcript entry that never increments
	// s.modelResponses.
	if err := tw.Append(schema.NewTurn(schema.TurnSteering, llm.User("reminder: ..."))); err != nil {
		t.Fatalf("append steering turn: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		TurnCount: 1, // fewer than the transcript's 2 persisted entries
		Config:    (SessionConfig{}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}

	data, ok := resumeTurnSeedFindSessionStart(t, restored)
	restored.Close()
	if !ok {
		t.Fatal("no SESSION_START event found on the restored session's stream")
	}
	if data.Turns != 1 {
		t.Fatalf("test setup: SessionStartData.Turns = %d, want 1 (meta.TurnCount passthrough)", data.Turns)
	}
	if data.TranscriptEntries != 2 {
		t.Fatalf("SessionStartData.TranscriptEntries = %d, want 2 — must count persisted entries, not s.modelResponses (Turns=%d)", data.TranscriptEntries, data.Turns)
	}
}

// resumeTurnSeedFindSessionStart drains sess.Events() non-destructively up to
// the first SESSION_START (every event up to and including it was already
// queued synchronously during construction, so this never blocks).
func resumeTurnSeedFindSessionStart(t *testing.T, sess *Session) (events.SessionStartData, bool) {
	t.Helper()
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == events.EventSessionStart {
				data, ok := ev.Data.(events.SessionStartData)
				return data, ok
			}
		default:
			return events.SessionStartData{}, false
		}
	}
}

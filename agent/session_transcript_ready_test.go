package agent

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// The transcript writer cannot be created until initSessionState has run: its
// header carries the rendered system prompt and the agent's starting task list,
// neither of which exists until plugins, skills and tools are loaded. SessionStart
// hooks run inside that same window, so a session has a period during which turns
// are produced and there is nowhere to put them.
//
// transcript.Writer.Append is nil-safe by design — a session with no state
// directory must be able to write without every call site nil-checking — which
// means a write into that window returns nil and vanishes. Kata qm9y lost every
// SessionStart hook exit that way with a green suite; kata d4es is the hazard
// itself. These tests pin the gate that makes the window safe for any writer,
// not just the one that discovered it.

// transcriptTurnKinds returns the kind of every entry in a transcript file.
func transcriptTurnKinds(t *testing.T, path string) []schema.TurnKind {
	t.Helper()
	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	kinds := make([]schema.TurnKind, 0, len(data.Entries))
	for _, e := range data.Entries {
		kinds = append(kinds, e.Turn.Kind)
	}
	return kinds
}

// A turn recorded before the writer exists must reach the file once it does.
// recordTurn is the path four of the six transcript call sites funnel through,
// so gating it is what stops the next construction-time writer repeating qm9y.
func TestTurnRecordedBeforeTranscriptWriterExistsReachesTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{StateDir: dir, MaxSubagentDepth: 1}))
	go func() {
		for range sess.Events() {
		}
	}()
	tpath := sess.TranscriptPath()

	// Reproduce the construction window: the writer has not been attached yet.
	w := sess.transcript
	sess.mu.Lock()
	sess.transcript = nil
	sess.transcriptReady = false
	sess.mu.Unlock()

	sess.recordTurn(
		schema.NewTurn(schema.TurnSteering, llm.User("written before the writer existed")),
		schema.NewTurn(schema.TurnSteering, llm.User("written before the writer existed")),
	)

	// The writer arrives, exactly as session_init.go attaches it.
	sess.attachTranscript(w)
	sess.Close()

	var found int
	for _, k := range transcriptTurnKinds(t, tpath) {
		if k == schema.TurnSteering {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("STEERING entries in transcript: got %d, want 1 — a turn recorded before the writer existed was dropped", found)
	}
}

// The durable variant is the same window with a stronger promise, so it has to
// be gated too. Its returned error stays nil: a held turn has not failed.
func TestDurableTurnRecordedBeforeTranscriptWriterExistsReachesTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{StateDir: dir, MaxSubagentDepth: 1}))
	go func() {
		for range sess.Events() {
		}
	}()
	tpath := sess.TranscriptPath()

	w := sess.transcript
	sess.mu.Lock()
	sess.transcript = nil
	sess.transcriptReady = false
	sess.mu.Unlock()

	if err := sess.appendTurnDurably(schema.TurnSteering, llm.User("held")); err != nil {
		t.Fatalf("appendTurnDurably before the writer exists: %v, want nil", err)
	}
	sess.attachTranscript(w)
	sess.Close()

	var found int
	for _, k := range transcriptTurnKinds(t, tpath) {
		if k == schema.TurnSteering {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("STEERING entries in transcript: got %d, want 1 — a durable turn recorded before the writer existed was dropped", found)
	}
}

// The reverse risk. A session with no state directory never gets a writer, and
// a session whose writer failed to open never gets one either — both are
// legitimate, permanent nil, and by far the common case (the whole agent suite
// runs that way). Holding turns for a sink that will never exist would grow the
// slice for the session's lifetime, so readiness, not the writer's presence, has
// to be what releases the gate.
func TestTurnsAreNotHeldOnceTheSessionKnowsItHasNoTranscript(t *testing.T) {
	t.Parallel()
	// No StateDir: attachTranscript is still reached, with a nil writer.
	sess := newSession(t)
	go func() {
		for range sess.Events() {
		}
	}()
	if sess.transcript != nil {
		t.Fatalf("session without a state directory has a transcript writer")
	}
	for range 32 {
		sess.recordTurn(
			schema.NewTurn(schema.TurnSteering, llm.User("no sink")),
			schema.NewTurn(schema.TurnSteering, llm.User("no sink")),
		)
	}
	sess.mu.Lock()
	held := len(sess.pendingTranscriptTurns)
	ready := sess.transcriptReady
	sess.mu.Unlock()
	if !ready {
		t.Fatal("a session with no state directory never became transcript-ready; turns would accumulate for its lifetime")
	}
	if held != 0 {
		t.Fatalf("held turns: got %d, want 0 — turns are being buffered for a sink that will never exist", held)
	}
}

// attachTranscript is the single readiness transition, so it must tolerate the
// nil writer that a failed transcript.NewWriter leaves behind without losing the
// hold on subsequent writes.
func TestAttachTranscriptWithNilWriterDrainsWithoutPanic(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	go func() {
		for range sess.Events() {
		}
	}()
	sess.mu.Lock()
	sess.transcript = nil
	sess.transcriptReady = false
	sess.pendingTranscriptTurns = []schema.Turn{schema.NewTurn(schema.TurnSteering, llm.User("orphan"))}
	sess.mu.Unlock()

	sess.attachTranscript(nil)

	sess.mu.Lock()
	held := len(sess.pendingTranscriptTurns)
	sess.mu.Unlock()
	if held != 0 {
		t.Fatalf("held turns after attaching a nil writer: got %d, want 0", held)
	}
}

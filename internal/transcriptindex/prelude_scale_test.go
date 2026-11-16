package transcriptindex

import (
	"fmt"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestRepeatedSnapshotReadsDoNotScaleWithSessionSize pins the cost of the read
// production makes: the hub opens a handle, calls ReadAt for one turn window,
// and releases it. That read wraps the handle in a fresh view per request, so
// anything cached on the view is recomputed on the next one. This goes through
// ReadAt rather than ResolveTurnWindow for exactly that reason: a test calling
// the handle directly would miss the per-request view, and a memo kept there
// would look effective while production paid the walk every time.
//
// The header carries a SystemPrompt and no entry opens a turn_system summary,
// so the prelude is a transcript position the turn table does not hold and its
// id is checked on every turn-addressed read.
//
// The first read on a handle may pay that check once — an O(turns) cost per
// acquire, which the issue does not claim to fix. What it claims is the
// per-request cost, so the assertion is on the second read: a steady-state read
// must be bounded, and must not grow with the session.
func TestRepeatedSnapshotReadsDoNotScaleWithSessionSize(t *testing.T) {
	previous := recordBufferLimit
	recordBufferLimit = 1
	t.Cleanup(func() { recordBufferLimit = previous })

	small := steadyStateSnapshotRead(t, 40)
	large := steadyStateSnapshotRead(t, 160)
	t.Logf("second read: small=%d bytes large=%d bytes", small, large)
	// A walk of 160 turn summaries is 160*turnRecordSize = 26,880 bytes. The
	// second read opens the sidecars and does a lookup, so a fixed ceiling a
	// walk cannot meet holds, and the fourfold larger session must not double
	// the cost.
	if large > 8<<10 {
		t.Fatalf("the second snapshot read cost %d bytes on a 160-turn session, over the 8 KiB ceiling", large)
	}
	if large >= 2*small {
		t.Fatalf("the second snapshot read cost %d bytes on a 160-turn session and %d on a 40-turn one: cost tracks the session, not the window",
			large, small)
	}
}

// steadyStateSnapshotRead answers two single-turn snapshot reads of a
// synthetic session and returns what the second was charged against the turn
// table.
func steadyStateSnapshotRead(t *testing.T, turns int) int64 {
	t.Helper()
	path := writePreludeSession(t, turns)
	x := openIndex(t, path, filepath.Join(t.TempDir(), "index"))
	t.Cleanup(func() { _ = x.Close() })

	counted := &countingRecordFile{recordFile: x.turns.file}
	x.turns.file = counted
	target := fmt.Sprintf("turn_%d", turns/2)
	var second int64
	for i := range 2 {
		before := counted.readBytes
		if err := x.ReadAt(x.meta.Length, func(s *Snapshot) error {
			_, err := s.ResolveTurnWindow(appwire.ThreadTurnWindow{Turn: target})
			return err
		}); err != nil {
			t.Fatalf("ReadAt: %v", err)
		}
		if i == 1 {
			second = counted.readBytes - before
		}
	}
	return second
}

// writePreludeSession writes a transcript whose header carries a system prompt,
// then execution turns named turn_0 through turn_{n-1}. No entry opens a
// turn_system summary, so the prelude stays a position of its own.
func writePreludeSession(t *testing.T, turns int) string {
	t.Helper()
	lines := make([]fixtureLine, 0, 3*turns)
	for i := range turns {
		id := fmt.Sprintf("turn_%d", i)
		lines = append(lines,
			entryLine(opens(id, schema.TurnSpanExecution, user(fmt.Sprintf("question %d", i)))),
			entryLine(inTurn(id, assistant(text(fmt.Sprintf("answer %d", i))))),
			entryLine(completion(id, schema.TurnCompleted, i, 1)),
		)
	}
	return writeFixture(t, fixture{
		header: transcript.Header{SessionID: "prelude_scale", SystemPrompt: "you are a test"},
		lines:  lines,
	})
}

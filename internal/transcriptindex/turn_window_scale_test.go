package transcriptindex

import (
	"fmt"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// countingRecordFile counts the record bytes a read consumes, so a test can
// assert how a read's cost scales without a wall-clock measurement.
type countingRecordFile struct {
	recordFile
	readBytes int64
	reads     int
}

func (f *countingRecordFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.recordFile.ReadAt(p, off)
	f.readBytes += int64(n)
	f.reads++
	return n, err
}

// TestTurnWindowScanDoesNotScaleWithSessionSize pins the cost of one
// turn-addressed read: a single-turn window must read a bounded number of
// record bytes, and that bound must hold for a transcript four times larger.
//
// This is the resource oracle the audit's issue names — records scanned
// scaling with the window rather than the session — and it needs no clock:
// recordBufferLimit is shrunk so the tables read through the files, and the
// turns table is wrapped to count what the read actually touches.
func TestTurnWindowScanDoesNotScaleWithSessionSize(t *testing.T) {
	previous := recordBufferLimit
	recordBufferLimit = 1
	t.Cleanup(func() { recordBufferLimit = previous })

	small := countTurnWindowRead(t, 40)
	large := countTurnWindowRead(t, 160)
	t.Logf("reads: small=%+v large=%+v", small, large)
	// A walk of 160 turn summaries is 160*turnRecordSize = 26,880 bytes.
	// Finding the boundary is a hash lookup plus a confirmation probe, so the
	// read stays under a fixed ceiling a walk cannot meet, and grows slower
	// than the fourfold larger session.
	if large.readBytes > 8<<10 {
		t.Fatalf("a one-turn window read %d bytes on a 160-turn session, over the 8 KiB ceiling", large.readBytes)
	}
	if large.readBytes >= 2*small.readBytes {
		t.Fatalf("a one-turn window read %d bytes on a 160-turn session and %d on a 40-turn one: cost tracks the session, not the window",
			large.readBytes, small.readBytes)
	}
}

// countTurnWindowRead answers one single-turn read of a synthetic session and
// returns what the turns table was charged for it.
func countTurnWindowRead(t *testing.T, turns int) *countingRecordFile {
	t.Helper()
	path := writeTurnWindowSession(t, turns)
	x := openIndex(t, path, filepath.Join(t.TempDir(), "index"))
	t.Cleanup(func() { _ = x.Close() })

	counted := &countingRecordFile{recordFile: x.turns.file}
	x.turns.file = counted
	if _, err := x.ResolveTurnWindow(appwire.ThreadTurnWindow{Turn: fmt.Sprintf("turn_%d", turns/2)}); err != nil {
		t.Fatalf("ResolveTurnWindow: %v", err)
	}
	return counted
}

// writeTurnWindowSession writes a transcript of execution turns, each with a
// question and an answer, named turn_0 through turn_{n-1}.
func writeTurnWindowSession(t *testing.T, turns int) string {
	t.Helper()
	lines := make([]fixtureLine, 0, 2*turns)
	for i := range turns {
		id := fmt.Sprintf("turn_%d", i)
		lines = append(lines,
			entryLine(opens(id, schema.TurnSpanExecution, user(fmt.Sprintf("question %d", i)))),
			entryLine(inTurn(id, assistant(text(fmt.Sprintf("answer %d", i))))),
			entryLine(completion(id, schema.TurnCompleted, i, 1)),
		)
	}
	return writeFixture(t, fixture{header: transcript.Header{SessionID: "turn_window_scale"}, lines: lines})
}

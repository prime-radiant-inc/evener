package transcriptindex

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestCacheIdleHandleDropsAndReopensItsSidecars pins the memory bound: a
// released handle keeps neither sidecar open, and the handle that re-acquires
// it reads from them again rather than scanning the turn table for the rest of
// its life.
func TestCacheIdleHandleDropsAndReopensItsSidecars(t *testing.T) {
	lines := make([]fixtureLine, 0, 40)
	for i := range 20 {
		id := fmt.Sprintf("turn_%d", i)
		lines = append(lines,
			entryLine(opens(id, schema.TurnSpanExecution, user(fmt.Sprintf("question %d", i)))),
			entryLine(completion(id, schema.TurnCompleted, i, 1)),
		)
	}
	path := writeFixture(t, fixture{header: transcript.Header{SessionID: "cache_sidecars"}, lines: lines})
	c := NewCache(2)
	t.Cleanup(func() { _ = c.Close() })

	x, err := c.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if x.turnSlots == nil || x.totals.file == nil {
		t.Fatal("a fresh build holds no sidecars")
	}
	c.Release(x)

	// Idle: both files are closed, and nothing borrows them.
	if x.turnSlots != nil {
		t.Fatal("a released handle kept its slot table open")
	}
	if x.totals.file != nil {
		t.Fatal("a released handle kept its totals file open")
	}
	if x.cutView != nil {
		t.Fatal("a released handle kept a cut view that borrows the sidecars")
	}

	// Re-acquired: the next read reopens both, so the read is served from them
	// rather than from a walk of every turn summary.
	again, err := c.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Release(again) })
	if _, err := again.ResolveTurnWindow(appwire.ThreadTurnWindow{Turn: "turn_10"}); err != nil {
		t.Fatalf("ResolveTurnWindow: %v", err)
	}
	if again.turnSlots == nil {
		t.Fatal("a read after re-acquire did not reopen the slot table")
	}
	if _, _, err := again.DerivedTotals(0); err != nil {
		t.Fatalf("DerivedTotals: %v", err)
	}
	if again.totals.file == nil {
		t.Fatal("a read after re-acquire did not reopen the totals file")
	}
	if again.totals.n != again.turns.n {
		t.Fatalf("reopened totals covers %d turns, table holds %d", again.totals.n, again.turns.n)
	}
}

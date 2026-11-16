package transcriptindex

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestReleasedHandleDropsItsTurnOrder is a review regression. A parked cache
// handle holds no record buffers, because idle retention is memory the cache's
// capacity bound was never meant to carry. The turn order is derived from those
// records and holds a copied ID per turn, so it is the same memory and must be
// released with them; otherwise retained memory grows with the turn count of
// every cached transcript.
func TestReleasedHandleDropsItsTurnOrder(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	lines := make([]fixtureLine, 0, 8)
	for i := range 8 {
		lines = append(lines, entryLine(opens("t"+strconv.Itoa(i), schema.TurnSpanExecution, user("q"))))
	}
	fx := fixture{name: "released-turn-order", header: header, lines: lines}
	fxHeader, encoded := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(encoded)...), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup
	x, err := c.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	selector := appwire.ThreadTurnWindow{Latest: 1}
	if _, err := x.ResolveTurnWindow(selector); err != nil {
		t.Fatalf("resolve before release: %v", err)
	}
	if !x.turnOrder.valid {
		t.Fatal("the turn order was not memoized, so this test proves nothing")
	}
	c.Release(x)
	if x.turnOrder.valid || x.turnOrder.order != nil {
		t.Fatal("a released handle still retains its turn order: idle memory grows with the turn count of every cached transcript")
	}

	// The re-acquired handle must still answer.
	again, err := c.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release(again)
	resolved, err := again.ResolveTurnWindow(selector)
	if err != nil {
		t.Fatalf("resolve after re-acquire: %v", err)
	}
	if len(resolved.TurnIDs) != 1 || resolved.TurnIDs[0] != "t7" {
		t.Fatalf("re-acquired resolve = %v, want the newest turn t7", resolved.TurnIDs)
	}
}

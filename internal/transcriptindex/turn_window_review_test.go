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

// turnWindowLines builds n single-entry turns.
func turnWindowLines(n int) []fixtureLine {
	lines := make([]fixtureLine, 0, n)
	for i := range n {
		lines = append(lines, entryLine(opens("t"+strconv.Itoa(i), schema.TurnSpanExecution, user("q"))))
	}
	return lines
}

// TestTurnWindowReplayOwnsItsTurnOrder is a review regression: the full-prefix
// replay must not answer from the handle's turn order.
//
// A private replay can reach the same epoch and turn count describing different
// records — a loaded one-turn build and a replayed one-entry cut are both
// (epoch=2, turns=1) — so sharing the handle's memo would answer a historical
// read with another incarnation's turn IDs.
func TestTurnWindowReplayOwnsItsTurnOrder(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	// The tail announces a deferred communicate call, which makes
	// reconstructAtCut refuse and take the replay.
	lines := append(turnWindowLines(2), entryLine(communicated("t1", "c1", "hello")))
	fx := fixture{name: "turn-replay", header: header, lines: lines}
	fxHeader, encoded := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(encoded)...), 0o600); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	cut := int64(len(fxHeader) + len(encoded[0]))
	if x.reconstructAtCut(cut) != nil {
		t.Fatal("the cut reconstructs, so this test would not exercise the replay fallback")
	}
	if err := x.ReadAt(cut, func(s *Snapshot) error {
		if s.index.turnOrderOwner != nil {
			t.Fatal("a full-prefix replay shares the handle's turn order: its private table can reach the handle's epoch and turn count describing different records")
		}
		resolved, err := s.ResolveTurnWindow(appwire.ThreadTurnWindow{Latest: 1})
		if err != nil {
			return err
		}
		if len(resolved.TurnIDs) != 1 || resolved.TurnIDs[0] != "t0" {
			t.Fatalf("replay resolved %v, want the single replayed turn t0", resolved.TurnIDs)
		}
		return nil
	}); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
}

// TestTurnWindowOrderMemoSeesAHeaderPrelude is a review regression: a header
// that establishes a system prompt changes the prelude without writing a turn
// record, so it moves neither the epoch nor the turn count. The memoized order
// must still include that turn.
func TestTurnWindowOrderMemoSeesAHeaderPrelude(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	fx := fixture{name: "turn-prelude", header: header, lines: turnWindowLines(2)}
	fxHeader, encoded := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(encoded)...), 0o600); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	// Populate the memo while the header carries no prelude.
	first, err := x.ResolveTurnWindow(appwire.ThreadTurnWindow{Latest: 1})
	if err != nil {
		t.Fatalf("resolve before the prelude: %v", err)
	}
	if first.Frontier.TurnCount != 2 {
		t.Fatalf("turn count before the prelude = %d, want 2", first.Frontier.TurnCount)
	}

	// The prelude arrives from the header alone: no turn record is written.
	x.prelude = &appwire.Turn{ID: "system"}

	second, err := x.ResolveTurnWindow(appwire.ThreadTurnWindow{Latest: 1})
	if err != nil {
		t.Fatalf("resolve after the prelude: %v", err)
	}
	if second.Frontier.TurnCount != 3 {
		t.Fatalf("turn count after the prelude = %d, want 3: the memo did not see the header turn", second.Frontier.TurnCount)
	}
	if second.Frontier.OldestTurnID != "system" {
		t.Fatalf("oldest turn after the prelude = %q, want system", second.Frontier.OldestTurnID)
	}
}

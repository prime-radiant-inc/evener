package transcriptindex

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestDerivedTotalsReplayOwnsItsMemo is the regression a review asked for, and
// it asserts the invariant rather than staging the collision.
//
// A full-prefix replay builds a private turn table by replaying a prefix, and
// it must answer from its own memo. The memo is keyed on the table's epoch and
// turn count, which a private prefix can reach describing different records
// than the handle's full table, so sharing the handle's memo lets a historical
// read and a current read answer each other's totals. Staging that collision
// needs an exact epoch and count match, which the public API does not offer;
// the invariant that prevents it is exact and is what the fix establishes.
//
// The read below must take the replay: the tail announces a deferred
// communicate call, and reconstructAtCut refuses a cut whose tail announced one
// because the announce leaves no committed record distinguishing it from a
// re-announcement.
func TestDerivedTotalsReplayOwnsItsMemo(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	usageTurn := func(turn schema.Turn, input, output int) schema.Turn {
		turn.Usage = llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		return turn
	}
	fx := fixture{name: "derived-replay-memo", header: header, lines: []fixtureLine{
		entryLine(opens("t1", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t1", schema.TurnSpanExecution, user("q")), 100, 10)),
		entryLine(opens("t2", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t2", schema.TurnSpanExecution, user("q")), 200, 20)),
		entryLine(communicated("t2", "c1", "hello")),
	}}
	fxHeader, lines := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(lines)...), 0o600); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	cut := int64(len(fxHeader) + len(lines[0]) + len(lines[1]))
	if x.reconstructAtCut(cut) != nil {
		t.Fatal("the cut reconstructs, so this test would not exercise the replay fallback")
	}

	if err := x.ReadAt(cut, func(s *Snapshot) error {
		if s.index.derivedOwner != nil {
			t.Fatal("a full-prefix replay shares the handle's derived memo: its private table can reach the handle's epoch and turn count describing different records")
		}
		// The read must still answer.
		usage, _, err := s.DerivedTotals(0)
		if err != nil {
			return err
		}
		if usage == nil || usage.InputTokens != 100 {
			t.Fatalf("replay totals = %+v, want the first turn's 100 input tokens", usage)
		}
		return nil
	}); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}

	// The current-boundary view does answer from the handle, or the memo it
	// exists to serve would never be reused.
	if err := x.ReadAt(x.meta.Length, func(s *Snapshot) error {
		if s.index.derivedOwner == nil {
			t.Fatal("a current-boundary view does not answer from the handle, so its memo dies with the request")
		}
		return nil
	}); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
}

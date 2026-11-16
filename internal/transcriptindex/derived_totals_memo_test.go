package transcriptindex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// TestDerivedTotalsMemoMatchesAFullRecomputation is the check #6417 asks for: a
// memoized answer must equal what a full recomputation of the same records
// gives, across a mixed sequence of appends.
//
// The comparison is between the resumed handle's memoized answer and a
// freshly opened handle, which recomputes from the records. Comparing against
// the transcript scan would compare an index answer to a different source:
// this handle has not necessarily folded the appended entries into turn
// summaries yet, and the scan reads raw transcript lines.
func TestDerivedTotalsMemoMatchesAFullRecomputation(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	usageTurn := func(turn schema.Turn, input, output int) schema.Turn {
		turn.Usage = llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		return turn
	}
	fx := fixture{name: "derived-memo", header: header, lines: []fixtureLine{
		entryLine(opens("t1", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t1", schema.TurnSpanExecution, user("q")), 100, 10)),
		entryLine(opens("t2", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t2", schema.TurnSpanExecution, user("q")), 200, 20)),
		entryLine(opens("t3", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t3", schema.TurnSpanExecution, user("q")), 300, 30)),
	}}
	fxHeader, lines := fx.encode(t)

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(lines[:2])...), 0o600); err != nil {
		t.Fatal(err)
	}
	// One sidecar directory for every handle, so a later open EXTENDS the
	// previous build instead of starting a private one.
	dir := t.TempDir()

	// The long-lived handle whose memo is under test.
	x := openIndex(t, path, dir)
	defer x.Close() //nolint:errcheck // read-only test handle
	if _, _, err := x.DerivedTotals(0); err != nil {
		t.Fatalf("initial DerivedTotals: %v", err)
	}

	for _, line := range lines[2:] {
		appendBytes(t, path, line)
		// An extender folds the append into the sidecar; without it the resumed
		// handle has no reason to see a new record.
		extender := openIndex(t, path, dir)
		if _, _, err := extender.DerivedTotals(0); err != nil {
			t.Fatalf("extender DerivedTotals: %v", err)
		}
		_ = extender.Close()

		gotUsage, gotFailures, gotErr := x.DerivedTotals(0)
		if gotErr != nil {
			t.Fatalf("memoized DerivedTotals: %v", gotErr)
		}
		fresh := openIndex(t, path, dir)
		wantUsage, wantFailures, wantErr := fresh.DerivedTotals(0)
		_ = fresh.Close()
		if wantErr != nil {
			t.Fatalf("full recomputation: %v", wantErr)
		}
		if gotUsage == nil || wantUsage == nil {
			if (gotUsage == nil) != (wantUsage == nil) {
				t.Fatalf("usage presence differs: memoized=%v recomputed=%v", gotUsage, wantUsage)
			}
			continue
		}
		if *gotUsage != *wantUsage {
			t.Fatalf("memoized usage = %+v, recomputed = %+v", *gotUsage, *wantUsage)
		}
		if gotFailures != wantFailures {
			t.Fatalf("memoized failures = %d, recomputed = %d", gotFailures, wantFailures)
		}
	}
}

// TestDerivedTotalsMemoAgreesWithTheScan pins the memo against the source the
// figure is defined by: the transcript scan. It runs once over a settled index,
// so both sides describe the same folded records.

func TestDerivedTotalsMemoAgreesWithTheScan(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	usageTurn := func(turn schema.Turn, input, output int) schema.Turn {
		turn.Usage = llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		return turn
	}
	fx := fixture{name: "derived-memo-scan", header: header, lines: []fixtureLine{
		entryLine(opens("t1", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t1", schema.TurnSpanExecution, user("q")), 100, 10)),
		entryLine(opens("t2", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t2", schema.TurnSpanExecution, user("q")), 200, 20)),
	}}
	fxHeader, lines := fx.encode(t)

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(lines)...), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	x := openIndex(t, path, dir)
	defer x.Close() //nolint:errcheck // read-only test handle

	for from := 0; from <= len(lines)+1; from++ {
		gotUsage, gotFailures, gotErr := x.DerivedTotals(from)
		wantUsage, wantFailures, wantErr := apptranscript.NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, from)
		if wantErr != nil {
			t.Fatalf("cut %d: scan failed: %v", from, wantErr)
		}
		if errors.Is(gotErr, ErrDerivedTotalsUnavailable) {
			// A cut between a turn's first and last entry cannot be expressed by
			// the turn record, so the index refuses and the caller falls back to
			// the scan. That refusal is the documented contract, not a failure —
			// what must never happen is a figure the scan would not produce.
			continue
		}
		if gotErr != nil {
			t.Fatalf("cut %d: unexpected error: %v", from, gotErr)
		}
		if gotUsage == nil || wantUsage == nil {
			if (gotUsage == nil) != (wantUsage == nil) {
				t.Fatalf("cut %d: usage presence differs: index=%v scan=%v", from, gotUsage, wantUsage)
			}
			continue
		}
		if *gotUsage != *wantUsage || gotFailures != wantFailures {
			t.Fatalf("cut %d: index = (%+v, %d), scan = (%+v, %d)", from, *gotUsage, gotFailures, *wantUsage, wantFailures)
		}
	}
}

// TestDerivedTotalsMemoSurvivesAcrossObservedReads pins the memo where
// production actually keeps it. ReadObserved wraps the handle in a fresh view
// per request (readAtLocked), so a memo stored on the view would be discarded
// before the next request could reuse it: every read of a large history would
// walk every turn record again. This drives the real entry point twice and
// requires the second read to answer without walking.
func TestDerivedTotalsMemoSurvivesAcrossObservedReads(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	usageTurn := func(turn schema.Turn, input, output int) schema.Turn {
		turn.Usage = llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		return turn
	}
	fx := fixture{name: "derived-memo-observed", header: header, lines: []fixtureLine{
		entryLine(opens("t1", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t1", schema.TurnSpanExecution, user("q")), 100, 10)),
		entryLine(opens("t2", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t2", schema.TurnSpanExecution, user("q")), 200, 20)),
		entryLine(opens("t3", schema.TurnSpanExecution, user("q"))),
		entryLine(usageTurn(opens("t3", schema.TurnSpanExecution, user("q")), 300, 30)),
	}}
	fxHeader, lines := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(lines)...), 0o600); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	read := func() *appwire.EvenerUsage {
		t.Helper()
		var got *appwire.EvenerUsage
		if err := x.ReadObserved(func() time.Time { return fixtureClock }, func(s *Snapshot, _ time.Time) error {
			usage, _, err := s.DerivedTotals(0)
			got = usage
			return err
		}); err != nil {
			t.Fatalf("ReadObserved: %v", err)
		}
		return got
	}
	first := read()
	if walked := x.derivedWalks; walked != 1 {
		t.Fatalf("walks after the first observed read = %d, want 1", walked)
	}
	second := read()
	if walked := x.derivedWalks; walked != 1 {
		t.Fatalf("walks after a repeated observed read = %d, want 1: the memo did not survive the per-request view", walked)
	}
	if first == nil || second == nil {
		t.Fatalf("usage presence = (%v, %v), want both", first, second)
	}
	if *first != *second {
		t.Fatalf("repeated read usage = %+v, first = %+v", *second, *first)
	}
}

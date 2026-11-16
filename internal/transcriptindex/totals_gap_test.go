package transcriptindex

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// TestRecoveryAfterTotalsGapKeepsTotalsTrue pins the totals file across
// writeMeta's durability gap: the turn records are flushed before the totals
// that summarize them, so a process that dies in between leaves committed
// records whose totals file stops short of them. Recovery replays the entries,
// but stampTurn skips versions the dead extension already stamped, so no
// record write lowers the recompute floor and the floor must come from the
// gap itself.
//
// The oracle is the definition: the recovered handle must answer the same
// figures as a fresh build of the same transcript, which recomputes every
// record from scratch.
func TestRecoveryAfterTotalsGapKeepsTotalsTrue(t *testing.T) {
	fx := fixture{name: "totals gap", header: transcript.Header{SessionID: "totals_gap"}, lines: []fixtureLine{
		entryLine(opens("turn_g", schema.TurnSpanExecution, withUsage(user("first question"), 1, 1, 0, 2))),
		entryLine(inTurn("turn_g", withUsage(assistant(text("first answer")), 2, 3, 0, 5))),
		entryLine(completion("turn_g", schema.TurnCompleted, 1, 1)),
		entryLine(opens("turn_h", schema.TurnSpanExecution, withUsage(user("second question"), 7, 11, 0, 18))),
		entryLine(inTurn("turn_h", withUsage(assistant(text("second answer")), 13, 17, 0, 30))),
		entryLine(completion("turn_h", schema.TurnCompleted, 2, 1)),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	for _, line := range lines[:3] {
		appendBytes(t, path, line)
	}
	catchUp(t, x)

	// The second turn's records flush and the process dies before their totals
	// are written.
	for _, line := range lines[3:] {
		appendBytes(t, path, line)
	}
	testKillAfterTurnFlush = func() error { return errors.New("killed between the turn flush and the totals write") }
	if err := x.CatchUp(); err == nil {
		t.Fatal("the killed catch-up reported success")
	}
	testKillAfterTurnFlush = nil
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}

	// The committed state after the crash: turn records for both turns, and a
	// totals file short of them. A later commit must not inherit that gap as
	// its own answer.
	crashed := openIndex(t, path, dir)
	if crashed.turns.n < 2 {
		t.Fatalf("the crashed commit holds %d turn records, want both", crashed.turns.n)
	}
	if crashed.totals.n > crashed.turns.n {
		t.Fatalf("totals covers %d turns and the table holds %d", crashed.totals.n, crashed.turns.n)
	}
	gapCovered := crashed.totals.n
	if err := crashed.Close(); err != nil {
		t.Fatal(err)
	}

	recovered := openIndex(t, path, dir)
	if err := recovered.CatchUp(); err != nil {
		t.Fatalf("catch up after the totals gap: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	if recovered.totals.n != recovered.turns.n {
		t.Fatalf("recovery committed totals covering %d turns against a table of %d, from a crash that left %d covered",
			recovered.totals.n, recovered.turns.n, gapCovered)
	}
	got, gotFailures, err := recovered.DerivedTotals(0)
	if err != nil {
		t.Fatalf("DerivedTotals: %v", err)
	}
	fresh := openIndex(t, path, t.TempDir())
	t.Cleanup(func() { _ = fresh.Close() })
	want, wantFailures, err := fresh.DerivedTotals(0)
	if err != nil {
		t.Fatalf("fresh DerivedTotals: %v", err)
	}
	if got == nil || want == nil {
		t.Fatalf("derived totals missing: recovered %+v, fresh %+v", got, want)
	}
	if *got != *want || gotFailures != wantFailures {
		t.Fatalf("recovered totals (%+v, %d) != fresh scan (%+v, %d)", got, gotFailures, want, wantFailures)
	}
}

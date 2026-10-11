package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// TestWindowPaginationGroupsANonAdjacentlyRevisitedTurn is the realistic
// recovery shape item 1 of the phase 3 review asked for: a turn opens, gets
// an item and CLOSES (a completion entry — a client-mutation turn a user
// regenerates completes before it runs again; see
// agent/session_execution.go's noteRecordedExecutionLocked, "only a
// client-mutation name can run again"), another turn runs to completion in
// between, then recovery reclaims the first turn UNDER ITS OLD, ALREADY-
// CLOSED id (TurnReopen, matching beginExecution and
// transcript.Writer.BeginExecution(turnID, reopen=true)), it gets another
// item, and completes again.
//
// Closing the turn before the intervening turn matters: only then does the
// builder's b.open map no longer hold the id when the reopen entry arrives,
// so placing it must fall back to findTurn's backward scan of the turns
// table (build.go) rather than the open map's fast path — the actual
// mechanism a non-adjacent revisit exercises. A crash-without-completion
// variant (b.open never loses the id) exercises neither: it was tried
// first here and, verified by deliberately disabling findTurn's reuse
// (mutation testing), passed regardless, so it proved nothing about this
// path. This version was verified the same way to fail under that mutation
// (and to pass once findTurn is restored) before being kept.
//
// This is deliberately independent of the trailing-communicate flush
// (flushProjection/pendingFlush): the transcript ends on a completed turn, so
// it isolates window pagination's own turn-identity/adjacency logic
// (HasEarlierItems/HasLaterItems, span's per-item Turn-slot comparison) from
// the flush heuristic bug TestAppendEntryByEntryMatchesTheReference exposed
// (see flushProjection's fix in reference_test.go and this package's
// TestPagedReadFlushesTrailingZeroItemGroupCommunicate).
func TestWindowPaginationGroupsANonAdjacentlyRevisitedTurn(t *testing.T) {
	fx := fixture{header: transcript.Header{SessionID: "th_reopen_non_adjacent"}, lines: []fixtureLine{
		// turn_ra opens, gets one item, and closes (failed).
		entryLine(opens("turn_ra", schema.TurnSpanExecution, user("first attempt"))),
		entryLine(inTurn("turn_ra", assistant(text("first try")))),
		entryLine(completion("turn_ra", schema.TurnFailed, 1, 10)),
		// turn_rb runs to completion in between, physically adjacent to
		// turn_ra's items on both sides.
		entryLine(opens("turn_rb", schema.TurnSpanExecution, user("unrelated turn"))),
		entryLine(inTurn("turn_rb", assistant(text("rb's own answer")))),
		entryLine(completion("turn_rb", schema.TurnCompleted, 2, 10)),
		// The user regenerates: recovery reclaims turn_ra under its old,
		// already-closed id and finishes it.
		entryLine(reopen("turn_ra")),
		entryLine(inTurn("turn_ra", assistant(text("second try")))),
		entryLine(completion("turn_ra", schema.TurnCompleted, 3, 20)),
	}}
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())

	want := referenceCandidates(t, path)
	assertAllWindows(t, x, path)

	// Pin down the exact adjacency this scenario is testing: turn_ra's two
	// items are NOT each other's physical neighbours (turn_rb's two items —
	// its own opening userMessage and its answer — sit between them in the
	// file), so neither reports the other as adjacent, even though both
	// belong to the same turn id.
	var raFirst, rbFirst, rbLast, raSecond *appitempaging.TranscriptItemCandidate
	for i := range want {
		switch {
		case want[i].TurnID == "turn_ra" && want[i].Item.Text == "first try":
			raFirst = &want[i]
		case want[i].TurnID == "turn_rb":
			if rbFirst == nil {
				rbFirst = &want[i]
			}
			rbLast = &want[i]
		case want[i].TurnID == "turn_ra" && want[i].Item.Text == "second try":
			raSecond = &want[i]
		}
	}
	if raFirst == nil || rbFirst == nil || rbLast == nil || raSecond == nil {
		t.Fatalf("fixture did not produce the expected items; candidates: %s", dump(want))
	}
	if raFirst.HasLaterItems {
		t.Errorf("turn_ra's first-attempt item reports HasLaterItems=true, want false (turn_rb's item is next, not turn_ra's)")
	}
	if rbFirst.HasEarlierItems {
		t.Errorf("turn_rb's first item reports HasEarlierItems=true, want false (turn_ra's first-attempt item precedes it, a different turn)")
	}
	if rbLast.HasLaterItems {
		t.Errorf("turn_rb's last item reports HasLaterItems=true, want false (turn_ra's second-attempt item follows it, a different turn)")
	}
	if raSecond.HasEarlierItems {
		t.Errorf("turn_ra's second-attempt item reports HasEarlierItems=true, want false (turn_rb's item is the physical predecessor, not turn_ra's)")
	}

	// The index must agree with the (now corrected) reference on every one
	// of these, at every window boundary — assertAllWindows above already
	// checked that; this is the same check with the bug's exact shape named
	// for the report.
	window, err := x.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "latest", window, want, len(want), appwire.TranscriptItemPageLimit)
}

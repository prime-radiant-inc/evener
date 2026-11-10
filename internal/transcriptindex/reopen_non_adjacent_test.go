package transcriptindex

import (
	"fmt"
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

	// Pin down the exact shape this scenario is testing: turn_ra's two items
	// are NOT each other's physical neighbours (turn_rb's two items — its own
	// opening userMessage and its answer — sit between them in the file), yet
	// both belong to the same logical turn id, so each reports that the other
	// continues it.
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
	if !raFirst.HasLaterItems {
		t.Errorf("turn_ra's first-attempt item reports HasLaterItems=false, want true (the reopened turn has a later item)")
	}
	if rbFirst.HasEarlierItems {
		t.Errorf("turn_rb's first item reports HasEarlierItems=true, want false (turn_ra's first-attempt item precedes it, a different turn)")
	}
	if rbLast.HasLaterItems {
		t.Errorf("turn_rb's last item reports HasLaterItems=true, want false (turn_ra's second-attempt item follows it, a different turn)")
	}
	if !raSecond.HasEarlierItems {
		t.Errorf("turn_ra's second-attempt item reports HasEarlierItems=false, want true (the reopened turn has an earlier item)")
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

// TestLatestWindowMarksAReopenedTurnsLaterFragmentAsIncomplete covers the page
// split where the reopened turn's later fragment lands on the newest page and
// its first fragment is one page further back: A1, B1..B39, A2. The later
// fragment must report that its logical turn has earlier items even though its
// physical predecessor belongs to turn B.
func TestLatestWindowMarksAReopenedTurnsLaterFragmentAsIncomplete(t *testing.T) {
	lines := []fixtureLine{
		entryLine(opens("turn_a", schema.TurnSpanExecution, user("first attempt"))),
		entryLine(inTurn("turn_a", assistant(text("a-first")))),
		entryLine(completion("turn_a", schema.TurnCompleted, 1, 10)),
		entryLine(opens("turn_b", schema.TurnSpanExecution, user("unrelated turn"))),
	}
	for i := range 39 {
		lines = append(lines, entryLine(inTurn("turn_b", assistant(text(fmt.Sprintf("b-%02d", i))))))
	}
	lines = append(lines,
		entryLine(completion("turn_b", schema.TurnCompleted, 2, 10)),
		entryLine(reopen("turn_a")),
		entryLine(inTurn("turn_a", assistant(text("a-second")))),
		entryLine(completion("turn_a", schema.TurnCompleted, 3, 20)),
	)
	path := writeFixture(t, fixture{header: transcript.Header{SessionID: "th_reopen_paged"}, lines: lines})
	x := openIndex(t, path, t.TempDir())

	want := referenceCandidates(t, path)
	window, err := x.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	var laterFragment bool
	for i := range window.Candidates {
		candidate := window.Candidates[i]
		if candidate.Item.Text == "a-second" {
			laterFragment = true
			if !candidate.HasEarlierItems {
				t.Fatal("the reopened turn's later fragment reports no earlier items")
			}
		}
	}
	if !laterFragment {
		t.Fatal("latest window did not carry the reopened turn's later fragment")
	}
	for i := range want {
		switch want[i].Item.Text {
		case "first attempt":
			if want[i].HasEarlierItems {
				t.Fatal("the reopened turn's first item reports earlier items")
			}
		case "unrelated turn":
			if want[i].HasEarlierItems {
				t.Fatal("turn B's first item reports earlier items")
			}
		case "a-first":
			if !want[i].HasLaterItems {
				t.Fatal("the reopened turn's first fragment reports no later items")
			}
		}
	}
	assertWindow(t, "latest", window, want, len(want), appwire.TranscriptItemPageLimit)
	assertAllWindows(t, x, path)
}

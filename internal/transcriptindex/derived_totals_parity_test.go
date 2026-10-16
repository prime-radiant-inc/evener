package transcriptindex

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/apptranscript"
)

// TestDerivedTotalsMatchScan proves the index's derived totals equal the
// corrected DerivedTotalsFromFile for every real divergence cut — a cut always
// names a USER_INPUT entry, which opens a turn, so it lands on a turn boundary —
// on a transcript carrying a fold copy (which neither counts) and a failing tool
// call (which both count once).
func TestDerivedTotalsMatchScan(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	prefix := withUsage(assistant(text("prefix answer")), 5, 7, 0, 12)
	own := withUsage(assistant(text("own answer")), 2, 2, 0, 4)
	callPart := call("c1", "shell", `{"argv":["x"]}`)
	failPart := result("c1", "shell", "boom")
	failPart.ToolResult.IsError = true
	fx := fixture{name: "two-turns-with-fold", header: header, lines: []fixtureLine{
		entryLine(opens("t1", schema.TurnSpanExecution, user("prefix question"))), // entry 1 opens turn t1
		entryLine(inTurn("t1", prefix)),                                           // entry 2
		entryLine(opens("t2", schema.TurnSpanExecution, user("own question"))),    // entry 3 opens turn t2 — a real divergence cut
		entryLine(inTurn("t2", own)),                                              // entry 4
		entryLine(inTurn("t2", assistant(text("run"), callPart))),                 // entry 5
		entryLine(inTurn("t2", results(failPart))),                                // entry 6
		entryLine(foldCopy(inTurn("t2", own), 3)),                                 // entry 7, a fold copy of t2's answer
		entryLine(completion("t2", schema.TurnCompleted, 8, 10)),                  // entry 8
	}}

	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	// 0/1 count the whole file; 3 is turn t2's first entry, the only cut a real
	// fork child has here.
	for _, d := range []int{0, 1, 3} {
		gotUsage, gotFailures, err := x.DerivedTotals(d)
		if err != nil {
			t.Fatalf("DerivedTotals(%d): %v", d, err)
		}
		wantUsage, wantFailures, err := apptranscript.NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, d)
		if err != nil {
			t.Fatalf("scan(%d): %v", d, err)
		}
		if !reflect.DeepEqual(gotUsage, wantUsage) || gotFailures != wantFailures {
			t.Fatalf("d=%d: derived (%+v, %d) != scan (%+v, %d)", d, gotUsage, gotFailures, wantUsage, wantFailures)
		}
	}
}

package transcriptindex

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// TestDerivedTotalsMatchScan proves the index's derived totals equal the
// corrected DerivedTotalsFromFile for every real divergence cut — a cut always
// names a USER_INPUT entry, which opens a turn, so it lands on a turn boundary.
// The fixture covers the cases the derived totals must agree on: a fold copy
// (neither counts), an isError failure and a shell nonzero exit with a clean
// result (both counted once), a communicate result (excluded), and a span with
// no usage (nil).
func TestDerivedTotalsMatchScan(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	shellExit := results(llm.ContentPart{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "s1", Name: "shell", Content: "clean output",
			ToolState: json.RawMessage(`{"exit_code":1}`),
		},
	})
	communicate := results(llm.ContentPart{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "m1", Name: "communicate", Content: "delivered", IsError: true,
		},
	})
	t1Answer := inTurn("t1", withUsage(assistant(text("prefix answer")), 5, 7, 0, 12))
	t2Answer := inTurn("t2", withUsage(assistant(text("own answer")), 2, 2, 0, 4))
	fx := fixture{name: "totals-parity", header: header, lines: []fixtureLine{
		entryLine(opens("t1", schema.TurnSpanExecution, user("prefix question"))), // 1 opens t1
		entryLine(t1Answer), // 2
		entryLine(opens("t2", schema.TurnSpanExecution, user("own question"))), // 3 opens t2, a real cut
		entryLine(t2Answer), // 4
		entryLine(inTurn("t2", assistant(text("run shell"), call("s1", "shell", `{"argv":["x"]}`)))), // 5
		entryLine(inTurn("t2", shellExit)), // 6 shell, exit 1: counted
		entryLine(inTurn("t2", assistant(text("say"), call("m1", "communicate", `{"message":"hi"}`)))), // 7
		entryLine(inTurn("t2", communicate)),                                        // 8 communicate: excluded
		entryLine(foldCopy(t2Answer, 3)),                                            // 9 fold copy: not counted
		entryLine(completion("t2", schema.TurnCompleted, 10, 10)),                   // 10
		entryLine(opens("t3", schema.TurnSpanExecution, user("no usage question"))), // 11 opens t3, a real cut
		entryLine(inTurn("t3", assistant(text("answer without usage")))),            // 12 carries no usage
		entryLine(completion("t3", schema.TurnCompleted, 13, 10)),                   // 13
	}}

	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	// 0/1 count the whole file; 3 is t2's first entry and 11 is t3's, the cuts a
	// real fork child has here.
	for _, d := range []int{0, 1, 3, 11} {
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

// A fold copy is normally identity-format, but the parity guarantee must hold
// whatever its format: the index and the corrected scan both skip it. This
// pins a legacy fold copy, which the builder's format dispatch would otherwise
// count through stampTurn.
func TestDerivedTotalsExcludeLegacyFoldCopies(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	answer := withUsage(assistant(text("answer")), 2, 2, 0, 4)
	copyOfAnswer := answer
	ordinal := uint64(1)
	copyOfAnswer.OriginalOrdinal = &ordinal
	fx := fixture{name: "legacy-fold", header: header, lines: []fixtureLine{
		entryLine(user("q")),    // legacy, opens turn t1
		entryLine(answer),       // legacy answer
		entryLine(copyOfAnswer), // legacy fold copy of the answer
	}}

	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	gotUsage, gotFailures, err := x.DerivedTotals(0)
	if err != nil {
		t.Fatalf("DerivedTotals: %v", err)
	}
	wantUsage, wantFailures, err := apptranscript.NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(gotUsage, wantUsage) || gotFailures != wantFailures {
		t.Fatalf("derived (%+v, %d) != scan (%+v, %d) for a legacy fold copy", gotUsage, gotFailures, wantUsage, wantFailures)
	}
}

// A divergence cut can fall inside one logical turn: a fork child's transcript
// opens with its parent's entries and the child's own entries continue the same
// legacy group (see apptranscript's TestDerivedTotalsFromFileSkipsInheritedForkPrefix).
// A per-turn record cannot express that suffix, so DerivedTotals must refuse it
// rather than drop the turn's contribution or count the whole thing.
func TestDerivedTotalsRefusesAMidTurnCut(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	answer := withUsage(assistant(text("answer")), 5, 5, 0, 10)
	callPart := call("c1", "shell", `{}`)
	failPart := result("c1", "shell", "boom")
	failPart.ToolResult.IsError = true
	fx := fixture{name: "mid-turn-cut", header: header, lines: []fixtureLine{
		entryLine(user("q")),
		entryLine(answer),
		entryLine(assistant(text("run"), callPart)),
		entryLine(results(failPart)),
	}}

	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	// Cut 4 names an entry inside the single turn: refuse, do not mis-state.
	if _, _, err := x.DerivedTotals(4); !errors.Is(err, ErrDerivedTotalsUnavailable) {
		t.Fatalf("DerivedTotals(4) = %v, want ErrDerivedTotalsUnavailable for a mid-turn cut", err)
	}
	// Cut 0 (whole file) still answers, and agrees with the scan.
	gotUsage, gotFailures, err := x.DerivedTotals(0)
	if err != nil {
		t.Fatalf("DerivedTotals(0): %v", err)
	}
	wantUsage, wantFailures, err := apptranscript.NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(gotUsage, wantUsage) || gotFailures != wantFailures {
		t.Fatalf("derived (%+v, %d) != scan (%+v, %d)", gotUsage, gotFailures, wantUsage, wantFailures)
	}
}

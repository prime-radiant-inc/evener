package transcriptindex

import (
	"encoding/json"
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

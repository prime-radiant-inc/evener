package transcriptindex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// TestDerivedTotalsNeverDisagreeAfterAnExtension pins the safety property the
// extension path must keep: after the covered sidecar is extended, DerivedTotals
// either reports the totals unavailable or agrees with the scan — it never
// presents a figure the scan would not.
//
// The FailuresInexact guard exists for an extension that cannot resolve a
// nameless result for a call in the covered prefix. Every shape probed here
// (legacy and identity) instead rebuilds — rebuilding is exact — so the guard is
// defense in depth and this test pins the invariant rather than one mechanism.
func TestDerivedTotalsNeverDisagreeAfterAnExtension(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	identity := func(turn schema.Turn) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		return turn
	}
	nameless := identity(inTurn("t1", results(llm.ContentPart{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "c1", Content: "clean output",
			ToolState: json.RawMessage(`{"exit_code":1}`),
		},
	})))
	fx := fixture{name: "extension-safety", header: header, lines: []fixtureLine{
		entryLine(identity(opens("t1", schema.TurnSpanExecution, user("q")))),                // covered prefix
		entryLine(identity(inTurn("t1", assistant(text("run"), call("c1", "shell", `{}`))))), // covered prefix: announces c1
		entryLine(nameless), // appended: a nameless result for the prefix's call
	}}
	fxHeader, lines := fx.encode(t)

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(append([]byte(nil), fxHeader...), joinLines(lines[:2])...), 0o600); err != nil {
		t.Fatal(err)
	}
	// Build the prefix under one handle, then close it so the extension runs
	// against a restored builder with no call names of its own — the condition
	// an inexact count would arise from.
	prefix := openIndex(t, path, t.TempDir())
	if err := prefix.Close(); err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, joinLines(lines[2:]))
	extended := openIndex(t, path, t.TempDir())
	if err := extended.Close(); err != nil {
		t.Fatal(err)
	}

	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	gotUsage, gotFailures, derivedErr := x.DerivedTotals(0)
	if errors.Is(derivedErr, ErrDerivedTotalsUnavailable) {
		return // refused: the caller falls back to the scan, never a wrong figure
	}
	if derivedErr != nil {
		t.Fatalf("DerivedTotals: %v", derivedErr)
	}
	wantUsage, wantFailures, err := apptranscript.NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(gotUsage, wantUsage) || gotFailures != wantFailures {
		t.Fatalf("after extension: derived (%+v, %d) disagrees with scan (%+v, %d)",
			gotUsage, gotFailures, wantUsage, wantFailures)
	}
}

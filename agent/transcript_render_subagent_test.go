package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/transcript"
)

// renderToolCardForResult builds a one-round transcript (assistant call + paired
// result), renders it, and returns the markdown. It is the shared fixture for the
// E7 result-body rendering tests: a single tool call named toolName whose result
// body is the given content string.
func renderToolCardForResult(toolName, callID string, content any) string {
	entries := []transcript.Entry{
		toolCallEntry(call(callID, toolName, `{}`)),
		toolResultEntry(result(callID, toolName, content, false)),
	}
	return renderMarkdown(transcript.Header{}, entries, 0, renderOpts{})
}

// TestRenderMarkdown_SubagentWaitResult is the headline E7 case: a wait result
// whose subagentResult body carries a multi-line output renders legibly — a status
// line surfacing success/status and the transcript_ref PROMINENTLY BEFORE the
// output, then the output with REAL newlines (the backslash-escaped \n form gone).
func TestRenderMarkdown_SubagentWaitResult(t *testing.T) {
	childRef := "local:01CHILDSUBAGENT00000000"
	output := "First line of the report.\nSECOND_LINE_NEEDLE summarizing findings.\nThird line with a conclusion."
	body, err := json.Marshal(subagentResult{
		Status:        SubagentCompleted,
		Output:        output,
		Success:       true,
		TurnsUsed:     7,
		TranscriptRef: childRef,
	})
	if err != nil {
		t.Fatalf("marshal subagentResult: %v", err)
	}

	out := renderToolCardForResult("wait", "call_wait", string(body))

	// The status line surfaces success/status/turns_used.
	if !strings.Contains(out, "success=true") {
		t.Errorf("expected success=true on the status line, got:\n%s", out)
	}
	if !strings.Contains(out, "status=completed") {
		t.Errorf("expected status=completed on the status line, got:\n%s", out)
	}
	if !strings.Contains(out, "turns_used=7") {
		t.Errorf("expected turns_used=7 on the status line, got:\n%s", out)
	}

	// The transcript_ref is present and PROMINENT (before the output body).
	if !strings.Contains(out, childRef) {
		t.Errorf("expected transcript_ref %q in output, got:\n%s", childRef, out)
	}
	refIdx := strings.Index(out, childRef)
	outputIdx := strings.Index(out, "SECOND_LINE_NEEDLE")
	if refIdx < 0 || outputIdx < 0 {
		t.Fatalf("missing content: refIdx=%d outputIdx=%d\n%s", refIdx, outputIdx, out)
	}
	if refIdx > outputIdx {
		t.Errorf("transcript_ref (%d) must appear BEFORE the output body (%d):\n%s", refIdx, outputIdx, out)
	}

	// The output renders with REAL newlines: the known second line appears on its
	// own line (preceded by a newline, not glued to the first line).
	if !strings.Contains(out, "\nSECOND_LINE_NEEDLE summarizing findings.") &&
		!strings.Contains(out, "  SECOND_LINE_NEEDLE summarizing findings.") {
		t.Errorf("output second line must appear on its own line with real newlines, got:\n%s", out)
	}

	// The backslash-escaped form must be GONE: no literal \n escape, no JSON-quoted
	// output field dumped verbatim.
	if strings.Contains(out, `\n`) {
		t.Errorf("escaped \\n must not appear (output must be de-escaped), got:\n%s", out)
	}
	if strings.Contains(out, `"output":`) {
		t.Errorf("raw JSON subagentResult must not be dumped verbatim, got:\n%s", out)
	}
}

// TestRenderMarkdown_SubagentFailedResult verifies the gated render also surfaces a
// failed subagent honestly (success=false status=failed) with its ref.
func TestRenderMarkdown_SubagentFailedResult(t *testing.T) {
	childRef := "local:01FAILEDCHILD0000000000"
	body, err := json.Marshal(subagentResult{
		Status:        SubagentFailed,
		Output:        "context deadline exceeded",
		Success:       false,
		TurnsUsed:     2,
		TranscriptRef: childRef,
	})
	if err != nil {
		t.Fatalf("marshal subagentResult: %v", err)
	}

	out := renderToolCardForResult("wait", "call_wait", string(body))

	if !strings.Contains(out, "success=false") {
		t.Errorf("expected success=false, got:\n%s", out)
	}
	if !strings.Contains(out, "status=failed") {
		t.Errorf("expected status=failed, got:\n%s", out)
	}
	if !strings.Contains(out, childRef) {
		t.Errorf("expected transcript_ref %q, got:\n%s", childRef, out)
	}
	if !strings.Contains(out, "context deadline exceeded") {
		t.Errorf("expected the error output to be shown, got:\n%s", out)
	}
}

// TestRenderMarkdown_SubagentResultHugeOutputBounded verifies a subagent result
// with a very long output is still bounded (head+tail truncated with the elision
// marker), and the ref stays visible (before the truncated output).
func TestRenderMarkdown_SubagentResultHugeOutputBounded(t *testing.T) {
	childRef := "local:01HUGECHILD000000000000"
	// Far more than resultBodyWholeMax non-empty lines so truncation kicks in.
	output := makeNumberedLines(resultBodyWholeMax + 100)
	body, err := json.Marshal(subagentResult{
		Status:        SubagentCompleted,
		Output:        output,
		Success:       true,
		TurnsUsed:     12,
		TranscriptRef: childRef,
	})
	if err != nil {
		t.Fatalf("marshal subagentResult: %v", err)
	}

	out := renderToolCardForResult("wait", "call_wait", string(body))

	// Bounded: an elision marker is present.
	if !strings.Contains(out, "lines elided") {
		t.Errorf("huge subagent output must be truncated with an elision marker, got:\n%s", out)
	}
	// The ref is still visible even though the output was truncated.
	if !strings.Contains(out, childRef) {
		t.Errorf("transcript_ref must remain visible after truncation, got:\n%s", out)
	}
	// The ref comes before the output (and thus before the elision marker).
	refIdx := strings.Index(out, childRef)
	elideIdx := strings.Index(out, "lines elided")
	if refIdx < 0 || elideIdx < 0 || refIdx > elideIdx {
		t.Errorf("ref (%d) must precede the truncated output marker (%d):\n%s", refIdx, elideIdx, out)
	}
	// First line present (head), a deep-middle line absent (elided).
	if !strings.Contains(out, "line001") {
		t.Errorf("head of output must be present, got:\n%s", out)
	}
	if strings.Contains(out, "line050") {
		t.Errorf("a middle line should be elided, got:\n%s", out)
	}
}

// TestRenderMarkdown_SubagentResultWithExtraKeysFallsBack verifies that when a
// subagent-named result body carries keys beyond the known subagentResult fields
// (e.g. data/artifacts), the render falls back to the general JSON pretty-print
// rather than hiding the extra evidence. Gated on tool name, but evidence fidelity
// wins over the status-line treatment.
func TestRenderMarkdown_SubagentResultWithExtraKeysFallsBack(t *testing.T) {
	// A subagent-shaped body that ALSO carries an "artifacts" key not in the struct.
	bodyStr := `{"status":"completed","output":"did stuff","success":true,"turns_used":3,"transcript_ref":"local:01X","artifacts":["a.txt","b.txt"]}`

	out := renderToolCardForResult("wait", "call_wait", bodyStr)

	// The extra evidence must remain visible (not hidden by struct decoding).
	if !strings.Contains(out, "artifacts") {
		t.Errorf("extra 'artifacts' key must remain visible (fallback to JSON pretty-print), got:\n%s", out)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") {
		t.Errorf("artifact values must remain visible, got:\n%s", out)
	}
}

// TestRenderMarkdown_GenericJSONResultPrettyPrinted verifies a generic JSON result
// from a NON-subagent tool is pretty-printed (indented) but NOT given the subagent
// status-line treatment — the no-false-positive case.
func TestRenderMarkdown_GenericJSONResultPrettyPrinted(t *testing.T) {
	// A read_file result whose content is compact JSON.
	out := renderToolCardForResult("read_file", "call_read", `{"a":1,"b":[2,3]}`)

	// Pretty-printed: indentation introduces real newlines so "a" and "b" are on
	// separate lines, and the array elements expand.
	if !strings.Contains(out, `"a": 1`) {
		t.Errorf("expected pretty-printed JSON with spaced keys, got:\n%s", out)
	}
	if !strings.Contains(out, `"b":`) {
		t.Errorf("expected key b in pretty-printed JSON, got:\n%s", out)
	}
	// The compact single-line form should NOT survive verbatim (it was reformatted).
	if strings.Contains(out, `{"a":1,"b":[2,3]}`) {
		t.Errorf("compact JSON must be reformatted, not dumped verbatim, got:\n%s", out)
	}

	// NO false positive: the subagent status-line treatment must NOT appear.
	if strings.Contains(out, "success=") || strings.Contains(out, "turns_used=") {
		t.Errorf("non-subagent JSON must NOT get the subagent status-line treatment, got:\n%s", out)
	}
}

// TestRenderMarkdown_NonJSONResultUnchanged verifies a plain-text (non-JSON)
// result renders exactly as before: no pretty-print, no status line.
func TestRenderMarkdown_NonJSONResultUnchanged(t *testing.T) {
	plain := "ok  primeradiant.com/serf/agent  1.20s\nPASS"
	out := renderToolCardForResult("shell", "call_shell", plain)

	if !strings.Contains(out, "ok  primeradiant.com/serf/agent  1.20s") {
		t.Errorf("plain-text result must render unchanged, got:\n%s", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("plain-text second line must render, got:\n%s", out)
	}
	// No subagent treatment.
	if strings.Contains(out, "success=") {
		t.Errorf("plain-text result must not get subagent treatment, got:\n%s", out)
	}
}

// TestRenderMarkdown_SubagentResultUnparseableFallsBack verifies that a
// subagent-named result whose body is NOT a subagentResult (here: plain text that
// is not even JSON) falls back to the normal/plain rendering without error.
func TestRenderMarkdown_SubagentResultUnparseableFallsBack(t *testing.T) {
	// A wait result that errored at the tool layer and returned a plain string.
	plain := "wait timeout"
	out := renderToolCardForResult("wait", "call_wait", plain)

	if !strings.Contains(out, "wait timeout") {
		t.Errorf("non-JSON subagent body must render as plain text, got:\n%s", out)
	}
	if strings.Contains(out, "success=") {
		t.Errorf("non-decodable subagent body must not fabricate a status line, got:\n%s", out)
	}
}

// TestDecodeSubagentResult exercises the unified decode path directly: a full
// subagentResult decodes (with output/turns_used available to E7), while empty and
// non-matching bodies report false so callers fall back.
func TestDecodeSubagentResult(t *testing.T) {
	t.Run("full struct decodes with all fields", func(t *testing.T) {
		body := `{"status":"completed","output":"line one\nline two","success":true,"turns_used":9,"transcript_ref":"local:01Z"}`
		r, ok := decodeSubagentResult(body)
		if !ok {
			t.Fatalf("expected decode to succeed for %q", body)
		}
		if r.Status != SubagentCompleted {
			t.Errorf("Status: got %q, want completed", r.Status)
		}
		if !r.Success {
			t.Errorf("Success: got false, want true")
		}
		if r.TurnsUsed != 9 {
			t.Errorf("TurnsUsed: got %d, want 9", r.TurnsUsed)
		}
		if r.TranscriptRef != "local:01Z" {
			t.Errorf("TranscriptRef: got %q, want local:01Z", r.TranscriptRef)
		}
		// Output is de-escaped into a real newline by the JSON decoder.
		if r.Output != "line one\nline two" {
			t.Errorf("Output: got %q, want de-escaped two-line string", r.Output)
		}
	})

	t.Run("empty body reports false", func(t *testing.T) {
		if _, ok := decodeSubagentResult("   "); ok {
			t.Errorf("empty body must report false")
		}
	})

	t.Run("non-JSON body reports false", func(t *testing.T) {
		if _, ok := decodeSubagentResult("not json at all"); ok {
			t.Errorf("non-JSON body must report false")
		}
	})

	t.Run("extractSubagentResult subset agrees with decode", func(t *testing.T) {
		body := `{"status":"failed","output":"boom","success":false,"turns_used":1,"transcript_ref":"local:01Q"}`
		info, ok := extractSubagentResult(body)
		if !ok {
			t.Fatalf("extractSubagentResult should succeed")
		}
		full, _ := decodeSubagentResult(body)
		if info.success != full.Success || info.status != string(full.Status) || info.transcriptRef != full.TranscriptRef {
			t.Errorf("subset %+v disagrees with full decode %+v", info, full)
		}
	})
}

// TestRenderMarkdown_GenericJSONArrayResult verifies a top-level JSON array body
// (not just objects) is also pretty-printed.
func TestRenderMarkdown_GenericJSONArrayResult(t *testing.T) {
	out := renderToolCardForResult("read_file", "call_read", `[1,2,3]`)
	// Pretty-printed arrays put each element on its own line.
	if !strings.Contains(out, "1,\n") {
		t.Errorf("expected pretty-printed array with each element on its own line, got:\n%s", out)
	}
}

// TestPrettyJSON_PreservesBigInt verifies that a non-subagent JSON result
// containing an integer larger than 2^53 is rendered with the EXACT original
// digits (float64 decoding would silently mangle it to the nearest
// representable value).
func TestPrettyJSON_PreservesBigInt(t *testing.T) {
	// 2^53 + 1 — the smallest integer that cannot be represented exactly as
	// float64 (which would round it down to 9007199254740992).
	out := renderToolCardForResult("read_file", "call_read", `{"id":9007199254740993}`)
	if !strings.Contains(out, "9007199254740993") {
		t.Errorf("expected exact big int literal 9007199254740993 in output, got:\n%s", out)
	}
}

// TestPrettyJSON_NoHTMLEscaping verifies that a JSON result containing URL
// metacharacters and angle brackets is rendered with literal & and < > rather
// than the \u-escaped forms produced by the default json.MarshalIndent.
func TestPrettyJSON_NoHTMLEscaping(t *testing.T) {
	out := renderToolCardForResult("read_file", "call_read", `{"url":"https://x/?a=1&b=2","tag":"<x>"}`)
	if !strings.Contains(out, "&") {
		t.Errorf("expected literal & in output (not \\u0026), got:\n%s", out)
	}
	if !strings.Contains(out, "<") {
		t.Errorf("expected literal < in output (not \\u003c), got:\n%s", out)
	}
	if !strings.Contains(out, ">") {
		t.Errorf("expected literal > in output (not \\u003e), got:\n%s", out)
	}
}

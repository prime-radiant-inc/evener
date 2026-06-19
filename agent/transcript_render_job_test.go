package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// renderToolCardForResult builds a one-round transcript (assistant call + paired
// result), renders it, and returns the markdown. It is the shared fixture for the
// result-body rendering tests: a single tool call named toolName whose result
// body is the given content string.
func renderToolCardForResult(toolName, callID string, content any) string {
	entries := []transcript.Entry{
		toolCallEntry(call(callID, toolName, `{}`)),
		toolResultEntry(result(callID, toolName, content, false)),
	}
	return renderMarkdown(transcript.Header{}, entries, 0, renderOpts{})
}

func renderToolCardForStateResult(toolName, callID string, content any, toolState []byte) string {
	res := result(callID, toolName, content, false)
	res.ToolState = toolState
	entries := []transcript.Entry{
		toolCallEntry(call(callID, toolName, `{}`)),
		toolResultEntry(res),
	}
	return renderMarkdown(transcript.Header{}, entries, 0, renderOpts{})
}

// TestRenderMarkdown_JobDelegateResult is the headline lifecycle case: a
// delegate result whose job body carries a multi-line output renders legibly: a
// status line surfacing status and the transcript_ref before the output, then
// the output with real newlines.
func TestRenderMarkdown_JobDelegateResult(t *testing.T) {
	childRef := "local:01CHILDJOB000000000000"
	output := "First line of the report.\nSECOND_LINE_NEEDLE summarizing findings.\nThird line with a conclusion."
	body, err := json.Marshal(map[string]any{
		"job_id":                "job_delegate_1",
		"type":                  "delegate",
		"status":                "completed",
		"running_in_background": false,
		"timed_out":             false,
		"transcript_ref":        childRef,
		"output":                output,
		"truncated":             false,
	})
	if err != nil {
		t.Fatalf("marshal job result: %v", err)
	}

	out := renderToolCardForResult("delegate", "call_delegate", string(body))

	// The status line surfaces status and job identity without a legacy success field.
	if !strings.Contains(out, "status=completed") {
		t.Errorf("expected status=completed on the status line, got:\n%s", out)
	}
	if !strings.Contains(out, "job_id=job_delegate_1") {
		t.Errorf("expected job_id on the status line, got:\n%s", out)
	}
	if strings.Contains(out, "success=") {
		t.Errorf("job result must not render legacy success field, got:\n%s", out)
	}

	// The transcript_ref is present before the output body.
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

	// The output renders with real newlines: the known second line appears on its
	// own line (preceded by a newline, not glued to the first line).
	if !strings.Contains(out, "\nSECOND_LINE_NEEDLE summarizing findings.") &&
		!strings.Contains(out, "  SECOND_LINE_NEEDLE summarizing findings.") {
		t.Errorf("output second line must appear on its own line with real newlines, got:\n%s", out)
	}

	// The backslash-escaped form must be gone: no literal \n escape, no JSON-quoted
	// output field dumped verbatim.
	if strings.Contains(out, `\n`) {
		t.Errorf("escaped \\n must not appear (output must be de-escaped), got:\n%s", out)
	}
	if strings.Contains(out, `"output":`) {
		t.Errorf("raw JSON job result must not be dumped verbatim, got:\n%s", out)
	}
}

// TestRenderMarkdown_JobSendMessageResult verifies the gated render also applies
// to job_send_message results that resume a delegate and return a transcript ref.
func TestRenderMarkdown_JobSendMessageResult(t *testing.T) {
	childRef := "local:01RESUMEDJOB0000000000"
	body, err := json.Marshal(map[string]any{
		"target":                "job_old",
		"job_id":                "job_new",
		"type":                  "delegate",
		"status":                "failed",
		"reason":                "child_failed",
		"running_in_background": false,
		"timed_out":             false,
		"action":                "resumed",
		"resumed_from_job_id":   "job_old",
		"transcript_ref":        childRef,
		"output":                "context deadline exceeded",
		"truncated":             false,
	})
	if err != nil {
		t.Fatalf("marshal job_send_message result: %v", err)
	}

	out := renderToolCardForResult("job_send_message", "call_send", string(body))

	if !strings.Contains(out, "status=failed") {
		t.Errorf("expected status=failed, got:\n%s", out)
	}
	if !strings.Contains(out, "job_id=job_new") {
		t.Errorf("expected resumed job id, got:\n%s", out)
	}
	if !strings.Contains(out, "reason=child_failed") {
		t.Errorf("expected reason, got:\n%s", out)
	}
	if strings.Contains(out, "success=") {
		t.Errorf("job result must not render legacy success field, got:\n%s", out)
	}
	if !strings.Contains(out, childRef) {
		t.Errorf("expected transcript_ref %q, got:\n%s", childRef, out)
	}
	if !strings.Contains(out, "context deadline exceeded") {
		t.Errorf("expected the error output to be shown, got:\n%s", out)
	}
}

func TestRenderMarkdown_DelegateSendResult(t *testing.T) {
	childRef := "local:01DELEGATESEND000000"
	body, err := json.Marshal(map[string]any{
		"delegate_id":           "dlg_01J",
		"started_job_id":        "job_new",
		"current_job_id":        "job_new",
		"latest_job_id":         "job_new",
		"type":                  "delegate",
		"status":                "completed",
		"running_in_background": false,
		"timed_out":             false,
		"action":                "started",
		"transcript_ref":        childRef,
		"output":                "done",
		"truncated":             false,
	})
	if err != nil {
		t.Fatalf("marshal delegate_send result: %v", err)
	}

	out := renderToolCardForResult("delegate_send", "call_send", string(body))

	for _, want := range []string{
		"job_id=job_new",
		"status=completed",
		"transcript_ref=" + childRef,
		"delegate_id=dlg_01J",
		"started_job_id=job_new",
		"current_job_id=job_new",
		"action=started",
		"done",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("delegate_send render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderMarkdown_DelegateSendStateResult(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("first complete") },
	}}
	c.Register(adapter)
	s := newDelegateTestSession(t, c)
	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("second complete")
	})
	adapter.mu.Unlock()

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	if len(res.ToolState) == 0 {
		t.Fatalf("delegate_send missing tool_state; output=%s", res.Output)
	}
	if _, ok := decodeJobResult(res.Output); ok {
		t.Fatalf("delegate_send output unexpectedly decodes as lifecycle JSON; regression requires tool_state: %s", res.Output)
	}

	out := renderToolCardForStateResult("delegate_send", "send", res.Output, res.ToolState)
	for _, want := range []string{
		"job_id=",
		"status=completed",
		"transcript_ref=" + first.TranscriptRef,
		"delegate_id=" + first.DelegateID,
		"action=started",
		"second complete",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("delegate_send state render missing %q:\n%s", want, out)
		}
	}

	entries := []transcript.Entry{
		toolCallEntry(call("send", "delegate_send", `{}`)),
		toolResultEntry(&llm.ToolResultData{
			ToolCallID: "send",
			Name:       "delegate_send",
			Content:    res.Output,
			ToolState:  res.ToolState,
		}),
	}
	outline, _, _ := renderOutline(entries, 0, len(entries)-1)
	want := "delegate_send[status=completed child=" + first.TranscriptRef + "]"
	if !strings.Contains(outline, want) {
		t.Fatalf("outline missing delegate_send lifecycle bracket %q:\n%s", want, outline)
	}
}

// TestRenderMarkdown_JobResultHugeOutputBounded verifies a job result with a
// very long output is still bounded, and the ref stays visible before the
// truncated output.
func TestRenderMarkdown_JobResultHugeOutputBounded(t *testing.T) {
	childRef := "local:01HUGEJOB00000000000000"
	// Far more than resultBodyWholeMax non-empty lines so truncation kicks in.
	output := makeNumberedLines(resultBodyWholeMax + 100)
	body, err := json.Marshal(map[string]any{
		"job_id":                "job_huge",
		"type":                  "delegate",
		"status":                "completed",
		"running_in_background": false,
		"timed_out":             false,
		"transcript_ref":        childRef,
		"output":                output,
		"truncated":             false,
	})
	if err != nil {
		t.Fatalf("marshal job result: %v", err)
	}

	out := renderToolCardForResult("delegate", "call_delegate", string(body))

	// Bounded: an elision marker is present.
	if !strings.Contains(out, "lines elided") {
		t.Errorf("huge job output must be truncated with an elision marker, got:\n%s", out)
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

// TestRenderMarkdown_JobResultWithExtraKeysFallsBack verifies that when a job
// result body carries keys beyond the known job-result fields, the render falls
// back to the general JSON pretty-print rather than hiding the extra evidence.
func TestRenderMarkdown_JobResultWithExtraKeysFallsBack(t *testing.T) {
	bodyStr := `{"job_id":"job_extra","type":"delegate","status":"completed","running_in_background":false,"timed_out":false,"transcript_ref":"local:01X","output":"did stuff","truncated":false,"artifacts":["a.txt","b.txt"]}`

	out := renderToolCardForResult("delegate", "call_delegate", bodyStr)

	// The extra evidence must remain visible (not hidden by struct decoding).
	if !strings.Contains(out, "artifacts") {
		t.Errorf("extra 'artifacts' key must remain visible (fallback to JSON pretty-print), got:\n%s", out)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") {
		t.Errorf("artifact values must remain visible, got:\n%s", out)
	}
}

func TestRenderMarkdown_JobResultPreservesStructuredResultNumbers(t *testing.T) {
	bodyStr := `{"job_id":"job_structured","type":"delegate","status":"completed","running_in_background":false,"timed_out":false,"transcript_ref":"local:01STRUCTURED","output":"done","truncated":false,"structured_result":{"large_id":9007199254740993}}`

	out := renderToolCardForResult("delegate", "call_delegate", bodyStr)

	if !strings.Contains(out, "9007199254740993") {
		t.Fatalf("structured_result large integer must render exactly, got:\n%s", out)
	}
	if strings.Contains(out, "9007199254740992") {
		t.Fatalf("structured_result large integer was rounded, got:\n%s", out)
	}
}

func TestRenderMarkdown_JobResultMetadataVisible(t *testing.T) {
	bodyStr := `{"target":"job_old","job_id":"job_new","type":"delegate","status":"running","reason":"foreground_timeout","running_in_background":true,"timed_out":true,"action":"resumed","resumed_from_job_id":"job_old","transcript_ref":"local:01META","output":"partial","truncated":true,"structured_result":{"ok":false},"structured_result_valid":false}`

	out := renderToolCardForResult("job_send_message", "call_send", bodyStr)

	for _, want := range []string{
		"target=job_old",
		"type=delegate",
		"action=resumed",
		"resumed_from_job_id=job_old",
		"running_in_background=true",
		"timed_out=true",
		"truncated=true",
		"structured_result_valid=false",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("job metadata %q must remain visible, got:\n%s", want, out)
		}
	}
}

// TestRenderMarkdown_GenericJSONResultPrettyPrinted verifies a generic JSON result
// from a non-job tool is pretty-printed (indented) but NOT given the job
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

	// NO false positive: the job status-line treatment must NOT appear.
	if strings.Contains(out, "job_id=") || strings.Contains(out, "transcript_ref=") {
		t.Errorf("non-job JSON must NOT get the job status-line treatment, got:\n%s", out)
	}
}

func TestPrettyJSONRejectsTrailingData(t *testing.T) {
	if _, ok := prettyJSON(`{"a":1} {"b":2}`); ok {
		t.Fatal("prettyJSON must reject trailing data")
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
	// No job treatment.
	if strings.Contains(out, "job_id=") {
		t.Errorf("plain-text result must not get job treatment, got:\n%s", out)
	}
}

// TestRenderMarkdown_JobResultUnparseableFallsBack verifies that a job-named
// result whose body is not a job result falls back to normal/plain rendering.
func TestRenderMarkdown_JobResultUnparseableFallsBack(t *testing.T) {
	plain := "delegate timeout"
	out := renderToolCardForResult("delegate", "call_delegate", plain)

	if !strings.Contains(out, "delegate timeout") {
		t.Errorf("non-JSON job body must render as plain text, got:\n%s", out)
	}
	if strings.Contains(out, "job_id=") {
		t.Errorf("non-decodable job body must not fabricate a status line, got:\n%s", out)
	}
}

// TestDecodeJobResult exercises the unified decode path directly: a full job
// result decodes, while empty and non-matching bodies report false so callers
// fall back.
func TestDecodeJobResult(t *testing.T) {
	t.Run("full struct decodes with all fields", func(t *testing.T) {
		body := `{"job_id":"job_decode","type":"delegate","status":"completed","running_in_background":false,"timed_out":false,"transcript_ref":"local:01Z","output":"line one\nline two","truncated":false}`
		r, ok := decodeJobResult(body)
		if !ok {
			t.Fatalf("expected decode to succeed for %q", body)
		}
		if r.JobID != "job_decode" {
			t.Errorf("JobID: got %q, want job_decode", r.JobID)
		}
		if r.Status != "completed" {
			t.Errorf("Status: got %q, want completed", r.Status)
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
		if _, ok := decodeJobResult("   "); ok {
			t.Errorf("empty body must report false")
		}
	})

	t.Run("non-JSON body reports false", func(t *testing.T) {
		if _, ok := decodeJobResult("not json at all"); ok {
			t.Errorf("non-JSON body must report false")
		}
	})

	t.Run("trailing data reports false", func(t *testing.T) {
		body := `{"job_id":"job_decode","type":"delegate","status":"completed","transcript_ref":"local:01Z"} {"job_id":"job_other"}`
		if _, ok := decodeJobResult(body); ok {
			t.Errorf("job result with trailing data must report false")
		}
	})

	t.Run("extractJobResult subset agrees with decode", func(t *testing.T) {
		body := `{"job_id":"job_failed","type":"delegate","status":"failed","reason":"child_failed","running_in_background":false,"timed_out":false,"transcript_ref":"local:01Q","output":"boom","truncated":false}`
		info, ok := extractJobResult(body)
		if !ok {
			t.Fatalf("extractJobResult should succeed")
		}
		full, _ := decodeJobResult(body)
		if info.jobID != full.JobID || info.status != full.Status || info.transcriptRef != full.TranscriptRef {
			t.Errorf("subset %+v disagrees with full decode %+v", info, full)
		}
	})

	t.Run("delegate_send ids decode to concrete job pivot", func(t *testing.T) {
		body := `{"delegate_id":"dlg_decode","started_job_id":"job_started","current_job_id":"job_current","latest_job_id":"job_current","type":"delegate","status":"running","transcript_ref":"local:01D"}`
		r, ok := decodeJobResult(body)
		if !ok {
			t.Fatalf("expected decode to succeed for delegate_send body")
		}
		if r.effectiveJobID() != "job_current" {
			t.Fatalf("effectiveJobID = %q, want job_current", r.effectiveJobID())
		}
		info, ok := extractJobResult(body)
		if !ok {
			t.Fatalf("extractJobResult should succeed")
		}
		if info.jobID != "job_current" || info.transcriptRef != "local:01D" {
			t.Fatalf("extractJobResult = %+v, want job_current/local:01D", info)
		}
	})
}

func TestRenderOutline_JobLifecycleBrackets(t *testing.T) {
	childRef := "local:01OUTLINEJOB000000000"
	body := `{"job_id":"job_outline","type":"delegate","status":"completed","running_in_background":false,"timed_out":false,"transcript_ref":"` + childRef + `","output":"done","truncated":false}`
	entries := []transcript.Entry{
		toolCallEntry(call("call_delegate", "delegate", `{}`)),
		toolResultEntry(result("call_delegate", "delegate", body, false)),
	}

	out, _, _ := renderOutline(entries, 0, len(entries)-1)

	want := "delegate[status=completed child=" + childRef + "]"
	if !strings.Contains(out, want) {
		t.Fatalf("outline missing job lifecycle bracket %q:\n%s", want, out)
	}
	if strings.Contains(out, "success=") {
		t.Fatalf("outline job bracket must not include legacy success field:\n%s", out)
	}
}

func TestRenderOutline_DelegateSendLifecycleBrackets(t *testing.T) {
	childRef := "local:01OUTLINEDELEGATESEND"
	body := `{"delegate_id":"dlg_outline","started_job_id":"job_started","current_job_id":"job_started","latest_job_id":"job_started","type":"delegate","status":"running","running_in_background":true,"timed_out":false,"action":"started","transcript_ref":"` + childRef + `","truncated":false}`
	entries := []transcript.Entry{
		toolCallEntry(call("call_send", "delegate_send", `{}`)),
		toolResultEntry(result("call_send", "delegate_send", body, false)),
	}

	out, _, _ := renderOutline(entries, 0, len(entries)-1)

	want := "delegate_send[status=running child=" + childRef + "]"
	if !strings.Contains(out, want) {
		t.Fatalf("outline missing delegate_send lifecycle bracket %q:\n%s", want, out)
	}
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

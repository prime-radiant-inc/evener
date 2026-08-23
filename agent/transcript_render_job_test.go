package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
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

// delegateSendToolResult marshals res via the REAL delegate_send marshaler
// (marshalDelegateSendResult) and shapes a ToolResultData exactly like
// production: ToolState carries the JSON state
// (agent/internal/tool/registry.go's StateResult handling — json.Marshal(State)
// — is what session_tool_round.go actually persists), Content carries the
// LLM-facing footer text. toolResultStateOrContent (agent/session_outline.go)
// prefers ToolState, so this is the shape the renderers actually see.
func delegateSendToolResult(t *testing.T, callID string, res sendMessageResult, maxChars int) *llm.ToolResultData {
	t.Helper()
	value, err := marshalDelegateSendResult(res, maxChars)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult: %v", err)
	}
	sr, ok := value.(tool.StateResult)
	if !ok {
		t.Fatalf("marshalDelegateSendResult returned %T, want tool.StateResult", value)
	}
	state, err := json.Marshal(sr.State)
	if err != nil {
		t.Fatalf("marshal delegate_send state: %v", err)
	}
	r := result(callID, "delegate_send", sr.Output, false)
	r.ToolState = state
	return r
}

// TestRenderMarkdown_JobDelegateResult is the headline lifecycle case: a
// delegate create result — the real stable-delegate wire shape produced by
// marshalStableDelegateCreateResult (issue #194) — renders as the condensed
// status line (job identity via the delegate_id fallback, status, and
// transcript_ref), not a raw JSON dump.
func TestRenderMarkdown_JobDelegateResult(t *testing.T) {
	t.Parallel()
	childRef := "local:01CHILDJOB000000000000"
	body, err := marshalStableDelegateCreateResult(stableDelegateCreateResult{
		DelegateID:    "dlg_delegate_1",
		Type:          "delegate",
		Status:        "completed",
		TranscriptRef: childRef,
		Resumable:     new(true),
	}, 0)
	if err != nil {
		t.Fatalf("marshalStableDelegateCreateResult: %v", err)
	}

	out := renderToolCardForResult("delegate", "call_delegate", body)

	// The status line surfaces status and job identity without a legacy success field.
	if !strings.Contains(out, "status=completed") {
		t.Errorf("expected status=completed on the status line, got:\n%s", out)
	}
	// A stable-delegate create result carries no job_id family — the status line
	// must fall back to delegate_id (jobResult.effectiveJobID, session_outline.go).
	if !strings.Contains(out, "job_id=dlg_delegate_1") {
		t.Errorf("expected job_id=dlg_delegate_1 (delegate_id fallback), got:\n%s", out)
	}
	if strings.Contains(out, "success=") {
		t.Errorf("job result must not render legacy success field, got:\n%s", out)
	}
	if !strings.Contains(out, "transcript_ref="+childRef) {
		t.Errorf("expected transcript_ref=%s on the status line, got:\n%s", childRef, out)
	}

	// NOT a raw JSON dump: the issue #194 regression was exactly this — an
	// allowlist-unknown top-level key (e.g. child_session_id, model) made
	// jobResultBody bail to prettyJSON instead of the condensed status line.
	if strings.Contains(out, `"delegate_id":`) {
		t.Errorf("delegate create result rendered as a raw JSON dump instead of the condensed status line, got:\n%s", out)
	}
}

// TestRenderMarkdown_JobSendMessageResult verifies the gated render also applies
// to job_send_message results that resume a delegate and return a transcript ref.
func TestRenderMarkdown_JobSendMessageResult(t *testing.T) {
	t.Parallel()
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

// TestRenderMarkdown_DelegateSendResult drives the real delegate_send
// marshaler (marshalDelegateSendResult) so the fixture matches production's
// actual wire shape: sendMessageResult has no job_id family at all (only
// delegate_id), so the status line's job identity depends entirely on the
// delegate_id fallback (agent/session_outline.go effectiveJobID).
func TestRenderMarkdown_DelegateSendResult(t *testing.T) {
	t.Parallel()
	childRef := "local:01DELEGATESEND000000"
	output := "First line of the report.\nSECOND_LINE_NEEDLE summarizing findings.\nThird line with a conclusion."
	res := delegateSendToolResult(t, "call_send", sendMessageResult{
		DelegateID:    "dlg_01J",
		Type:          "delegate",
		Status:        jobstore.StatusCompleted,
		Action:        "started",
		TranscriptRef: childRef,
		Output:        output,
	}, 0)

	entries := []transcript.Entry{
		toolCallEntry(call("call_send", "delegate_send", `{}`)),
		toolResultEntry(res),
	}
	out := renderMarkdown(transcript.Header{}, entries, 0, renderOpts{})

	for _, want := range []string{
		"job_id=dlg_01J",
		"status=completed",
		"transcript_ref=" + childRef,
		"action=started",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("delegate_send render missing %q:\n%s", want, out)
		}
	}

	// The transcript_ref precedes the output, and the output de-escapes to real
	// newlines (the pre-#194 pin, replayed against the real marshaler).
	refIdx := strings.Index(out, childRef)
	outputIdx := strings.Index(out, "SECOND_LINE_NEEDLE")
	if refIdx < 0 || outputIdx < 0 || refIdx > outputIdx {
		t.Fatalf("transcript_ref (%d) must appear BEFORE the output body (%d):\n%s", refIdx, outputIdx, out)
	}
	if strings.Contains(out, `\n`) {
		t.Errorf("escaped \\n must not appear (output must be de-escaped), got:\n%s", out)
	}

	// NOT a raw JSON dump: delegate_send's new fields (task, description,
	// agent_type, ...) used to be allowlist-unknown, forcing the same fallback
	// via toolResultStateOrContent.
	if strings.Contains(out, `"delegate_id":`) {
		t.Errorf("delegate_send result rendered as a raw JSON dump instead of the condensed status line, got:\n%s", out)
	}
}

func TestRenderMarkdown_UnpairedDelegateSendUsesToolState(t *testing.T) {
	t.Parallel()
	state := []byte(`{"delegate_id":"dlg_unpaired","current_job_id":"job_unpaired","type":"delegate","status":"completed","running_in_background":false,"action":"started","transcript_ref":"local:01UNPAIRED","output":"done","truncated":false}`)
	res := result("send", "delegate_send", "done\n[delegate footer without lifecycle JSON]", false)
	res.ToolState = state
	entries := []transcript.Entry{toolResultEntry(res)}

	out := renderMarkdown(transcript.Header{}, entries, 0, renderOpts{})
	for _, want := range []string{
		"[call not shown] `delegate_send`",
		"job_id=job_unpaired",
		"status=completed",
		"transcript_ref=local:01UNPAIRED",
		"delegate_id=dlg_unpaired",
		"done",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unpaired delegate_send render missing %q:\n%s", want, out)
		}
	}
}

// TestRenderMarkdown_JobResultHugeOutputBounded verifies a job result with a
// very long output is still bounded, and the ref stays visible before the
// truncated output.
func TestRenderMarkdown_JobResultHugeOutputBounded(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if _, ok := prettyJSON(`{"a":1} {"b":2}`); ok {
		t.Fatal("prettyJSON must reject trailing data")
	}
}

// TestRenderMarkdown_NonJSONResultUnchanged verifies a plain-text (non-JSON)
// result renders exactly as before: no pretty-print, no status line.
func TestRenderMarkdown_NonJSONResultUnchanged(t *testing.T) {
	t.Parallel()
	plain := "ok  primeradiant.com/evener/agent  1.20s\nPASS"
	out := renderToolCardForResult("shell", "call_shell", plain)

	if !strings.Contains(out, "ok  primeradiant.com/evener/agent  1.20s") {
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
	t.Parallel()
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
	t.Parallel()
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

// TestRenderOutline_JobLifecycleBrackets drives the real stable-delegate create
// marshaler so the fixture matches production's wire shape (issue #194): no
// job_id family, only delegate_id.
func TestRenderOutline_JobLifecycleBrackets(t *testing.T) {
	t.Parallel()
	childRef := "local:01OUTLINEJOB000000000"
	body, err := marshalStableDelegateCreateResult(stableDelegateCreateResult{
		DelegateID:    "dlg_outline",
		Type:          "delegate",
		Status:        "completed",
		TranscriptRef: childRef,
	}, 0)
	if err != nil {
		t.Fatalf("marshalStableDelegateCreateResult: %v", err)
	}
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

// TestRenderOutline_DelegateSendLifecycleBrackets drives the real
// delegate_send marshaler (marshalDelegateSendResult), via the same
// ToolState-carrying shape production actually emits.
func TestRenderOutline_DelegateSendLifecycleBrackets(t *testing.T) {
	t.Parallel()
	childRef := "local:01OUTLINEDELEGATESEND"
	res := delegateSendToolResult(t, "call_send", sendMessageResult{
		DelegateID:          "dlg_outline",
		Type:                "delegate",
		Status:              jobstore.StatusRunning,
		Action:              "started",
		RunningInBackground: true,
		TranscriptRef:       childRef,
	}, 0)
	entries := []transcript.Entry{
		toolCallEntry(call("call_send", "delegate_send", `{}`)),
		toolResultEntry(res),
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestJobResultKnownKeysCoversLiveDelegateShapes is the schema-drift guard
// (issue #194 RCA fix plan item 3): it walks the REAL marshaled output of the
// two live result marshalers that feed jobResultBody/extractJobResult —
// marshalStableDelegateCreateResult (the "delegate" create tool) and
// marshalDelegateSendResult (the "delegate_send" tool, via the same
// tool.StateResult path session_tool_round.go actually persists) — and asserts
// every top-level key either marshaler ever emits is present in
// jobResultKnownKeys. fillEveryField (agent/session_client_mutation_doctor_drift_test.go)
// sets every field to a non-zero value via reflection, so a field added to
// either struct tomorrow is populated and checked with no edit to this test —
// the next drift fails CI, named, instead of silently degrading rendering to a
// raw JSON dump.
func TestJobResultKnownKeysCoversLiveDelegateShapes(t *testing.T) {
	t.Parallel()

	t.Run("stableDelegateCreateResult via marshalStableDelegateCreateResult", func(t *testing.T) {
		var out stableDelegateCreateResult
		fillEveryField(t, reflect.ValueOf(&out).Elem(), "stableDelegateCreateResult")
		// maxChars<=0 is unbounded (marshalBoundedJSON), so the fit-downgrade
		// path never drops a field out of the fully-populated fixture.
		wire, err := marshalStableDelegateCreateResult(out, 0)
		if err != nil {
			t.Fatalf("marshalStableDelegateCreateResult: %v", err)
		}
		assertKeysKnownToJobResult(t, wire, "stableDelegateCreateResult")
	})

	t.Run("delegate_send result via marshalDelegateSendResult", func(t *testing.T) {
		var res sendMessageResult
		fillEveryField(t, reflect.ValueOf(&res).Elem(), "sendMessageResult")
		value, err := marshalDelegateSendResult(res, 0)
		if err != nil {
			t.Fatalf("marshalDelegateSendResult: %v", err)
		}
		sr, ok := value.(tool.StateResult)
		if !ok {
			t.Fatalf("marshalDelegateSendResult returned %T, want tool.StateResult", value)
		}
		// json.Marshal(State) is exactly what agent/internal/tool/registry.go
		// persists into ToolResultData.ToolState (the wire toolResultStateOrContent
		// hands to jobResultBody).
		wire, err := json.Marshal(sr.State)
		if err != nil {
			t.Fatalf("marshal delegate_send state: %v", err)
		}
		assertKeysKnownToJobResult(t, string(wire), "delegate_send result")
	})
}

// assertKeysKnownToJobResult fails, naming the offending key, if wire (a JSON
// object) carries a top-level key absent from jobResultKnownKeys
// (agent/transcript_render.go) — the same allowlist hasNonJobResultKeys checks
// before jobResultBody renders the condensed status line.
func assertKeysKnownToJobResult(t *testing.T, wire, label string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(wire), &m); err != nil {
		t.Fatalf("%s: unmarshal wire output: %v\n%s", label, err, wire)
	}
	for k := range m {
		if !jobResultKnownKeys[k] {
			t.Errorf("%s: emits key %q, which jobResultKnownKeys (agent/transcript_render.go) does not know — "+
				"add it to the allowlist or the condensed status line silently degrades to a raw JSON dump", label, k)
		}
	}
}

// TestJobResult_EffectiveJobIDDelegateIDFallback pins jobResult.effectiveJobID's
// priority order (agent/session_outline.go): the legacy job_id family first
// (JobID, CurrentJobID, StartedJobID, LatestJobID, in that order), DelegateID as
// the fallback for the stable-delegate shape (issue #194 RCA fix plan item 2,
// which carries no job_id family at all), empty when nothing is set.
func TestJobResult_EffectiveJobIDDelegateIDFallback(t *testing.T) {
	t.Parallel()

	t.Run("delegate_id used when no job_id family is present", func(t *testing.T) {
		r := jobResult{DelegateID: "dlg_only"}
		if got := r.effectiveJobID(); got != "dlg_only" {
			t.Errorf("effectiveJobID() = %q, want dlg_only (delegate_id fallback)", got)
		}
	})

	t.Run("job_id family takes priority over delegate_id", func(t *testing.T) {
		r := jobResult{DelegateID: "dlg_low_priority", CurrentJobID: "job_wins"}
		if got := r.effectiveJobID(); got != "job_wins" {
			t.Errorf("effectiveJobID() = %q, want job_wins (job_id family before delegate_id)", got)
		}
	})

	t.Run("empty when nothing set", func(t *testing.T) {
		r := jobResult{}
		if got := r.effectiveJobID(); got != "" {
			t.Errorf("effectiveJobID() = %q, want empty", got)
		}
	})
}

// TestRenderMarkdown_DelegateCreateErrorSurfaces guards against a narrower
// version of the issue #194 regression: once "error" is added to
// jobResultKnownKeys so a failed-but-identified delegate create no longer dumps
// raw JSON, the StartError text itself must still be visible in the condensed
// rendering (via jobResultMetadataKeys) — not silently dropped now that the
// dump-with-full-evidence fallback no longer fires for it.
func TestRenderMarkdown_DelegateCreateErrorSurfaces(t *testing.T) {
	t.Parallel()
	body, err := marshalStableDelegateCreateResult(stableDelegateCreateResult{
		DelegateID: "dlg_partial_fail",
		Type:       "delegate",
		Status:     "failed",
		StartError: "sandbox denied: escalation beyond parent floor",
	}, 0)
	if err != nil {
		t.Fatalf("marshalStableDelegateCreateResult: %v", err)
	}

	out := renderToolCardForResult("delegate", "call_delegate", body)

	if !strings.Contains(out, "sandbox denied: escalation beyond parent floor") {
		t.Errorf("StartError must remain visible in the condensed rendering, got:\n%s", out)
	}
	if strings.Contains(out, `"delegate_id":`) {
		t.Errorf("delegate create result rendered as a raw JSON dump instead of the condensed status line, got:\n%s", out)
	}
}

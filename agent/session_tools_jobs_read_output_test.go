package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// TestJobReadOutputReportsStatus pins job_read_output legibility: a small read
// window of a fully-retained log reports output_status="windowed" with
// dropped_bytes=0, so the model knows the rest is reachable, not lost.
func TestJobReadOutputReportsStatus(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000"}`),
	})
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("9 KiB output returned no job_id")
	}

	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "r1",

		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","tail_lines":500}`),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	var ro struct {
		TotalBytes   int64  `json:"total_bytes"`
		DroppedBytes int64  `json:"dropped_bytes"`
		OutputStatus string `json:"output_status"`
	}
	if err := json.Unmarshal(toolResultJSON(readRes), &ro); err != nil {
		t.Fatalf("unmarshal read: %v (output: %s)", err, readRes.Output)
	}
	if ro.OutputStatus != "windowed" {
		t.Fatalf("output_status = %q, want windowed (total %d > shown, dropped %d)", ro.OutputStatus, ro.TotalBytes, ro.DroppedBytes)
	}
	if ro.DroppedBytes != 0 {
		t.Fatalf("dropped_bytes = %d, want 0", ro.DroppedBytes)
	}
}

// TestJobReadOutputUnknownIDPointsToJobList pins the not-found recovery hint:
// when a job_id resolves to nothing (a guessed id, or a foreground command whose
// output rode inline and kept no durable job), the error must redirect the model
// to job_list rather than dead-ending on a bare "not found".
func TestJobReadOutputUnknownIDPointsToJobList(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "r1",

		Arguments: json.RawMessage(`{"job_id":"0"}`),
	})
	if !res.IsError {
		t.Fatalf("expected an error for an unknown job id, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_list") {
		t.Fatalf("not-found error must point the model at job_list, got: %s", res.Output)
	}
}

// TestJobReadOutputDefaultWindowIsBounded pins A5: a bare job_read_output (no
// head_lines/tail_lines) returns a small bounded default window, not up to the
// full retention. The agent pages with an explicit tail_lines for more.

// TestJobReadOutputDefaultWindowIsBounded pins A5: a bare job_read_output (no
// head_lines/tail_lines) returns a small bounded default window, not up to the
// full retention. The agent pages with an explicit tail_lines for more.
func TestJobReadOutputDefaultWindowIsBounded(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 20000"}`),
	})
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("20 KiB output returned no job_id")
	}

	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "r1",

		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `"}`),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	var ro struct {
		Content    string `json:"output"`
		TotalBytes int64  `json:"total_bytes"`
	}
	if err := json.Unmarshal(toolResultJSON(readRes), &ro); err != nil {
		t.Fatalf("unmarshal read: %v", err)
	}
	if ro.TotalBytes < 20000 {
		t.Fatalf("total_bytes = %d, want >= 20000", ro.TotalBytes)
	}
	if len(ro.Content) > 9000 {
		t.Fatalf("bare-read content = %d bytes, want a small bounded default window (<= ~8 KiB)", len(ro.Content))
	}
}

// TestJobReadOutputHeadAndTailTogether pins that head_lines + tail_lines in one
// call returns a custom-sized head+tail digest (not an error): the first N + last
// M lines with the middle elided.

// TestJobReadOutputHeadAndTailTogether pins that head_lines + tail_lines in one
// call returns a custom-sized head+tail digest (not an error): the first N + last
// M lines with the middle elided.
func TestJobReadOutputHeadAndTailTogether(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"seq 1 5000","max_runtime_ms":5000}`),
	})
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(toolResultJSON(res), &out)
	if out.JobID == "" {
		t.Fatalf("seq 1 5000 should be a handle: %s", res.Output)
	}
	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "r1",

		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","head_lines":3,"tail_lines":3}`),
	})
	if readRes.IsError {
		t.Fatalf("head_lines+tail_lines together must be allowed, got error: %s", readRes.Output)
	}
	var ro struct {
		Content string `json:"output"`
	}
	if err := json.Unmarshal(toolResultJSON(readRes), &ro); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"1\n", "5000\n", "elided"} {
		if !strings.Contains(ro.Content, want) {
			t.Fatalf("head+tail digest missing %q:\n%s", want, ro.Content)
		}
	}
}

// TestJobReadOutputFromLineMiddleSlice pins the middle-slice accessor: from_line
// + line_count returns exactly that line range, marked windowed (lines exist on
// both sides).

// TestJobReadOutputFromLineMiddleSlice pins the middle-slice accessor: from_line
// + line_count returns exactly that line range, marked windowed (lines exist on
// both sides).
func TestJobReadOutputFromLineMiddleSlice(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"seq 1 5000","max_runtime_ms":5000}`),
	})
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(toolResultJSON(res), &out)
	if out.JobID == "" {
		t.Fatalf("seq 1 5000 should be a handle: %s", res.Output)
	}
	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "r1",

		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","from_line":2500,"line_count":3}`),
	})
	if readRes.IsError {
		t.Fatalf("from_line read error: %s", readRes.Output)
	}
	var ro struct {
		Content      string `json:"output"`
		OutputStatus string `json:"output_status"`
	}
	if err := json.Unmarshal(toolResultJSON(readRes), &ro); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ro.Content != "2500\n2501\n2502\n" {
		t.Fatalf("from_line slice = %q, want lines 2500-2502", ro.Content)
	}
	if ro.OutputStatus != "windowed" {
		t.Fatalf("output_status = %q, want windowed (lines exist on both sides)", ro.OutputStatus)
	}
}

// TestJobListRowIsLean pins that a job_list scan row drops detail-only fields
// and null/empty fields: no transcript_ref/resumable/visible_to_session_id, no
// explicit nulls, and no empty recent_watches array.

func TestJobReadOutputReturnsBackgroundDelegateStructuredResult(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("background report", map[string]any{
					"summary": "persisted",
				})
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"produce a structured result",
			"result_schema":{
				"type":"object",
				"properties":{"summary":{"type":"string"}},
				"required":["summary"]
			}
		}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	var delegateOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	if delegateOut.JobID == "" || delegateOut.Status != string(jobstore.StatusRunning) {
		t.Fatalf("delegate output = %+v, want running background job", delegateOut)
	}
	waitForShellDone(t, s.jobManager, delegateOut.JobID)

	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, delegateOut.JobID)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	var readOut jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(readRes), &readOut); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, readRes.Output)
	}
	if readOut.Status != string(jobstore.StatusCompleted) ||
		!readOut.StructuredResultValid ||
		readOut.StructuredResult["summary"] != "persisted" {
		t.Fatalf("job_read_output = %+v, want persisted structured result", readOut)
	}
}

func TestJobReadOutputReturnsBackgroundDelegateSchemaResultMissingReason(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("background missing structured result")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"produce a structured result",
			"result_schema":{
				"type":"object",
				"properties":{"summary":{"type":"string"}},
				"required":["summary"]
			}
		}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	var delegateOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	if delegateOut.JobID == "" || delegateOut.Status != string(jobstore.StatusRunning) {
		t.Fatalf("delegate output = %+v, want running background job", delegateOut)
	}
	waitForShellDone(t, s.jobManager, delegateOut.JobID)

	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, delegateOut.JobID)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	assertStructuredResultInvalidReason(t, string(toolResultJSON(readRes)), "schema_result_missing")
}

func TestJobReadOutputBlockReturnsOnNewOutput(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "manual running job"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	t.Cleanup(func() {
		_ = s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "test_cleanup", nil)
		waitForShellDone(t, s.jobManager, rec.JobID)
	})

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.jobManager.mu.Lock()
		run := s.jobManager.running[rec.JobID]
		s.jobManager.mu.Unlock()
		if run != nil {
			_, _ = run.output.Append([]byte("new output\n"))
		}
	}()

	started := time.Now()
	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":1000}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	if elapsed := time.Since(started); elapsed >= 900*time.Millisecond {
		t.Fatalf("job_read_output blocked for %s, want return on output before timeout", elapsed)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
	}
	if !strings.Contains(out.Content, "new output") || out.Status != string(jobstore.StatusRunning) {
		t.Fatalf("job_read_output = %+v, want running job with new output", out)
	}
}

func TestJobReadOutputBlockGrepReturnsImmediatelyOnExistingMatch(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "boot log\nready to serve\n")

	out, elapsed := blockingGrepRead(t, s, rec.JobID, "ready", 1000)
	if elapsed >= 900*time.Millisecond {
		t.Fatalf("job_read_output blocked for %s, want immediate return on existing match", elapsed)
	}
	if out.Status != string(jobstore.StatusRunning) {
		t.Fatalf("status = %q, want running", out.Status)
	}
	if len(out.Matches) != 1 || !strings.Contains(out.Matches[0].Line, "ready to serve") {
		t.Fatalf("matches = %+v, want existing match for ready to serve", out.Matches)
	}
}

func TestJobReadOutputBlockGrepWaitsForMatchNotJustNewOutput(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "starting up\n")

	go func() {
		time.Sleep(50 * time.Millisecond)
		appendManualJobOutput(s.jobManager, rec.JobID, "still warming\n")
		time.Sleep(250 * time.Millisecond)
		appendManualJobOutput(s.jobManager, rec.JobID, "now ready\n")
	}()

	out, elapsed := blockingGrepRead(t, s, rec.JobID, "ready", 5000)
	if elapsed >= 2*time.Second {
		t.Fatalf("job_read_output blocked for %s, want return shortly after the match lands", elapsed)
	}
	if out.Status != string(jobstore.StatusRunning) {
		t.Fatalf("status = %q, want running", out.Status)
	}
	if len(out.Matches) != 1 || !strings.Contains(out.Matches[0].Line, "now ready") {
		t.Fatalf("matches = %+v, want the mid-stream match (non-matching output must not end the wait)", out.Matches)
	}
}

func TestJobReadOutputBlockGrepTimesOutWithoutMatch(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "no signal here\n")

	out, elapsed := blockingGrepRead(t, s, rec.JobID, "ready", 1000)
	if elapsed < 800*time.Millisecond {
		t.Fatalf("job_read_output returned after %s, want block until timeout without match", elapsed)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("job_read_output blocked for %s, want return at timeout", elapsed)
	}
	if out.Status != string(jobstore.StatusRunning) {
		t.Fatalf("status = %q, want running", out.Status)
	}
	if out.Grep != "ready" || len(out.Matches) != 0 {
		t.Fatalf("grep = %q matches = %+v, want empty matches on timeout", out.Grep, out.Matches)
	}
}

func TestJobReadOutputBlockGrepReturnsWhenJobGoesTerminal(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "working\n")

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil)
	}()

	out, elapsed := blockingGrepRead(t, s, rec.JobID, "ready", 5000)
	if elapsed >= 2*time.Second {
		t.Fatalf("job_read_output blocked for %s, want return when the job goes terminal", elapsed)
	}
	if out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed final snapshot", out.Status)
	}
	if out.Grep != "ready" || len(out.Matches) != 0 {
		t.Fatalf("grep = %q matches = %+v, want empty matches for terminal job without match", out.Grep, out.Matches)
	}
}

func TestJobGrepScanCarriesPartialLineAcrossSteps(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	re := regexp.MustCompile("ready")
	var scan jobGrepScan

	appendManualJobOutput(s.jobManager, rec.JobID, "boot\nrea")
	if scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan matched before the split token completed")
	}
	appendManualJobOutput(s.jobManager, rec.JobID, "dy\n")
	if !scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan missed a token split across appends (partial-line carry)")
	}
}

func TestJobGrepScanMatchesUnterminatedTrailingLine(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	re := regexp.MustCompile("ready")
	var scan jobGrepScan

	appendManualJobOutput(s.jobManager, rec.JobID, "almost ready")
	if !scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan missed a match on the unterminated trailing line (snapshot grep matches it at end of output)")
	}
}

func TestJobGrepScanSkipsUnchangedOutput(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	re := regexp.MustCompile("ready")
	var scan jobGrepScan

	appendManualJobOutput(s.jobManager, rec.JobID, "nothing to see\n")
	if scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan matched output without the token")
	}
	if scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("re-step without new output must not match")
	}
	appendManualJobOutput(s.jobManager, rec.JobID, "ready\n")
	if !scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan missed a match appended after a no-op step")
	}
}

func TestJobGrepScanNeverMatchesOverlongLines(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	re := regexp.MustCompile("ready")
	var scan jobGrepScan

	// A line whose content exceeds maxJobGrepLineBytes never matches the
	// snapshot grep, so the incremental scan must not match it either — at
	// any of: complete-in-one-segment, streamed-past-the-cap, or its end.
	appendManualJobOutput(s.jobManager, rec.JobID, strings.Repeat("x", maxJobGrepLineBytes)+" ready\n")
	if scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan matched an overlong complete line")
	}
	appendManualJobOutput(s.jobManager, rec.JobID, strings.Repeat("y", maxJobGrepLineBytes+2))
	if scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan matched a dead (overlong) unterminated line")
	}
	appendManualJobOutput(s.jobManager, rec.JobID, " ready\nready again\n")
	if !scan.step(s.jobManager, rec.JobID, re, maxJobGrepLineBytes) {
		t.Fatal("scan missed the matching line after the dead line ended")
	}
}

// TestReadJobOutputFromWidensPastStaleTotal verifies that readJobOutputFrom
// widens past a stale caller-supplied total and returns the full retained
// content anchored at the true start. When total=50 but 100 bytes exist, the
// first attempt requests only 50 bytes and gets back the tail [50,100) so
// start=50 > from=0; the loop widens want to 100, the next attempt reads all
// 100 bytes with start=0 <= from=0, and exits via the start<=from branch.
// This exercises the pre-existing widen behavior (it exits via start<=from,
// not the retry-exhausted path), so it is independent of the not-ok-on-race
// change.

// TestReadJobOutputFromWidensPastStaleTotal verifies that readJobOutputFrom
// widens past a stale caller-supplied total and returns the full retained
// content anchored at the true start. When total=50 but 100 bytes exist, the
// first attempt requests only 50 bytes and gets back the tail [50,100) so
// start=50 > from=0; the loop widens want to 100, the next attempt reads all
// 100 bytes with start=0 <= from=0, and exits via the start<=from branch.
// This exercises the pre-existing widen behavior (it exits via start<=from,
// not the retry-exhausted path), so it is independent of the not-ok-on-race
// change.
func TestReadJobOutputFromWidensPastStaleTotal(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, strings.Repeat("a", 100))

	// Pass a stale total of 50 while 100 bytes actually exist.
	content, start, ok := readJobOutputFrom(s.jobManager, rec.JobID, 0, 50)
	if !ok {
		t.Fatal("readJobOutputFrom returned not-ok; want ok after widening to full content")
	}
	if start != 0 {
		t.Fatalf("start = %d, want 0 (all retained bytes returned)", start)
	}
	if len(content) != 100 {
		t.Fatalf("len(content) = %d, want 100 (full 100 bytes returned after widen)", len(content))
	}
}

// TestJobReadOutputBlockGrepReturnsImmediatelyOnTerminalJobWithMatch verifies
// that block+grep on an already-terminal job returns at once (not after the
// full timeout) and delivers matches from the terminal snapshot.

// TestJobReadOutputBlockGrepReturnsImmediatelyOnTerminalJobWithMatch verifies
// that block+grep on an already-terminal job returns at once (not after the
// full timeout) and delivers matches from the terminal snapshot.
func TestJobReadOutputBlockGrepReturnsImmediatelyOnTerminalJobWithMatch(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "boot\nall ready now\n")

	// Finalize to terminal BEFORE the blocking read.
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	waitForShellDone(t, s.jobManager, rec.JobID)
	// The t.Cleanup registered by newManualRunningJob will call finalize again
	// with StatusCancelled; since the job is already terminal (run==nil), that
	// second finalize is a no-op (returns nil) — safe.

	out, elapsed := blockingGrepRead(t, s, rec.JobID, "ready", 1000)
	if elapsed >= 900*time.Millisecond {
		t.Fatalf("job_read_output blocked for %s, want immediate return on terminal job", elapsed)
	}
	if out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", out.Status)
	}
	if len(out.Matches) != 1 || !strings.Contains(out.Matches[0].Line, "ready") {
		t.Fatalf("matches = %+v, want exactly one match for 'ready'", out.Matches)
	}
}

func TestJobReadOutputRejectsInvalidArgs(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'ready-line\n'; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	for _, tc := range []struct {
		name string
		args string
	}{
		{"tail_lines", fmt.Sprintf(`{"job_id":%q,"tail_lines":-1}`, shellOut.JobID)},
		{"grep", fmt.Sprintf(`{"job_id":%q,"grep":"["}`, shellOut.JobID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
				ID: "read",

				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
			}
		})
	}
}

func TestJobReadOutputGrepSearchesRetainedOutputBeyondTail(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'needle-start\n'; yes filler-line | head -c 70000; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	readOut := waitForJobGrepMatchResult(t, s, shellOut.JobID, "needle-start", 1024)
	if len(readOut.Matches) != 1 || !strings.Contains(readOut.Matches[0].Line, "needle-start") {
		t.Fatalf("matches = %+v, want retained output match", readOut.Matches)
	}
	if readOut.Matches[0].ByteOffset == nil || *readOut.Matches[0].ByteOffset != 0 {
		t.Fatalf("match byte offset = %+v, want 0", readOut.Matches[0].ByteOffset)
	}
}

func TestJobReadOutputProjectionTooLargeDoesNotMutateDurableStructuredResult(t *testing.T) {
	t.Parallel()
	// Payload must exceed the registry default cap (jobToolResultDefaultMaxChar) so the
	// projected job_read_output result overflows and yields projection_too_large.
	payload := strings.Repeat("x", jobToolResultDefaultMaxChar+10000)
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("background report", map[string]any{
					"payload": payload,
				})
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"produce a large structured result",
			"result_schema":{
				"type":"object",
				"properties":{"payload":{"type":"string"}},
				"required":["payload"]
			}
		}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	var delegateOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	waitForShellDone(t, s.jobManager, delegateOut.JobID)

	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, delegateOut.JobID)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	assertStructuredResultInvalidReason(t, string(toolResultJSON(readRes)), "projection_too_large")

	rec := loadShellRecord(t, s.jobManager, delegateOut.JobID)
	structured, ok := rec.StructuredResult.(map[string]any)
	if !ok || structured["payload"] != payload {
		t.Fatalf("durable structured_result = %+v, want original payload", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || !*rec.StructuredResultValid {
		t.Fatalf("durable structured_result_valid = %v, want true", rec.StructuredResultValid)
	}
	if rec.StructuredResultReason != "" {
		t.Fatalf("durable structured_result_reason = %q, want empty", rec.StructuredResultReason)
	}
}

func TestJobReadOutputIsNotModelFacing(t *testing.T) {
	s := newTestSession(t)
	if got := s.reg.Get("job_read_output"); got != nil {
		t.Fatalf("job_read_output is still registered: %+v", got.Definition.Name)
	}
	if got := s.reg.Get("job_status"); got == nil {
		t.Fatalf("job_status is not registered")
	}
	if got := s.reg.Get("wait_for_transcript_match"); got != nil {
		t.Fatalf("wait_for_transcript_match is still registered: %+v", got.Definition.Name)
	}
}

func TestJobReadOutputGrepSearchesTerminalOutputFileBeyondTail(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// Build a durable completed job whose output exceeds the tail budget:
	// "needle-start" at byte 0 followed by ~70 KB of filler. This exercises
	// the retained-output grep path without relying on complete-or-handle (A3)
	// to keep the record — A3 keeps records whose output exceeds the inline
	// embed budget; constructing the record directly keeps the test focused on
	// §6.2 grep-beyond-tail behaviour.
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "needle-start\n")
	appendManualJobOutput(s.jobManager, rec.JobID, strings.Repeat("filler-line\n", 6000)) // ~72 KB
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
		t.Fatalf("finalize manual job: %v", err)
	}
	waitForShellDone(t, s.jobManager, rec.JobID)
	jobID := rec.JobID

	readOut := waitForJobGrepMatchResult(t, s, jobID, "needle-start", 1024)
	if readOut.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", readOut.Status)
	}
	if len(readOut.Matches) != 1 || !strings.Contains(readOut.Matches[0].Line, "needle-start") {
		t.Fatalf("matches = %+v, want terminal retained output match", readOut.Matches)
	}
	if readOut.Matches[0].ByteOffset == nil || *readOut.Matches[0].ByteOffset != 0 {
		t.Fatalf("match byte offset = %+v, want 0", readOut.Matches[0].ByteOffset)
	}
}

// TestJobReadOutputGrepScansFullRetainedOutputBeyondOldBudget verifies that
// grep finds a match deep in retained output whose byte position exceeds the
// former 65536-byte scan budget.  The test constructs 30 matching lines of
// ~3000 bytes each (~90 KB total matched bytes, well above the old 64 KB cap)
// followed by a uniquely-identifiable "FINAL-NEEDLE" line.  Under the old
// budget the scan halted after ~22 lines (~66 KB) and FINAL-NEEDLE was never
// reached; under full-scan all 31 lines are scanned (31 < 100-match cap) and
// FINAL-NEEDLE is present.

// TestJobReadOutputGrepScansFullRetainedOutputBeyondOldBudget verifies that
// grep finds a match deep in retained output whose byte position exceeds the
// former 65536-byte scan budget.  The test constructs 30 matching lines of
// ~3000 bytes each (~90 KB total matched bytes, well above the old 64 KB cap)
// followed by a uniquely-identifiable "FINAL-NEEDLE" line.  Under the old
// budget the scan halted after ~22 lines (~66 KB) and FINAL-NEEDLE was never
// reached; under full-scan all 31 lines are scanned (31 < 100-match cap) and
// FINAL-NEEDLE is present.
func TestJobReadOutputGrepScansFullRetainedOutputBeyondOldBudget(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)

	// Build 30 matching lines of ~3000 bytes each.
	// Content per line: "row " + 2996-byte padding + newline = 3001 bytes.
	// 30 lines × ~3000 matched bytes ≈ 90 KB > 65536 (old budget).
	// Each line is well under maxJobGrepLineBytes (4096), so the per-line cap
	// does not apply and all 30 lines are valid matches.
	padding := strings.Repeat("x", 2996)
	var buf strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&buf, "row %s\n", padding)
	}
	// Final line that must appear in results under full-scan.
	buf.WriteString("row FINAL-NEEDLE\n")
	appendManualJobOutput(s.jobManager, rec.JobID, buf.String())

	re := regexp.MustCompile(`row`)
	matches, err := s.jobManager.grepOutput(rec.JobID, re)
	if err != nil {
		t.Fatalf("grepOutput returned error: %v", err)
	}
	// Under the old 65536-byte budget the scan stops after ~22 matches and
	// FINAL-NEEDLE (line 31) is never reached.  Under full-scan, all 31 lines
	// are scanned (31 < maxJobGrepMatches=100) and FINAL-NEEDLE appears.
	found := false
	for _, m := range matches {
		if strings.Contains(m.Line, "FINAL-NEEDLE") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("grep did not find FINAL-NEEDLE in retained output (got %d matches); silent-miss budget regression", len(matches))
	}
}

func TestJobReadOutputRejectsLargeGrepBeforeRegistryTruncation(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// Use a manual job; fast small-output shell commands return ephemeral (no
	// job_id) under complete-or-handle, so the shell tool cannot reliably
	// produce a durable id here. The test is about grep validation, not shell.
	rec := newManualRunningJob(t, s)

	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%q}`, rec.JobID, strings.Repeat("a", maxJobGrepPatternBytes+1))),
	})
	if !res.IsError {
		t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "grep must be at most") {
		t.Fatalf("job_read_output error = %q, want grep limit", res.Output)
	}
}

func TestJobReadOutputRejectsJSONExpandedGrepBeforeRegistryTruncation(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// Use a manual job; fast small-output shell commands return ephemeral (no
	// job_id) under complete-or-handle, so the shell tool cannot reliably
	// produce a durable id here. The test is about grep validation, not shell.
	rec := newManualRunningJob(t, s)

	patternJSON, err := json.Marshal(strings.Repeat("\x00", maxJobGrepPatternJSONChars(jobToolResultDefaultMaxChar)/4))
	if err != nil {
		t.Fatalf("marshal grep pattern: %v", err)
	}
	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%s}`, rec.JobID, patternJSON)),
	})
	if !res.IsError {
		t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "grep is too large after JSON escaping") {
		t.Fatalf("job_read_output error = %q, want JSON escaping limit", res.Output)
	}
}

// TestJobReadOutputHeadBytesReadsFromStart verifies that head_lines reads from
// the beginning of retained output — the symmetric counterpart to tail_lines.
// A job whose head output was pushed out of the default tail window is only
// reachable by grep or by head_lines; this test closes that gap.
func TestJobReadOutputHeadLinesReadsFromStart(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'HEAD_MARKER_9\n'; yes filler-line | head -c 70000; printf '\nTAIL_MARKER_7\n'; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	// Wait until TAIL_MARKER_7 has been written (the whole HEAD..filler..TAIL run).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _, err := s.jobManager.readOutput(shellOut.JobID, jobLineReadBudget)
		if err == nil && strings.Contains(out, "TAIL_MARKER_7") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// (a) The default head+tail digest must contain BOTH ends + an elision marker.
	digRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read-digest",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, shellOut.JobID)),
	})
	if digRes.IsError {
		t.Fatalf("job_read_output (digest) returned error: %s", digRes.Output)
	}
	var digOut jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(digRes), &digOut); err != nil {
		t.Fatalf("unmarshal digest output: %v (output: %s)", err, digRes.Output)
	}
	if !strings.Contains(digOut.Content, "HEAD_MARKER_9") || !strings.Contains(digOut.Content, "TAIL_MARKER_7") {
		t.Fatalf("default digest must contain both head and tail markers; content: %q", digOut.Content)
	}
	if !strings.Contains(digOut.Content, "elided") {
		t.Fatalf("default digest must carry an elision marker; content: %q", digOut.Content)
	}

	// (b) head_lines:1024 read must contain HEAD_MARKER_9, not TAIL_MARKER_7, and be truncated.
	headRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read-head",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"head_lines":1024}`, shellOut.JobID)),
	})
	if headRes.IsError {
		t.Fatalf("job_read_output (head_lines) returned error: %s", headRes.Output)
	}
	var headOut jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(headRes), &headOut); err != nil {
		t.Fatalf("unmarshal head output: %v (output: %s)", err, headRes.Output)
	}
	if !strings.Contains(headOut.Content, "HEAD_MARKER_9") {
		t.Fatalf("head_lines read does not contain HEAD_MARKER_9; content: %q", headOut.Content)
	}
	if strings.Contains(headOut.Content, "TAIL_MARKER_7") {
		t.Fatalf("head_lines read unexpectedly contains TAIL_MARKER_7; content: %q", headOut.Content)
	}
	if !headOut.Truncated {
		t.Fatalf("head_lines read must report truncated=true (1024 < total output), got false")
	}
}

// TestJobReadOutputHeadAndTailMutuallyExclusive verifies that supplying both
// head_lines and tail_lines in the same call fails with invalid_request.

// TestJobReadOutputHeadAndTailMutuallyExclusive verifies that supplying both
// head_lines and tail_lines in the same call fails with invalid_request.
func TestJobReadOutputFromLineExclusiveWithHeadTail(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"from_line":5,"head_lines":3}`, rec.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_read_output with from_line+head_lines succeeded, want error; output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request") || !strings.Contains(res.Output, "from_line") {
		t.Fatalf("error = %q, want invalid_request mentioning from_line", res.Output)
	}
}

// TestJobReadOutputZeroHeadTailTreatedAsUnset verifies that head_lines:0 and/or
// tail_lines:0 are treated as unset (strict-zero rule), matching max_wait_ms
// behavior. Regression: gpt-5.5 sent both as 0 on every call, causing
// invalid_request loops.

// TestJobReadOutputZeroHeadTailTreatedAsUnset verifies that head_lines:0 and/or
// tail_lines:0 are treated as unset (strict-zero rule), matching max_wait_ms
// behavior. Regression: gpt-5.5 sent both as 0 on every call, causing
// invalid_request loops.
func TestJobReadOutputZeroHeadTailTreatedAsUnset(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'ZERO_RULE_MARKER\n'; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	waitForJobOutputWithGrep(t, s, shellOut.JobID, "ZERO_RULE_MARKER", "ZERO_RULE_MARKER")
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})
	waitForJobOutputContent(t, s, shellOut.JobID, "ZERO_RULE_MARKER")

	cases := []struct {
		name string
		args string
	}{
		{"both_zero", fmt.Sprintf(`{"job_id":%q,"head_lines":0,"tail_lines":0}`, shellOut.JobID)},
		{"tail_zero", fmt.Sprintf(`{"job_id":%q,"tail_lines":0}`, shellOut.JobID)},
		{"head_zero", fmt.Sprintf(`{"job_id":%q,"head_lines":0}`, shellOut.JobID)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
				ID:        "read-" + tc.name,
				Arguments: json.RawMessage(tc.args),
			})
			if res.IsError {
				t.Fatalf("job_read_output(%s) returned error: %s", tc.name, res.Output)
			}
			var out jobReadOutputTestResult
			if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
				t.Fatalf("unmarshal output: %v (output: %s)", err, res.Output)
			}
			if !strings.Contains(out.Content, "ZERO_RULE_MARKER") {
				t.Fatalf("job_read_output(%s) content missing ZERO_RULE_MARKER; content: %q", tc.name, out.Content)
			}
		})
	}
}

// TestJobReadOutputNegativeHeadBytesRejected verifies that head_lines:-1
// returns invalid_request with a non-negative message.
func TestJobReadOutputNegativeHeadBytesRejected(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"head_lines":-1}`, rec.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_read_output with head_lines:-1 succeeded, want error; output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request") {
		t.Fatalf("error = %q, want invalid_request", res.Output)
	}
	if !strings.Contains(res.Output, "non-negative") {
		t.Fatalf("error = %q, want mention of non-negative", res.Output)
	}
}

// TestDelegateToolParsesDelegationAllowance proves the grant knob is reachable
// from the model: delegation_allowance flows through the registered delegate
// tool's JSON boundary into the grant rule. A grant >= the caller's own
// allowance is rejected through the tool; a negative value is rejected as
// non-negative.

func TestJobReadOutputInvalidGrepCarriesPrefix(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	_, gerr := jobReadOutputTool(context.Background(), s, map[string]any{"job_id": rec.JobID, "grep": "["}, 1<<20)
	if gerr == nil || !strings.HasPrefix(gerr.Error(), "invalid_request:") {
		t.Fatalf("invalid grep error = %v, want invalid_request: prefix", gerr)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// toolResultJSON returns the structured JSON for a tool result: the StateResult
// ride-along (tool_state) when the tool sets one — the case for shell and the job
// tools, which now return plain text to the model and the struct as state — else
// the model-facing output (tools that still return JSON directly, e.g. delegate).
func toolResultJSON(res tooldefs.ExecResult) []byte {
	if len(res.ToolState) > 0 {
		return res.ToolState
	}
	return []byte(res.Output)
}

// handlerJSON returns the structured JSON from a tool handler called directly
// (not via the registry), whose return is now a tooldefs.StateResult: it marshals
// the State. Tools still returning a JSON string yield that string verbatim.
func handlerJSON(t *testing.T, v any) []byte {
	t.Helper()
	switch r := v.(type) {
	case tooldefs.StateResult:
		b, err := json.Marshal(r.State)
		if err != nil {
			t.Fatalf("marshal handler state: %v", err)
		}
		return b
	case string:
		return []byte(r)
	default:
		t.Fatalf("unexpected handler result type %T", v)
		return nil
	}
}

// TestJobReadOutputReportsStatus pins job_read_output legibility: a small read
// window of a fully-retained log reports output_status="windowed" with
// dropped_bytes=0, so the model knows the rest is reachable, not lost.
func TestJobReadOutputReportsStatus(t *testing.T) {
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

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
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

func TestJobListIncludesDelegatesRecoverySurface(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	res := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false, BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if call.IsError {
		t.Fatalf("job_list returned error: %s", call.Output)
	}
	var out struct {
		Delegates []struct {
			DelegateID   string `json:"delegate_id"`
			CurrentJobID string `json:"current_job_id"`
			LatestJobID  string `json:"latest_job_id"`
			Status       string `json:"status"`
			Resumable    bool   `json:"resumable"`
		} `json:"delegates"`
		Jobs []struct {
			JobID      string `json:"job_id"`
			DelegateID string `json:"delegate_id"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(toolResultJSON(call), &out); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(out.Delegates) != 1 || out.Delegates[0].DelegateID != res.DelegateID || out.Delegates[0].LatestJobID != res.JobID {
		t.Fatalf("delegates = %+v, want delegate recovery row", out.Delegates)
	}
	if len(out.Jobs) == 0 || out.Jobs[0].DelegateID != res.DelegateID {
		t.Fatalf("jobs = %+v, want job annotated with delegate_id", out.Jobs)
	}
}

func TestJobToolsRejectDelegateIDWithActionableGuidance(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	res := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false, BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	for _, tc := range []struct {
		name string
		tool string
		args string
		want string
	}{
		{"read", "job_read_output", fmt.Sprintf(`{"job_id":%q}`, res.DelegateID), "delegate_id is a conversation handle; read output from job_id"},
		{"stop", "job_stop", fmt.Sprintf(`{"job_id":%q}`, res.DelegateID), "delegate_id is a conversation handle; stop a concrete job_id"},
		{"watch", "job_watch", fmt.Sprintf(`{"operation":"create","target":%q,"events":["assistant.message"]}`, res.DelegateID), "delegate_id is not watchable; watch current_job_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        tc.name,
				Name:      tc.tool,
				Arguments: json.RawMessage(tc.args),
			})
			if !call.IsError {
				t.Fatalf("%s succeeded, want error: %s", tc.tool, call.Output)
			}
			if !strings.Contains(call.Output, tc.want) {
				t.Fatalf("%s error = %q, want %q", tc.tool, call.Output, tc.want)
			}
		})
	}
}

// TestJobReadOutputUnknownIDPointsToJobList pins the not-found recovery hint:
// when a job_id resolves to nothing (a guessed id, or a foreground command whose
// output rode inline and kept no durable job), the error must redirect the model
// to job_list rather than dead-ending on a bare "not found".
func TestJobReadOutputUnknownIDPointsToJobList(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
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
func TestJobReadOutputDefaultWindowIsBounded(t *testing.T) {
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

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
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
func TestJobReadOutputHeadAndTailTogether(t *testing.T) {
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
	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
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
func TestJobReadOutputFromLineMiddleSlice(t *testing.T) {
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
	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
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
func TestJobListRowIsLean(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(toolResultJSON(res), &out)
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(out.JobID)
		waitForShellDone(t, s.jobManager, out.JobID)
	})

	listRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "l1",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if listRes.IsError {
		t.Fatalf("job_list returned error: %s", listRes.Output)
	}
	// The model-facing job_list is plain text: a lean one-line row carrying no JSON
	// noise (no null fields, internal keys, or resumable/transcript clutter for an
	// ordinary running shell job).
	body := listRes.Output
	for _, banned := range []string{"transcript_ref", "resumable", "not_resumable_reason", "visible_to_session_id", "recent_watches", "null", "{"} {
		if strings.Contains(body, banned) {
			t.Errorf("lean job_list row must not contain %q:\n%s", banned, body)
		}
	}
	// A shell job's row identifies it by its command and reports its output size.
	if !strings.Contains(body, "sleep 30") {
		t.Errorf("shell job_list row must include its command:\n%s", body)
	}
	if !strings.Contains(body, "bytes") {
		t.Errorf("shell job_list row must report its output size:\n%s", body)
	}
	// The structured row (State) names the size field total_bytes everywhere the
	// agent reads it (shell result, job_read_output, job_list) — not output_bytes.
	state := string(toolResultJSON(listRes))
	if !strings.Contains(state, "total_bytes") || strings.Contains(state, "output_bytes") {
		t.Errorf("job_list state must use total_bytes, not output_bytes:\n%s", state)
	}
}

func TestJobToolsControlBackgroundShellJob(t *testing.T) {
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
		JobID  string `json:"job_id"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" || shellOut.Type != string(jobstore.JobShell) || shellOut.Status != string(jobstore.StatusRunning) {
		t.Fatalf("shell output = %+v, want running shell job", shellOut)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	listRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{"status":["running"],"type":["shell"],"limit":10}`),
	})
	if listRes.IsError {
		t.Fatalf("job_list returned error: %s", listRes.Output)
	}
	var listOut struct {
		Jobs []struct {
			JobID               string  `json:"job_id"`
			Type                string  `json:"type"`
			Status              string  `json:"status"`
			Reason              *string `json:"reason"`
			Description         string  `json:"description"`
			ParentJobID         *string `json:"parent_job_id"`
			OwnerSessionID      string  `json:"owner_session_id"`
			VisibleToSessionID  string  `json:"visible_to_session_id"`
			TranscriptRef       *string `json:"transcript_ref"`
			Resumable           *bool   `json:"resumable"`
			NotResumableReason  *string `json:"not_resumable_reason"`
			StartedAt           string  `json:"started_at"`
			EndedAt             *string `json:"ended_at"`
			ExitCode            *int    `json:"exit_code"`
			TotalBytes          int64   `json:"total_bytes"`
			TerminalGeneration  *string `json:"terminal_generation"`
			TerminalNotifyState string  `json:"terminal_notification_state"`
		} `json:"jobs"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(toolResultJSON(listRes), &listOut); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, listRes.Output)
	}
	if listOut.Count != len(listOut.Jobs) {
		t.Fatalf("job_list output = %+v, want count=len(jobs)", listOut)
	}
	if len(listOut.Jobs) != 1 {
		t.Fatalf("job_list jobs = %+v, want one running shell job", listOut.Jobs)
	}
	job := listOut.Jobs[0]
	if job.JobID != shellOut.JobID ||
		job.Type != string(jobstore.JobShell) ||
		job.Status != string(jobstore.StatusRunning) ||
		job.Reason != nil ||
		job.ParentJobID != nil ||
		job.OwnerSessionID == "" ||
		job.StartedAt == "" ||
		job.EndedAt != nil ||
		job.ExitCode != nil {
		t.Fatalf("job_list job = %+v, want running shell projection for %s", job, shellOut.JobID)
	}

	readOut := waitForJobOutput(t, s, shellOut.JobID, "ready-line")
	if readOut.JobID != shellOut.JobID ||
		readOut.Type != string(jobstore.JobShell) ||
		readOut.Status != string(jobstore.StatusRunning) ||
		readOut.Reason != nil ||
		!strings.Contains(readOut.Content, "ready-line") ||
		readOut.TotalBytes == 0 ||
		readOut.Truncated ||
		readOut.ExitCode != nil ||
		readOut.Grep != "ready" ||
		len(readOut.Matches) != 1 ||
		!strings.Contains(readOut.Matches[0].Line, "ready-line") ||
		readOut.Matches[0].ByteOffset == nil {
		t.Fatalf("job_read_output = %+v, want running shell output with grep match", readOut)
	}

	stopRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":1000}`, shellOut.JobID)),
	})
	if stopRes.IsError {
		t.Fatalf("job_stop returned error: %s", stopRes.Output)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(toolResultJSON(stopRes), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, stopRes.Output)
	}
	if stopOut.JobID != shellOut.JobID || stopOut.Status != string(jobstore.StatusCancelled) || stopOut.Reason == nil || *stopOut.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want cancelled/stopped_by_parent", stopOut)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
	rec := loadShellRecord(t, s.jobManager, shellOut.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("durable job after stop = %+v, want cancelled/stopped_by_parent", rec)
	}
}

func TestJobListToolIncludeNestedSurfacesForwardedRecords(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	child.jobManager.forward = parent.jobManager.forwardEvent
	child.jobManager.parentJobID = "job_PARENTDELEGATE"

	parentRec, err := parent.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "parent"})
	if err != nil {
		t.Fatalf("create parent shell: %v", err)
	}
	childRec, err := child.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, parent.jobManager, parentRec.JobID)
		finishRunningTestJob(t, child.jobManager, childRec.JobID)
	})

	runList := func(args string) jobListToolOutput {
		t.Helper()
		res := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
			ID:        "list",
			Name:      "job_list",
			Arguments: json.RawMessage(args),
		})
		if res.IsError {
			t.Fatalf("job_list returned error: %s", res.Output)
		}
		var out jobListToolOutput
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
		}
		return out
	}

	defaultOut := runList(`{}`)
	if jobListToolOutputContains(defaultOut.Jobs, childRec.JobID) {
		t.Fatalf("default job_list jobs = %+v, want nested job hidden", defaultOut.Jobs)
	}
	if !jobListToolOutputContains(defaultOut.Jobs, parentRec.JobID) {
		t.Fatalf("default job_list jobs = %+v, want parent job %q", defaultOut.Jobs, parentRec.JobID)
	}

	nestedOut := runList(`{"include_nested":true}`)
	nestedJob := findJobListToolOutput(nestedOut.Jobs, childRec.JobID)
	if nestedJob == nil {
		t.Fatalf("include_nested job_list jobs = %+v, want nested job %q", nestedOut.Jobs, childRec.JobID)
	}
	if nestedJob.ParentJobID == nil || *nestedJob.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("nested job parent_job_id = %v, want job_PARENTDELEGATE", nestedJob.ParentJobID)
	}
}

// TestJobListDefaultListingOmitsDepth proves the default listing does not
// serialize the depth field: projectJobRecord never sets Depth for default rows
// (only walkDescendantJobs does for include_descendants), so a zero Depth must be
// omitted, not emitted as "depth":0.
func TestJobListDefaultListingOmitsDepth(t *testing.T) {
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "job"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	out, err := jobListTool(s, decodeJobListArgs(t, `{}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	if strings.Contains(string(handlerJSON(t, out)), `"depth"`) {
		t.Fatalf("default job_list output contains depth key: %s", out)
	}
}

// jobListDescendantEntry parses the descendant-walk row fields: the existing
// owner_session_id plus the new depth annotation, and the resumability
// projection (which must key on the owner session, not the root caller).
type jobListDescendantEntry struct {
	JobID              string  `json:"job_id"`
	Status             string  `json:"status"`
	OwnerSessionID     string  `json:"owner_session_id"`
	Depth              int     `json:"depth"`
	Resumable          *bool   `json:"resumable"`
	NotResumableReason *string `json:"not_resumable_reason"`
}

type jobListDescendantOutput struct {
	Jobs  []jobListDescendantEntry `json:"jobs"`
	Count int                      `json:"count"`
}

func findDescendantRow(rows []jobListDescendantEntry, jobID string) *jobListDescendantEntry {
	for i := range rows {
		if rows[i].JobID == jobID {
			return &rows[i]
		}
	}
	return nil
}

// newWalkJobManager builds an isolated jobManager for a single tree level.
func newWalkJobManager(t *testing.T, sessionID string) *jobManager {
	t.Helper()
	jm, err := newJobManager(t.TempDir(), sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new jobManager %q: %v", sessionID, err)
	}
	return jm
}

// TestJobListIncludeDescendantsWalksLiveTree drives a depth-3 live tree
// (root -> coordinator -> worker) plus a dead coordinator branch, then asserts
// job_list(include_descendants=true) returns one row per job at its real owner
// depth, suppresses forwarded copies whose owner appears live, surfaces only the
// terminal forwarded copy for a dead coordinator (no recursion into the gone
// session), and leaves default + include_nested semantics unchanged.
func TestJobListIncludeDescendantsWalksLiveTree(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	deadJM := newWalkJobManager(t, "DEAD")
	deadChildJM := newWalkJobManager(t, "DEADCHILD")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
		_ = deadJM.store.Close()
		_ = deadChildJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"
	deadJM.forward = rootJM.forwardEvent
	deadJM.parentJobID = "job_root_delegate_dead"
	deadChildJM.forward = deadJM.forwardEvent
	deadChildJM.parentJobID = "job_dead_delegate_child"

	// Owner records, each forwarded one hop into its parent's store.
	rootRec, err := rootJM.createShell(createShellOpts{Command: "sleep 1", Description: "root job"})
	if err != nil {
		t.Fatalf("create root shell: %v", err)
	}
	coordRec, err := coordJM.createShell(createShellOpts{Command: "sleep 1", Description: "coordinator job"})
	if err != nil {
		t.Fatalf("create coordinator shell: %v", err)
	}
	workerRec, err := workerJM.createShell(createShellOpts{Command: "sleep 1", Description: "worker job"})
	if err != nil {
		t.Fatalf("create worker shell: %v", err)
	}
	deadRec, err := deadJM.createShell(createShellOpts{Command: "sleep 1", Description: "dead coordinator job"})
	if err != nil {
		t.Fatalf("create dead coordinator shell: %v", err)
	}
	// A job owned by the dead coordinator's own child, forwarded only one hop
	// into the dead coordinator's store. To surface it the walk would have to
	// recurse INTO the dead (closed) coordinator; its absence proves live-only
	// recursion ("resume it to dig deeper").
	deadGrandRec, err := deadChildJM.createShell(createShellOpts{Command: "sleep 1", Description: "dead grandchild job"})
	if err != nil {
		t.Fatalf("create dead grandchild shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, rootJM, rootRec.JobID)
		finishRunningTestJob(t, coordJM, coordRec.JobID)
		finishRunningTestJob(t, workerJM, workerRec.JobID)
		finishRunningTestJob(t, deadChildJM, deadGrandRec.JobID)
	})

	// The dead coordinator finalized its forwarded job before dying; the terminal
	// forwarded copy survives in the root's store.
	deadExit := 0
	if err := deadJM.finalize(deadRec.JobID, jobstore.StatusCompleted, "exit_zero", &deadExit); err != nil {
		t.Fatalf("finalize dead coordinator job: %v", err)
	}

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	dead := &Session{id: "DEAD", jobManager: deadJM, subagents: newSubagentManager(nil)}

	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})
	root.subagents.track(&subagent{id: "DEAD", sess: dead, status: SubagentCompleted, closed: true})

	run := func(args string) jobListDescendantOutput {
		t.Helper()
		out, err := jobListTool(root, decodeJobListArgs(t, args), 1<<20)
		if err != nil {
			t.Fatalf("jobListTool(%s): %v", args, err)
		}
		var parsed jobListDescendantOutput
		if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
			t.Fatalf("unmarshal job_list output: %v (output: %s)", err, out)
		}
		return parsed
	}

	descendants := run(`{"include_descendants":true}`)

	// Each owner job appears exactly once, at its real owner depth.
	rootRow := findDescendantRow(descendants.Jobs, rootRec.JobID)
	if rootRow == nil || rootRow.OwnerSessionID != "ROOT" || rootRow.Depth != 0 {
		t.Fatalf("root row = %+v, want owner=ROOT depth=0", rootRow)
	}
	coordRow := findDescendantRow(descendants.Jobs, coordRec.JobID)
	if coordRow == nil || coordRow.OwnerSessionID != "COORD" || coordRow.Depth != 1 {
		t.Fatalf("coordinator row = %+v, want owner=COORD depth=1", coordRow)
	}
	workerRow := findDescendantRow(descendants.Jobs, workerRec.JobID)
	if workerRow == nil || workerRow.OwnerSessionID != "WORK" || workerRow.Depth != 2 {
		t.Fatalf("worker row = %+v, want owner=WORK depth=2", workerRow)
	}

	// Dedupe: exactly one row per job_id (forwarded copies of live-owner jobs
	// are suppressed in favor of the owner record found by recursion).
	for _, jobID := range []string{rootRec.JobID, coordRec.JobID, workerRec.JobID} {
		count := 0
		for _, row := range descendants.Jobs {
			if row.JobID == jobID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("job_id %q appears %d times, want exactly 1: %+v", jobID, count, descendants.Jobs)
		}
	}

	// Dead coordinator: only the terminal forwarded copy surfaces (from the root
	// store, depth 0). No recursion into the gone session.
	deadRow := findDescendantRow(descendants.Jobs, deadRec.JobID)
	if deadRow == nil || deadRow.OwnerSessionID != "DEAD" || deadRow.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("dead coordinator row = %+v, want owner=DEAD completed terminal copy", deadRow)
	}
	if findDescendantRow(descendants.Jobs, deadGrandRec.JobID) != nil {
		t.Fatalf("dead grandchild job %q leaked into walk: %+v", deadGrandRec.JobID, descendants.Jobs)
	}

	// Default job_list: own jobs only, forwarded copies hidden, no descendants.
	defaultOut := run(`{}`)
	if findDescendantRow(defaultOut.Jobs, rootRec.JobID) == nil {
		t.Fatalf("default job_list = %+v, want root job", defaultOut.Jobs)
	}
	for _, hidden := range []string{coordRec.JobID, workerRec.JobID} {
		if findDescendantRow(defaultOut.Jobs, hidden) != nil {
			t.Fatalf("default job_list leaked nested job %q: %+v", hidden, defaultOut.Jobs)
		}
	}

	// include_nested: one hop only — root's own forwarded copies (coordinator,
	// dead coordinator) are visible; the worker (two hops) is not.
	nestedOut := run(`{"include_nested":true}`)
	if findDescendantRow(nestedOut.Jobs, coordRec.JobID) == nil {
		t.Fatalf("include_nested job_list = %+v, want forwarded coordinator job", nestedOut.Jobs)
	}
	if findDescendantRow(nestedOut.Jobs, workerRec.JobID) != nil {
		t.Fatalf("include_nested job_list leaked two-hop worker job %q: %+v", workerRec.JobID, nestedOut.Jobs)
	}
}

// TestJobListIncludeDescendantsSurfacesOwnStoreError proves the depth-0 (own
// store) error is surfaced, not swallowed: a closed own store makes both plain
// job_list and job_list(include_descendants=true) return the same ErrStoreClosed.
// Before the fix the descendant walk swallowed the depth-0 error and returned an
// empty list with success — a silent regression from the plain path.
func TestJobListIncludeDescendantsSurfacesOwnStoreError(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}

	// Close the depth-0 (own) store before the call so both paths hit
	// ErrStoreClosed from the same store.
	if err := rootJM.store.Close(); err != nil {
		t.Fatalf("close root store: %v", err)
	}

	// Parity: plain job_list surfaces the closed-store error today.
	if _, plainErr := jobListTool(root, decodeJobListArgs(t, `{}`), 1<<20); !errors.Is(plainErr, jobstore.ErrStoreClosed) {
		t.Fatalf("plain job_list error = %v, want ErrStoreClosed", plainErr)
	}

	_, descErr := jobListTool(root, decodeJobListArgs(t, `{"include_descendants":true}`), 1<<20)
	if descErr == nil {
		t.Fatalf("job_list(include_descendants=true) error = nil, want ErrStoreClosed surfaced from the own store")
	}
	if !errors.Is(descErr, jobstore.ErrStoreClosed) {
		t.Fatalf("job_list(include_descendants=true) error = %v, want ErrStoreClosed", descErr)
	}
}

// TestJobListIncludeDescendantsProjectsRuntimeLostViaOwner proves the
// include_descendants walk projects each descendant row against its OWNER
// session, not the root caller. A worker-owned runtime_lost delegate's
// resumability is assessed by assessDelegateResumability, which gates on the
// assessing session's identity (descriptor.ParentSessionID == session ID).
// Projecting against the root mis-reads that gate (parent_linkage_unavailable);
// the owner projection clears it and reports a different, downstream reason. The
// list row must match the owner projection.
func TestJobListIncludeDescendantsProjectsRuntimeLostViaOwner(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"

	// A worker-owned runtime_lost delegate (descriptor.ParentSessionID == "WORK"),
	// forwarded one hop up into the coordinator's store.
	delegRec := workerOwnedRuntimeLostDelegate(t, workerJM, "WORK")

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	// Oracle: projecting against the true owner (worker) clears the parent-linkage
	// gate; projecting against the root does not.
	viaOwner := projectJobRecord(worker, delegRec)
	viaRoot := projectJobRecord(root, delegRec)
	if viaRoot.NotResumableReason == nil || *viaRoot.NotResumableReason != notResumableParentLinkageUnavailable {
		t.Fatalf("root projection reason = %v, want %q (mis-projection oracle)", viaRoot.NotResumableReason, notResumableParentLinkageUnavailable)
	}
	if viaOwner.NotResumableReason != nil && *viaOwner.NotResumableReason == notResumableParentLinkageUnavailable {
		t.Fatalf("owner projection reason = %q, want owner to clear the parent-linkage gate", *viaOwner.NotResumableReason)
	}

	out, err := jobListTool(root, decodeJobListArgs(t, `{"include_descendants":true}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(include_descendants): %v", err)
	}
	var parsed jobListDescendantOutput
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, out)
	}

	row := findDescendantRow(parsed.Jobs, delegRec.JobID)
	if row == nil {
		t.Fatalf("worker runtime_lost delegate %q missing from descendant walk: %+v", delegRec.JobID, parsed.Jobs)
	}
	// The row must match the OWNER projection, NOT the root mis-projection.
	if row.NotResumableReason != nil && *row.NotResumableReason == notResumableParentLinkageUnavailable {
		t.Fatalf("list row reason = %q, want owner projection (root mis-projection leaked)", *row.NotResumableReason)
	}
	if !stringPtrEqual(row.NotResumableReason, viaOwner.NotResumableReason) {
		t.Fatalf("list row not_resumable_reason = %v, want owner projection %v", row.NotResumableReason, viaOwner.NotResumableReason)
	}
	if !boolPtrEqual(row.Resumable, viaOwner.Resumable) {
		t.Fatalf("list row resumable = %v, want owner projection %v", row.Resumable, viaOwner.Resumable)
	}
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func decodeJobListArgs(t *testing.T, args string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("decode job_list args %s: %v", args, err)
	}
	return decoded
}

func runJobListTool(t *testing.T, s *Session) jobListToolOutput {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("job_list returned error: %s", res.Output)
	}
	var out jobListToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
	}
	return out
}

func TestJobListWatchesOmittedWhenNoneConfigured(t *testing.T) {
	s := newTestSession(t)
	out := runJobListTool(t, s)
	if len(out.Watches) != 0 {
		t.Fatalf("job_list watches = %+v, want omitted when none configured", out.Watches)
	}
	// The empty watches array is omitted from the wire entirely (lean scan), not
	// serialized as `"watches":[]`.
	raw := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "lraw", Name: "job_list", Arguments: json.RawMessage(`{}`),
	})
	if strings.Contains(raw.Output, "\"watches\"") {
		t.Fatalf("job_list must omit the empty watches key:\n%s", raw.Output)
	}
}

func TestJobListEnumeratesActiveWatches(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30", Description: "watched shell"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("configure output_match watch: %v", err)
	}
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	if _, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "fyi"},
	}); err != nil {
		t.Fatalf("configure event watch with send: %v", err)
	}

	out := runJobListTool(t, s)
	if len(out.Watches) != 2 {
		t.Fatalf("job_list watches = %+v, want 2 entries", out.Watches)
	}

	notify := findJobListToolWatch(out.Watches, rec.JobID, "")
	if notify == nil {
		t.Fatalf("job_list watches = %+v, want notify-caller output_match watch", out.Watches)
	}
	if notify.Condition != "output_match: ready" {
		t.Fatalf("notify watch condition = %q, want %q", notify.Condition, "output_match: ready")
	}
	if notify.SendTo != "" {
		t.Fatalf("notify watch send_to = %q, want empty", notify.SendTo)
	}
	if notify.CreatedAt == "" {
		t.Fatalf("notify watch created_at must be populated, got empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, notify.CreatedAt); err != nil {
		t.Fatalf("notify watch created_at = %q, not RFC3339Nano: %v", notify.CreatedAt, err)
	}

	sidecar := findJobListToolWatch(out.Watches, rec.JobID, "dlg_obs")
	if sidecar == nil {
		t.Fatalf("job_list watches = %+v, want sidecar event watch", out.Watches)
	}
	if sidecar.SendTo != "dlg_obs" {
		t.Fatalf("sidecar watch send_to = %q, want dlg_obs", sidecar.SendTo)
	}
	if sidecar.Condition != "events: [job.notification]" {
		t.Fatalf("sidecar watch condition = %q, want %q", sidecar.Condition, "events: [job.notification]")
	}
}

func TestJobListWatchConditionSummaryFormats(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	jm.enqueue = func(jobNotification) {}

	// progress watch on a running shell.
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: 2000}); err != nil {
		t.Fatalf("configure progress watch: %v", err)
	}

	// every-Nth event watch with a send (legal: not a self-delivery back to caller).
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	if _, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"assistant.message"},
		Every:  5,
		Send:   &watchSendArgs{To: "dlg_obs"},
	}); err != nil {
		t.Fatalf("configure every-N event watch: %v", err)
	}

	out := runJobListTool(t, s)

	progress := findJobListToolWatch(out.Watches, rec.JobID, "")
	if progress == nil {
		t.Fatalf("watches = %+v, want progress watch", out.Watches)
	}
	if progress.Condition != "progress_interval_ms: 2000" {
		t.Fatalf("progress condition = %q, want %q", progress.Condition, "progress_interval_ms: 2000")
	}

	every := findJobListToolWatch(out.Watches, rec.JobID, "dlg_obs")
	if every == nil {
		t.Fatalf("watches = %+v, want every-N watch", out.Watches)
	}
	if every.Condition != "events: [assistant.message] every 5" {
		t.Fatalf("every-N condition = %q, want %q", every.Condition, "events: [assistant.message] every 5")
	}
}

func TestJobListWatchReflectsDeliveries(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	jm.enqueue = func(jobNotification) {}

	// A no-send caller event watch counts one delivery per fired event.
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	for i := 0; i < 3; i++ {
		onSessionEventKD(jm, events.EventAssistantTextEnd, nil)
	}

	out := runJobListTool(t, s)
	w := findJobListToolWatch(out.Watches, "caller", "")
	if w == nil {
		t.Fatalf("watches = %+v, want caller watch", out.Watches)
	}
	if w.Deliveries != 3 {
		t.Fatalf("caller watch deliveries = %d, want 3", w.Deliveries)
	}
}

func TestJobListExcludesTerminalFlushWatches(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager

	// A config that lives ONLY in terminalFlush (with a pending send) must not be
	// enumerated: F2 reads jm.watches exclusively.
	flushCfg := &watchConfig{target: "job_GONE", send: &watchSendArgs{To: "dlg_obs"}}
	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[flushCfg] = true
	jm.mu.Unlock()

	out := runJobListTool(t, s)
	if findJobListToolWatch(out.Watches, "job_GONE", "dlg_obs") != nil {
		t.Fatalf("terminal-flush watch leaked into job_list watches: %+v", out.Watches)
	}
	if len(out.Watches) != 0 {
		t.Fatalf("watches = %+v, want only live watches (none)", out.Watches)
	}
}

func TestDefJobListDescriptionMentionsActiveWatches(t *testing.T) {
	desc := tooldefs.DefJobList().Description
	if !strings.Contains(desc, "The result also includes your active watches.") {
		t.Fatalf("DefJobList description = %q, want it to mention active watches", desc)
	}
}

func TestJobListStoppedDelegateResumableAssessmentIsDynamicAndPure(t *testing.T) {
	cases := []struct {
		name       string
		breakState func(*testing.T, *Session, *jobstore.JobRecord)
		wantReason string
	}{
		{
			name: "resumable",
		},
		{
			name: "missing descriptor",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore = nil
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "missing_delegate_resume_metadata",
		},
		{
			name: "bad linkage",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ParentSessionID = "other"
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "missing local env policy",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.LocalEnvPolicy = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "invalid local env policy",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.LocalEnvPolicy = "all-ish"
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "missing working dir",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.WorkingDir = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "missing meta",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				removeChildSessionMeta(t, s, rec)
			},
			wantReason: "missing_child_session_meta",
		},
		{
			name: "corrupt meta",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildSessionMeta(t, s, rec, []byte(`{`))
			},
			wantReason: "corrupt_child_session_meta",
		},
		{
			name: "wrong meta id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
				if err != nil {
					t.Fatalf("load child meta: %v", err)
				}
				meta.ID = "other-child"
				data, err := json.Marshal(meta)
				if err != nil {
					t.Fatalf("marshal child meta: %v", err)
				}
				writeChildSessionMeta(t, s, rec, data)
			},
			wantReason: "corrupt_child_session_meta",
		},
		{
			name: "empty meta id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
				if err != nil {
					t.Fatalf("load child meta: %v", err)
				}
				meta.ID = ""
				data, err := json.Marshal(meta)
				if err != nil {
					t.Fatalf("marshal child meta: %v", err)
				}
				writeChildSessionMeta(t, s, rec, data)
			},
			wantReason: "corrupt_child_session_meta",
		},
		{
			name: "missing transcript",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				removeChildTranscript(t, s, rec)
			},
			wantReason: "missing_child_transcript",
		},
		{
			name: "corrupt transcript",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				appendChildTranscript(t, s, rec, "\n{not-json}\n")
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "corrupt transcript misleading kind",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				appendChildTranscript(t, s, rec, "\n{\"kind\":\"transcript_session_mismatch\"}\n")
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "oversized transcript line",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				restoreLimit := setStrictTranscriptMaxLineBytesForTest(512)
				t.Cleanup(restoreLimit)
				appendChildTranscript(t, s, rec, "\n"+strings.Repeat("x", 513)+"\n")
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "session mismatch",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildTranscript(t, s, rec, []byte(`{"kind":"header","format_version":1,"session_id":"other"}`+"\n"))
			},
			wantReason: "transcript_session_mismatch",
		},
		{
			name: "corrupt transcript header shape",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildTranscript(t, s, rec, []byte(fmt.Sprintf(`{"session_id":%q}`+"\n", rec.DelegateRestore.ChildSessionID)))
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "busy child",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				s.subagents.track(&subagent{
					id:      rec.DelegateRestore.ChildSessionID,
					sess:    newTestSession(t),
					running: true,
					done:    make(chan struct{}),
				})
			},
			wantReason: "child_session_busy",
		},
		{
			name: "profile unavailable",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
				if err != nil {
					t.Fatalf("load child meta: %v", err)
				}
				meta.Model = "missing/gpt-5.2"
				if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
					t.Fatalf("save child meta: %v", err)
				}
				s.resolveProfile = func(ref string) (*provider.Profile, error) {
					return nil, fmt.Errorf("no profile for %s", ref)
				}
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor profile unavailable while meta model valid",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = "openai"
				rec.DelegateRestore.ResolvedModel = "stale-model"
				replaceStoredDelegateRecord(t, s, rec)
				s.resolveProfile = func(ref string) (*provider.Profile, error) {
					return nil, fmt.Errorf("no profile for %s", ref)
				}
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor profile id without model",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = "openai"
				rec.DelegateRestore.ResolvedModel = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor model without profile id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = ""
				rec.DelegateRestore.ResolvedModel = "gpt-5.2"
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor missing resolved profile fields",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = ""
				rec.DelegateRestore.ResolvedModel = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "profile_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai"})
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			if tc.breakState != nil {
				tc.breakState(t, s, rec)
			}
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))

			raw, err := jobListTool(s, map[string]any{"type": []any{"delegate"}}, jobToolResultDefaultMaxChar)
			if err != nil {
				t.Fatalf("jobListTool: %v", err)
			}
			if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeEvents {
				t.Fatalf("jobstore event count = %d, want unchanged %d", got, beforeEvents)
			}
			var out jobListToolOutput
			if err := json.Unmarshal(handlerJSON(t, raw), &out); err != nil {
				t.Fatalf("unmarshal job_list: %v (output: %s)", err, raw)
			}
			listed := findJobListToolOutput(out.Jobs, rec.JobID)
			if listed == nil {
				t.Fatalf("job_list jobs = %+v, want %s", out.Jobs, rec.JobID)
			}
			if tc.wantReason == "" {
				if listed.Resumable == nil || !*listed.Resumable || listed.NotResumableReason != nil {
					t.Fatalf("listed job = %+v, want resumable with no reason", listed)
				}
				return
			}
			if listed.Resumable == nil || *listed.Resumable {
				t.Fatalf("listed job = %+v, want not resumable", listed)
			}
			if listed.NotResumableReason == nil || *listed.NotResumableReason != tc.wantReason {
				t.Fatalf("not_resumable_reason = %v, want %s", listed.NotResumableReason, tc.wantReason)
			}
		})
	}
}

func TestJobListStoppedDelegateResumabilityDoesNotBuildResumeHistory(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	original := delegateRestoreResumeHistory
	delegateRestoreResumeHistory = func(entries []transcript.Entry) []schema.Turn {
		t.Fatalf("job_list built resume history from %d entries", len(entries))
		return nil
	}
	defer func() { delegateRestoreResumeHistory = original }()

	raw, err := jobListTool(s, map[string]any{"type": []any{"delegate"}}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var out jobListToolOutput
	if err := json.Unmarshal(handlerJSON(t, raw), &out); err != nil {
		t.Fatalf("unmarshal job_list: %v (output: %s)", err, raw)
	}
	listed := findJobListToolOutput(out.Jobs, rec.JobID)
	if listed == nil {
		t.Fatalf("job_list jobs = %+v, want %s", out.Jobs, rec.JobID)
	}
	if listed.Resumable == nil || !*listed.Resumable || listed.NotResumableReason != nil {
		t.Fatalf("listed job = %+v, want resumable without building history", listed)
	}
}

func TestJobWatchToolConfiguresWatch(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":"(?i)ready"}`, shellOut.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_watch returned error: %s", res.Output)
	}
	watchJSON := string(toolResultJSON(res))
	if !strings.Contains(watchJSON, `"watching":true`) {
		t.Fatalf("job_watch state = %s, want watching true", watchJSON)
	}
	// The contract's install example shows replaced_existing explicitly false,
	// not omitted (docs/job-control.md § job_watch result).
	if !strings.Contains(watchJSON, `"replaced_existing":false`) {
		t.Fatalf("job_watch state = %s, want explicit replaced_existing:false", watchJSON)
	}
	if s.jobManager.watchCount() != 1 {
		t.Fatalf("watch count = %d, want 1", s.jobManager.watchCount())
	}
}

func TestJobWatchCreateReturnsIDAndClearUsesIDOnly(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	createRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":"ready"}`, shellOut.JobID)),
	})
	if createRes.IsError {
		t.Fatalf("job_watch create returned error: %s", createRes.Output)
	}
	var created struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(createRes), &created); err != nil {
		t.Fatalf("unmarshal create watch output: %v (output: %s)", err, createRes.Output)
	}
	if !strings.HasPrefix(created.WatchID, "watch_") {
		t.Fatalf("watch_id = %q, want watch_ prefix", created.WatchID)
	}
	if !created.Watching {
		t.Fatalf("create result = %+v, want watching=true", created)
	}

	clearRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "clear",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"clear","watch_id":%q}`, created.WatchID)),
	})
	if clearRes.IsError {
		t.Fatalf("job_watch clear returned error: %s", clearRes.Output)
	}
	var cleared struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(clearRes), &cleared); err != nil {
		t.Fatalf("unmarshal clear watch output: %v (output: %s)", err, clearRes.Output)
	}
	if cleared.WatchID != created.WatchID {
		t.Fatalf("cleared watch_id = %q, want %q", cleared.WatchID, created.WatchID)
	}
	if cleared.Watching {
		t.Fatalf("clear result = %+v, want watching=false", cleared)
	}
	watches, err := s.jobManager.store.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if w := watches[created.WatchID]; w == nil || w.Active || w.EndReason != "cleared" {
		t.Fatalf("durable watch %s = %+v, want inactive cleared", created.WatchID, w)
	}

	staleClearRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "clear-again",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"clear","watch_id":%q}`, created.WatchID)),
	})
	if staleClearRes.IsError {
		t.Fatalf("stale job_watch clear returned error: %s", staleClearRes.Output)
	}
	if !strings.Contains(staleClearRes.Output, created.WatchID) || strings.Contains(staleClearRes.Output, "watch on  cleared") {
		t.Fatalf("stale clear output = %q, want watch_id and no empty target footer", staleClearRes.Output)
	}
}

func TestJobWatchRejectsRemovedPublicShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{name: "missing operation", args: `{"target":"caller","events":["job.notification"]}`, want: "missing properties: 'operation'"},
		{name: "unsupported operation", args: `{"operation":"pause","target":"caller","events":["job.notification"]}`, want: "value must be one of"},
		{name: "create without target", args: `{"operation":"create","events":["job.notification"]}`, want: "target is required"},
		{name: "inspect without watch id", args: `{"operation":"inspect"}`, want: "watch_id is required"},
		{name: "clear without watch id", args: `{"operation":"clear"}`, want: "watch_id is required"},
		{name: "target wildcard", args: `{"operation":"create","target":"*","events":["job.notification"]}`, want: "wildcard watch target is not supported"},
		{name: "send to job id", args: `{"operation":"create","target":"caller","events":["job.notification"],"send":{"to":"job_observer","message":"observe"}}`, want: "job_id is a job/turn handle"},
		{name: "send to watched", args: `{"operation":"create","target":"caller","events":["job.notification"],"send":{"to":"watched","message":"observe"}}`, want: "watched is not a v1 delivery target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("job_watch succeeded, want error containing %q: %s", tc.want, res.Output)
			}
			if !strings.Contains(res.Output, tc.want) {
				t.Fatalf("job_watch error = %q, want %q", res.Output, tc.want)
			}
			if s.jobManager.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
			}
		})
	}
}

func TestJobWatchDuplicateCreateReturnsSameIDAndChangedConfigReturnsNewID(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	createWatch := func(outputMatch string) string {
		t.Helper()
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "watch-" + outputMatch,
			Name:      "job_watch",
			Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":%q}`, shellOut.JobID, outputMatch)),
		})
		if res.IsError {
			t.Fatalf("job_watch create %q returned error: %s", outputMatch, res.Output)
		}
		var out struct {
			WatchID string `json:"watch_id"`
		}
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal watch output: %v (output: %s)", err, res.Output)
		}
		if !strings.HasPrefix(out.WatchID, "watch_") {
			t.Fatalf("watch_id = %q, want watch_ prefix", out.WatchID)
		}
		return out.WatchID
	}

	firstID := createWatch("ready")
	duplicateID := createWatch("ready")
	if duplicateID != firstID {
		t.Fatalf("duplicate create watch_id = %q, want %q", duplicateID, firstID)
	}

	changedID := createWatch("done")
	if changedID == firstID {
		t.Fatalf("changed config reused watch_id %q", changedID)
	}
}

func TestJobWatchListAndInspectReturnWatchIDs(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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

	createRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":"ready"}`, shellOut.JobID)),
	})
	if createRes.IsError {
		t.Fatalf("job_watch create returned error: %s", createRes.Output)
	}
	var created struct {
		WatchID string `json:"watch_id"`
	}
	if err := json.Unmarshal(toolResultJSON(createRes), &created); err != nil {
		t.Fatalf("unmarshal create watch output: %v (output: %s)", err, createRes.Output)
	}

	listRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"list"}`),
	})
	if listRes.IsError {
		t.Fatalf("job_watch list returned error: %s", listRes.Output)
	}
	var listed struct {
		Watches []struct {
			WatchID  string `json:"watch_id"`
			Target   string `json:"target"`
			Watching bool   `json:"watching"`
		} `json:"watches"`
	}
	if err := json.Unmarshal(toolResultJSON(listRes), &listed); err != nil {
		t.Fatalf("unmarshal list watch output: %v (output: %s)", err, listRes.Output)
	}
	if len(listed.Watches) != 1 || listed.Watches[0].WatchID != created.WatchID || listed.Watches[0].Target != shellOut.JobID || !listed.Watches[0].Watching {
		t.Fatalf("list result = %+v, want active watch %s", listed, created.WatchID)
	}

	inspectRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "inspect",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"inspect","watch_id":%q}`, created.WatchID)),
	})
	if inspectRes.IsError {
		t.Fatalf("job_watch inspect returned error: %s", inspectRes.Output)
	}
	var inspected struct {
		WatchID  string `json:"watch_id"`
		Target   string `json:"target"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(inspectRes), &inspected); err != nil {
		t.Fatalf("unmarshal inspect watch output: %v (output: %s)", err, inspectRes.Output)
	}
	if inspected.WatchID != created.WatchID || inspected.Target != shellOut.JobID || !inspected.Watching {
		t.Fatalf("inspect result = %+v, want active watch %s", inspected, created.WatchID)
	}
}

func TestJobWatchCanImmediatelyWatchReturnedBackgroundShellJob(t *testing.T) {
	s := newPersistentTestSession(t)
	const token = "WATCH_OUTPUT_TOKEN_ONCE"

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 1; echo 'WATCH_OUTPUT_TOKEN_ONCE'; sleep 1","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	watchArgs, err := json.Marshal(map[string]any{
		"operation":    "create",
		"target":       shellOut.JobID,
		"output_match": token,
		"send": map[string]any{
			"to":              "caller",
			"message":         "output_match watch fired",
			"include_excerpt": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: watchArgs,
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error for returned job_id %s: %s", shellOut.JobID, watchRes.Output)
	}

	// The shell prints the token asynchronously; observation enqueues a caller
	// wake token on the notification queue (no synchronous delivery, spec §3). The
	// live loop would wake and accept; here we wait for the wake and accept it,
	// which renders the frame into the notification turn (a TurnSteering in
	// history) and settles the pending.
	waitForJobNotification(t, s)
	drainAndAccept(t, s)

	first := waitForSteeringEntryContaining(t, s, token)
	if !strings.Contains(first, token) || !strings.Contains(first, "output_match watch fired") {
		t.Fatalf("watch delivery = %q, want configured message and token", first)
	}
	waitForShellDone(t, s.jobManager, shellOut.JobID)
	if got := countSteeringEntriesContaining(s, token); got != 1 {
		t.Fatalf("watch deliveries containing %q = %d, want 1", token, got)
	}
}

// TestJobWatchTerminalOutputMatchCatchupThroughTool drives spec §7.1's terminal
// catch-up end to end through the job_watch tool: an output_match-only watch on an
// already-terminal job whose retained output matches returns terminal_catchup with
// fired=true (no live watch installed), and the new fields surface in the tool
// JSON. A non-matching catch-up reports terminal_catchup with an explicit
// fired=false — contract §7.1 promises "fired=false on none", not omission.
func TestJobWatchTerminalOutputMatchCatchupThroughTool(t *testing.T) {
	s := newTestSession(t)

	// Use a manual job so we have a durable record with known output. Fast
	// commands that produce small output return ephemeral (no job_id) after the
	// complete-or-handle invariant lands; creating the record directly avoids
	// that path and keeps the test focused on §7.1 terminal catch-up.
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "already-done\n")
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
		t.Fatalf("finalize manual job: %v", err)
	}
	waitForShellDone(t, s.jobManager, rec.JobID)
	watchedJobID := rec.JobID

	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":"already-done"}`, watchedJobID)),
	})
	if watchRes.IsError {
		t.Fatalf("terminal output_match catch-up must not error: %s", watchRes.Output)
	}
	var matched struct {
		Watching        bool   `json:"watching"`
		Fired           bool   `json:"fired"`
		TerminalCatchup bool   `json:"terminal_catchup"`
		Status          string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(watchRes), &matched); err != nil {
		t.Fatalf("unmarshal watch output: %v (%s)", err, watchRes.Output)
	}
	if matched.Watching || !matched.Fired || !matched.TerminalCatchup || matched.Status != "completed" {
		t.Fatalf("matched catch-up tool result = %+v, want fired+terminal_catchup+completed", matched)
	}

	// A non-matching output_match-only watch on the same terminal job catches up
	// without firing.
	noMatchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch2",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":%q,"output_match":"never-printed"}`, watchedJobID)),
	})
	if noMatchRes.IsError {
		t.Fatalf("non-matching terminal catch-up must not error: %s", noMatchRes.Output)
	}
	noMatchJSON := string(toolResultJSON(noMatchRes))
	if !strings.Contains(noMatchJSON, `"terminal_catchup":true`) {
		t.Fatalf("non-matching catch-up state = %s, want terminal_catchup", noMatchJSON)
	}
	if !strings.Contains(noMatchJSON, `"fired":false`) {
		t.Fatalf("non-matching catch-up must report explicit fired:false (contract §7.1): %s", noMatchJSON)
	}
}

func TestJobWatchNoConditionErrors(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"caller"}`),
	})
	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request: nothing to watch") {
		t.Fatalf("job_watch error = %q, want no-condition error", res.Output)
	}
}

func TestJobWatchToolMainAliasTargetFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"main"}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_watch error = %q, want target_not_found", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchToolWatchedTargetWithoutContextFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"watched"}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_watch error = %q, want target_not_found", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchToolSendToMainAliasFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"caller","events":["assistant.message"],"send":{"to":"main","message":"observe"}}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_watch error = %q, want target_not_found", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestDelegateSendToolMainAliasFailsInvalidRequestWithoutSideEffects(t *testing.T) {
	s := newTestSession(t)
	called := false
	s.cfg.spawn.parentSteer = func(string, *provenance.Causal) { called = true }

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(`{"to":"main","message":"hello"}`),
	})

	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request") {
		t.Fatalf("delegate_send error = %q, want invalid_request", res.Output)
	}
	if called {
		t.Fatal("main alias called parentSteer")
	}
	if queue := s.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no side effects", queue)
	}
	if jobs := s.jobManager.list(listFilter{}); len(jobs) != 0 {
		t.Fatalf("jobs = %+v, want no jobs created", jobs)
	}
	s.jobManager.mu.Lock()
	runningJobs := len(s.jobManager.running)
	s.jobManager.mu.Unlock()
	if runningJobs != 0 {
		t.Fatalf("running jobs = %d, want no runs created", runningJobs)
	}
}

func TestJobWatchSendToRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		send string
	}{
		{name: "message without target", send: `{"message":"observe"}`},
		{name: "excerpt without target", send: `{"to":"   ","include_excerpt":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","target":"caller","events":["assistant.message"],"send":%s}`, tc.send)),
			})
			if !res.IsError {
				t.Fatalf("job_watch succeeded, want send.to error: %s", res.Output)
			}
			if !strings.Contains(res.Output, "invalid_request: send.to is required") {
				t.Fatalf("job_watch error = %q, want send.to validation", res.Output)
			}
			if s.jobManager.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
			}
		})
	}
}

func TestJobWatchEmptySendPlaceholderIsOmitted(t *testing.T) {
	s := newTestSession(t)
	// job.notification (not a self-generated kind) keeps this a legal caller
	// watch; the feedback-loop guard rejects only self-generated kinds delivered
	// back to the caller, which an empty send placeholder would otherwise be.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"caller","events":["job.notification"],"send":{"to":"","message":"","include_excerpt":false}}`),
	})
	if res.IsError {
		t.Fatalf("job_watch returned error for empty send placeholder: %s", res.Output)
	}
	var out jobWatchToolResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_watch output: %v (output: %s)", err, res.Output)
	}
	if out.Send != nil {
		t.Fatalf("send placeholder should be omitted from result, got %+v", out.Send)
	}
}

// TestMarshalWatchResultSurfacesFired pins the tool-JSON projection of an
// attach-time fire: a watchResult with Fired=true renders "fired":true, and
// Fired=false omits the field (omitempty), so the agent learns its condition was
// already true without waiting a turn (spec §7.1).
func TestMarshalWatchResultSurfacesFired(t *testing.T) {
	firedOut, err := marshalWatchResult(watchResult{
		Target:      "job_1",
		Watching:    true,
		OutputMatch: "ready",
		Fired:       true,
	}, 4096)
	if err != nil {
		t.Fatalf("marshal fired result: %v", err)
	}
	var fired jobWatchToolResult
	if err := json.Unmarshal(handlerJSON(t, firedOut), &fired); err != nil {
		t.Fatalf("unmarshal fired result: %v (%s)", err, firedOut)
	}
	if !fired.Fired {
		t.Fatalf("fired result must project fired=true, got %s", firedOut)
	}
	if !strings.Contains(string(handlerJSON(t, firedOut)), `"fired":true`) {
		t.Fatalf("fired result JSON = %s, want it to contain \"fired\":true", firedOut)
	}

	notFiredOut, err := marshalWatchResult(watchResult{
		Target:      "job_1",
		Watching:    true,
		OutputMatch: "ready",
		Fired:       false,
	}, 4096)
	if err != nil {
		t.Fatalf("marshal not-fired result: %v", err)
	}
	// Contract §7.1: fired serializes explicitly even when false.
	if !strings.Contains(string(handlerJSON(t, notFiredOut)), `"fired":false`) {
		t.Fatalf("not-fired result JSON = %s, want explicit \"fired\":false", notFiredOut)
	}
	var notFired jobWatchToolResult
	if err := json.Unmarshal(handlerJSON(t, notFiredOut), &notFired); err != nil {
		t.Fatalf("unmarshal not-fired result: %v (%s)", err, notFiredOut)
	}
	if notFired.Fired {
		t.Fatal("not-fired result must project fired=false")
	}
}

func TestJobWatchAdvertisedDefinitionUsesCanonicalEventKinds(t *testing.T) {
	want := tooldefs.DefJobWatch(WatchEventKindNames)
	var got *llm.ToolDefinition
	for _, def := range NewOpenAIProfile("gpt-5.2").ToolDefinitions() {
		if def.Name == "job_watch" {
			got = &def
			break
		}
	}
	if got == nil {
		t.Fatal("OpenAI profile does not advertise job_watch")
	}
	if got.Description != want.Description {
		t.Fatalf("job_watch description drifted from WatchEventKindNames\n got: %q\nwant: %q", got.Description, want.Description)
	}
}

func TestJobStopDefaultReturnsRequestedCancellation(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}

	stopRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, shellOut.JobID)),
	})
	if stopRes.IsError {
		t.Fatalf("job_stop returned error: %s", stopRes.Output)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(toolResultJSON(stopRes), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, stopRes.Output)
	}
	if stopOut.JobID != shellOut.JobID || stopOut.Status != string(jobstore.StatusCancelled) || stopOut.Reason == nil || *stopOut.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want immediate cancelled/stopped_by_parent", stopOut)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
}

func TestJobStopBlockTimeoutReturnsStopPending(t *testing.T) {
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() {
		if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "stopped_by_parent", nil); err != nil {
			t.Fatalf("cleanup finalize: %v", err)
		}
		waitForShellDone(t, s.jobManager, rec.JobID)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := jobStopTool(ctx, s, map[string]any{
		"job_id":      rec.JobID,
		"max_wait_ms": 60000,
	}, 20000)
	if err != nil {
		t.Fatalf("jobStopTool returned error: %v", err)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, out)
	}
	if stopOut.JobID != rec.JobID || stopOut.Status != string(jobstore.StatusRunning) || stopOut.Reason == nil || *stopOut.Reason != "stop_pending" {
		t.Fatalf("job_stop = %+v, want running/stop_pending", stopOut)
	}
}

func TestDelegateToolForegroundReturnsStructuredResult(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("done report", map[string]any{
					"summary": "fixed",
				})
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"fix the issue",
			"max_wait_ms":5000,
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
	var out struct {
		JobID                 string         `json:"job_id"`
		Type                  string         `json:"type"`
		Status                string         `json:"status"`
		TranscriptRef         string         `json:"transcript_ref"`
		StructuredResult      map[string]any `json:"structured_result"`
		StructuredResultValid bool           `json:"structured_result_valid"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Status != string(jobstore.StatusCompleted) ||
		out.TranscriptRef == "" {
		t.Fatalf("delegate output = %+v, want completed delegate job", out)
	}
	if !out.StructuredResultValid || out.StructuredResult["summary"] != "fixed" {
		t.Fatalf("structured result = %+v valid=%v, want summary=fixed", out.StructuredResult, out.StructuredResultValid)
	}
}

func TestDelegateToolForegroundSchemaResultMissingProjectsReason(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("done without structured result")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"return a structured result",
			"max_wait_ms":5000,
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
	assertStructuredResultInvalidReason(t, string(toolResultJSON(res)), "schema_result_missing")
}

func TestDelegateToolForegroundNoSchemaNoStructuredOmitsStructuredFields(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("plain delegate result")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "delegate",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"return prose only","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	assertStructuredResultFieldsAbsent(t, res.Output)
}

func TestJobReadOutputReturnsBackgroundDelegateStructuredResult(t *testing.T) {
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

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
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

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, delegateOut.JobID)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	assertStructuredResultInvalidReason(t, string(toolResultJSON(readRes)), "schema_result_missing")
}

func TestJobReadOutputBlockReturnsOnNewOutput(t *testing.T) {
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
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
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

func newManualRunningJob(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	rec, err := s.jobManager.createShell(createShellOpts{Command: "manual running job"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	t.Cleanup(func() {
		_ = s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "test_cleanup", nil)
		waitForShellDone(t, s.jobManager, rec.JobID)
	})
	return rec
}

func appendManualJobOutput(jm *jobManager, jobID string, output string) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		_, _ = run.output.Append([]byte(output))
	}
}

func blockingGrepRead(t *testing.T, s *Session, jobID, grep string, timeoutMS int) (jobReadOutputTestResult, time.Duration) {
	t.Helper()
	started := time.Now()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%q,"max_wait_ms":%d}`, jobID, grep, timeoutMS)),
	})
	elapsed := time.Since(started)
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
	}
	return out, elapsed
}

func TestJobReadOutputBlockGrepReturnsImmediatelyOnExistingMatch(t *testing.T) {
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
func TestReadJobOutputFromWidensPastStaleTotal(t *testing.T) {
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
func TestJobReadOutputBlockGrepReturnsImmediatelyOnTerminalJobWithMatch(t *testing.T) {
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

func TestDelegateSendForegroundStartReturnsTerminalResult(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	}
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
	var out struct {
		DelegateID          string `json:"delegate_id"`
		StartedJobID        string `json:"started_job_id"`
		CurrentJobID        string `json:"current_job_id"`
		LatestJobID         string `json:"latest_job_id"`
		Type                string `json:"type"`
		Status              string `json:"status"`
		RunningInBackground bool   `json:"running_in_background"`
		TimedOut            bool   `json:"timed_out"`
		Action              string `json:"action"`
		TranscriptRef       string `json:"transcript_ref"`
		Output              string `json:"output"`
		Truncated           bool   `json:"truncated"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.DelegateID != first.DelegateID ||
		out.StartedJobID == "" ||
		out.StartedJobID == first.JobID ||
		out.CurrentJobID != out.StartedJobID ||
		out.LatestJobID != out.StartedJobID ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Status != string(jobstore.StatusCompleted) ||
		out.RunningInBackground ||
		out.TimedOut ||
		out.Action != "started" ||
		out.TranscriptRef != first.TranscriptRef ||
		!strings.Contains(out.Output, "second complete") ||
		out.Truncated {
		t.Fatalf("delegate_send output = %+v, want foreground terminal started result", out)
	}
	rec := loadShellRecord(t, s.jobManager, out.CurrentJobID)
	if rec.Status != jobstore.StatusCompleted || rec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("started record = %+v, want completed with same transcript ref", rec)
	}
}

func TestDelegateSendRejectsJobIDTargetWithGuidance(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again"}`, first.JobID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_id is a job/turn handle") || !strings.Contains(res.Output, first.DelegateID) {
		t.Fatalf("delegate_send error = %q, want job_id guidance with delegate_id %q", res.Output, first.DelegateID)
	}
}

func TestDelegateSendIdleDefaultFailsAndOnIdleStartResumes(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	}
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

	idle := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send-idle",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again"}`, first.DelegateID)),
	})
	if !idle.IsError {
		t.Fatalf("delegate_send idle default succeeded, want error: %s", idle.Output)
	}
	if !strings.Contains(idle.Output, "target_idle") {
		t.Fatalf("delegate_send idle error = %q, want target_idle", idle.Output)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send-start",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	var out struct {
		DelegateID   string `json:"delegate_id"`
		StartedJobID string `json:"started_job_id"`
		CurrentJobID string `json:"current_job_id"`
		LatestJobID  string `json:"latest_job_id"`
		Action       string `json:"action"`
		Output       string `json:"output"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.DelegateID != first.DelegateID ||
		out.StartedJobID == "" ||
		out.StartedJobID == first.JobID ||
		out.CurrentJobID != out.StartedJobID ||
		out.LatestJobID != out.StartedJobID ||
		out.Action != "started" ||
		!strings.Contains(out.Output, "second") {
		t.Fatalf("delegate_send output = %+v, want started resumed delegate result", out)
	}
}

func TestJobSendMessageRestoreRuntimeLostStructuredInvalidResult(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("old structured output", map[string]any{"summary": "old"})
			},
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("resumed prose without structured output")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
		},
		"required": []string{"summary"},
	}

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "runtime-lost structured delegate",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema:   resultSchema,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	rec := loadShellRecord(t, s.jobManager, first.JobID)
	setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusStopped, "runtime_lost")
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	res := restored.reg.ExecuteCall(context.Background(), restored.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"resume without structured output","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	var out struct {
		DelegateID             string         `json:"delegate_id"`
		StartedJobID           string         `json:"started_job_id"`
		CurrentJobID           string         `json:"current_job_id"`
		LatestJobID            string         `json:"latest_job_id"`
		Type                   string         `json:"type"`
		Status                 string         `json:"status"`
		Action                 string         `json:"action"`
		TranscriptRef          string         `json:"transcript_ref"`
		Output                 string         `json:"output"`
		StructuredResult       map[string]any `json:"structured_result"`
		StructuredResultValid  *bool          `json:"structured_result_valid"`
		StructuredResultReason string         `json:"structured_result_reason"`
		RunningInBackground    bool           `json:"running_in_background"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.DelegateID != first.DelegateID ||
		out.StartedJobID == "" ||
		out.StartedJobID == first.JobID ||
		out.CurrentJobID != out.StartedJobID ||
		out.LatestJobID != out.StartedJobID ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Action != "started" ||
		out.TranscriptRef != first.TranscriptRef ||
		out.RunningInBackground {
		t.Fatalf("delegate_send output = %+v, want foreground restored start", out)
	}
	if out.Status != string(jobstore.StatusFailed) || !strings.Contains(out.Output, "model returned bare text") {
		t.Fatalf("delegate_send output = %+v, want failed missing-structured started turn", out)
	}
	if out.StructuredResult != nil {
		t.Fatalf("structured_result = %+v, want omitted", out.StructuredResult)
	}
	if out.StructuredResultValid == nil || *out.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want false", out.StructuredResultValid)
	}
	if out.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("structured_result_reason = %q, want schema_result_missing", out.StructuredResultReason)
	}
	newRec := loadShellRecord(t, restored.jobManager, out.CurrentJobID)
	if newRec.DelegateRestore == nil || newRec.DelegateRestore.ResultSchema == nil || newRec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed record = %+v, want inherited schema and transcript ref", newRec)
	}
	if newRec.StructuredResult != nil ||
		newRec.StructuredResultValid == nil ||
		*newRec.StructuredResultValid ||
		newRec.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("durable structured result = value:%+v valid:%v reason:%q, want missing schema result", newRec.StructuredResult, newRec.StructuredResultValid, newRec.StructuredResultReason)
	}
}

func TestMarshalDelegateResultsBoundLargeOutput(t *testing.T) {
	largeOutput := strings.Repeat("prefix-", 200) + "delegate-tail"
	out, err := marshalDelegateResult(delegateResult{
		JobID:               "job_delegate",
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusCompleted,
		RunningInBackground: false,
		TranscriptRef:       "local:child",
		Output:              largeOutput,
	}, jobToolResultMinJSONChars)
	if err != nil {
		t.Fatalf("marshalDelegateResult: %v", err)
	}
	if !json.Valid(handlerJSON(t, out)) {
		t.Fatalf("delegate result returned invalid JSON: %s", out)
	}
	if len([]rune(out)) > jobToolResultMinJSONChars {
		t.Fatalf("delegate result length = %d, want <= %d", len([]rune(out)), jobToolResultMinJSONChars)
	}
	var parsed struct {
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal delegate result: %v", err)
	}
	if !parsed.Truncated {
		t.Fatalf("truncated = false, want true in %s", out)
	}
	if !strings.Contains(parsed.Output, "delegate-tail") {
		t.Fatalf("output tail not retained: %q", parsed.Output)
	}
}

func TestDelegateSendNegativeBlockTimeoutDoesNotStart(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("must not resume")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}
	before := s.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(before) != 1 {
		t.Fatalf("delegate jobs before send = %+v, want original terminal job only", before)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again","max_wait_ms":-1}`, first.DelegateID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("delegate_send error = %q, want non-negative max_wait_ms error", res.Output)
	}
	after := s.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
}

func TestDelegateSendToShellJobIDRejectsJobHandle(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"hi"}`, shellOut.JobID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_id is a job/turn handle") {
		t.Fatalf("delegate_send error = %q, want job_id handle rejection", res.Output)
	}
}

func TestDelegateSendCallerTargetReturnsRuntimeShape(t *testing.T) {
	s := newTestSession(t)
	s.cfg.spawn.parentSteer = s.SteerWithProvenance

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(`{"to":"caller","message":"runtime advisory"}`),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	var out struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.Type != "runtime" || out.Status != "delivered" || out.Action != "delivered" {
		t.Fatalf("delegate_send output = %+v, want runtime delivered shape", out)
	}
	queue := s.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "runtime advisory" {
		t.Fatalf("steering queue = %+v, want runtime advisory", queue)
	}
}

func TestJobToolsDefinitions(t *testing.T) {
	required := func(t *testing.T, def llm.ToolDefinition, name string, want []string) {
		t.Helper()
		if def.Name != name {
			t.Fatalf("definition name = %q, want %q", def.Name, name)
		}
		required := requiredParams(t, name, def.Parameters["required"])
		for _, param := range want {
			if !containsString(required, param) {
				t.Fatalf("%s required = %v, want %q", name, required, param)
			}
		}
		props, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %T, want map[string]any", name, def.Parameters["properties"])
		}
		for _, param := range want {
			if _, ok := props[param]; !ok {
				t.Fatalf("%s missing required property %q", name, param)
			}
		}
	}

	required(t, tooldefs.DefJobReadOutput(), "job_read_output", []string{"job_id"})
	required(t, tooldefs.DefJobStop(), "job_stop", []string{"job_id"})
	required(t, tooldefs.DefJobList(), "job_list", nil)
	required(t, tooldefs.DefDelegate([]string{"reviewer"}), "delegate", []string{"task"})
	required(t, tooldefs.DefJobWatch(WatchEventKindNames), "job_watch", []string{"operation"})
	required(t, tooldefs.DefDelegateSend(), "delegate_send", []string{"to", "message"})

	readProps := tooldefs.DefJobReadOutput().Parameters["properties"].(map[string]any)
	for _, param := range []string{"tail_lines", "grep", "max_wait_ms"} {
		if _, ok := readProps[param]; !ok {
			t.Fatalf("job_read_output missing param %q", param)
		}
	}
	if _, ok := readProps["max_chars"]; ok {
		t.Fatalf("job_read_output schema unexpectedly contains removed max_chars param")
	}
	if _, ok := readProps["limit_bytes"]; ok {
		t.Fatalf("job_read_output schema unexpectedly contains removed limit_bytes param")
	}
	listProps := tooldefs.DefJobList().Parameters["properties"].(map[string]any)
	for _, param := range []string{"status", "type", "include_nested", "limit"} {
		if _, ok := listProps[param]; !ok {
			t.Fatalf("job_list missing param %q", param)
		}
	}
	if _, ok := listProps["cursor"]; ok {
		t.Fatalf("job_list schema unexpectedly contains removed cursor param")
	}
	stopProps := tooldefs.DefJobStop().Parameters["properties"].(map[string]any)
	for _, param := range []string{"job_id", "max_wait_ms", "include_children"} {
		if _, ok := stopProps[param]; !ok {
			t.Fatalf("job_stop missing param %q", param)
		}
	}
	if _, ok := stopProps["signal"]; ok {
		t.Fatalf("job_stop exposes unsupported signal parameter")
	}
	delegateProps := tooldefs.DefDelegate([]string{"reviewer"}).Parameters["properties"].(map[string]any)
	for _, param := range []string{"task", "agent_type", "model", "reasoning_effort", "max_wait_ms", "result_schema"} {
		if _, ok := delegateProps[param]; !ok {
			t.Fatalf("delegate missing param %q", param)
		}
	}
	sendProps := tooldefs.DefDelegateSend().Parameters["properties"].(map[string]any)
	for _, param := range []string{"to", "message", "on_idle", "max_wait_ms"} {
		if _, ok := sendProps[param]; !ok {
			t.Fatalf("delegate_send missing param %q", param)
		}
	}
	watchProps := tooldefs.DefJobWatch(WatchEventKindNames).Parameters["properties"].(map[string]any)
	for _, param := range []string{"operation", "watch_id", "target", "output_match", "progress_interval_ms", "events", "every", "send"} {
		if _, ok := watchProps[param]; !ok {
			t.Fatalf("job_watch missing param %q", param)
		}
	}
	if _, ok := watchProps["clear"]; ok {
		t.Fatalf("job_watch exposes removed clear param")
	}
}

func TestJobReadOutputRejectsInvalidArgs(t *testing.T) {
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
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "read",
				Name:      "job_read_output",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
			}
		})
	}
}

func TestJobReadOutputGrepSearchesRetainedOutputBeyondTail(t *testing.T) {
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

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
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

func TestJobToolOutputLimitsHaveJSONMinimum(t *testing.T) {
	s := newShellToolTestSession(t, SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"job_read_output": {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_list":        {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_stop":        {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"delegate":        {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_watch":       {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"delegate_send":   {MaxChars: 1, Strategy: schema.TruncHeadTail},
		},
	})

	for _, name := range []string{"job_read_output", "job_list", "job_stop", "delegate", "job_watch", "delegate_send"} {
		rt := s.reg.Get(name)
		if rt == nil {
			t.Fatalf("%s not registered", name)
		}
		if rt.Limit.MaxChars != jobToolResultMinJSONChars {
			t.Fatalf("%s MaxChars = %d, want JSON minimum %d", name, rt.Limit.MaxChars, jobToolResultMinJSONChars)
		}
	}
}

func TestJobReadOutputGrepSearchesTerminalOutputFileBeyondTail(t *testing.T) {
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
func TestJobReadOutputGrepScansFullRetainedOutputBeyondOldBudget(t *testing.T) {
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

func TestJobStopSchemaRejectsUnsupportedSignal(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"signal":"KILL"}`, shellOut.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_stop accepted unsupported signal argument: %s", res.Output)
	}
	rec := loadShellRecord(t, s.jobManager, shellOut.JobID)
	if rec.Status != jobstore.StatusRunning {
		t.Fatalf("job_stop with unsupported signal changed job state to %+v, want still running", rec)
	}
}

func TestJobStopAcceptsIncludeChildrenThroughRegistry(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"include_children":true}`, shellOut.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_stop rejected include_children through registry: %s", res.Output)
	}
}

func TestJobReadOutputRejectsLargeGrepBeforeRegistryTruncation(t *testing.T) {
	s := newTestSession(t)

	// Use a manual job; fast small-output shell commands return ephemeral (no
	// job_id) under complete-or-handle, so the shell tool cannot reliably
	// produce a durable id here. The test is about grep validation, not shell.
	rec := newManualRunningJob(t, s)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
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
	s := newTestSession(t)

	// Use a manual job; fast small-output shell commands return ephemeral (no
	// job_id) under complete-or-handle, so the shell tool cannot reliably
	// produce a durable id here. The test is about grep validation, not shell.
	rec := newManualRunningJob(t, s)

	patternJSON, err := json.Marshal(strings.Repeat("\x00", maxJobGrepPatternJSONChars(jobToolResultDefaultMaxChar)/4))
	if err != nil {
		t.Fatalf("marshal grep pattern: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%s}`, rec.JobID, patternJSON)),
	})
	if !res.IsError {
		t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "grep is too large after JSON escaping") {
		t.Fatalf("job_read_output error = %q, want JSON escaping limit", res.Output)
	}
}

type jobReadOutputTestResult struct {
	JobID   string  `json:"job_id"`
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Reason  *string `json:"reason"`
	Content string  `json:"output"`
	Grep    string  `json:"grep"`
	Matches []struct {
		ByteOffset *int64 `json:"byte_offset"`
		Line       string `json:"line"`
	} `json:"matches"`
	TotalBytes            int64          `json:"total_bytes"`
	Truncated             bool           `json:"truncated"`
	ExitCode              *int           `json:"exit_code"`
	StructuredResult      map[string]any `json:"structured_result"`
	StructuredResultValid bool           `json:"structured_result_valid"`
	LastActivity          *string        `json:"last_activity"`
}

func assertStructuredResultInvalidReason(t *testing.T, out, reason string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["structured_result"]; ok {
		t.Fatal("structured_result present for invalid/missing schema result")
	}
	if parsed["structured_result_valid"] != false {
		t.Fatalf("structured_result_valid = %v, want false", parsed["structured_result_valid"])
	}
	if parsed["structured_result_reason"] != reason {
		t.Fatalf("reason = %v, want %s", parsed["structured_result_reason"], reason)
	}
}

func assertStructuredResultFieldsAbsent(t *testing.T, out string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["structured_result"]; ok {
		t.Fatal("structured_result present without schema or structured output")
	}
	if _, ok := parsed["structured_result_valid"]; ok {
		t.Fatal("structured_result_valid present without schema or structured output")
	}
	if _, ok := parsed["structured_result_reason"]; ok {
		t.Fatal("structured_result_reason present without schema or structured output")
	}
}

func waitForJobOutput(t *testing.T, s *Session, jobID, want string) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_lines":65536,"grep":"ready"}`, jobID)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
		}
		if strings.Contains(out.Content, want) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job_read_output never contained %q; last output: %s", want, last)
	return jobReadOutputTestResult{}
}

func waitForJobGrepMatchResult(t *testing.T, s *Session, jobID, want string, tailBytes int) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_lines":%d,"grep":%q}`, jobID, tailBytes, want)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
		}
		if out.TotalBytes > int64(tailBytes) && len(out.Matches) > 0 {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job_read_output never found grep match %q; last output: %s", want, last)
	return jobReadOutputTestResult{}
}

type jobListToolOutput struct {
	Jobs    []jobListToolEntry `json:"jobs"`
	Watches []jobListToolWatch `json:"watches"`
}

type jobListToolEntry struct {
	JobID              string  `json:"job_id"`
	Type               string  `json:"type"`
	ParentJobID        *string `json:"parent_job_id"`
	Resumable          *bool   `json:"resumable"`
	NotResumableReason *string `json:"not_resumable_reason"`
	StartedAt          string  `json:"started_at"`
	EndedAt            *string `json:"ended_at"`
	LastActivity       *string `json:"last_activity"`
}

type jobListToolWatch struct {
	Target     string `json:"target"`
	Condition  string `json:"condition"`
	SendTo     string `json:"send_to"`
	Deliveries int    `json:"deliveries"`
	CreatedAt  string `json:"created_at"`
}

func findJobListToolWatch(watches []jobListToolWatch, target, sendTo string) *jobListToolWatch {
	for i := range watches {
		if watches[i].Target == target && watches[i].SendTo == sendTo {
			return &watches[i]
		}
	}
	return nil
}

func jobListToolOutputContains(records []jobListToolEntry, jobID string) bool {
	return findJobListToolOutput(records, jobID) != nil
}

func findJobListToolOutput(records []jobListToolEntry, jobID string) *jobListToolEntry {
	for i := range records {
		if records[i].JobID == jobID {
			return &records[i]
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestDelegateMaxWaitMSDecodeTable pins spec §2 delegate decode: negative
// max_wait_ms is rejected; the old background+block_timeout_ms combo rejection
// is gone (spec §3 — combo is inexpressible).
func TestDelegateMaxWaitMSDecodeTable(t *testing.T) {
	s := newDelegateTestSession(t, llm.NewClient())

	// Negative max_wait_ms → invalid_request.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "delegate",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"x","max_wait_ms":-5}`),
	})
	if !res.IsError {
		t.Fatalf("delegate with max_wait_ms=-5 should return error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("delegate error = %q, want max_wait_ms must be non-negative", res.Output)
	}
}

// TestDelegateSendMaxWaitMSDecodeTable pins spec §2 delegate_send decode:
// negative max_wait_ms is rejected. The old combo rejection is gone (spec §3).
func TestDelegateSendMaxWaitMSDecodeTable(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("done")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:       "finish first",
		Background: false,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	waitForShellDone(t, s.jobManager, first.JobID)

	// Negative max_wait_ms → invalid_request.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"m","max_wait_ms":-5}`, first.DelegateID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send with max_wait_ms=-5 should return error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("delegate_send error = %q, want max_wait_ms must be non-negative", res.Output)
	}
}

// TestDelegateAndDelegateSendAcceptZeroMaxWaitMS pins spec §2: max_wait_ms=0 is
// accepted (strict-provider safe — zero reads as unset on all five tools).
func TestDelegateAndDelegateSendAcceptZeroMaxWaitMS(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("done")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d0",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"zero is unset","max_wait_ms":0}`),
	})
	if res.IsError {
		t.Fatalf("delegate with max_wait_ms=0 (unset) failed unexpectedly: %s", res.Output)
	}
	var spawned struct {
		DelegateID          string `json:"delegate_id"`
		JobID               string `json:"job_id"`
		RunningInBackground bool   `json:"running_in_background"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &spawned); err != nil || spawned.JobID == "" || spawned.DelegateID == "" {
		t.Fatalf("delegate result missing job_id: %s", res.Output)
	}
	if !spawned.RunningInBackground {
		t.Fatalf("delegate with max_wait_ms=0 should be running_in_background, got: %s", res.Output)
	}
	waitForShellDone(t, s.jobManager, spawned.JobID)
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("done again")
	})
	adapter.mu.Unlock()

	res2 := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send0",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"m","max_wait_ms":0,"on_idle":"start"}`, spawned.DelegateID)),
	})
	if res2.IsError {
		t.Fatalf("delegate_send with max_wait_ms=0 (unset) failed unexpectedly: %s", res2.Output)
	}
}

// TestMaxWaitMSDecoders covers spec §2's decode table for delegate,
// delegate_send, job_read_output, and job_stop: negative max_wait_ms must
// return invalid_request; 0/absent must succeed with unset behavior.
func TestMaxWaitMSDecoders(t *testing.T) {
	const wantNegErr = "invalid_request: max_wait_ms must be non-negative"

	t.Run("delegate_negative", func(t *testing.T) {
		s := newDelegateTestSession(t, llm.NewClient())
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "d",
			Name:      "delegate",
			Arguments: json.RawMessage(`{"task":"x","max_wait_ms":-1}`),
		})
		if !res.IsError {
			t.Fatalf("delegate with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("delegate error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("delegate_zero_is_unset", func(t *testing.T) {
		adapter := &fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return communicateWithDefaultOutput("done")
				},
			},
		}
		c := llm.NewClient()
		c.Register(adapter)
		s := newDelegateTestSession(t, c)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "d",
			Name:      "delegate",
			Arguments: json.RawMessage(`{"task":"x","max_wait_ms":0}`),
		})
		if res.IsError {
			t.Fatalf("delegate with max_wait_ms=0: want no-wait success, got error: %s", res.Output)
		}
		// With unset (0), should return immediately with running_in_background:true.
		var out struct {
			RunningInBackground bool   `json:"running_in_background"`
			JobID               string `json:"job_id"`
		}
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !out.RunningInBackground || out.JobID == "" {
			t.Fatalf("delegate with max_wait_ms=0 = %+v, want running_in_background with job_id", out)
		}
		waitForShellDone(t, s.jobManager, out.JobID)
	})

	t.Run("delegate_send_negative", func(t *testing.T) {
		adapter := &fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return communicateWithDefaultOutput("done")
				},
			},
		}
		c := llm.NewClient()
		c.Register(adapter)
		s := newDelegateTestSession(t, c)
		first := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false})
		if first.Err != nil {
			t.Fatalf("createDelegate: %v", first.Err)
		}
		waitForShellDone(t, s.jobManager, first.JobID)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "send",
			Name:      "delegate_send",
			Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"m","max_wait_ms":-1}`, first.DelegateID)),
		})
		if !res.IsError {
			t.Fatalf("delegate_send with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("delegate_send error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("job_read_output_negative", func(t *testing.T) {
		s := newTestSession(t)
		// Use internal API to create a running job (avoids shell decoder chicken-and-egg).
		rec := newManualRunningJob(t, s)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":-1}`, rec.JobID)),
		})
		if !res.IsError {
			t.Fatalf("job_read_output with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("job_read_output error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("job_read_output_zero_is_snapshot", func(t *testing.T) {
		s := newTestSession(t)
		// Use internal API to create a running job.
		rec := newManualRunningJob(t, s)
		// max_wait_ms=0: snapshot now (should not block).
		started := time.Now()
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":0}`, rec.JobID)),
		})
		if res.IsError {
			t.Fatalf("job_read_output with max_wait_ms=0: %s", res.Output)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("job_read_output with max_wait_ms=0 blocked for %s, want immediate snapshot", elapsed)
		}
	})

	t.Run("job_stop_negative", func(t *testing.T) {
		s := newTestSession(t)
		rec := newManualRunningJob(t, s)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "stop",
			Name:      "job_stop",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":-1}`, rec.JobID)),
		})
		if !res.IsError {
			t.Fatalf("job_stop with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("job_stop error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("job_stop_zero_is_return_now", func(t *testing.T) {
		s := newTestSession(t)
		rec := newManualRunningJob(t, s)
		// max_wait_ms=0: request stop and return without waiting.
		started := time.Now()
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "stop",
			Name:      "job_stop",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":0}`, rec.JobID)),
		})
		if res.IsError {
			t.Fatalf("job_stop with max_wait_ms=0: %s", res.Output)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("job_stop with max_wait_ms=0 blocked for %s, want return immediately", elapsed)
		}
	})
}

// TestGrantedReadBlockUnsupportedErrReword pins the error message wording for
// max_wait_ms>0 on a cross-session read — both a watch-granted read (spec §3)
// and a depth >= 2 descendant read resolved through the recursive owner path
// (spec §2). The message generalizes to "cross-session reads" so it is truthful
// for both.
func TestGrantedReadBlockUnsupportedErrReword(t *testing.T) {
	const want = "invalid_request: max_wait_ms is not supported for cross-session reads"
	if grantedReadBlockUnsupportedErr != want {
		t.Fatalf("grantedReadBlockUnsupportedErr = %q, want %q", grantedReadBlockUnsupportedErr, want)
	}
}

// TestJobReadOutputHeadBytesReadsFromStart verifies that head_lines reads from
// the beginning of retained output — the symmetric counterpart to tail_lines.
// A job whose head output was pushed out of the default tail window is only
// reachable by grep or by head_lines; this test closes that gap.
func TestJobReadOutputHeadLinesReadsFromStart(t *testing.T) {
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
	digRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read-digest",
		Name:      "job_read_output",
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
	headRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read-head",
		Name:      "job_read_output",
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
func TestJobReadOutputFromLineExclusiveWithHeadTail(t *testing.T) {
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
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
func TestJobReadOutputZeroHeadTailTreatedAsUnset(t *testing.T) {
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
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

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
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "read-" + tc.name,
				Name:      "job_read_output",
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
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
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
func TestDelegateToolParsesDelegationAllowance(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateTestSession(t, c)
	s.mu.Lock()
	s.delegationAllowance = 1 // default root with MaxSubagentDepth=1
	s.mu.Unlock()

	// Grant equal to own allowance (1) is rejected with the grant-rule message,
	// proving the parsed value reached createDelegate.
	overGrant := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d1",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"recurse","delegation_allowance":1}`),
	})
	if !overGrant.IsError {
		t.Fatalf("delegate with delegation_allowance=1 and own allowance 1 succeeded, want error; output: %s", overGrant.Output)
	}
	if !strings.Contains(overGrant.Output, "must be less than your own allowance (1)") {
		t.Fatalf("error = %q, want grant-rule rejection naming allowance 1", overGrant.Output)
	}

	// Negative grant is rejected at the boundary.
	negGrant := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d2",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"recurse","delegation_allowance":-1}`),
	})
	if !negGrant.IsError {
		t.Fatalf("delegate with delegation_allowance=-1 succeeded, want error; output: %s", negGrant.Output)
	}
	if !strings.Contains(negGrant.Output, "invalid_request") || !strings.Contains(negGrant.Output, "non-negative") {
		t.Fatalf("error = %q, want invalid_request non-negative", negGrant.Output)
	}
}

func requiredParams(t *testing.T, name string, raw any) []string {
	t.Helper()
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		required := make([]string, 0, len(values))
		for _, value := range values {
			required = append(required, fmt.Sprint(value))
		}
		return required
	default:
		t.Fatalf("%s required = %T, want array", name, raw)
		return nil
	}
}

func TestJobListReportsDelegationAllowance(t *testing.T) {
	s := newTestSession(t)
	s.delegationAllowance = 2

	out, err := jobListTool(s, decodeJobListArgs(t, `{}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var got struct {
		DelegationAllowance int `json:"delegation_allowance"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &got); err != nil {
		t.Fatalf("unmarshal job_list output: %v (out=%s)", err, out)
	}
	if got.DelegationAllowance != 2 {
		t.Fatalf("delegation_allowance = %d, want 2", got.DelegationAllowance)
	}
}

func TestLiveSteerWaitIgnoredReason(t *testing.T) {
	const want = "live steer returns on delivery; max_wait_ms applies only to started jobs"
	cases := []struct {
		name    string
		ms      int
		status  jobstore.Status
		action  string
		wantStr string
	}{
		{"live steer with wait flagged", 5000, jobstore.StatusRunning, "steered", want},
		{"no wait requested not flagged", 0, jobstore.StatusRunning, "steered", ""},
		{"started running not flagged", 5000, jobstore.StatusRunning, "started", ""},
		{"started terminal not flagged", 5000, jobstore.StatusCompleted, "started", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := liveSteerWaitIgnoredReason(tc.ms, tc.status, tc.action)
			if got != tc.wantStr {
				t.Fatalf("liveSteerWaitIgnoredReason(%d,%q,%q) = %q, want %q", tc.ms, tc.status, tc.action, got, tc.wantStr)
			}
		})
	}
}

func TestMarshalDelegateSendResultCarriesWaitIgnoredReason(t *testing.T) {
	const reason = "live steer returns on delivery; max_wait_ms applies only to started jobs"
	res := sendMessageResult{
		Target:              "dlg_x",
		DelegateID:          "dlg_x",
		JobID:               "job_x",
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "steered",
		WaitIgnoredReason:   reason,
	}
	out, err := marshalDelegateSendResult(res, 1<<20)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult: %v", err)
	}
	var got struct {
		WaitIgnoredReason string `json:"wait_ignored_reason"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &got); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out)
	}
	if got.WaitIgnoredReason != reason {
		t.Fatalf("wait_ignored_reason = %q, want %q", got.WaitIgnoredReason, reason)
	}

	res.WaitIgnoredReason = ""
	out2, err := marshalDelegateSendResult(res, 1<<20)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult (empty): %v", err)
	}
	if strings.Contains(string(handlerJSON(t, out2)), "wait_ignored_reason") {
		t.Fatalf("wait_ignored_reason must be omitted when empty, got %s", out2)
	}
}

func TestClassifyStopOutcome(t *testing.T) {
	run := jobstore.StatusRunning
	cases := []struct {
		name string
		prev jobstore.Status
		rec  *jobstore.JobRecord
		want string
	}{
		{"already terminal", jobstore.StatusCompleted, &jobstore.JobRecord{Status: jobstore.StatusCancelled}, "already_terminal"},
		{"still running", run, &jobstore.JobRecord{Status: jobstore.StatusRunning}, "stop_requested"},
		{"nil record", run, nil, "stop_requested"},
		{"cancelled by request", run, &jobstore.JobRecord{Status: jobstore.StatusCancelled}, "cancelled_by_request"},
		{"completed during stop", run, &jobstore.JobRecord{Status: jobstore.StatusCompleted}, "completed_during_stop"},
		{"failed during stop", run, &jobstore.JobRecord{Status: jobstore.StatusFailed}, "completed_during_stop"},
	}
	for _, tc := range cases {
		if got := classifyStopOutcome(tc.prev, tc.rec); got != tc.want {
			t.Errorf("%s: classifyStopOutcome(%q,rec) = %q, want %q", tc.name, tc.prev, got, tc.want)
		}
	}
}

func TestJobStopReportsOutcomeAndPreviousStatus(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}

	out, err := jobStopTool(context.Background(), s, map[string]any{"job_id": shellOut.JobID}, 1<<20)
	if err != nil {
		t.Fatalf("jobStopTool: %v", err)
	}
	var stopOut struct {
		Status         string `json:"status"`
		PreviousStatus string `json:"previous_status"`
		Outcome        string `json:"outcome"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (out=%s)", err, out)
	}
	if stopOut.Outcome != "cancelled_by_request" {
		t.Fatalf("outcome = %q, want cancelled_by_request (out=%s)", stopOut.Outcome, out)
	}
	if stopOut.PreviousStatus != string(jobstore.StatusRunning) {
		t.Fatalf("previous_status = %q, want running", stopOut.PreviousStatus)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
}

func TestJobToolRequiredArgErrorsCarryInvalidRequestPrefix(t *testing.T) {
	s := newTestSession(t)

	if res := s.createDelegate(context.Background(), delegateArgs{Task: ""}); res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "invalid_request:") {
		t.Fatalf("empty task error = %v, want invalid_request: prefix", res.Err)
	}
	if res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: "", Message: "hi"}); res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "invalid_request:") {
		t.Fatalf("empty target error = %v, want invalid_request: prefix", res.Err)
	}
	if res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: "job_x", Message: ""}); res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "invalid_request:") {
		t.Fatalf("empty message error = %v, want invalid_request: prefix", res.Err)
	}
	if _, err := jobStopTool(context.Background(), s, map[string]any{"job_id": ""}, 1<<20); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("stop empty job_id error = %v, want invalid_request: prefix", err)
	}
	if _, err := jobReadOutputTool(context.Background(), s, map[string]any{"job_id": ""}, 1<<20); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("read empty job_id error = %v, want invalid_request: prefix", err)
	}
}

func TestJobReadOutputInvalidGrepCarriesPrefix(t *testing.T) {
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

func TestJobListSurfacesRecentWatches(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	out, err := jobListTool(s, decodeJobListArgs(t, `{}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var got struct {
		RecentWatches []struct {
			ID        string `json:"id"`
			Target    string `json:"target"`
			EndReason string `json:"end_reason"`
		} `json:"recent_watches"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &got); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out)
	}
	if len(got.RecentWatches) != 1 || got.RecentWatches[0].EndReason != "cleared" || got.RecentWatches[0].Target != rec.JobID {
		t.Fatalf("recent_watches = %+v, want one cleared entry on %s", got.RecentWatches, rec.JobID)
	}
}

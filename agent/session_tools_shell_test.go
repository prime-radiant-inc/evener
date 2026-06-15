package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

type bufferedShellEnv struct {
	agenttest.FakeEnv
	timeoutMS int
}

func (e *bufferedShellEnv) ExecCommand(_ context.Context, _ string, timeoutMS int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	e.timeoutMS = timeoutMS
	return execenv.ExecResult{
		ExitCode:   -1,
		DurationMS: int64(timeoutMS),
		TimedOut:   true,
	}, nil
}

func TestShellToolBackgroundReturnsJobID(t *testing.T) {
	s := newTestSession(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res := s.reg.ExecuteCall(ctx, s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","max_wait_ms":1000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID               string `json:"job_id"`
		Type                string `json:"type"`
		Status              string `json:"status"`
		RunningInBackground bool   `json:"running_in_background"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" ||
		out.Type != string(jobstore.JobShell) ||
		out.Status != string(jobstore.StatusRunning) ||
		!out.RunningInBackground {
		t.Fatalf("shell output = %+v, want running background shell job", out)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(out.JobID)
		waitForShellDone(t, s.jobManager, out.JobID)
	})

	jobs := s.jobManager.list(listFilter{})
	if len(jobs) != 1 || jobs[0].JobID != out.JobID || jobs[0].Status != jobstore.StatusRunning {
		t.Fatalf("jobs = %+v, want one running shell job %q", jobs, out.JobID)
	}
}

func TestShellToolTinyMaxCharsStillReturnsJSON(t *testing.T) {
	s := newShellToolTestSession(t, SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"shell": {MaxChars: 1, Strategy: schema.TruncHeadTail},
		},
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"head -c 60000 </dev/zero | tr '\\0' 'x'","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	if strings.Contains(res.Output, "Tool output was truncated") {
		t.Fatalf("registry truncated shell JSON:\n%s", res.Output)
	}
	if got := len([]rune(res.Output)); got > shellToolResultMinJSONChars {
		t.Fatalf("shell JSON escaped internal minimum: got %d want <= %d", got, shellToolResultMinJSONChars)
	}
	if len(res.FullOutput) <= len(res.Output) {
		t.Fatalf("FullOutput length = %d, Output length = %d; want full event payload preserved", len(res.FullOutput), len(res.Output))
	}

	var out struct {
		JobID     string `json:"job_id"`
		Status    string `json:"status"`
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
	}
	// 60KB output exceeds the tool-result char bound (MaxChars:1) — complete-or-handle
	// keeps the job durable so the full output remains accessible (spec §6.4c).
	if out.JobID == "" {
		t.Fatalf("shell output missing job_id: want kept durable record for tool-result overflow (spec §6.4c)")
	}
	if out.Status != string(jobstore.StatusCompleted) || out.Output == "" || !out.Truncated || len(out.Output) >= 60_000 {
		t.Fatalf("shell output = %+v, want completed truncated JSON", out)
	}
}

func TestShellToolClampsSmallMaxRuntime(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 0.2; printf survived","max_wait_ms":5000,"max_runtime_ms":1}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}

	var out struct {
		Status    string `json:"status"`
		Reason    string `json:"reason"`
		TimedOut  bool   `json:"timed_out"`
		ExitCode  int    `json:"exit_code"`
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
	}
	if out.Status != string(jobstore.StatusCompleted) ||
		out.Reason != "exit_zero" ||
		out.TimedOut ||
		out.ExitCode != 0 ||
		out.Output != "survived" ||
		out.Truncated {
		t.Fatalf("shell output = %+v, want completed command before clamped runtime", out)
	}
}

func TestBufferedShellHonorsMaxRuntime(t *testing.T) {
	env := &bufferedShellEnv{}
	out, err := runBufferedShell(context.Background(), env, nil, shellArgs{
		Command:        "sleep 30",
		BlockTimeoutMS: 5000,
		MaxRuntimeMS:   1000,
	})
	if err != nil {
		t.Fatalf("runBufferedShell: %v", err)
	}
	if env.timeoutMS != 1000 {
		t.Fatalf("ExecCommand timeout = %d, want max_runtime_ms cap 1000", env.timeoutMS)
	}
	if !strings.Contains(out, "max_runtime_ms") {
		t.Fatalf("timeout output = %q, want max_runtime_ms guidance", out)
	}
}

func TestShellToolStreamingPathHonorsSessionTimeouts(t *testing.T) {
	s := newShellToolTestSession(t, SessionConfig{
		DefaultCommandTimeoutMS: 1000,
		MaxCommandTimeoutMS:     1000,
	})

	for _, tc := range []struct {
		name string
		args json.RawMessage
	}{
		{
			name: "default",
			args: json.RawMessage(`{"command":"printf start; sleep 2; printf end"}`),
		},
		{
			name: "max",
			args: json.RawMessage(`{"command":"printf start; sleep 2; printf end","max_wait_ms":5000}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "c1",
				Name:      "shell",
				Arguments: tc.args,
			})
			if res.IsError {
				t.Fatalf("shell returned error: %s", res.Output)
			}
			var out struct {
				JobID               string `json:"job_id"`
				Status              string `json:"status"`
				Reason              string `json:"reason"`
				RunningInBackground bool   `json:"running_in_background"`
			}
			if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
				t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
			}
			if out.JobID == "" ||
				out.Status != string(jobstore.StatusRunning) ||
				out.Reason != "foreground_timeout" ||
				!out.RunningInBackground {
				t.Fatalf("shell output = %+v, want foreground timeout promoted to background", out)
			}
			_, _ = s.jobManager.stop(out.JobID)
			waitForShellDone(t, s.jobManager, out.JobID)
		})
	}
}

func TestSessionCloseMarksBackgroundShellCancelledBeforeEnvCleanup(t *testing.T) {
	stateDir := t.TempDir()
	s := newShellToolTestSession(t, SessionConfig{StateDir: stateDir})
	sessionID := s.ID()

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","max_wait_ms":1000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}

	s.Close()

	st, err := jobstore.Open(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen job store: %v", err)
	}
	defer st.Close()
	recs, err := st.Load()
	if err != nil {
		t.Fatalf("load jobs: %v", err)
	}
	rec := recs[out.JobID]
	if rec == nil {
		t.Fatalf("job %s not found after close", out.JobID)
	}
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent", rec)
	}
}

func TestParentCloseMarksSubagentBackgroundShellCancelledBeforeSharedEnvCleanup(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(workDir)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession parent: %v", err)
	}
	t.Cleanup(func() { parent.Close() })

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession child: %v", err)
	}
	childID := child.ID()
	parent.subagents.track(&subagent{id: childID, sess: child})

	res := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","max_wait_ms":1000}`),
	})
	if res.IsError {
		t.Fatalf("child shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal child shell output: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatal("child background shell returned no job_id")
	}

	parent.Close()

	st, err := jobstore.Open(filepath.Join(jobsDir(stateDir, childID), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen child job store: %v", err)
	}
	defer st.Close()
	recs, err := st.Load()
	if err != nil {
		t.Fatalf("load child jobs: %v", err)
	}
	rec := recs[out.JobID]
	if rec == nil {
		t.Fatalf("child job %s not found after parent close", out.JobID)
	}
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("child record = %+v, want cancelled/stopped_by_parent", rec)
	}
}

func TestParentCloseRejectsSubagentShellStartedDuringClose(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(workDir)
	modelEntered := make(chan struct{})
	releaseModel := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseModel) })
	}
	defer release()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				close(modelEntered)
				<-releaseModel
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "late-shell",
								Name:      "shell",
								Arguments: json.RawMessage(`{"command":"sleep 30","max_wait_ms":1000}`),
								Type:      "function",
							},
						}},
					},
				}
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)

	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession parent: %v", err)
	}
	t.Cleanup(func() { parent.Close() })

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession child: %v", err)
	}
	childID := child.ID()
	parent.subagents.track(&subagent{id: childID, sess: child})

	childDone := make(chan struct{})
	go func() {
		_, _ = child.ProcessInput(context.Background(), "start late shell", nil)
		close(childDone)
	}()
	select {
	case <-modelEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("child model call did not start")
	}

	parentCloseDone := make(chan struct{})
	go func() {
		parent.Close()
		close(parentCloseDone)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !child.isClosingOrClosed() {
		if time.Now().After(deadline) {
			t.Fatal("child session was not marked closing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()

	select {
	case <-parentCloseDone:
	case <-time.After(5 * time.Second):
		t.Fatal("parent close did not finish")
	}
	select {
	case <-childDone:
	case <-time.After(5 * time.Second):
		t.Fatal("child turn did not finish")
	}

	st, err := jobstore.Open(filepath.Join(jobsDir(stateDir, childID), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen child job store: %v", err)
	}
	defer st.Close()
	recs, err := st.Load()
	if err != nil {
		t.Fatalf("load child jobs: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("late child shell created durable jobs during close: %+v", recs)
	}
}

// TestShellNegativeMaxWaitMSIsRejected pins spec §2: negative max_wait_ms is
// invalid_request on shell. The old background+block_timeout_ms combo rejection
// is gone (spec §3).
func TestShellNegativeMaxWaitMSIsRejected(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"echo hi","max_wait_ms":-1}`),
	})
	if !res.IsError {
		t.Fatalf("shell with max_wait_ms=-1 should return error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("shell error = %q, want error about max_wait_ms must be non-negative", res.Output)
	}
}

// TestShellZeroMaxWaitMSIsAccepted pins that max_wait_ms=0 is accepted
// (strict-provider safe: strict providers force every param including zero).
func TestShellZeroMaxWaitMSIsAccepted(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"echo hi","max_wait_ms":0,"max_runtime_ms":0,"description":""}`),
	})
	// max_wait_ms=0 = session default; echo hi is fast, so it returns inline.
	if res.IsError {
		t.Fatalf("shell with strict-forced zero params should succeed, got error: %s", res.Output)
	}
}

// TestParseShellToolArgsMaxWaitMS covers max_wait_ms decode at the tool boundary
// (spec §2): negative → invalid_request error; 0/absent → no error (unset);
// positive → no error (used as BlockTimeoutMS). Replaces the old
// block_timeout_ms and background combo-rejection paths.
func TestParseShellToolArgsMaxWaitMS(t *testing.T) {
	// Negative must error.
	_, err := parseShellToolArgs(map[string]any{
		"command":     "echo hi",
		"max_wait_ms": -1,
	})
	if err == nil {
		t.Fatal("parseShellToolArgs with max_wait_ms=-1: want error, got nil")
	}
	if !strings.Contains(err.Error(), "max_wait_ms must be non-negative") {
		t.Fatalf("parseShellToolArgs with max_wait_ms=-1 error = %q, want max_wait_ms must be non-negative", err.Error())
	}

	// Zero must succeed (unset).
	args, err := parseShellToolArgs(map[string]any{
		"command":     "echo hi",
		"max_wait_ms": 0,
	})
	if err != nil {
		t.Fatalf("parseShellToolArgs with max_wait_ms=0: want success, got %v", err)
	}
	if args.BlockTimeoutMS != 0 {
		t.Fatalf("BlockTimeoutMS = %d, want 0 for unset", args.BlockTimeoutMS)
	}

	// Absent must succeed (unset).
	args, err = parseShellToolArgs(map[string]any{
		"command": "echo hi",
	})
	if err != nil {
		t.Fatalf("parseShellToolArgs with absent max_wait_ms: want success, got %v", err)
	}
	if args.BlockTimeoutMS != 0 {
		t.Fatalf("BlockTimeoutMS = %d, want 0 for absent max_wait_ms", args.BlockTimeoutMS)
	}

	// Positive must succeed and set BlockTimeoutMS.
	args, err = parseShellToolArgs(map[string]any{
		"command":     "echo hi",
		"max_wait_ms": 3000,
	})
	if err != nil {
		t.Fatalf("parseShellToolArgs with max_wait_ms=3000: want success, got %v", err)
	}
	if args.BlockTimeoutMS != 3000 {
		t.Fatalf("BlockTimeoutMS = %d, want 3000", args.BlockTimeoutMS)
	}
}

// TestCompleteOrHandleEphemeral pins spec §6.4(a): a fast quiet command
// finishes within its max_wait_ms bound and returns complete output inline
// with no job_id and no durable record.
func TestCompleteOrHandleEphemeral(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'hello\n'","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID != "" {
		t.Fatalf("fast quiet command returned job_id=%q, want ephemeral (no job_id)", out.JobID)
	}
	if out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", out.Status)
	}
	if !strings.Contains(out.Output, "hello") {
		t.Fatalf("output = %q, want inline hello", out.Output)
	}
}

// TestCompleteOrHandleKeptLargeOutput pins spec §6.4(b): a fast command
// whose output exceeds the ride-whole budget (shellRideWholeBytes = 8KB)
// returns a kept result with job_id + truncated:true, and job_read_output
// returns the full retained bytes.
func TestCompleteOrHandleKeptLargeOutput(t *testing.T) {
	s := newTestSession(t)

	// yes produces >64KB output quickly; max_wait_ms:5000 gives it ample room.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 70000","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID     string `json:"job_id"`
		Status    string `json:"status"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("large-output command returned no job_id; want kept durable record")
	}
	if out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", out.Status)
	}
	if !out.Truncated {
		t.Fatalf("truncated = false, want true for large output")
	}

	// job_read_output must report TotalBytes >= 70000 — proving all bytes were
	// retained in the OutputStore (not just what fits in the tool-result).
	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","tail_bytes":1048576}`),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	var readOut struct {
		TotalBytes int64 `json:"total_bytes"`
	}
	if err := json.Unmarshal([]byte(readRes.Output), &readOut); err != nil {
		t.Fatalf("unmarshal read output: %v", err)
	}
	if readOut.TotalBytes < 70000 {
		t.Fatalf("retained TotalBytes = %d, want >= 70000", readOut.TotalBytes)
	}
}

// TestShellRideWholeThresholdIs8KiB pins the context-managed default: completed
// output above shellRideWholeBytes (8 KiB) becomes a navigable handle (job_id)
// rather than being auto-injected inline. A ~9 KiB command must return a job_id;
// under the old 64 KiB threshold it rode whole with no job_id.
func TestShellRideWholeThresholdIs8KiB(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("9 KiB output returned no job_id; want a navigable handle above the 8 KiB ride-whole threshold")
	}
	if out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", out.Status)
	}
}

// TestShellHandlePeekTailIsSmall pins the small-default-window rule: when
// completed output becomes a handle, the inline result carries only a small
// peek tail (shellDefaultTailBytes = 1 KiB), not the whole output — the full
// bytes stay retrievable via job_read_output.
func TestShellHandlePeekTailIsSmall(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID  string `json:"job_id"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("9 KiB output returned no job_id; want a handle")
	}
	if len(out.Output) > 1200 {
		t.Fatalf("inline peek tail = %d bytes, want a small peek (<= ~1 KiB), not the whole output", len(out.Output))
	}

	// The full bytes remain retrievable through the handle.
	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "r1",
		Name:      "job_read_output",
		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","tail_bytes":1048576}`),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	var readOut struct {
		TotalBytes int64 `json:"total_bytes"`
	}
	if err := json.Unmarshal([]byte(readRes.Output), &readOut); err != nil {
		t.Fatalf("unmarshal read output: %v", err)
	}
	if readOut.TotalBytes < 9000 {
		t.Fatalf("retained TotalBytes = %d, want >= 9000", readOut.TotalBytes)
	}
}

// TestCompleteOrHandleKeptToolResultOverflow pins spec §6.4(c): a command
// whose output fits in the ride-whole budget but exceeds the tool-result char
// bound still gets a durable job_id.
func TestCompleteOrHandleKeptToolResultOverflow(t *testing.T) {
	// Use MaxChars:1 so even a tiny output overflows the tool-result bound.
	s := newShellToolTestSession(t, SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"shell": {MaxChars: 1, Strategy: schema.TruncHeadTail},
		},
	})

	// 4KB < shellRideWholeBytes (8KB), so layer 1 budget is fine, but
	// MaxChars:1 ensures the tool-result char bound is exceeded (layer 2).
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"head -c 4000 </dev/zero | tr '\\0' 'x'","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID     string `json:"job_id"`
		Status    string `json:"status"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("tool-result overflow command returned no job_id; want kept durable record (spec §6.4c)")
	}
	if out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", out.Status)
	}
}

// TestCompleteOrHandleKeptNoNotification pins spec §6.4(d): a within-bound
// job that finishes and is kept (large output) has NotifyState "not_armed" —
// synchronous completion needs no duplicate terminal notification.
func TestCompleteOrHandleKeptNoNotification(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 70000","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Skip("command did not produce a kept job_id; notification test requires kept record")
	}

	rec := loadShellRecord(t, s.jobManager, out.JobID)
	if rec.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("kept within-bound job NotifyState = %q, want %q (spec §6.4d — synchronous completion must not arm notification)", rec.NotifyState, jobstore.NotifyNotArmed)
	}
}

func newShellToolTestSession(t *testing.T, cfg SessionConfig) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

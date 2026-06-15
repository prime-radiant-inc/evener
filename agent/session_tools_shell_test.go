package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestPaginateDirEntries(t *testing.T) {
	entries := []execenv.DirEntry{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"},
	}

	// First page of 2 of 5: truncated, total reported so the agent knows more exists.
	r := paginateDirEntries("/x", entries, 0, 2)
	if r.Path != "/x" || r.Total != 5 || r.Returned != 2 || len(r.Entries) != 2 || !r.Truncated || r.Offset != 0 {
		t.Fatalf("first page = %+v", r)
	}
	if r.Entries[0].Name != "a" || r.Entries[1].Name != "b" {
		t.Fatalf("first page entries = %+v", r.Entries)
	}

	// Middle page via offset.
	r = paginateDirEntries("/x", entries, 2, 2)
	if r.Returned != 2 || r.Entries[0].Name != "c" || !r.Truncated {
		t.Fatalf("offset page = %+v", r)
	}

	// Last page: exactly reaches the end, not truncated.
	r = paginateDirEntries("/x", entries, 4, 2)
	if r.Returned != 1 || r.Entries[0].Name != "e" || r.Truncated {
		t.Fatalf("last page = %+v", r)
	}

	// Offset past the end: empty page, not truncated.
	r = paginateDirEntries("/x", entries, 10, 2)
	if r.Returned != 0 || r.Truncated {
		t.Fatalf("past-end page = %+v", r)
	}

	// Unset limit (0, as strict-schema providers send) applies the default cap;
	// a small dir is returned whole and untruncated.
	r = paginateDirEntries("/x", entries, 0, 0)
	if r.Returned != 5 || r.Truncated {
		t.Fatalf("default-limit page = %+v", r)
	}
}

func TestFormatDirListing(t *testing.T) {
	entries := []execenv.DirEntry{
		{Name: "alpha", Size: 10},
		{Name: "sub", IsDir: true},
		{Name: "zeta", Size: 2048},
	}

	// A whole small directory renders like ls: one entry per line, directories end
	// with a slash, files show their size, and there is no JSON scaffolding.
	out := formatDirListing(paginateDirEntries("/d", entries, 0, 0))
	if strings.Contains(out, "{") || strings.Contains(out, `"name"`) || strings.Contains(out, "is_dir") {
		t.Fatalf("listing must be plain text, not JSON:\n%s", out)
	}
	if !strings.Contains(out, "sub/") {
		t.Fatalf("directory entry must end with a slash:\n%s", out)
	}
	if !strings.Contains(out, "alpha\t10") || !strings.Contains(out, "zeta\t2048") {
		t.Fatalf("file entry must show name and size:\n%s", out)
	}
	if !strings.Contains(out, "3 entries") {
		t.Fatalf("a complete listing reports the total count:\n%s", out)
	}

	// A truncated page reports the count and how to fetch the next page.
	many := make([]execenv.DirEntry, 50)
	for i := range many {
		many[i] = execenv.DirEntry{Name: fmt.Sprintf("f%02d", i)}
	}
	out = formatDirListing(paginateDirEntries("/d", many, 0, 10))
	if !strings.Contains(out, "10 of 50 entries") || !strings.Contains(out, "offset=10") {
		t.Fatalf("truncated listing must report count and next offset:\n%s", out)
	}
}

func TestPaginateDirEntriesStaysUnderToolCap(t *testing.T) {
	// list_dir's tool-output cap (registry defaultToolLimit) — the marshalled page
	// must stay under it so the generic char truncator never guts the entries array.
	const toolCap = 20_000

	// A large directory of realistically-named entries whose full listing would
	// blow past the cap many times over.
	var entries []execenv.DirEntry
	for i := 0; i < 5000; i++ {
		entries = append(entries, execenv.DirEntry{Name: "some-package-binary-name-" + strings.Repeat("x", 12), Size: 12345})
	}

	// Even with no caller limit (the strict-zero default), the page is bounded by a
	// record budget: returned < total, truncated true, and at least one entry.
	r := paginateDirEntries("/usr/bin", entries, 0, 0)
	if r.Total != 5000 {
		t.Fatalf("total = %d, want 5000", r.Total)
	}
	if !r.Truncated || r.Returned >= r.Total || r.Returned == 0 {
		t.Fatalf("budget-bounded page = {returned:%d total:%d truncated:%v}, want a bounded non-empty prefix", r.Returned, r.Total, r.Truncated)
	}
	out := formatDirListing(r)
	if len(out) > toolCap {
		t.Fatalf("rendered page is %d chars, exceeds the %d tool cap — would be middle-truncated", len(out), toolCap)
	}

	// Paging from the returned offset advances through the directory.
	next := paginateDirEntries("/usr/bin", entries, r.Returned, 0)
	if next.Offset != r.Returned || next.Returned == 0 || next.Entries[0].Name == "" {
		t.Fatalf("second page = {offset:%d returned:%d}, want continuation", next.Offset, next.Returned)
	}
}

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
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
		Arguments: json.RawMessage(`{"command":"head -c 60000 </dev/zero | tr '\\0' 'x'"}`),
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
		Arguments: json.RawMessage(`{"command":"sleep 0.2; printf survived","max_runtime_ms":1}`),
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
			args: json.RawMessage(`{"command":"printf start; sleep 2; printf end"}`),
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
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
								Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
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
// TestShellRejectsMaxWaitMS pins that shell no longer accepts max_wait_ms: the
// param is gone from the schema, so additionalProperties:false rejects it at the
// registry (the wait knob on shell is `background`).
func TestShellRejectsMaxWaitMS(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"echo hi","max_wait_ms":1000}`),
	})
	if !res.IsError {
		t.Fatalf("shell with max_wait_ms should be rejected, got success: %s", res.Output)
	}
}

// TestParseShellToolArgsBackground covers the wait-knob decode at the tool
// boundary: `background` decodes to shellArgs.Background; absent is false
// (strict-provider safe). max_runtime_ms keeps its negative check.
func TestParseShellToolArgsBackground(t *testing.T) {
	args, err := parseShellToolArgs(map[string]any{
		"command":    "echo hi",
		"background": true,
	})
	if err != nil {
		t.Fatalf("parseShellToolArgs with background=true: want success, got %v", err)
	}
	if !args.Background {
		t.Fatal("Background = false, want true")
	}

	// Absent background is false (the strict-provider-forced default).
	args, err = parseShellToolArgs(map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("parseShellToolArgs with absent background: want success, got %v", err)
	}
	if args.Background {
		t.Fatal("Background = true, want false for absent background")
	}

	// Negative max_runtime_ms still errors.
	if _, err := parseShellToolArgs(map[string]any{
		"command":        "echo hi",
		"max_runtime_ms": -1,
	}); err == nil {
		t.Fatal("parseShellToolArgs with max_runtime_ms=-1: want error, got nil")
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
		Arguments: json.RawMessage(`{"command":"printf 'hello\n'"}`),
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
		Arguments: json.RawMessage(`{"command":"yes x | head -c 70000"}`),
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
		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","tail_lines":1048576}`),
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
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000"}`),
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

// TestShellResultReportsOutputBytes pins the legibility metadata: a handle
// result reports total_bytes (lifetime output) so the agent knows how much
// exists beyond the peek, and dropped_bytes=0 when nothing was evicted.
func TestShellResultReportsOutputBytes(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000"}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID        string `json:"job_id"`
		TotalBytes   int64  `json:"total_bytes"`
		DroppedBytes int64  `json:"dropped_bytes"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("9 KiB output returned no job_id; want a handle")
	}
	if out.TotalBytes < 9000 {
		t.Fatalf("total_bytes = %d, want >= 9000 (lifetime output)", out.TotalBytes)
	}
	if out.DroppedBytes != 0 {
		t.Fatalf("dropped_bytes = %d, want 0 (nothing evicted under the 8 MiB cap)", out.DroppedBytes)
	}
}

// TestShellOutputStatus pins the self-describing window status: a small command
// that rides whole is "complete"; a handle whose full log is retained but only
// peeked is "windowed".
func TestShellOutputStatus(t *testing.T) {
	s := newTestSession(t)

	r1 := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'hi\n'"}`),
	})
	var o1 struct {
		JobID        string `json:"job_id"`
		OutputStatus string `json:"output_status"`
	}
	if err := json.Unmarshal([]byte(r1.Output), &o1); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, r1.Output)
	}
	if o1.JobID != "" {
		t.Fatalf("small command got job_id=%q, want ephemeral", o1.JobID)
	}
	if o1.OutputStatus != "all_retained" {
		t.Fatalf("output_status = %q, want all_retained", o1.OutputStatus)
	}

	r2 := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000"}`),
	})
	var o2 struct {
		JobID        string `json:"job_id"`
		OutputStatus string `json:"output_status"`
	}
	if err := json.Unmarshal([]byte(r2.Output), &o2); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, r2.Output)
	}
	if o2.JobID == "" {
		t.Fatalf("9 KiB output got no job_id, want a handle")
	}
	if o2.OutputStatus != "windowed" {
		t.Fatalf("output_status = %q, want windowed", o2.OutputStatus)
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
		Arguments: json.RawMessage(`{"command":"yes x | head -c 9000"}`),
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
		Arguments: json.RawMessage(`{"job_id":"` + out.JobID + `","tail_lines":1048576}`),
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
		Arguments: json.RawMessage(`{"command":"head -c 4000 </dev/zero | tr '\\0' 'x'"}`),
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
		Arguments: json.RawMessage(`{"command":"yes x | head -c 70000"}`),
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

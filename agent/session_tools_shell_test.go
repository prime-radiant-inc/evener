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
		Arguments: json.RawMessage(`{"command":"head -c 60000 </dev/zero | tr '\\0' 'x'","block_timeout_ms":5000}`),
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
		Status    string `json:"status"`
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
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
		Arguments: json.RawMessage(`{"command":"sleep 0.2; printf survived","block_timeout_ms":5000,"max_runtime_ms":1}`),
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
			args: json.RawMessage(`{"command":"printf start; sleep 2; printf end","block_timeout_ms":5000}`),
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

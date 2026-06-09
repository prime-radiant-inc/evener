package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

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

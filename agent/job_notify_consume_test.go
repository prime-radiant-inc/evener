package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// startBackgroundShellJob runs a real background shell command through the real
// tool registry and returns its job id. The command is short but not instant:
// the job must survive long enough to be a durable background job rather than
// an ephemeral inline completion.
func startBackgroundShellJob(t *testing.T, s *Session, command string) string {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "bg1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":` + mustJSONString(command) + `,"mode":"background"}`),
	})
	if res.IsError {
		t.Fatalf("background shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal shell result: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("background shell returned no job_id: %s", res.Output)
	}
	return out.JobID
}

func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestTerminalJobStatusReadConsumesPendingNotification pins the consume rule: a
// caller that reads a terminal job_status has LEARNED the job ended, so the
// queued terminal notification must not interrupt it later with the same news.
//
// The learning is durable and has its own terminal-notification state
// ("consumed"), distinct from "delivered": the jobstore's told-the-caller
// invariant stays truthful, and forensics can still tell a notification the
// model was shown from one the model went and looked up itself.
func TestPersistedTerminalJobStatusReadConsumesPendingNotification(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, "printf 'consume me\\n'")

	waitForCondition(t, 30*time.Second, "terminal notification queued", func() bool {
		return s.peekNotifications() > 0
	})

	call := llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(`{"job_id":` + mustJSONString(jobID) + `}`),
	}
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("job_status: %s", result.Output)
	}

	rec := loadShellRecord(t, s.jobManager, jobID)
	if !rec.Status.IsTerminal() {
		t.Fatalf("job status = %q, want terminal before asserting the consume", rec.Status)
	}
	if rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal_notification_state before result persistence = %q, want %q",
			rec.NotifyState, jobstore.NotifyPending)
	}
	if err := s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	rec = loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("terminal_notification_state = %q, want %q after a terminal job_status read",
			rec.NotifyState, jobstore.NotifyConsumed)
	}
	if got := s.peekNotifications(); got != 0 {
		t.Fatalf("pending notifications = %d, want 0 after persisted terminal status", got)
	}

	// The session goes idle and takes its notification turn: there must be
	// nothing left to tell it about this job.
	if s.acceptNotificationInput(context.Background()) {
		t.Fatal("a notification turn ran after the caller already read the terminal status")
	}
}

func TestTerminalJobStatusReadContinuesCurrentTurn(t *testing.T) {
	t.Parallel()

	client := llm.NewClient()
	adapter := &fakeAdapter{name: "openai"}
	client.Register(adapter)
	s, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID := startBackgroundShellJob(t, s, "printf 'done\\n'")
	waitForCondition(t, 30*time.Second, "terminal notification queued", func() bool {
		return s.peekNotifications() > 0
	})

	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "status",
						Name:      "job_status",
						Arguments: json.RawMessage(`{"job_id":` + mustJSONString(jobID) + `}`),
						Type:      "function",
					},
				}},
			}}
		},
		func(llm.Request) llm.Response { return finalResponse("continued") },
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := s.ProcessInput(ctx, "finish after checking the job", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if output != "continued" {
		t.Fatalf("ProcessInput output = %q, want continued", output)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
}

// TestRunningJobStatusReadLeavesNotificationArmed is the other half of the
// rule: reading a job that has NOT ended teaches the caller nothing about its
// completion, so the terminal notification stays armed.
func TestRunningJobStatusReadLeavesNotificationArmed(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, "sleep 30")
	t.Cleanup(func() { _, _ = s.jobManager.stop(jobID) })

	if _, err := jobStatusTool(s, map[string]any{"job_id": jobID}, 8000); err != nil {
		t.Fatalf("job_status: %v", err)
	}

	rec := loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState == jobstore.NotifyConsumed {
		t.Fatal("a running job's status read must not consume its terminal notification")
	}
}

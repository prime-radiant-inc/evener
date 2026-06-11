package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestWatchSendBuildsObserverFrame(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	captured := captureWatchSends(t, s.jobManager)
	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "watch",
		Name: "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"target":%q,"output_match":"(?i)ready","send":{"to":"job_obs","include_frame":true,"message":"observe"}}`,
			shellOut.JobID,
		)),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error: %s", watchRes.Output)
	}

	s.jobManager.feedJobOutput(shellOut.JobID, []byte("server READY\n"))
	sends := captured()
	if len(sends) != 1 {
		t.Fatalf("expected one watch send, got %#v", sends)
	}
	if sends[0].Target != "job_obs" {
		t.Fatalf("watch send target = %q, want job_obs", sends[0].Target)
	}
	if !sends[0].FromWatch || !sends[0].Background || !sends[0].BackgroundSet {
		t.Fatalf("watch send args = %+v, want background watch delivery", sends[0])
	}
	if !strings.Contains(sends[0].Message, "observe") || !strings.Contains(sends[0].Message, "server READY") {
		t.Fatalf("watch send message = %q, want configured message and trigger context", sends[0].Message)
	}
}

func captureWatchSends(t *testing.T, jm *jobManager) func() []sendMessageArgs {
	t.Helper()
	var mu sync.Mutex
	var sent []sendMessageArgs

	seedCommonWatchSendTargets(t, jm)
	jm.mu.Lock()
	original := jm.send
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, a)
		return sendMessageResult{}
	}
	jm.mu.Unlock()

	t.Cleanup(func() {
		jm.mu.Lock()
		jm.send = original
		jm.mu.Unlock()
	})

	return func() []sendMessageArgs {
		mu.Lock()
		defer mu.Unlock()
		return append([]sendMessageArgs(nil), sent...)
	}
}

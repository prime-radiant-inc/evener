package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
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
	if !strings.Contains(res.Output, "job_") {
		t.Fatalf("expected a job_id in %q", res.Output)
	}

	jobID := shellToolOutputField(res.Output, "job_id")
	if jobID == "" ||
		shellToolOutputField(res.Output, "status") != string(jobstore.StatusRunning) ||
		shellToolOutputField(res.Output, "running_in_background") != "true" {
		t.Fatalf("shell output = %q, want running background job", res.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(jobID)
		waitForShellDone(t, s.jobManager, jobID)
	})

	jobs := s.jobManager.list(listFilter{})
	if len(jobs) != 1 || jobs[0].JobID != jobID || jobs[0].Status != jobstore.StatusRunning {
		t.Fatalf("jobs = %+v, want one running shell job %q", jobs, jobID)
	}
}

func shellToolOutputField(output, key string) string {
	prefix := key + "="
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, prefix) {
			return strings.TrimPrefix(field, prefix)
		}
	}
	return ""
}

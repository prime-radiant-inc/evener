package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// TestS3Cov_JobReadOutput_Grep drives the real job_read_output tool with a grep
// pattern so readJobOutputSnapshot's grep arm (grepOutput + the follow-up record
// lookup) is exercised end to end on a real completed job.
func TestS3Cov_JobReadOutput_Grep(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// Emit distinct lines so a grep pattern has a non-trivial match set.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "sh",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'NEEDLE-one\\n'; yes x | head -c 9000; printf '\\nNEEDLE-two\\n'"}`),
	})
	var started struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(toolResultJSON(res), &started)
	if started.JobID == "" {
		// A short foreground command may ride inline; skip if no durable job.
		t.Skipf("no durable job id (inline output): %s", res.Output)
	}

	// Give the job a moment to finish and flush its log.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := readJobStatus(t, s, started.JobID)
		if st.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	readRes := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID:        "r",
		Arguments: json.RawMessage(`{"job_id":"` + started.JobID + `","grep":"NEEDLE","tail_lines":500}`),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output grep error: %s", readRes.Output)
	}
	if !strings.Contains(readRes.Output, "NEEDLE") {
		t.Fatalf("expected grep matches in output: %s", readRes.Output)
	}
}

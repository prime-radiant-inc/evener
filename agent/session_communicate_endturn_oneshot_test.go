package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// endTurnWarningWithRunningJob starts a background shell on s, ends the turn
// through the REAL tool registry, and returns the warning the model would
// actually see plus the job id.
//
// It deliberately drives s.reg.ExecuteCall rather than calling
// runningJobsEndTurnWarning: a session flag that never reaches the communicate
// handler produces a correct-looking helper and a wrong warning, which is the
// class of defect this test exists to catch.
func endTurnWarningWithRunningJob(t *testing.T, s *Session) (warning, jobID string) {
	t.Helper()

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" || shellOut.Status != "running" {
		t.Fatalf("shell output = %+v, want a running job", shellOut)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, communicateCallArgs("end-turn-warning", map[string]any{
		"message":  "done for now",
		"end_turn": true,
	}))
	if res.IsError {
		t.Fatalf("communicate error: %s", res.Output)
	}
	var resp map[string]any
	if err := json.Unmarshal(toolResultJSON(res), &resp); err != nil {
		t.Fatalf("unmarshal communicate output: %v", err)
	}
	got, ok := resp["warning"].(string)
	if !ok || got == "" {
		t.Fatalf("expected a non-empty warning naming the running job, got: %v", resp)
	}
	if !strings.Contains(got, shellOut.JobID) {
		t.Fatalf("warning = %q, want it to name job id %q", got, shellOut.JobID)
	}
	return got, shellOut.JobID
}

// TestCommunicate_EndTurnWarningIsHonestInOneShot pins the correction to the
// warn-first text for a session whose process exits with the turn. The old
// wording promised "each job remains notification-armed and will report
// separately on completion" unconditionally; under `evener run` there is no
// separately, and a job still running when the drain gives up is killed rather
// than reported on. Telling the model otherwise is what led it to end the turn
// on a live server in Terminal-Bench trial hf-model-inference (#297).
func TestCommunicate_EndTurnWarningIsHonestInOneShot(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))

	warning, _ := endTurnWarningWithRunningJob(t, s)

	if strings.Contains(warning, "report separately on completion") {
		t.Fatalf("one-shot warning = %q, want it NOT to promise separate reporting: this process exits with the turn", warning)
	}
	if !strings.Contains(warning, "killed at exit") {
		t.Fatalf("one-shot warning = %q, want it to say a job still running is killed at exit", warning)
	}
}

// TestCommunicate_EndTurnWarningKeepsTheServeContract pins the other side: a
// session that outlives its turn genuinely does report background jobs later,
// so docs/job-control.md's notification contract is correct there and the
// warning must keep saying so.
func TestCommunicate_EndTurnWarningKeepsTheServeContract(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))

	warning, _ := endTurnWarningWithRunningJob(t, s)

	if !strings.Contains(warning, "report separately on completion") {
		t.Fatalf("serve warning = %q, want today's notification contract preserved", warning)
	}
	if strings.Contains(warning, "killed at exit") {
		t.Fatalf("serve warning = %q, want no claim that the job dies: the session outlives the turn", warning)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func callTool(s *Session, name string, args map[string]any) tool.ExecResult {
	raw, _ := json.Marshal(args)
	return s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "c", Name: name, Arguments: raw})
}

func isWaitRefused(res tool.ExecResult) bool {
	return res.IsError && errors.Is(res.Err, errWaitRefused)
}

func TestRepeatedJobStatusOnUnchangedRunningJobIsRefused(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, "ls; sleep 30")
	t.Cleanup(func() { _, _ = s.jobManager.stop(jobID) })

	first := callTool(s, "job_status", map[string]any{"target": jobID})
	if first.IsError {
		t.Fatalf("first job_status on a target was refused: %s", first.Output)
	}
	second := callTool(s, "job_status", map[string]any{"target": jobID})
	if !isWaitRefused(second) {
		t.Fatalf("repeat job_status on an unchanged running job executed: %+v", second)
	}
	if len(second.ToolState) != 0 {
		t.Fatalf("refusal carried a status projection: %s", second.ToolState)
	}

	s.jobManager.mu.Lock()
	s.jobManager.running[jobID].rec.Phase = "stalled"
	s.jobManager.mu.Unlock()
	third := callTool(s, "job_status", map[string]any{"target": jobID})
	if third.IsError {
		t.Fatalf("job_status after the phase changed was refused: %s", third.Output)
	}
	if again := callTool(s, "job_status", map[string]any{"target": jobID}); !isWaitRefused(again) {
		t.Fatalf("repeat after the new phase executed: %+v", again)
	}
}

func TestJobStatusAfterNotificationIsAllowed(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, "ls; sleep 30")
	t.Cleanup(func() { _, _ = s.jobManager.stop(jobID) })

	callTool(s, "job_status", map[string]any{"target": jobID})
	if res := callTool(s, "job_status", map[string]any{"target": jobID}); !isWaitRefused(res) {
		t.Fatalf("setup: repeat was not refused: %+v", res)
	}
	s.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: jobID, WatchID: "w1"})
	if res := callTool(s, "job_status", map[string]any{"target": jobID}); res.IsError {
		t.Fatalf("job_status after a notification about the job was refused: %s", res.Output)
	}
}

func TestFirstJobStatusOnQuietDelegateIsAllowed(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	id := "dlg_quiet_wait"
	seedStableToolRunningDelegate(t, s, id, "", time.Unix(10, 0).UTC())

	if res := callTool(s, "job_status", map[string]any{"target": id}); res.IsError {
		t.Fatalf("first job_status on a quiet delegate was refused: %s", res.Output)
	}
}

func TestSleepOnlyShellCommandIsRefused(t *testing.T) {
	t.Parallel()
	refused := []string{
		"sleep 30",
		"sleep 1.5",
		"sleep 5m",
		"sleep 30; echo done",
		"sleep 30 && echo done",
		"echo waiting; sleep 30",
		"sleep 10 && sleep 10",
		"while true; do sleep 60; done",
		"for i in 1 2 3; do sleep 10; done",
		"  sleep   30  ",
	}
	for _, cmd := range refused {
		t.Run("refused/"+cmd, func(t *testing.T) {
			t.Parallel()
			s := newTestSession(t)
			res := callTool(s, "shell", map[string]any{"command": cmd})
			if !isWaitRefused(res) {
				t.Fatalf("%q was not refused: %+v", cmd, res)
			}
			jobs, _, err := s.jobManager.listWithError(listFilter{IncludeNested: true})
			if err != nil || len(jobs) != 0 {
				t.Fatalf("refused command started a job: jobs=%d err=%v", len(jobs), err)
			}
		})
	}
	allowed := []string{
		"sleep 0.01 && ls",
		"sleep 0.01; ls",
		"until true; do sleep 0.01; done",
		"for i in 1 2; do sleep 0.01; printf x > out.txt; done",
		"printf ok",
		"sleep 0.01 | cat",
		"sleep $(printf 0.01)",
	}
	for _, cmd := range allowed {
		t.Run("allowed/"+cmd, func(t *testing.T) {
			t.Parallel()
			s := newTestSession(t)
			res := callTool(s, "shell", map[string]any{"command": cmd})
			if isWaitRefused(res) {
				t.Fatalf("%q was refused: %+v", cmd, res)
			}
		})
	}
}

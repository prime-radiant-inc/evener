package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// idleShellCommand is a long-running command that does nothing and is not
// sleep-only by the wait classifier's rules, for tests that need a job that
// stays running until the test stops it.
const idleShellCommand = "exec tail -f /dev/null"

// idleShellJSON is shell tool arguments running idleShellCommand; fields is
// extra JSON members such as `,"mode":"background"`.
func idleShellJSON(fields string) json.RawMessage {
	return json.RawMessage(`{"command":` + mustJSONString(idleShellCommand) + fields + `}`)
}

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
		"exec sleep 30",
		"/bin/sleep 30",
		"command sleep 30",
		"sleep infinity",
		"exec sleep 30 && echo done",
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
		"echo 'first;sleep 30;echo last'",
		`printf "%s\n" "a; sleep 30"`,
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

func TestRepeatedJobStatusOnUnchangedRunningDelegateIsRefused(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	id := "dlg_repeat_wait"
	seedStableToolRunningDelegate(t, s, id, "", time.Unix(10, 0).UTC())

	if res := callTool(s, "job_status", map[string]any{"target": id}); res.IsError {
		t.Fatalf("first job_status on a delegate was refused: %s", res.Output)
	}
	if res := callTool(s, "job_status", map[string]any{"target": id}); !isWaitRefused(res) {
		t.Fatalf("repeat job_status on an unchanged running delegate executed: %+v", res)
	}
}

// A delegate that Evener reports as quiet may still be working, and the
// prompt tells the agent to check it before stopping it. That check follows an
// attention report, so it must run even when the agent polled earlier.
func TestJobStatusOnDelegateAfterAttentionReportIsAllowed(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t, withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))
	id := "dlg_quiet_recheck"
	seedStableToolRunningDelegate(t, s, id, "", time.Unix(10, 0).UTC())

	callTool(s, "job_status", map[string]any{"target": id})
	if res := callTool(s, "job_status", map[string]any{"target": id}); !isWaitRefused(res) {
		t.Fatalf("setup: repeat was not refused: %+v", res)
	}
	if _, err := s.appendDelegateNotificationDurably("delegate:dlg_quiet_recheck/delivery/1", `<delegate-notification delegate_id="dlg_quiet_recheck">quiet</delegate-notification>`); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if res := callTool(s, "job_status", map[string]any{"target": id}); res.IsError {
		t.Fatalf("job_status after a delegate attention report was refused: %s", res.Output)
	}
}

// Only a session that has a human partner is offered end_reason on its result
// tool; advice to pass it elsewhere fails schema validation.
func TestWaitGuidanceNamesEndReasonOnlyWhereTheResultToolOffersIt(t *testing.T) {
	t.Parallel()
	child := minimalTestSessionConfig(t)
	child.spawn.parentSessionID = "01PARENT"
	for name, s := range map[string]*Session{
		"root":     newTestSession(t),
		"delegate": newSession(t, withConfig(child)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			offered := false
			if def := s.reg.Get(s.resultToolName()); def != nil {
				props, _ := def.Definition.Parameters["properties"].(map[string]any)
				_, offered = props["end_reason"]
			}
			if offered != s.hasHumanPartnerToAsk() {
				t.Fatalf("setup: end_reason offered = %v, human partner = %v", offered, s.hasHumanPartnerToAsk())
			}
			mentions := func(text string) bool { return strings.Contains(text, "end_reason") }
			for _, toolName := range []string{"job_status", "job_list"} {
				if got := mentions(s.reg.Get(toolName).Definition.Description); got != offered {
					t.Errorf("%s description names end_reason = %v, result tool offers it = %v", toolName, got, offered)
				}
			}
			refusal := callTool(s, "shell", map[string]any{"command": "sleep 30"})
			if !isWaitRefused(refusal) {
				t.Fatalf("sleep not refused: %+v", refusal)
			}
			if got := mentions(refusal.Output); got != offered {
				t.Errorf("shell refusal names end_reason = %v, result tool offers it = %v", got, offered)
			}
		})
	}
}

func TestJobStatusAfterSelfTimerWakeIsAllowed(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, idleShellCommand)
	t.Cleanup(func() { _, _ = s.jobManager.stop(jobID) })

	callTool(s, "job_status", map[string]any{"target": jobID})
	if res := callTool(s, "job_status", map[string]any{"target": jobID}); !isWaitRefused(res) {
		t.Fatalf("setup: repeat was not refused: %+v", res)
	}
	// A self timer carries no job id: it is not about any one job.
	s.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, WatchID: "timer1", Fires: 1})
	if res := callTool(s, "job_status", map[string]any{"target": jobID}); res.IsError {
		t.Fatalf("scheduled recheck after a self-timer wake was refused: %s", res.Output)
	}
}

func TestJobStatusOnDelegateFollowsNewActivityReportOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	id := "dlg_activity_reports"
	start := time.Unix(10, 0).UTC()
	seedStableToolRunningDelegate(t, s, id, "", start)
	lease := delegateLease{delegateID: id, generation: 1}

	callTool(s, "job_status", map[string]any{"target": id})
	if res := callTool(s, "job_status", map[string]any{"target": id}); !isWaitRefused(res) {
		t.Fatalf("repeat with no new activity report executed: %+v", res)
	}
	if err := s.delegateController.ReportActivityPhase(lease, start.Add(time.Minute), "tool_running"); err != nil {
		t.Fatal(err)
	}
	if res := callTool(s, "job_status", map[string]any{"target": id}); res.IsError {
		t.Fatalf("job_status after a new activity report was refused: %s", res.Output)
	}
	if res := callTool(s, "job_status", map[string]any{"target": id}); !isWaitRefused(res) {
		t.Fatalf("repeat after consuming the activity report executed: %+v", res)
	}
}

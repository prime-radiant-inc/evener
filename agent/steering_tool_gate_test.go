package agent

import (
	"strings"
	"testing"

	taskpkg "primeradiant.com/serf/agent/task"
)

// TestSelfCompactNudgeFallsBackWhenToolMissing pins the rule ruled 2026-08-06:
// canned steering never instructs a tool the session does not have. A session
// whose registry lost compact_context still needs the pressure warning, so the
// nudge keeps its warning and drops the call instruction.
func TestSelfCompactNudgeFallsBackWhenToolMissing(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.reg.Remove("compact_context")
	s.contextMgr.WarnThreshold = 0

	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge did not fire")
	}
	got := s.SteeringQueueSnapshot()
	if len(got) != 1 {
		t.Fatalf("steering queue = %+v, want one nudge", got)
	}
	if strings.Contains(got[0].Text, "compact_context") {
		t.Fatalf("nudge names a tool this session cannot call: %q", got[0].Text)
	}
	for _, want := range []string{"low on context-window headroom", "Summarize and drop stale context in your next messages"} {
		if !strings.Contains(got[0].Text, want) {
			t.Fatalf("tool-free nudge = %q, want it to contain %q", got[0].Text, want)
		}
	}
}

// TestSelfCompactNudgeNamesToolWhenPresent is the other half: the wording that
// names the tool is what a session that HAS the tool must still get.
func TestSelfCompactNudgeNamesToolWhenPresent(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.contextMgr.WarnThreshold = 0

	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge did not fire")
	}
	got := s.SteeringQueueSnapshot()
	if len(got) != 1 || !strings.Contains(got[0].Text, "compact_context") {
		t.Fatalf("steering queue = %+v, want a nudge naming compact_context", got)
	}
}

// TestCurrentTaskSteeringFallsBackWhenToolMissing covers the task-step reminder,
// whose closing line is a task_list call instruction.
func TestCurrentTaskSteeringFallsBackWhenToolMissing(t *testing.T) {
	t.Parallel()
	task := taskpkg.Task{ID: 3, Description: "ship it"}

	withTool := formatCurrentTaskSteering(task, true)
	if !strings.Contains(withTool, "task_list") {
		t.Fatalf("steering with the tool present = %q, want it to name task_list", withTool)
	}
	withoutTool := formatCurrentTaskSteering(task, false)
	if strings.Contains(withoutTool, "task_list") {
		t.Fatalf("steering names a tool this session cannot call: %q", withoutTool)
	}
	if !strings.Contains(withoutTool, "ship it") {
		t.Fatalf("tool-free steering lost the task itself: %q", withoutTool)
	}
}

// TestTaskNudgeSuppressedWhenToolMissing: the "you have a task_list tool"
// suggestion is nothing BUT a tool instruction, so a session without the tool
// gets no reminder at all rather than tool-free filler.
func TestTaskNudgeSuppressedWhenToolMissing(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.reg.Remove("task_list")
	s.mu.Lock()
	s.totalRounds = 10
	s.mu.Unlock()

	if msg, kind := s.maybeInjectTaskReminder(); msg != "" {
		t.Fatalf("task nudge fired without the tool: kind=%s msg=%q", kind, msg)
	}

	s2 := newTestSession(t)
	s2.mu.Lock()
	s2.totalRounds = 10
	s2.mu.Unlock()
	msg, _ := s2.maybeInjectTaskReminder()
	if !strings.Contains(msg, "task_list") {
		t.Fatalf("task nudge with the tool present = %q, want it to name task_list", msg)
	}
}

// TestTasksDoneReminderUsesTheSessionResultToolName: the reminder that closes a
// finished task list points at the result tool, whose name a session may
// rename. Hardcoding "communicate" names a tool the session does not have.
func TestTasksDoneReminderUsesTheSessionResultToolName(t *testing.T) {
	t.Parallel()
	if got := taskReminderAllDone("report_result"); !strings.Contains(got, "report_result") {
		t.Fatalf("all-done reminder = %q, want it to name the session's result tool", got)
	}
	if got := taskReminderAllDone("report_result"); strings.Contains(got, "communicate") {
		t.Fatalf("all-done reminder still hardcodes communicate: %q", got)
	}
}

// TestTranscriptPointerSteeringSuppressedWithoutTheTool covers the
// pre-compaction pointer, which is a read_transcript usage recipe end to end.
func TestTranscriptPointerSteeringSuppressedWithoutTheTool(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.stateDir = t.TempDir()
	s.reg.Remove("read_transcript")
	s.steerCompactionTranscriptReminder()
	if got := s.SteeringQueueSnapshot(); len(got) != 0 {
		t.Fatalf("transcript pointer steering = %+v, want none without read_transcript", got)
	}

	s2 := newTestSession(t)
	s2.stateDir = t.TempDir()
	s2.steerCompactionTranscriptReminder()
	got := s2.SteeringQueueSnapshot()
	if len(got) != 1 || !strings.Contains(got[0].Text, "read_transcript") {
		t.Fatalf("transcript pointer steering = %+v, want the read_transcript recipe", got)
	}
}

// TestCompactionMetaReportsTheSessionsTranscriptTools pins the wiring the
// contextmgr gate depends on: the checkpoint's recovery instruction is worded
// from this list, so a session that never reports its tools would silently go
// back to naming tools it may not have.
func TestCompactionMetaReportsTheSessionsTranscriptTools(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.stateDir = t.TempDir()
	if got := s.buildCompactionMeta().AvailableTranscriptTools; !hasString(got, "read_transcript") {
		t.Fatalf("AvailableTranscriptTools = %v, want read_transcript for a session that serves it", got)
	}

	s.reg.Remove("read_transcript")
	if got := s.buildCompactionMeta().AvailableTranscriptTools; hasString(got, "read_transcript") {
		t.Fatalf("AvailableTranscriptTools = %v, want read_transcript gone once the registry drops it", got)
	}
}

// TestJobNotificationOutputPointerGatesOnTranscriptTool: a job notification
// tells the agent where the job's output is. Without read_transcript that
// pointer is unfollowable, so the notification says nothing about it.
func TestJobNotificationOutputPointerGatesOnTranscriptTool(t *testing.T) {
	t.Parallel()
	n := jobNotification{JobID: "job_1", Status: "completed", TranscriptRef: "local:abc"}

	withTool := formatJobNotificationBlock(n, notificationExcerpt{}, true)
	if !strings.Contains(withTool, "read_transcript") {
		t.Fatalf("notification with the tool present = %q, want the read_transcript pointer", withTool)
	}
	withoutTool := formatJobNotificationBlock(n, notificationExcerpt{}, false)
	if strings.Contains(withoutTool, "read_transcript") {
		t.Fatalf("notification names a tool this session cannot call: %q", withoutTool)
	}
	if !strings.Contains(withoutTool, "job_1") {
		t.Fatalf("tool-free notification lost the job itself: %q", withoutTool)
	}
}

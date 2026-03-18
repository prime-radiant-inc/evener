package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestTaskReminderFull(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	store.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: TaskTypeResearch, Description: "B", Prompt: "b", DependsOn: []int{1}},
	})
	store.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})

	msg := taskReminderFull(store)
	if msg == "" {
		t.Fatal("expected non-empty full reminder")
	}
	if !strings.Contains(msg, "done") || !strings.Contains(msg, "open") {
		t.Fatalf("full reminder should list all statuses: %s", msg)
	}
	if !strings.Contains(msg, "depends_on: [1]") {
		t.Fatalf("full reminder should show dependencies: %s", msg)
	}
}

func TestTaskReminderFull_Empty(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	msg := taskReminderFull(store)
	if msg != "" {
		t.Fatalf("expected empty reminder for empty store, got: %s", msg)
	}
}

func TestTaskReminderForInactivity(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	store.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "a"},
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "b"},
	})
	store.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}})

	msg := taskReminderForInactivity(store)
	if msg == "" {
		t.Fatal("expected non-empty reminder")
	}
	if !strings.Contains(msg, "Task A") {
		t.Fatalf("reminder should mention in-progress task: %s", msg)
	}
	// Progress: 0 done out of 2 (in_progress is not done).
	if !strings.Contains(msg, "0/2") {
		t.Fatalf("reminder should show progress 0/2: %s", msg)
	}
}

func TestTaskReminderForInactivity_Empty(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	msg := taskReminderForInactivity(store)
	if msg != "" {
		t.Fatalf("expected empty reminder for empty store, got: %s", msg)
	}
}

func TestTaskReminderNudge(t *testing.T) {
	msg := taskReminderNudge()
	if msg == "" {
		t.Fatal("expected non-empty nudge")
	}
	if !strings.Contains(msg, "task_list") {
		t.Fatalf("nudge should mention task_list: %s", msg)
	}
}

func TestMaybeInjectTaskReminder_NudgeAfter10Rounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// 9 rounds — no nudge yet.
	sess.totalRounds = 9
	if msg := sess.maybeInjectTaskReminder(); msg != "" {
		t.Fatalf("expected no nudge at 9 rounds, got: %s", msg)
	}

	// At 10 rounds — nudge fires.
	sess.totalRounds = 10
	msg := sess.maybeInjectTaskReminder()
	if msg == "" || !strings.Contains(msg, "task_list") {
		t.Fatalf("expected nudge at 10 rounds, got: %q", msg)
	}

	// Second call — nudge should not fire again.
	sess.totalRounds = 15
	if msg := sess.maybeInjectTaskReminder(); msg != "" {
		t.Fatalf("nudge should fire only once, got: %s", msg)
	}
}

func TestMaybeInjectTaskReminder_InactivityAfter5Rounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Create tasks via store directly (simulating prior tool use).
	store := sess.getOrCreateTaskStore()
	store.Append([]TaskInput{{Type: TaskTypeResearch, Description: "A", Prompt: "a"}})
	sess.taskToolEverUsed = true
	sess.taskToolLastRound = 0

	// At 4 rounds — no reminder.
	sess.totalRounds = 4
	if msg := sess.maybeInjectTaskReminder(); msg != "" {
		t.Fatalf("expected no reminder at 4 rounds, got: %s", msg)
	}

	// At 5 rounds — reminder fires.
	sess.totalRounds = 5
	msg := sess.maybeInjectTaskReminder()
	if msg == "" {
		t.Fatal("expected inactivity reminder at 5 rounds")
	}
}

func TestMaybeInjectTaskReminder_NoNudgeIfEverUsed(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.taskToolEverUsed = true
	sess.totalRounds = 15

	// No tasks exist, tool was used before — no nudge, no inactivity reminder.
	if msg := sess.maybeInjectTaskReminder(); msg != "" {
		t.Fatalf("expected no reminder when tool was used but no tasks: %s", msg)
	}
}

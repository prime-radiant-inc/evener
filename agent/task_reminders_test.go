package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

func TestFormatCurrentTaskSteering(t *testing.T) {
	task := Task{
		ID:              3,
		Description:     "Note what must be preserved",
		Prompt:          "Before you do anything else, identify what determines\nwhether the work is correct.",
		ReasoningEffort: "low",
	}
	msg := formatCurrentTaskSteering(task)

	wants := []string{
		"<SYSTEM-REMINDER>",
		`<CURRENT-TASK id="3">`,
		"<TITLE>Note what must be preserved</TITLE>",
		"<INSTRUCTIONS>",
		"Before you do anything else",
		"whether the work is correct.",
		"</INSTRUCTIONS>",
		"</CURRENT-TASK>",
		"task_list to mark task 3 as done",
		"when this step is complete.",
		"</SYSTEM-REMINDER>",
	}
	for _, w := range wants {
		if !strings.Contains(msg, w) {
			t.Errorf("steering message missing %q. Got:\n%s", w, msg)
		}
	}

	// Effort level MUST NOT appear in the message — it is applied to the
	// session internally via SetReasoningEffort, not rendered for the model.
	unwants := []string{"REASONING-EFFORT", "reasoning: low", "reasoning_effort"}
	for _, u := range unwants {
		if strings.Contains(msg, u) {
			t.Errorf("steering message should not contain %q. Got:\n%s", u, msg)
		}
	}
}

func TestFormatCurrentTaskSteering_NoPrompt(t *testing.T) {
	task := Task{
		ID:          1,
		Description: "Bare task",
	}
	msg := formatCurrentTaskSteering(task)

	if strings.Contains(msg, "<INSTRUCTIONS>") {
		t.Errorf("no-prompt task should omit INSTRUCTIONS block: %s", msg)
	}
	if !strings.Contains(msg, "<TITLE>Bare task</TITLE>") {
		t.Errorf("expected TITLE to be present: %s", msg)
	}
	if !strings.Contains(msg, "task_list to mark task 1 as done") {
		t.Errorf("expected task_list completion instruction: %s", msg)
	}
}

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
	if !strings.HasPrefix(msg, "<SYSTEM-REMINDER>") {
		t.Fatalf("full reminder should start with <SYSTEM-REMINDER>: %s", msg)
	}
	if !strings.HasSuffix(strings.TrimRight(msg, "\n"), "</SYSTEM-REMINDER>") {
		t.Fatalf("full reminder should end with </SYSTEM-REMINDER>: %s", msg)
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

func TestTaskReminderForInactivity_InProgressReFiresCurrentTaskSteering(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	store.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Prompt for A"},
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "Prompt for B"},
	})
	store.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}})

	msg := taskReminderForInactivity(store)
	if msg == "" {
		t.Fatal("expected non-empty reminder when a task is in_progress")
	}
	// The inactivity reminder must be the same content as formatCurrentTaskSteering
	// for the currently in_progress task, so nothing new gets fabricated.
	current, ok := store.CurrentInProgress()
	if !ok {
		t.Fatal("expected an in_progress task in the fixture")
	}
	want := formatCurrentTaskSteering(current)
	if msg != want {
		t.Fatalf("inactivity reminder should equal formatCurrentTaskSteering(current)\nwant:\n%s\ngot:\n%s", want, msg)
	}
}

func TestTaskReminderForInactivity_NoInProgressReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	store.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "a"},
	})
	// No task set to in_progress — reminder skipped.
	msg := taskReminderForInactivity(store)
	if msg != "" {
		t.Fatalf("expected empty reminder when no task is in_progress, got: %s", msg)
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
	if !strings.HasPrefix(msg, "<SYSTEM-REMINDER>") {
		t.Fatalf("nudge should start with <SYSTEM-REMINDER>: %s", msg)
	}
	if !strings.HasSuffix(strings.TrimRight(msg, "\n"), "</SYSTEM-REMINDER>") {
		t.Fatalf("nudge should end with </SYSTEM-REMINDER>: %s", msg)
	}
}

func TestTaskReminderAllDone(t *testing.T) {
	msg := taskReminderAllDone()
	if !strings.HasPrefix(msg, "<SYSTEM-REMINDER>") {
		t.Fatalf("should start with <SYSTEM-REMINDER>: %s", msg)
	}
	if !strings.HasSuffix(strings.TrimRight(msg, "\n"), "</SYSTEM-REMINDER>") {
		t.Fatalf("should end with </SYSTEM-REMINDER>: %s", msg)
	}
	if !strings.Contains(msg, "completed all tasks") {
		t.Fatalf("should say 'completed all tasks': %s", msg)
	}
	if !strings.Contains(msg, "communicate") {
		t.Fatalf("should mention communicate tool: %s", msg)
	}
}

func TestMaybeInjectTaskReminder_NudgeAfter10Rounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestMaybeInjectTaskReminder_InactivityAfter25Rounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Create tasks via store directly (simulating prior tool use).
	store := sess.getOrCreateTaskStore()
	store.Append([]TaskInput{{Type: TaskTypeResearch, Description: "A", Prompt: "a"}})
	// An inactivity reminder only fires when there's a task in_progress — the
	// point of the reminder is to re-state the current step.
	if err := store.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}}); err != nil {
		t.Fatalf("mark task 1 in_progress: %v", err)
	}
	sess.taskToolEverUsed = true
	sess.taskToolLastRound = 0

	// At 24 rounds — no reminder.
	sess.totalRounds = 24
	if msg := sess.maybeInjectTaskReminder(); msg != "" {
		t.Fatalf("expected no reminder at 24 rounds, got: %s", msg)
	}

	// At 25 rounds — reminder fires.
	sess.totalRounds = 25
	msg := sess.maybeInjectTaskReminder()
	if msg == "" {
		t.Fatal("expected inactivity reminder at 25 rounds")
	}
}

func TestMaybeInjectTaskReminder_NoNudgeIfEverUsed(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

package agent

import (
	"strings"
	"testing"

	taskpkg "primeradiant.com/serf/agent/task"
)

// CoreToolNames stands up a throwaway session and returns the schema-bearing
// core tool names.
func TestS5Cov_CoreToolNames(t *testing.T) {
	names, err := CoreToolNames()
	if err != nil {
		t.Fatalf("CoreToolNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("expected at least one schema-bearing core tool")
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "read_file") {
		t.Errorf("expected read_file among core tools, got %v", names)
	}
}

// builtinAgents parses the embedded agent definitions.
func TestS5Cov_BuiltinAgents(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected at least one builtin agent")
	}
	for name, a := range agents {
		if name == "" || a.Name == "" {
			t.Errorf("agent keyed/named empty: %q -> %+v", name, a)
		}
	}
}

func TestS5Cov_TaskReminderFull(t *testing.T) {
	store := taskpkg.NewTaskStore(t.TempDir(), "s")
	// Empty store yields no reminder.
	if got := taskReminderFull(store); got != "" {
		t.Errorf("empty store should yield no reminder, got %q", got)
	}
	added, err := store.Append([]taskpkg.TaskInput{
		{Description: "first", Prompt: "do first", ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]taskpkg.TaskInput{
		{Description: "second", Prompt: "do second", DependsOn: []int{added[0].ID}},
	}); err != nil {
		t.Fatal(err)
	}
	// Add a progress note to the first task.
	if err := store.Update([]taskpkg.TaskUpdate{{ID: added[0].ID, Status: taskpkg.TaskOpen, Notes: "started it"}}); err != nil {
		t.Fatal(err)
	}

	out := taskReminderFull(store)
	for _, want := range []string{"Task list:", "first", "[high]", "depends_on", "note: started it"} {
		if !strings.Contains(out, want) {
			t.Errorf("reminder missing %q:\n%s", want, out)
		}
	}
}

func TestS5Cov_FormatCurrentTaskSteering(t *testing.T) {
	out := formatCurrentTaskSteering(taskpkg.Task{ID: 7, Description: "title", Prompt: "  instructions  "})
	for _, want := range []string{`id="7"`, "<TITLE>title</TITLE>", "<INSTRUCTIONS>", "instructions", "mark task 7 as done"} {
		if !strings.Contains(out, want) {
			t.Errorf("steering missing %q:\n%s", want, out)
		}
	}
}

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestTaskWorkflow_PopulateAndAutoStart(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "workflow-test")
	store.AutoVerify = false
	store.Load()

	templates := []TaskTemplate{
		{Title: "Inventory", Prompt: "List files", ReasoningEffort: "low"},
		{Title: "Plan", Prompt: "Analyze task", ReasoningEffort: "xhigh"},
		{Title: "Delegate", Prompt: "Spawn implementer", ReasoningEffort: "low"},
	}

	if err := store.PopulateFromTemplates(templates, nil); err != nil {
		t.Fatal(err)
	}

	// First task should be auto-started.
	current, ok := store.CurrentInProgress()
	if !ok {
		t.Fatal("expected a task in progress")
	}
	if current.Description != "Inventory" {
		t.Errorf("current task = %q, want Inventory", current.Description)
	}
	if current.ReasoningEffort != "low" {
		t.Errorf("effort = %q, want low", current.ReasoningEffort)
	}
}

func TestTaskWorkflow_AdvanceSequence(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "advance-test")
	store.AutoVerify = false
	store.Load()

	store.PopulateFromTemplates([]TaskTemplate{
		{Title: "Step 1", Prompt: "First", ReasoningEffort: "low"},
		{Title: "Step 2", Prompt: "Second", ReasoningEffort: "xhigh"},
		{Title: "Step 3", Prompt: "Third", ReasoningEffort: "low"},
	}, nil)

	// Complete step 1, check next eligible is step 2.
	store.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})
	eligible := store.NextEligible()
	if len(eligible) == 0 || eligible[0].Description != "Step 2" {
		t.Fatalf("expected Step 2 eligible, got %+v", eligible)
	}
	if eligible[0].ReasoningEffort != "xhigh" {
		t.Errorf("step 2 effort = %q, want xhigh", eligible[0].ReasoningEffort)
	}

	// Start step 2, complete it.
	store.Update([]TaskUpdate{{ID: 2, Status: TaskInProgress}})
	store.Update([]TaskUpdate{{ID: 2, Status: TaskDone}})
	eligible = store.NextEligible()
	if len(eligible) == 0 || eligible[0].Description != "Step 3" {
		t.Fatalf("expected Step 3 eligible, got %+v", eligible)
	}
}

func TestTaskWorkflow_ParentTaskInsertion(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "parent-test")
	store.AutoVerify = false
	store.Load()

	templates := []TaskTemplate{
		{Title: "Understand", Prompt: "Read spec"},
		{Title: "Do work", Prompt: "Implement", Insert: "parent_tasks"},
		{Title: "Verify", Prompt: "Check"},
		{Title: "Clean up", Prompt: "Remove scratch"},
	}
	parentTasks := []TaskTemplate{
		{Title: "Fix solver", Prompt: "Use LAPACK", ReasoningEffort: "low"},
		{Title: "Benchmark", Prompt: "Beat numpy", ReasoningEffort: "low"},
		{Title: "Profile", Prompt: "Check perf", ReasoningEffort: "low"},
	}

	store.PopulateFromTemplates(templates, parentTasks)
	tasks := store.View()

	// Expected: Understand, Fix solver, Benchmark, Profile, Verify, Clean up
	expected := []string{"Understand", "Fix solver", "Benchmark", "Profile", "Verify", "Clean up"}
	if len(tasks) != len(expected) {
		t.Fatalf("expected %d tasks, got %d: %+v", len(expected), len(tasks), tasks)
	}
	for i, name := range expected {
		if tasks[i].Description != name {
			t.Errorf("task %d: got %q, want %q", i, tasks[i].Description, name)
		}
	}
}

func TestTaskWorkflow_AgentDefinitionWithTasks(t *testing.T) {
	// Parse a coordinator-like agent definition.
	input := []byte("---\nname: test-coord\ndescription: \"Test coordinator\"\nmodel: inherit\ntools: [read_file, spawn_agent]\ntasks:\n  - title: Inventory\n    prompt: \"List files\"\n    reasoning_effort: low\n  - title: Plan\n    prompt: \"Analyze task\"\n    reasoning_effort: xhigh\n  - title: Delegate\n    prompt: \"Spawn agent\"\n    reasoning_effort: low\n---\n\nYou coordinate.\n")

	agent, err := parsePluginAgent(input, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(agent.Tasks))
	}

	// Populate a store from the parsed tasks.
	dir := t.TempDir()
	store := NewTaskStore(dir, "agent-def-test")
	store.AutoVerify = false
	store.Load()

	store.PopulateFromTemplates(agent.Tasks, nil)
	current, ok := store.CurrentInProgress()
	if !ok {
		t.Fatal("expected in_progress task")
	}
	if current.Description != "Inventory" || current.ReasoningEffort != "low" {
		t.Errorf("unexpected current: %+v", current)
	}
}

func TestTaskWorkflow_AllTasksComplete(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "complete-test")
	store.AutoVerify = false
	store.Load()

	store.PopulateFromTemplates([]TaskTemplate{
		{Title: "Only task", Prompt: "Do it"},
	}, nil)

	// Complete the only task.
	store.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})
	eligible := store.NextEligible()
	if len(eligible) != 0 {
		t.Errorf("expected no eligible tasks, got %+v", eligible)
	}

	// All done.
	tasks := store.View()
	for _, task := range tasks {
		if task.Status != TaskDone {
			t.Errorf("task %d status = %q, want done", task.ID, task.Status)
		}
	}
}

func TestTaskWorkflow_ImplementerGetsOwnTasks(t *testing.T) {
	// When a coordinator spawns an implementer, the implementer must get
	// its own task list (Understand, Do the work, Verify, Clean up) —
	// NOT the coordinator's (Inventory, Plan, Delegate, Verify, Fix, Submit).
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}

	implAgent := agents["implementer"]
	coordAgent := agents["coordinator"]

	// Both agents should have tasks defined
	if len(implAgent.Tasks) == 0 {
		t.Fatal("implementer agent has no default tasks")
	}
	if len(coordAgent.Tasks) == 0 {
		t.Fatal("coordinator agent has no default tasks")
	}

	// Implementer should have "insert: parent_tasks" placeholder
	hasInsert := false
	for _, tt := range implAgent.Tasks {
		if tt.Insert == "parent_tasks" {
			hasInsert = true
			break
		}
	}
	if !hasInsert {
		t.Fatal("implementer tasks should have an 'insert: parent_tasks' placeholder")
	}

	// Coordinator should NOT have "insert: parent_tasks"
	for _, tt := range coordAgent.Tasks {
		if tt.Insert == "parent_tasks" {
			t.Fatal("coordinator should not have 'insert: parent_tasks'")
		}
	}

	// Populate a store with implementer tasks
	dir := t.TempDir()
	store := NewTaskStore(dir, "impl-tasks-test")
	store.AutoVerify = false
	store.Load()
	store.PopulateFromTemplates(implAgent.Tasks, nil)
	tasks := store.View()

	// The first task should NOT be "Inventory" (that's the coordinator's)
	if tasks[0].Description == "Inventory" {
		t.Errorf("implementer got coordinator's first task 'Inventory', expected implementer's task")
	}

	// Should not contain coordinator-only tasks
	for _, task := range tasks {
		if task.Description == "Delegate" || task.Description == "Submit" || task.Description == "Plan" {
			t.Errorf("implementer has coordinator task %q — wrong task list injected", task.Description)
		}
	}

	// Should contain implementer-specific tasks
	foundUnderstand := false
	for _, task := range tasks {
		if task.Description == "Understand requirements" {
			foundUnderstand = true
		}
	}
	if !foundUnderstand {
		var names []string
		for _, tsk := range tasks {
			names = append(names, tsk.Description)
		}
		t.Errorf("implementer missing 'Understand requirements' task. Tasks: %v", names)
	}
}

func TestTaskWorkflow_ImplementerGetsOwnRolePrompt(t *testing.T) {
	// The implementer's system prompt must say "You implement code" —
	// NOT "You are a coordinator" or "You delegate, verify, and iterate."
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}

	impl := agents["implementer"]
	coord := agents["coordinator"]

	// Implementer prompt should contain implementation language
	if !strings.Contains(impl.SystemPrompt, "implement") {
		t.Errorf("implementer prompt should mention 'implement', got: %s", impl.SystemPrompt[:200])
	}

	// Implementer prompt should NOT contain coordinator language
	if strings.Contains(impl.SystemPrompt, "You delegate, verify, and iterate") {
		t.Error("implementer prompt contains coordinator language 'You delegate, verify, and iterate'")
	}
	if strings.Contains(impl.SystemPrompt, "spawn an implementer") {
		t.Error("implementer prompt contains 'spawn an implementer' — coordinator language")
	}

	// Coordinator prompt should contain coordinator language
	if !strings.Contains(coord.SystemPrompt, "delegate") {
		t.Errorf("coordinator prompt should mention 'delegate', got: %s", coord.SystemPrompt[:200])
	}
}

func TestTaskWorkflow_ParentTasksNotClobberedByNewSession(t *testing.T) {
	// Regression test: NewSession was calling PopulateFromTemplates(agent.Tasks, nil)
	// for ALL NonInteractive sessions, including subagents. This populated the
	// implementer's task store with the parent_tasks placeholder unexpanded.
	// When spawnAgent later called PopulateFromTemplates(agent.Tasks, parentTasks),
	// it was a no-op because tasks already existed. Parent tasks were silently dropped.
	//
	// The fix: only populate in NewSession for root sessions (ParentSessionID=="").
	// Subagent task population happens in spawnAgent where parentTasks are available.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(r llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	})

	// Simulate a subagent session (has ParentSessionID set).
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		AgentName:       "implementer",
		NonInteractive:  true,
		StateDir:        dir,
		ParentSessionID: "parent-coord-123", // marks this as a subagent
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	store := sess.getOrCreateTaskStore()
	tasks := store.View()

	// Subagent session should NOT have tasks pre-populated.
	// spawnAgent will populate them with parentTasks.
	if len(tasks) > 0 {
		var names []string
		for _, task := range tasks {
			names = append(names, task.Description)
		}
		t.Errorf("subagent session should not have pre-populated tasks (spawnAgent does this), got: %v", names)
	}
}

func TestTaskWorkflow_RootSessionPopulatesTasks(t *testing.T) {
	// Root sessions (no parent) should still get tasks from NewSession.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(r llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		AgentName:      "coordinator",
		NonInteractive: true,
		StateDir:       dir,
		// No ParentSessionID — this is a root session
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	store := sess.getOrCreateTaskStore()
	tasks := store.View()

	if len(tasks) == 0 {
		t.Fatal("root session should have tasks populated")
	}
	if tasks[0].Description != "Plan" {
		t.Errorf("root coordinator first task = %q, want Plan", tasks[0].Description)
	}
}

func TestTaskWorkflow_NewSessionPopulatesCorrectTasks(t *testing.T) {
	// Simulates what happens when spawnAgent creates a subagent with
	// AgentName="implementer" and NonInteractive=true. The task store
	// should get implementer tasks, not coordinator tasks.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(r llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		AgentName:      "implementer",
		NonInteractive: true,
		StateDir:       dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	store := sess.getOrCreateTaskStore()
	tasks := store.View()

	if len(tasks) == 0 {
		t.Fatal("no tasks populated for implementer session")
	}

	// Should NOT have coordinator tasks
	for _, task := range tasks {
		if task.Description == "Inventory" || task.Description == "Plan" || task.Description == "Delegate" {
			t.Errorf("implementer session has coordinator task %q", task.Description)
		}
	}

	// Should have implementer tasks
	found := false
	for _, task := range tasks {
		if task.Description == "Understand requirements" {
			found = true
		}
	}
	if !found {
		var names []string
		for _, task := range tasks {
			names = append(names, task.Description)
		}
		t.Errorf("implementer session missing 'Understand requirements'. Tasks: %v", names)
	}
}

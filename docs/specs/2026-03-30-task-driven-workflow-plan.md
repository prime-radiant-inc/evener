# Task-Driven Agent Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move agent workflows from prose in system prompts to structured task lists managed by the framework — with per-task reasoning effort, auto-advance, and parent-to-child task passing.

**Architecture:** Extend the existing TaskStore with `ReasoningEffort` and `Insert` fields. Parse default tasks from agent definition YAML frontmatter. Auto-inject task prompts as steering messages on task transitions. Add `task_list` parameter to spawn_agent/resume_agent for structured delegation.

**Tech Stack:** Go (agent runtime), YAML frontmatter (agent definitions), existing TaskStore/Session/PluginAgent systems.

---

### Task 1: Add ReasoningEffort and Insert fields to Task struct

**Files:**
- Modify: `agent/task_store.go:32-40` (Task struct)
- Modify: `agent/task_store.go:43-48` (TaskInput struct)
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write test for ReasoningEffort on Task**

```go
// In agent/task_store_test.go, add:
func TestTask_ReasoningEffort_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-effort")
	store.Load()

	added, err := store.Append([]TaskInput{{
		Type:            TaskTypeImplement,
		Description:     "plan the work",
		Prompt:          "think carefully",
		ReasoningEffort: "xhigh",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].ReasoningEffort != "xhigh" {
		t.Errorf("got %q, want xhigh", added[0].ReasoningEffort)
	}

	// Reload from disk.
	store2 := NewTaskStore(dir, "test-effort")
	store2.Load()
	tasks := store2.View()
	if tasks[0].ReasoningEffort != "xhigh" {
		t.Errorf("after reload: got %q, want xhigh", tasks[0].ReasoningEffort)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -run TestTask_ReasoningEffort_RoundTrips -v`
Expected: FAIL — `ReasoningEffort` field does not exist on TaskInput.

- [ ] **Step 3: Add fields to Task and TaskInput**

In `agent/task_store.go`, modify the Task struct (line 32):

```go
type Task struct {
	ID              int        `json:"id"`
	Type            TaskType   `json:"type"`
	Description     string     `json:"description"`
	Prompt          string     `json:"prompt"`
	Status          TaskStatus `json:"status"`
	DependsOn       []int      `json:"depends_on,omitempty"`
	Notes           []string   `json:"notes,omitempty"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"`
	Insert          string     `json:"insert,omitempty"`
}
```

Modify TaskInput (line 43):

```go
type TaskInput struct {
	Type            TaskType `json:"type"`
	Description     string   `json:"description"`
	Prompt          string   `json:"prompt"`
	DependsOn       []int    `json:"depends_on,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Insert          string   `json:"insert,omitempty"`
}
```

In `Append()` (line 235), add the new fields to the created Task:

```go
t := Task{
	ID:              s.nextID,
	Type:            taskType,
	Description:     item.Description,
	Prompt:          item.Prompt,
	Status:          TaskOpen,
	DependsOn:       item.DependsOn,
	ReasoningEffort: item.ReasoningEffort,
	Insert:          item.Insert,
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test -run TestTask_ReasoningEffort_RoundTrips -v`
Expected: PASS

- [ ] **Step 5: Write test for Insert field**

```go
func TestTask_Insert_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-insert")
	store.Load()

	added, err := store.Append([]TaskInput{{
		Type:        TaskTypeImplement,
		Description: "placeholder",
		Prompt:      "do the work",
		Insert:      "parent_tasks",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].Insert != "parent_tasks" {
		t.Errorf("got %q, want parent_tasks", added[0].Insert)
	}
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd agent && go test -run TestTask_Insert_RoundTrips -v`
Expected: PASS (already implemented in step 3)

- [ ] **Step 7: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All existing tests still pass.

- [ ] **Step 8: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "task_store: add ReasoningEffort and Insert fields to Task"
```

---

### Task 2: Add CurrentInProgress method to TaskStore

**Files:**
- Modify: `agent/task_store.go`
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write test**

```go
func TestTaskStore_CurrentInProgress(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-current")
	store.Load()

	store.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "first", Prompt: "do first", ReasoningEffort: "low"},
		{Type: TaskTypeResearch, Description: "second", Prompt: "do second", ReasoningEffort: "xhigh"},
	})

	// No task in progress yet.
	task, ok := store.CurrentInProgress()
	if ok {
		t.Fatal("expected no in_progress task")
	}

	// Start first task.
	store.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}})
	task, ok = store.CurrentInProgress()
	if !ok || task.ID != 1 {
		t.Fatalf("expected task 1 in progress, got ok=%v task=%+v", ok, task)
	}
	if task.ReasoningEffort != "low" {
		t.Errorf("effort = %q, want low", task.ReasoningEffort)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -run TestTaskStore_CurrentInProgress -v`
Expected: FAIL — `CurrentInProgress` does not exist.

- [ ] **Step 3: Implement CurrentInProgress**

Add to `agent/task_store.go`:

```go
// CurrentInProgress returns the first task with status in_progress, if any.
func (s *TaskStore) CurrentInProgress() (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.Status == TaskInProgress {
			return t, true
		}
	}
	return Task{}, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test -run TestTaskStore_CurrentInProgress -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "task_store: add CurrentInProgress method"
```

---

### Task 3: Parse tasks from agent definition YAML frontmatter

**Files:**
- Modify: `agent/plugin_agents.go:13-23` (PluginAgent struct), `agent/plugin_agents.go:28-105` (parsePluginAgent)
- Test: `agent/plugin_agents_test.go`

- [ ] **Step 1: Write test**

```go
func TestParsePluginAgent_WithTasks(t *testing.T) {
	input := []byte(`---
name: test-agent
description: "Test agent with tasks"
model: inherit
tasks:
  - title: First step
    prompt: "Do the first thing"
    reasoning_effort: low
  - title: Do work
    insert: parent_tasks
    prompt: "Implement it"
    reasoning_effort: xhigh
  - title: Verify
    prompt: "Check it"
---

You are a test agent.
`)
	agent, err := parsePluginAgent(input, "test-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(agent.Tasks))
	}
	if agent.Tasks[0].Title != "First step" {
		t.Errorf("task 0 title = %q", agent.Tasks[0].Title)
	}
	if agent.Tasks[0].ReasoningEffort != "low" {
		t.Errorf("task 0 effort = %q", agent.Tasks[0].ReasoningEffort)
	}
	if agent.Tasks[1].Insert != "parent_tasks" {
		t.Errorf("task 1 insert = %q", agent.Tasks[1].Insert)
	}
	if agent.SystemPrompt != "You are a test agent.\n" {
		t.Errorf("body = %q", agent.SystemPrompt)
	}
}

func TestParsePluginAgent_NoTasks(t *testing.T) {
	input := []byte(`---
name: simple
description: "No tasks"
---

Just a prompt.
`)
	agent, err := parsePluginAgent(input, "builtin")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.Tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(agent.Tasks))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -run TestParsePluginAgent_WithTasks -v`
Expected: FAIL — `Tasks` field does not exist on PluginAgent.

- [ ] **Step 3: Add TaskTemplate struct and Tasks field**

In `agent/plugin_agents.go`, add the struct and update PluginAgent:

```go
// TaskTemplate defines a default task in an agent's workflow.
type TaskTemplate struct {
	Title           string `yaml:"title" json:"title"`
	Prompt          string `yaml:"prompt" json:"prompt"`
	ReasoningEffort string `yaml:"reasoning_effort" json:"reasoning_effort,omitempty"`
	Type            string `yaml:"type" json:"type,omitempty"`
	Insert          string `yaml:"insert" json:"insert,omitempty"`
}

type PluginAgent struct {
	Name         string
	Description  string
	Model        string
	Color        string
	Tools        []string
	Skills       []string
	Tasks        []TaskTemplate // default workflow tasks
	SystemPrompt string
	PluginName   string
}
```

- [ ] **Step 4: Parse tasks in parsePluginAgent**

Add after the skills parsing block (line ~93):

```go
var tasks []TaskTemplate
if raw, ok := doc.Meta["tasks"]; ok {
	items, ok := raw.([]any)
	if !ok {
		return PluginAgent{}, fmt.Errorf("agent field \"tasks\" must be a list")
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return PluginAgent{}, fmt.Errorf("each task must be an object")
		}
		tt := TaskTemplate{}
		if v, ok := m["title"].(string); ok {
			tt.Title = v
		}
		if v, ok := m["prompt"].(string); ok {
			tt.Prompt = v
		}
		if v, ok := m["reasoning_effort"].(string); ok {
			tt.ReasoningEffort = v
		}
		if v, ok := m["type"].(string); ok {
			tt.Type = v
		}
		if v, ok := m["insert"].(string); ok {
			tt.Insert = v
		}
		tasks = append(tasks, tt)
	}
}
```

Add `Tasks: tasks,` to the return struct.

- [ ] **Step 5: Run tests**

Run: `cd agent && go test -run TestParsePluginAgent -v`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add agent/plugin_agents.go agent/plugin_agents_test.go
git commit -m "plugin_agents: parse tasks from YAML frontmatter"
```

---

### Task 4: Inject default tasks at session startup

**Files:**
- Modify: `agent/session.go` (spawnAgent or session init)
- Modify: `agent/task_store.go` (add method to populate from templates)
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write test for PopulateFromTemplates**

```go
func TestTaskStore_PopulateFromTemplates(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-populate")
	store.Load()

	templates := []TaskTemplate{
		{Title: "Inventory", Prompt: "List files", ReasoningEffort: "low"},
		{Title: "Do work", Prompt: "Implement", Insert: "parent_tasks", ReasoningEffort: "xhigh"},
		{Title: "Verify", Prompt: "Check it", ReasoningEffort: "low"},
	}

	err := store.PopulateFromTemplates(templates, nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks := store.View()
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Description != "Inventory" || tasks[0].ReasoningEffort != "low" {
		t.Errorf("task 0: %+v", tasks[0])
	}
	if tasks[1].Insert != "parent_tasks" {
		t.Errorf("task 1 insert: %q", tasks[1].Insert)
	}
	// First task should be auto-started.
	if tasks[0].Status != TaskInProgress {
		t.Errorf("task 0 status: %q, want in_progress", tasks[0].Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -run TestTaskStore_PopulateFromTemplates -v`
Expected: FAIL — method does not exist.

- [ ] **Step 3: Write test for PopulateFromTemplates with parent tasks**

```go
func TestTaskStore_PopulateFromTemplates_WithParentTasks(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-parent")
	store.Load()

	templates := []TaskTemplate{
		{Title: "Understand", Prompt: "Read spec"},
		{Title: "Do work", Prompt: "Implement", Insert: "parent_tasks"},
		{Title: "Verify", Prompt: "Check it"},
	}
	parentTasks := []TaskTemplate{
		{Title: "Fix eigenvalue solver", Prompt: "Use scipy LAPACK"},
		{Title: "Benchmark sizes 2-10", Prompt: "Must beat numpy"},
	}

	err := store.PopulateFromTemplates(templates, parentTasks)
	if err != nil {
		t.Fatal(err)
	}

	tasks := store.View()
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks (understand + 2 parent + verify), got %d", len(tasks))
	}
	if tasks[0].Description != "Understand" {
		t.Errorf("task 0: %q", tasks[0].Description)
	}
	if tasks[1].Description != "Fix eigenvalue solver" {
		t.Errorf("task 1: %q", tasks[1].Description)
	}
	if tasks[2].Description != "Benchmark sizes 2-10" {
		t.Errorf("task 2: %q", tasks[2].Description)
	}
	if tasks[3].Description != "Verify" {
		t.Errorf("task 3: %q", tasks[3].Description)
	}
}
```

- [ ] **Step 4: Implement PopulateFromTemplates**

Add to `agent/task_store.go`:

```go
// PopulateFromTemplates initializes the task store from agent definition templates.
// If parentTasks is non-nil, they replace the template with Insert=="parent_tasks".
// The first task is auto-started (set to in_progress).
func (s *TaskStore) PopulateFromTemplates(templates []TaskTemplate, parentTasks []TaskTemplate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.tasks) > 0 {
		return nil // already populated, don't overwrite
	}

	// Build the effective task list by expanding the insert placeholder.
	var effective []TaskTemplate
	for _, tt := range templates {
		if tt.Insert == "parent_tasks" && len(parentTasks) > 0 {
			effective = append(effective, parentTasks...)
		} else {
			effective = append(effective, tt)
		}
	}

	// Convert templates to tasks.
	for _, tt := range effective {
		taskType := TaskType(tt.Type)
		if taskType == "" {
			taskType = TaskTypeImplement
		}
		t := Task{
			ID:              s.nextID,
			Type:            taskType,
			Description:     tt.Title,
			Prompt:          tt.Prompt,
			Status:          TaskOpen,
			ReasoningEffort: tt.ReasoningEffort,
			Insert:          tt.Insert,
		}
		s.nextID++
		s.tasks = append(s.tasks, t)
	}

	// Auto-start the first task.
	if len(s.tasks) > 0 {
		s.tasks[0].Status = TaskInProgress
	}

	return s.save()
}
```

Also add the `TaskTemplate` type reference (it's defined in plugin_agents.go, same package — no import needed).

- [ ] **Step 5: Run tests**

Run: `cd agent && go test -run TestTaskStore_PopulateFromTemplates -v`
Expected: Both PASS.

- [ ] **Step 6: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "task_store: PopulateFromTemplates with parent task insertion"
```

---

### Task 5: Auto-advance and steering injection on task completion

**Files:**
- Modify: `agent/session.go:3145-3200` (task_list update handler)
- Test: `agent/task_store_test.go` or `agent/session_test.go`

- [ ] **Step 1: Write test for auto-advance behavior**

```go
func TestTaskStore_AutoAdvance(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-advance")
	store.Load()

	store.PopulateFromTemplates([]TaskTemplate{
		{Title: "Step 1", Prompt: "First", ReasoningEffort: "low"},
		{Title: "Step 2", Prompt: "Second", ReasoningEffort: "xhigh"},
		{Title: "Step 3", Prompt: "Third", ReasoningEffort: "low"},
	}, nil)

	// Task 1 is in_progress. Mark it done.
	advanced, err := store.CompleteAndAdvance(1)
	if err != nil {
		t.Fatal(err)
	}
	if advanced == nil || advanced.ID != 2 {
		t.Fatalf("expected advance to task 2, got %+v", advanced)
	}
	if advanced.ReasoningEffort != "xhigh" {
		t.Errorf("effort = %q, want xhigh", advanced.ReasoningEffort)
	}

	tasks := store.View()
	if tasks[0].Status != TaskDone {
		t.Errorf("task 1 status = %q", tasks[0].Status)
	}
	if tasks[1].Status != TaskInProgress {
		t.Errorf("task 2 status = %q", tasks[1].Status)
	}
}

func TestTaskStore_AutoAdvance_AllDone(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test-alldone")
	store.Load()

	store.PopulateFromTemplates([]TaskTemplate{
		{Title: "Only step", Prompt: "Do it"},
	}, nil)

	advanced, err := store.CompleteAndAdvance(1)
	if err != nil {
		t.Fatal(err)
	}
	if advanced != nil {
		t.Fatalf("expected nil (no more tasks), got %+v", advanced)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -run TestTaskStore_AutoAdvance -v`
Expected: FAIL — `CompleteAndAdvance` does not exist.

- [ ] **Step 3: Implement CompleteAndAdvance**

Add to `agent/task_store.go`:

```go
// CompleteAndAdvance marks the given task as done and starts the next eligible
// task. Returns the newly started task, or nil if no eligible tasks remain.
func (s *TaskStore) CompleteAndAdvance(taskID int) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Mark the task done.
	found := false
	for i := range s.tasks {
		if s.tasks[i].ID == taskID {
			s.tasks[i].Status = TaskDone
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("task %d not found", taskID)
	}

	// Find next eligible (lowest ID, all deps satisfied, status open).
	status := make(map[int]TaskStatus, len(s.tasks))
	for _, t := range s.tasks {
		status[t.ID] = t.Status
	}

	for i := range s.tasks {
		t := &s.tasks[i]
		if t.Status != TaskOpen {
			continue
		}
		satisfied := true
		for _, dep := range t.DependsOn {
			st := status[dep]
			if st != TaskDone && st != TaskCancelled {
				satisfied = false
				break
			}
		}
		if satisfied {
			t.Status = TaskInProgress
			if err := s.save(); err != nil {
				return nil, err
			}
			result := *t
			return &result, nil
		}
	}

	// No more eligible tasks.
	if err := s.save(); err != nil {
		return nil, err
	}
	return nil, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd agent && go test -run TestTaskStore_AutoAdvance -v`
Expected: PASS

- [ ] **Step 5: Wire into session's task_list update handler**

In `agent/session.go`, in the task_list tool executor's "update" case (around line 3180), after the existing `store.Update(updates)` call, add auto-advance logic:

```go
// Auto-advance: if a task was marked done, advance to next and inject prompt.
for _, u := range updates {
	if u.Status == TaskDone {
		next, advErr := store.CompleteAndAdvance(u.ID)
		if advErr != nil {
			// Log but don't fail the tool call.
			break
		}
		if next != nil {
			// Set reasoning effort from the new task.
			if next.ReasoningEffort != "" {
				s.SetReasoningEffort(next.ReasoningEffort)
			}
			// Inject the task prompt as a steering message.
			header := fmt.Sprintf("[Task #%d: %s", next.ID, next.Description)
			if next.ReasoningEffort != "" {
				header += " | reasoning: " + next.ReasoningEffort
			}
			header += "]"
			s.Steer(header + "\n" + next.Prompt)
		} else {
			// All tasks done — nudge.
			s.Steer("All tasks on your list are complete. If you have remaining work, add it to your task list. Otherwise, use communicate to indicate you're done.")
		}
		break // Only advance once per update call.
	}
}
```

**Important:** This needs to happen INSTEAD of the existing `CompleteAndAdvance` — we need to prevent the existing `Update()` from also marking the task done, since `CompleteAndAdvance` does it. Actually, re-reading the code: `Update()` already marks the task done. `CompleteAndAdvance` would double-mark it.

**Revised approach:** Don't create `CompleteAndAdvance`. Instead, after the existing `store.Update(updates)` call succeeds and `completedAny` is true, find the next eligible task and auto-start it:

```go
if completedAny {
	// Auto-advance: find next eligible and start it.
	eligible := store.NextEligible()
	if len(eligible) > 0 {
		next := eligible[0]
		store.Update([]TaskUpdate{{ID: next.ID, Status: TaskInProgress}})
		if next.ReasoningEffort != "" {
			s.SetReasoningEffort(next.ReasoningEffort)
		}
		header := fmt.Sprintf("[Task #%d: %s", next.ID, next.Description)
		if next.ReasoningEffort != "" {
			header += " | reasoning: " + next.ReasoningEffort
		}
		header += "]"
		s.Steer(header + "\n" + next.Prompt)
	} else {
		// Check if ALL tasks are done/cancelled.
		allDone := true
		for _, t := range store.View() {
			if t.Status == TaskOpen || t.Status == TaskInProgress {
				allDone = false
				break
			}
		}
		if allDone {
			s.Steer("All tasks on your list are complete. If you have remaining work, add it to your task list. Otherwise, use communicate to indicate you're done.")
		}
	}
}
```

- [ ] **Step 6: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go agent/session.go
git commit -m "session: auto-advance tasks and inject prompts on completion"
```

---

### Task 6: Dynamic reasoning effort from current task

**Files:**
- Modify: `agent/session.go` (before LLM call)

- [ ] **Step 1: Write test**

```go
func TestSession_ReasoningEffort_FromCurrentTask(t *testing.T) {
	// Create a session with a task store containing a task with reasoning_effort.
	// Verify that the LLM request uses that effort level.
	// (Integration-style test using the existing test harness pattern.)
}
```

This is harder to unit test in isolation — it touches the LLM call path. The simplest approach: verify in an integration test that when a task with `reasoning_effort: "xhigh"` is in_progress, the next LLM request has `ReasoningEffort: "xhigh"`.

- [ ] **Step 2: Implement dynamic reasoning**

In `agent/session.go`, find where `req.ReasoningEffort` is set (line ~940). Add a check before it:

```go
// Dynamic reasoning effort from current task (if task store exists).
if s.taskStore != nil {
	if current, ok := s.taskStore.CurrentInProgress(); ok && current.ReasoningEffort != "" {
		s.cfg.ReasoningEffort = current.ReasoningEffort
	}
}
if effort := strings.TrimSpace(s.cfg.ReasoningEffort); effort != "" {
	req.ReasoningEffort = &effort
}
```

- [ ] **Step 3: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add agent/session.go
git commit -m "session: dynamic reasoning effort from current in_progress task"
```

---

### Task 7: Add task_list parameter to spawn_agent

**Files:**
- Modify: `agent/profile.go:694-713` (defSpawnAgent)
- Modify: `agent/session.go:2985-3014` (spawn_agent executor)
- Modify: `agent/subagents.go` (spawnAgent method — pass task templates)
- Test: `agent/builtin_agents_test.go`

- [ ] **Step 1: Add task_list to spawn_agent tool definition**

In `agent/profile.go` defSpawnAgent(), add to properties:

```go
"task_list": map[string]any{
	"type":        "array",
	"description": "Pre-populate the subagent's task list. Items replace the agent's 'parent_tasks' placeholder.",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":            map[string]any{"type": "string", "description": "Short task title"},
			"prompt":           map[string]any{"type": "string", "description": "Detailed instructions"},
			"reasoning_effort": map[string]any{"type": "string", "description": "low|medium|high|xhigh"},
		},
		"required": []string{"title", "prompt"},
	},
},
```

- [ ] **Step 2: Parse task_list in spawn_agent executor**

In `agent/session.go` spawn_agent executor (line ~2985), add parsing:

```go
var parentTasks []TaskTemplate
if raw, ok := args["task_list"].([]any); ok {
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tt := TaskTemplate{}
		if v, ok := m["title"].(string); ok {
			tt.Title = v
		}
		if v, ok := m["prompt"].(string); ok {
			tt.Prompt = v
		}
		if v, ok := m["reasoning_effort"].(string); ok {
			tt.ReasoningEffort = v
		}
		parentTasks = append(parentTasks, tt)
	}
}
```

Pass `parentTasks` to `s.spawnAgent()` (which needs a new parameter).

- [ ] **Step 3: Thread parentTasks through spawnAgent**

Update `spawnAgent` signature in `agent/subagents.go` to accept `parentTasks []TaskTemplate`. In the method, after creating the subagent session, if the agent has default tasks, call:

```go
if agent != nil && len(agent.Tasks) > 0 {
	sub.sess.getOrCreateTaskStore().PopulateFromTemplates(agent.Tasks, parentTasks)
	// Inject first task prompt.
	if current, ok := sub.sess.getOrCreateTaskStore().CurrentInProgress(); ok {
		header := fmt.Sprintf("[Task #%d: %s", current.ID, current.Description)
		if current.ReasoningEffort != "" {
			header += " | reasoning: " + current.ReasoningEffort
		}
		header += "]"
		sub.sess.Steer(header + "\n" + current.Prompt)
	}
}
```

- [ ] **Step 4: Add same parameter to resume_agent (send_input)**

In `agent/profile.go` defSendInput(), add the same `task_list` parameter. In the executor, parse it and append to the subagent's existing task store.

- [ ] **Step 5: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass (existing calls don't provide task_list, so behavior unchanged).

- [ ] **Step 6: Commit**

```bash
git add agent/profile.go agent/session.go agent/subagents.go
git commit -m "spawn_agent: add task_list parameter for structured delegation"
```

---

### Task 8: Add default tasks to coordinator.md and slim the prompt

**Files:**
- Modify: `agent/agents/coordinator.md`

- [ ] **Step 1: Add tasks to YAML frontmatter**

Replace the entire coordinator.md with the new version. Keep the YAML tasks from the spec, keep the hard rules (must spawn implementer, one implementer, submitting gate), remove the numbered workflow steps.

- [ ] **Step 2: Verify binary embeds correctly**

Run: `make build-linux && strings serf-linux-amd64 | grep "Your task list defines"`
Expected: The new coordinator prompt text appears in the binary.

- [ ] **Step 3: Commit**

```bash
git add agent/agents/coordinator.md
git commit -m "coordinator: move workflow to YAML tasks, slim prompt to rules"
```

---

### Task 9: Add default tasks to implementer.md and slim the prompt

**Files:**
- Modify: `agent/agents/implementer.md`

- [ ] **Step 1: Add tasks to YAML frontmatter**

Add the 4 default tasks (understand, implement w/ parent_tasks placeholder, verify, clean up). Remove the "How to Work" numbered steps from the body. Keep implementation standards, spec authority, when you get stuck, output integrity.

- [ ] **Step 2: Verify binary embeds correctly**

Run: `make build-linux && strings serf-linux-amd64 | grep "insert: parent_tasks"`
Expected: Appears in binary.

- [ ] **Step 3: Commit**

```bash
git add agent/agents/implementer.md
git commit -m "implementer: move workflow to YAML tasks, slim prompt"
```

---

### Task 10: Add default tasks to remaining agent types

**Files:**
- Modify: `agent/agents/reviewer.md`
- Modify: `agent/agents/explorer.md`
- Modify: `agent/agents/worker.md`
- Modify: `agent/agents/planner.md`

- [ ] **Step 1: Add minimal task lists to each agent**

Each agent gets a simple task list appropriate to its role:

**reviewer.md:** Read deliverable → Check against spec → Report findings
**explorer.md:** Scan workspace → Report structure
**worker.md:** Understand task → Implement → Verify → Clean up (same pattern as implementer but with `insert: parent_tasks`)
**planner.md:** Analyze requirements → Propose approaches → Present plan

- [ ] **Step 2: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 3: Commit**

```bash
git add agent/agents/
git commit -m "all agents: add default YAML task lists"
```

---

### Task 11: Update task_list tool description and task reminders

**Files:**
- Modify: `agent/profile.go:835-891` (defTaskList description)
- Modify: `agent/task_reminders.go` (show reasoning_effort)

- [ ] **Step 1: Update tool description**

Update defTaskList() description to mention reasoning_effort:

```go
Description: "Manage your task list. Actions: view (show all tasks with reasoning effort levels), append (add new tasks), update (change status — when you mark a task done, the next task auto-starts). You can modify your task list at any time: add tasks, reorder via dependencies, or cancel tasks you don't need.",
```

- [ ] **Step 2: Update task reminder formatting**

In `agent/task_reminders.go`, update the task display to include reasoning_effort when present. Show it as `[xhigh]` after the task description.

- [ ] **Step 3: Run full test suite**

Run: `cd agent && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add agent/profile.go agent/task_reminders.go
git commit -m "task_list: update tool description and reminders for reasoning_effort"
```

---

### Task 12: Revert serf_agent.py to reasoning_effort="low"

**Files:**
- Modify: `tools/serf_agent.py:36`

- [ ] **Step 1: Change default back to "low"**

```python
def __init__(self, max_rounds: int = 100, min_result_round: int = 0, reasoning_effort: str = "low", ...):
```

The coordinator's Plan and Verify tasks now declare `xhigh` in their task definitions, so the session default can be `low`.

- [ ] **Step 2: Commit**

```bash
git add tools/serf_agent.py
git commit -m "serf_agent: revert to reasoning_effort=low (tasks control effort)"
```

---

### Task 13: Integration test — full lifecycle

**Files:**
- Test: `agent/task_workflow_integration_test.go` (new file)

- [ ] **Step 1: Write integration test**

Test the full lifecycle: agent starts with default tasks, marks #1 done, framework auto-advances to #2 with xhigh effort, inject prompt. Verify reasoning effort changes, steering messages injected, all tasks complete.

- [ ] **Step 2: Run test**

Run: `cd agent && go test -run TestTaskWorkflow_Integration -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add agent/task_workflow_integration_test.go
git commit -m "test: integration test for task-driven workflow lifecycle"
```

---

### Task 14: Eval validation

- [ ] **Step 1: Build and deploy**

Run: `make build-linux`

- [ ] **Step 2: Run regression set (3 reps)**

```bash
./tools/run_eval.sh --tasks crack-7z-hash,custom-memory-heap-crash,build-pov-ray,feal-linear-cryptanalysis,pypi-server,regex-log,distribution-search --reps 3 --instance-type r6i.large
```

- [ ] **Step 3: Run target tasks (3 reps)**

```bash
./tools/run_eval.sh --tasks constraints-scheduling,sanitize-git-repo,schemelike-metacircular-eval,sqlite-with-gcov,largest-eigenval --reps 3 --instance-type r6i.large
```

- [ ] **Step 4: Collect and compare**

Collect results, compare against v55 baseline (wave-121bc79). No regressions on regression set, comparable or better on target tasks.

- [ ] **Step 5: Commit results**

```bash
git add docs/experiments/
git commit -m "docs: task-driven workflow eval results"
```

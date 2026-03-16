# Task Tracking Improvements — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve the task_list tool with dependencies, current-task awareness, auto-advance suggestions, system prompt guidance, and periodic task reminders.

**Architecture:** Changes are concentrated in the `agent/` package. The TaskStore data layer gains dependency fields and cycle detection. The tool exec handler in session.go gains next-task enrichment. System prompt guidance goes in core.md. Periodic reminders use the existing steering injection mechanism.

**Tech Stack:** Go, TDD (red-green-commit)

**Spec:** `docs/plans/2026-03-16-task-tracking-improvements.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `agent/task_store.go` | Modify | Add `DependsOn` field, rename `TaskUndone` → `TaskOpen`, add `DependsOn` to `TaskInput`/`TaskUpdate`, add cycle detection, add `NextEligible()` and `Progress()` methods |
| `agent/task_store_test.go` | Modify | Tests for all TaskStore changes |
| `agent/profile.go` | Modify | Update `defTaskList()` tool schema: new enum values, `depends_on` in append/update |
| `agent/profile_test.go` | Modify | Update `TestAllProfiles_SystemPromptContainsTaskListGuidance` for `open` status |
| `agent/session.go` | Modify | Enrich update response with next-task suggestions, add task reminder tracking + injection |
| `agent/task_reminders.go` | Create | Reminder formatting functions |
| `agent/task_reminders_test.go` | Create | Tests for reminder formatters and injection logic |
| `agent/prompts/core.md` | Modify | Add `## Task tracking` guidance section |
| `agent/context_manager.go` | Modify | Update compaction snapshot format to include dependencies |

---

## Chunk 1: TaskStore data model changes

### Task 1: Rename `undone` to `open`

**Files:**
- Modify: `agent/task_store.go:14-18` (constants)
- Modify: `agent/task_store.go:121-142` (Append uses TaskUndone)
- Modify: `agent/task_store.go:150-153` (Update validation)
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Update tests to expect `open` instead of `undone`**

In `agent/task_store_test.go`, replace all references to `TaskUndone` with `TaskOpen` and all string literals `"undone"` with `"open"`. The affected tests:

- `TestTaskStore_AppendAndView` (line 31): `TaskUndone` → `TaskOpen`
- `TestTaskStore_ViewReturnsCopy` (line 250): `TaskUndone` → `TaskOpen`
- `TestTaskListTool_AppendViewUpdate` (line 442): `"undone"` → `"open"`

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore_AppendAndView|TestTaskStore_ViewReturnsCopy|TestTaskListTool_AppendViewUpdate' -v`

Expected: compilation error — `TaskOpen` undefined.

- [ ] **Step 3: Rename the constant and update validation**

In `agent/task_store.go`:

```go
// Line 15: rename constant
TaskOpen TaskStatus = "open"  // was TaskUndone = "undone"
```

Update `Append` (line 131):
```go
Status: TaskOpen,  // was TaskUndone
```

Update `Update` validation (line 151):
```go
case TaskOpen, TaskInProgress, TaskDone, TaskCancelled:
```

Update the error message (line 154) to say `"open, in_progress, done, or cancelled"`.

- [ ] **Step 4: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore' -v`

Expected: all pass.

- [ ] **Step 5: Update tool schema enum**

In `agent/profile.go`, `defTaskList()` at line 801:
```go
"enum": []string{"open", "in_progress", "done", "cancelled"},
```

Also update the tool description at line 773:
```go
"Manage a persistent task list. Actions: view (show all tasks), append (add new tasks), update (change task status to open/in_progress/done/cancelled).",
```

- [ ] **Step 6: Update the profile guidance test**

In `agent/profile_test.go`, `TestAllProfiles_SystemPromptContainsTaskListGuidance` (line 284):
Change the `"undone"` check to use a more specific string that won't match coincidentally:
```go
if !strings.Contains(prompt, "done") || !strings.Contains(prompt, "open/in_progress") {
    t.Errorf("profile %q system prompt missing task status guidance", name)
}
```

Note: this test will fail until we add the system prompt guidance in Task 9. That's expected.

- [ ] **Step 7: Run full agent test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -count=1`

Expected: all pass except `TestAllProfiles_SystemPromptContainsTaskListGuidance` (fixed in Task 9).

- [ ] **Step 8: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go agent/profile.go agent/profile_test.go
git commit -m "refactor: rename TaskUndone to TaskOpen across task store and tool schema"
```

---

### Task 2: Add `DependsOn` field to Task and TaskInput

**Files:**
- Modify: `agent/task_store.go:22-34` (Task and TaskInput structs)
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write test for appending tasks with dependencies**

Add to `agent/task_store_test.go`:

```go
func TestTaskStore_AppendWithDependsOn(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// Task 1 has no dependencies.
	if _, err := s.Append([]TaskInput{
		{Description: "Setup", Prompt: "Set up the project"},
	}); err != nil {
		t.Fatal(err)
	}

	// Task 2 depends on task 1.
	added, err := s.Append([]TaskInput{
		{Description: "Build", Prompt: "Build the project", DependsOn: []int{1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added[0].DependsOn) != 1 || added[0].DependsOn[0] != 1 {
		t.Fatalf("DependsOn: got %v, want [1]", added[0].DependsOn)
	}

	// Verify persisted.
	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn after View: got %v, want [1]", all[1].DependsOn)
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run TestTaskStore_AppendWithDependsOn -v`

Expected: compilation error — `TaskInput` has no `DependsOn` field.

- [ ] **Step 3: Add DependsOn to Task and TaskInput**

In `agent/task_store.go`:

```go
type Task struct {
	ID          int        `json:"id"`
	Description string     `json:"description"`
	Prompt      string     `json:"prompt"`
	Status      TaskStatus `json:"status"`
	DependsOn   []int      `json:"depends_on,omitempty"`
	Notes       []string   `json:"notes,omitempty"`
}

type TaskInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	DependsOn   []int  `json:"depends_on,omitempty"`
}
```

In `Append`, copy the field:
```go
t := Task{
	ID:          s.nextID,
	Description: item.Description,
	Prompt:      item.Prompt,
	Status:      TaskOpen,
	DependsOn:   item.DependsOn,
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run TestTaskStore_AppendWithDependsOn -v`

Expected: PASS.

- [ ] **Step 5: Write test for DependsOn persistence across loads**

```go
func TestTaskStore_DependsOnPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{{Description: "A", Prompt: "a"}})
	s.Append([]TaskInput{{Description: "B", Prompt: "b", DependsOn: []int{1}}})

	s2 := NewTaskStore(dir, "test-session")
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	all := s2.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn after reload: got %v", all[1].DependsOn)
	}
}
```

- [ ] **Step 6: Run test — verify it passes** (JSON serialization handles this already)

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run TestTaskStore_DependsOnPersistsAcrossLoads -v`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "feat: add DependsOn field to Task and TaskInput"
```

---

### Task 3: Add DependsOn to TaskUpdate

**Files:**
- Modify: `agent/task_store.go:37-41` (TaskUpdate struct)
- Modify: `agent/task_store.go:145-173` (Update method)
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write test for updating dependencies**

```go
func TestTaskStore_UpdateDependsOn(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b"},
		{Description: "C", Prompt: "c"},
	})

	// Set depends_on for task 3.
	setDeps := []int{1, 2}
	err := s.Update([]TaskUpdate{{ID: 3, Status: TaskOpen, DependsOn: &setDeps}})
	if err != nil {
		t.Fatal(err)
	}
	all := s.View()
	if len(all[2].DependsOn) != 2 {
		t.Fatalf("DependsOn after set: got %v, want [1 2]", all[2].DependsOn)
	}

	// Clear depends_on with empty slice.
	clearDeps := []int{}
	err = s.Update([]TaskUpdate{{ID: 3, Status: TaskOpen, DependsOn: &clearDeps}})
	if err != nil {
		t.Fatal(err)
	}
	all = s.View()
	if len(all[2].DependsOn) != 0 {
		t.Fatalf("DependsOn after clear: got %v, want []", all[2].DependsOn)
	}
}

func TestTaskStore_UpdateOmittedDependsOnPreserves(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
	})

	// Update status without touching depends_on (nil pointer = preserve).
	err := s.Update([]TaskUpdate{{ID: 2, Status: TaskInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn should be preserved: got %v", all[1].DependsOn)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore_UpdateDependsOn|TestTaskStore_UpdateOmittedDependsOnPreserves' -v`

Expected: compilation error — `TaskUpdate` has no `DependsOn` field.

- [ ] **Step 3: Add DependsOn to TaskUpdate and Update method**

In `agent/task_store.go`, change `TaskUpdate`:

```go
type TaskUpdate struct {
	ID        int        `json:"id"`
	Status    TaskStatus `json:"status"`
	Notes     string     `json:"notes,omitempty"`
	DependsOn *[]int     `json:"depends_on,omitempty"` // nil = no change, &[]int{} = clear
}
```

In the `Update` method, after the notes logic (around line 161), add:

```go
if u.DependsOn != nil {
	s.tasks[i].DependsOn = *u.DependsOn
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore_UpdateDependsOn|TestTaskStore_UpdateOmittedDependsOnPreserves' -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "feat: add DependsOn to TaskUpdate with nil-preserves semantics"
```

---

### Task 4: Dependency validation — nonexistent IDs and cycles

**Files:**
- Modify: `agent/task_store.go` (Append and Update methods)
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write tests for validation**

```go
func TestTaskStore_AppendRejectsNonexistentDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{{Description: "A", Prompt: "a"}})

	_, err := s.Append([]TaskInput{
		{Description: "B", Prompt: "b", DependsOn: []int{99}},
	})
	if err == nil {
		t.Fatal("expected error for nonexistent dependency ID")
	}
}

func TestTaskStore_UpdateRejectsNonexistentDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b"},
	})

	deps := []int{99}
	err := s.Update([]TaskUpdate{{ID: 2, Status: TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatal("expected error for nonexistent dependency ID")
	}
}

func TestTaskStore_RejectsCyclicDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
	})

	// Make task 1 depend on task 2 → cycle: 1→2→1
	deps := []int{2}
	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatal("expected error for cyclic dependency")
	}
}

func TestTaskStore_RejectsTransitiveCycle(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
		{Description: "C", Prompt: "c", DependsOn: []int{2}},
	})

	// Make task 1 depend on task 3 → cycle: 1→3→2→1
	deps := []int{3}
	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatal("expected error for transitive cyclic dependency")
	}
}

func TestTaskStore_RejectsSelfDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// First task gets ID 1. DependsOn [1] is a self-reference.
	_, err := s.Append([]TaskInput{
		{Description: "A", Prompt: "a", DependsOn: []int{1}},
	})
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
}

func TestTaskStore_RejectsIntraBatchCycle(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// Append two tasks that depend on each other in one batch.
	// First task gets ID 1, second gets ID 2.
	_, err := s.Append([]TaskInput{
		{Description: "A", Prompt: "a", DependsOn: []int{2}},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
	})
	if err == nil {
		t.Fatal("expected error for intra-batch cyclic dependency")
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore_.*Reject.*Depend|TestTaskStore_.*Cycl|TestTaskStore_.*Batch' -v`

Expected: FAIL — no validation yet.

- [ ] **Step 3: Add validation helpers to task_store.go**

Add a `validateDependencies` method and a `hasCycle` helper:

```go
// validateDependencies checks that all IDs in deps exist and that
// the resulting dependency graph is acyclic. taskID is the task
// being modified. pending is the set of tasks being appended in
// the same batch (not yet in s.tasks).
func (s *TaskStore) validateDependencies(taskID int, deps []int, pending []Task) error {
	if len(deps) == 0 {
		return nil
	}

	// Build ID set: existing tasks + pending.
	known := make(map[int]bool, len(s.tasks)+len(pending))
	for _, t := range s.tasks {
		known[t.ID] = true
	}
	for _, t := range pending {
		known[t.ID] = true
	}

	for _, d := range deps {
		if d == taskID {
			return fmt.Errorf("task %d cannot depend on itself", taskID)
		}
		if !known[d] {
			return fmt.Errorf("dependency ID %d does not exist", d)
		}
	}

	// Build adjacency for cycle detection: existing graph + proposed change + pending.
	adj := make(map[int][]int, len(s.tasks)+len(pending))
	for _, t := range s.tasks {
		if t.ID == taskID {
			adj[t.ID] = deps // use proposed deps for the task being modified
		} else {
			adj[t.ID] = t.DependsOn
		}
	}
	for _, t := range pending {
		adj[t.ID] = t.DependsOn
	}

	if hasCycle(adj) {
		return fmt.Errorf("dependency cycle detected")
	}
	return nil
}

// hasCycle returns true if the directed graph (node → dependencies) contains a cycle.
func hasCycle(adj map[int][]int) bool {
	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully explored
	)
	color := make(map[int]int, len(adj))

	var dfs func(int) bool
	dfs = func(node int) bool {
		color[node] = gray
		for _, dep := range adj[node] {
			switch color[dep] {
			case gray:
				return true // back edge = cycle
			case white:
				if dfs(dep) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}

	for node := range adj {
		if color[node] == white {
			if dfs(node) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 4: Wire validation into Append**

Rewrite the `Append` method to build tasks first, validate, then commit. Restore `s.nextID` on validation failure:

```go
func (s *TaskStore) Append(items []TaskInput) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	savedNextID := s.nextID

	// Build tasks first so intra-batch deps can be validated.
	var added []Task
	for _, item := range items {
		t := Task{
			ID:          s.nextID,
			Description: item.Description,
			Prompt:      item.Prompt,
			Status:      TaskOpen,
			DependsOn:   item.DependsOn,
		}
		s.nextID++
		added = append(added, t)
	}

	// Validate dependencies for all new tasks.
	for _, t := range added {
		if err := s.validateDependencies(t.ID, t.DependsOn, added); err != nil {
			s.nextID = savedNextID // restore on failure — no ID gaps
			return nil, fmt.Errorf("task %d (%s): %w", t.ID, t.Description, err)
		}
	}

	s.tasks = append(s.tasks, added...)

	if err := s.save(); err != nil {
		return added, fmt.Errorf("save: %w", err)
	}
	return added, nil
}
```

- [ ] **Step 5: Wire validation into Update**

In the `Update` method, after setting `DependsOn` on the task, validate:

```go
if u.DependsOn != nil {
	s.tasks[i].DependsOn = *u.DependsOn
	if err := s.validateDependencies(s.tasks[i].ID, s.tasks[i].DependsOn, nil); err != nil {
		return fmt.Errorf("task %d: %w", u.ID, err)
	}
}
```

- [ ] **Step 6: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore' -v`

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "feat: validate dependency references and reject cycles"
```

---

### Task 5: NextEligible method

**Files:**
- Modify: `agent/task_store.go`
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write tests for NextEligible**

```go
// ids is a test helper that extracts task IDs for readable assertions.
func ids(tasks []Task) []int {
	out := make([]int, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

func TestTaskStore_NextEligible(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
		{Description: "C", Prompt: "c", DependsOn: []int{1}},
		{Description: "D", Prompt: "d", DependsOn: []int{2, 3}},
	})

	// Initially only task 1 is eligible (no deps).
	eligible := s.NextEligible()
	if len(eligible) != 1 || eligible[0].ID != 1 {
		t.Fatalf("expected [1], got %v", ids(eligible))
	}

	// Mark task 1 done → tasks 2 and 3 become eligible.
	s.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})
	eligible = s.NextEligible()
	if len(eligible) != 2 || eligible[0].ID != 2 || eligible[1].ID != 3 {
		t.Fatalf("expected [2 3], got %v", ids(eligible))
	}

	// Mark task 2 done → task 3 still eligible, task 4 not yet (needs 3).
	s.Update([]TaskUpdate{{ID: 2, Status: TaskDone}})
	eligible = s.NextEligible()
	if len(eligible) != 1 || eligible[0].ID != 3 {
		t.Fatalf("expected [3], got %v", ids(eligible))
	}

	// Mark task 3 done → task 4 eligible.
	s.Update([]TaskUpdate{{ID: 3, Status: TaskDone}})
	eligible = s.NextEligible()
	if len(eligible) != 1 || eligible[0].ID != 4 {
		t.Fatalf("expected [4], got %v", ids(eligible))
	}

	// Mark task 4 done → nothing eligible.
	s.Update([]TaskUpdate{{ID: 4, Status: TaskDone}})
	eligible = s.NextEligible()
	if len(eligible) != 0 {
		t.Fatalf("expected [], got %v", ids(eligible))
	}
}

func TestTaskStore_NextEligibleSkipsInProgress(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b"},
	})

	// Mark task 1 in_progress — it's no longer "open", so not eligible.
	s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}})
	eligible := s.NextEligible()
	if len(eligible) != 1 || eligible[0].ID != 2 {
		t.Fatalf("expected [2], got %v", ids(eligible))
	}
}

func TestTaskStore_NextEligibleCancelledSatisfiesDeps(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
	})

	// Cancel task 1 → task 2's dependency is satisfied.
	s.Update([]TaskUpdate{{ID: 1, Status: TaskCancelled}})
	eligible := s.NextEligible()
	if len(eligible) != 1 || eligible[0].ID != 2 {
		t.Fatalf("expected [2], got %v", ids(eligible))
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore_NextEligible' -v`

Expected: compilation error — `NextEligible` not defined.

- [ ] **Step 3: Implement NextEligible**

Add to `agent/task_store.go`:

```go
// NextEligible returns open tasks whose dependencies are all satisfied
// (done or cancelled), sorted by ID (insertion order).
func (s *TaskStore) NextEligible() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Build status lookup.
	status := make(map[int]TaskStatus, len(s.tasks))
	for _, t := range s.tasks {
		status[t.ID] = t.Status
	}

	var eligible []Task
	for _, t := range s.tasks {
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
			eligible = append(eligible, t)
		}
	}
	return eligible
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskStore_NextEligible' -v`

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "feat: add NextEligible method for dependency-aware task ordering"
```

---

### Task 6: Progress summary helper

**Files:**
- Modify: `agent/task_store.go`
- Test: `agent/task_store_test.go`

- [ ] **Step 1: Write test for Progress**

```go
func TestTaskStore_Progress(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b"},
		{Description: "C", Prompt: "c"},
	})
	s.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})
	s.Update([]TaskUpdate{{ID: 2, Status: TaskCancelled}})

	total, done := s.Progress()
	// cancelled tasks are not "complete" — only done counts.
	if total != 3 || done != 1 {
		t.Fatalf("expected 3 total, 1 done; got %d total, %d done", total, done)
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run TestTaskStore_Progress -v`

Expected: compilation error.

- [ ] **Step 3: Implement Progress**

```go
// Progress returns (total tasks, completed tasks). Only tasks with
// status "done" count as completed. Cancelled tasks are not complete.
func (s *TaskStore) Progress() (total, done int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total = len(s.tasks)
	for _, t := range s.tasks {
		if t.Status == TaskDone {
			done++
		}
	}
	return
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run TestTaskStore_Progress -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/task_store.go agent/task_store_test.go
git commit -m "feat: add Progress helper to TaskStore"
```

---

## Chunk 2: Tool schema, exec handler, and system prompt

### Task 7: Update tool schema for depends_on

**Files:**
- Modify: `agent/profile.go:770-811`

- [ ] **Step 1: Add depends_on to the append and update schemas**

In `defTaskList()`, update the `tasks` items properties to include:
```go
"depends_on": map[string]any{
	"type":        "array",
	"items":       map[string]any{"type": "integer"},
	"description": "IDs of tasks this one depends on. Optional.",
},
```

Update the `updates` items properties to include:
```go
"depends_on": map[string]any{
	"type":        "array",
	"items":       map[string]any{"type": "integer"},
	"description": "Set dependencies. [] clears them. Omit to leave unchanged.",
},
```

- [ ] **Step 2: Run existing tool tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskListTool' -v`

Expected: PASS (schema change is additive).

- [ ] **Step 3: Commit**

```bash
git add agent/profile.go
git commit -m "feat: add depends_on to task_list tool schema"
```

---

### Task 8: Update tool exec handler — parse depends_on and enrich response

**Files:**
- Modify: `agent/session.go:2440-2496`
- Test: `agent/task_store_test.go` (tool-level tests)

- [ ] **Step 1: Write tests for tool-level depends_on and response enrichment**

```go
func TestTaskListTool_AppendWithDependsOn(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append task 1.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"description": "Setup", "prompt": "Do setup"}]
		}`),
	})

	// Append task 2 with depends_on.
	res := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c2",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"description": "Build", "prompt": "Do build", "depends_on": [1]}]
		}`),
	})
	if res.IsError {
		t.Fatalf("append with depends_on error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "depends_on") || !strings.Contains(res.Output, "1") {
		t.Fatalf("output should show depends_on: %s", res.Output)
	}
}

func TestTaskListTool_UpdateShowsNextTask(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Create tasks: A (no deps), B depends on A, C depends on A.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"description": "Task A", "prompt": "a"},
				{"description": "Task B", "prompt": "b", "depends_on": [1]},
				{"description": "Task C", "prompt": "c", "depends_on": [1]}
			]
		}`),
	})

	// Mark task A done — response should suggest B and C.
	res := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c2",
		Name: "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "done"}]}`),
	})
	if res.IsError {
		t.Fatalf("update error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Task B") || !strings.Contains(res.Output, "Task C") {
		t.Fatalf("expected next-task suggestions for B and C: %s", res.Output)
	}
	if !strings.Contains(res.Output, "in_progress") {
		t.Fatalf("expected suggestion to mark in_progress: %s", res.Output)
	}
}

func TestTaskListTool_UpdateShowsAllComplete(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"description": "Only task", "prompt": "do it"}]
		}`),
	})

	res := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c2",
		Name: "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "done"}]}`),
	})
	if res.IsError {
		t.Fatalf("update error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "All tasks complete") {
		t.Fatalf("expected 'All tasks complete': %s", res.Output)
	}
}

func TestTaskListTool_UpdateShowsBlocked(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// A → B → C. Cancel A. B is unblocked, mark B done. C depends on B (done), so C is ready.
	// Instead: A, B depends on A, C depends on B. Mark A done, then cancel B.
	// Now C depends on B which is cancelled — C should be ready (cancelled satisfies).
	// For a true "blocked" scenario: A, B depends on A. Don't touch A. Mark nothing done.
	// Then there's nothing to mark done/cancelled to trigger the message...
	// Let's create: A (in_progress), B depends on A, C depends on B.
	// Cancel C — the response should show that B is blocked (depends on A which is in_progress).
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"description": "Task A", "prompt": "a"},
				{"description": "Task B", "prompt": "b", "depends_on": [1]},
				{"description": "Task C", "prompt": "c", "depends_on": [2]}
			]
		}`),
	})

	// Mark A in_progress, then cancel C. B and C are the only other tasks.
	// B depends on A (in_progress, not done) → B is blocked.
	// C is cancelled. No open tasks with satisfied deps.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c2",
		Name: "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "in_progress"}]}`),
	})

	res := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c3",
		Name: "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 3, "status": "cancelled"}]}`),
	})
	if res.IsError {
		t.Fatalf("update error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "No tasks are currently ready") {
		t.Fatalf("expected blocked message: %s", res.Output)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskListTool_AppendWithDependsOn|TestTaskListTool_UpdateShows' -v`

Expected: FAIL.

- [ ] **Step 3: Update the append handler to parse depends_on**

In `session.go`, in the `case "append"` block, after parsing description and prompt:

```go
var depIDs []int
if depsRaw, ok := m["depends_on"].([]any); ok {
	for _, d := range depsRaw {
		if v, ok := d.(float64); ok {
			depIDs = append(depIDs, int(v))
		}
	}
}
items = append(items, TaskInput{
	Description: fmt.Sprint(m["description"]),
	Prompt:      fmt.Sprint(m["prompt"]),
	DependsOn:   depIDs,
})
```

- [ ] **Step 4: Update the update handler to parse depends_on and enrich response**

In the `case "update"` block, after parsing notes:

```go
if depsRaw, ok := m["depends_on"]; ok {
	var depIDs []int
	if arr, ok := depsRaw.([]any); ok {
		for _, d := range arr {
			if v, ok := d.(float64); ok {
				depIDs = append(depIDs, int(v))
			}
		}
	}
	u.DependsOn = &depIDs
}
```

Replace `return nil, store.Update(updates)` with enriched response logic:

```go
if err := store.Update(updates); err != nil {
	return nil, err
}

// Check if any task was marked done or cancelled — if so, suggest next tasks.
var completedAny bool
for _, u := range updates {
	if u.Status == TaskDone || u.Status == TaskCancelled {
		completedAny = true
		break
	}
}

if !completedAny {
	return "Updated.", nil
}

eligible := store.NextEligible()
total, done := store.Progress()

var msg strings.Builder
msg.WriteString(fmt.Sprintf("Updated. Progress: %d/%d tasks complete.\n", done, total))

switch len(eligible) {
case 0:
	if done == total {
		msg.WriteString("All tasks complete.")
	} else {
		msg.WriteString("No tasks are currently ready (remaining tasks have unsatisfied dependencies).")
	}
case 1:
	msg.WriteString(fmt.Sprintf("\nNext task: #%d — %s. Mark it in_progress to begin.", eligible[0].ID, eligible[0].Description))
default:
	msg.WriteString("\nReady tasks:\n")
	for _, t := range eligible {
		msg.WriteString(fmt.Sprintf("  #%d — %s\n", t.ID, t.Description))
	}
	msg.WriteString("Pick one and mark it in_progress.")
}
return msg.String(), nil
```

- [ ] **Step 5: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskListTool' -v`

Expected: all pass.

- [ ] **Step 6: Run full agent test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -count=1`

Expected: all pass except `TestAllProfiles_SystemPromptContainsTaskListGuidance` (fixed next).

- [ ] **Step 7: Commit**

```bash
git add agent/session.go agent/task_store_test.go
git commit -m "feat: parse depends_on in tool handler, enrich update response with next-task suggestions"
```

---

### Task 9: System prompt guidance in core.md

**Files:**
- Modify: `agent/prompts/core.md`
- Modify: `agent/profile_test.go` (the guidance test should now pass)

- [ ] **Step 1: Add task tracking section to core.md**

Append to `agent/prompts/core.md`:

```markdown
## Task tracking

Use the task_list tool to plan and track multi-step work.

- At the start of complex work, create a task list to organize your approach.
- Mark tasks open/in_progress/done/cancelled to track state.
- Mark tasks in_progress before starting work on them, done when complete.
- Use depends_on to express ordering relationships between tasks — a task with
  depends_on will not be suggested as "next" until its dependencies are done.
- When you complete a task, the tool tells you what to work on next. Follow its
  guidance to stay on track.
- Add notes when updating tasks to record what you tried and what happened.
```

- [ ] **Step 2: Run the guidance test**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run TestAllProfiles_SystemPromptContainsTaskListGuidance -v`

Expected: PASS.

- [ ] **Step 3: Run full test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -count=1`

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add agent/prompts/core.md agent/profile_test.go
git commit -m "feat: add task_list guidance to core.md system prompt"
```

---

## Chunk 3: Dynamic task reminders

### Task 10: Task reminder tracking state

**Files:**
- Modify: `agent/session.go` (add tracking fields)

- [ ] **Step 1: Add tracking fields to Session struct**

Find the Session struct fields (around line 200-264 in session.go). Add:

```go
taskToolLastRound int  // totalRounds value at last task_list tool call
taskToolEverUsed  bool // whether task_list has ever been called
taskNudgeFired    bool // whether the "consider using task_list" nudge has fired
totalRounds       int  // cumulative tool rounds across all inputs
```

- [ ] **Step 2: Increment totalRounds in the tool loop**

In `processOneInput`, where `s.currentRound = round` is set (line 1288), add:

```go
s.currentRound = round
s.totalRounds++
```

- [ ] **Step 3: Update the task_list tool exec to track usage**

In the task_list tool registration (around line 2441), at the top of the Exec function, add:

```go
s.mu.Lock()
s.taskToolEverUsed = true
s.taskToolLastRound = s.totalRounds
s.mu.Unlock()
```

- [ ] **Step 4: Commit**

```bash
git add agent/session.go
git commit -m "feat: add task reminder tracking state to Session"
```

---

### Task 11: Implement task reminder formatters

**Files:**
- Create: `agent/task_reminders.go`
- Create: `agent/task_reminders_test.go`

- [ ] **Step 1: Write tests for all three reminder formatters**

Create `agent/task_reminders_test.go`:

```go
package agent

import (
	"strings"
	"testing"
)

func TestTaskReminderFull(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "test")
	store.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
	})
	store.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})

	msg := taskReminderFull(store)
	if msg == "" {
		t.Fatal("expected non-empty full reminder")
	}
	// Should contain all tasks with statuses.
	if !strings.Contains(msg, "done") || !strings.Contains(msg, "open") {
		t.Fatalf("full reminder should list all statuses: %s", msg)
	}
	// Should show dependencies.
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
		{Description: "Task A", Prompt: "a"},
		{Description: "Task B", Prompt: "b"},
	})
	store.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}})

	msg := taskReminderForInactivity(store)
	if msg == "" {
		t.Fatal("expected non-empty reminder")
	}
	// Should mention in-progress task.
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
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskReminder' -v`

Expected: compilation error — functions not defined.

- [ ] **Step 3: Implement the three reminder formatters**

Create `agent/task_reminders.go`:

```go
package agent

import (
	"fmt"
	"strings"
)

// taskReminderFull generates the full task list for post-compaction injection.
func taskReminderFull(store *TaskStore) string {
	tasks := store.View()
	if len(tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Task list:\n")
	for _, t := range tasks {
		b.WriteString(fmt.Sprintf("  [%s] #%d: %s", t.Status, t.ID, t.Description))
		if len(t.DependsOn) > 0 {
			b.WriteString(fmt.Sprintf(" (depends_on: %v)", t.DependsOn))
		}
		b.WriteString("\n")
		for _, n := range t.Notes {
			b.WriteString(fmt.Sprintf("    note: %s\n", n))
		}
	}
	return b.String()
}

// taskReminderForInactivity generates the periodic reminder when tasks exist
// but the tool hasn't been used recently.
func taskReminderForInactivity(store *TaskStore) string {
	tasks := store.View()
	if len(tasks) == 0 {
		return ""
	}

	total, done := store.Progress()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Task reminder (Progress: %d/%d tasks complete):\n", done, total))

	// Show in-progress tasks.
	var hasInProgress bool
	for _, t := range tasks {
		if t.Status == TaskInProgress {
			b.WriteString(fmt.Sprintf("  Current: #%d — %s\n", t.ID, t.Description))
			hasInProgress = true
		}
	}

	// Show next eligible tasks (up to 3).
	eligible := store.NextEligible()
	if len(eligible) > 3 {
		eligible = eligible[:3]
	}
	if len(eligible) > 0 {
		if hasInProgress {
			b.WriteString("  Up next:\n")
		} else {
			b.WriteString("  Ready:\n")
		}
		for _, t := range eligible {
			b.WriteString(fmt.Sprintf("    #%d — %s\n", t.ID, t.Description))
		}
	}

	return b.String()
}

// taskReminderNudge generates the one-time suggestion to use task_list.
func taskReminderNudge() string {
	return "You have a task_list tool available for organizing multi-step work. " +
		"Consider creating a task list to track your progress."
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestTaskReminder' -v`

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add agent/task_reminders.go agent/task_reminders_test.go
git commit -m "feat: add task reminder formatters for inactivity, compaction, and nudge"
```

---

### Task 12: Wire reminder injection into the session loop

**Files:**
- Modify: `agent/session.go`
- Test: `agent/task_reminders_test.go`

- [ ] **Step 1: Write tests for maybeInjectTaskReminder**

Add to `agent/task_reminders_test.go`:

```go
func TestMaybeInjectTaskReminder_NudgeAfter10Rounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Simulate 9 rounds — no nudge yet.
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
	store.Append([]TaskInput{{Description: "A", Prompt: "a"}})
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
	if !strings.Contains(msg, "Task A") {
		t.Fatalf("reminder should mention tasks: %s", msg)
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
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestMaybeInjectTaskReminder' -v`

Expected: compilation error — method not defined.

- [ ] **Step 3: Add maybeInjectTaskReminder to Session**

In `agent/session.go`:

```go
// maybeInjectTaskReminder checks whether a task-related steering message
// should be injected before the next LLM call. Returns the message or "".
func (s *Session) maybeInjectTaskReminder() string {
	s.mu.Lock()
	totalRounds := s.totalRounds
	lastRound := s.taskToolLastRound
	everUsed := s.taskToolEverUsed
	nudgeFired := s.taskNudgeFired
	s.mu.Unlock()

	roundsSinceUse := totalRounds - lastRound

	// Trigger 3: never used task_list, 10+ rounds in.
	if !everUsed && !nudgeFired && totalRounds >= 10 {
		s.mu.Lock()
		s.taskNudgeFired = true
		s.mu.Unlock()
		return taskReminderNudge()
	}

	// Trigger 2: tasks exist, not used in 5+ rounds.
	if everUsed && roundsSinceUse >= 5 {
		store := s.getOrCreateTaskStore()
		if len(store.View()) > 0 {
			// Reset the counter so we don't fire every round after 5.
			s.mu.Lock()
			s.taskToolLastRound = totalRounds
			s.mu.Unlock()
			return taskReminderForInactivity(store)
		}
	}

	return ""
}
```

- [ ] **Step 4: Call it in the tool loop**

In `processOneInput`, after the steering queue drain at the end of the loop (around line 1683-1687), add:

```go
// Task reminder injection.
if reminder := s.maybeInjectTaskReminder(); reminder != "" {
	s.appendTurn(TurnSteering, llm.User(reminder))
	s.emit(EventSteeringInjected, SteeringInjectedData{Text: reminder})
}
```

- [ ] **Step 5: Wire compaction callback to inject full task reminder**

In the `OnCompactionTurn` callback setup (around line 360), extend it:

```go
s.contextMgr.OnCompactionTurn = func(t Turn) {
	if err := s.transcript.Append(t); err != nil {
		s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
	}
	// After compaction, inject full task list if tasks exist.
	if s.taskStore != nil {
		if reminder := taskReminderFull(s.taskStore); reminder != "" {
			s.Steer(reminder)
		}
	}
}
```

- [ ] **Step 6: Run tests — verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestMaybeInjectTaskReminder' -v`

Expected: all pass.

- [ ] **Step 7: Run full agent test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -count=1`

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add agent/session.go agent/task_reminders_test.go
git commit -m "feat: wire task reminder injection into session loop and compaction callback"
```

---

### Task 13: Update compaction snapshot format

**Files:**
- Modify: `agent/context_manager.go:719-725`

- [ ] **Step 1: Update the compaction checkpoint to show dependencies**

In `agent/context_manager.go`, update the task snapshot formatting (around line 720-725):

```go
if meta != nil && len(meta.TaskSnapshot) > 0 {
	fixed.WriteString("\nTask list:\n")
	for _, t := range meta.TaskSnapshot {
		line := fmt.Sprintf("  [%s] #%d: %s", string(t.Status), t.ID, t.Description)
		if len(t.DependsOn) > 0 {
			line += fmt.Sprintf(" (depends_on: %v)", t.DependsOn)
		}
		fixed.WriteString(line + "\n")
	}
}
```

- [ ] **Step 2: Run context manager tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -run 'TestContext' -v`

Expected: all pass.

- [ ] **Step 3: Commit**

```bash
git add agent/context_manager.go
git commit -m "feat: include dependencies in compaction task snapshot"
```

---

### Task 14: Final integration verification

- [ ] **Step 1: Run the full test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./agent/ -count=1 -v 2>&1 | tail -40`

Expected: all pass.

- [ ] **Step 2: Run the full project test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./... -count=1`

Expected: all pass.

- [ ] **Step 3: Verify clean state**

```bash
git status
git log --oneline -15
```

Expected: clean working tree on the feature branch, ~12 commits covering all tasks.

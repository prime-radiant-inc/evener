package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/afero"
)

// TaskTemplate defines a default task in an agent's workflow. When a session
// starts from such an agent, its templates seed the initial task list.
type TaskTemplate struct {
	Title           string `json:"title"`                      // becomes the Task description
	Prompt          string `json:"prompt"`                     // instruction for the task
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // per-task reasoning effort override
	Type            string `json:"type,omitempty"`             // TaskType string; empty defaults to "implement"
	Insert          string `json:"insert,omitempty"`           // expansion marker, e.g. "parent_tasks"
}

// TaskStatus represents the state of a task.
type TaskStatus string

const (
	// TaskOpen is the status of a task that has not been started.
	TaskOpen TaskStatus = "open"
	// TaskInProgress is the status of a task that is currently being worked on.
	TaskInProgress TaskStatus = "in_progress"
	// TaskDone is the status of a task that has been completed.
	TaskDone TaskStatus = "done"
	// TaskCancelled is the status of a task that was cancelled.
	TaskCancelled TaskStatus = "cancelled"
)

// TaskType classifies what kind of work a task represents.
type TaskType string

const (
	// TaskTypeResearch is a task that investigates or gathers information.
	TaskTypeResearch TaskType = "research"
	// TaskTypeImplement is a task that writes or changes code.
	TaskTypeImplement TaskType = "implement"
	// TaskTypeVerify is a task that checks or validates work.
	TaskTypeVerify TaskType = "verify"
	// TaskTypeFix is a task that corrects a problem.
	TaskTypeFix TaskType = "fix"
)

// Task is a single work item in the agent's task list.
type Task struct {
	ID          int        `json:"id"`          // store-assigned, 1-based identifier
	Type        TaskType   `json:"type"`        // classification of the work
	Description string     `json:"description"` // short human-readable summary
	Prompt      string     `json:"prompt"`      // full instruction handed to whoever runs the task
	Status      TaskStatus `json:"status"`      // current lifecycle state
	// DependsOn lists IDs of tasks that must complete before this one is ready.
	DependsOn []int `json:"depends_on,omitempty"`
	// Notes accumulates free-form progress notes appended over the task's life.
	Notes []string `json:"notes,omitempty"`
	// ReasoningEffort overrides the reasoning effort for a subagent that runs
	// this task (low|medium|high); empty uses the session default.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Insert is a template-expansion marker (e.g. "parent_tasks") carried over
	// from the task template it was created from; empty for ordinary tasks.
	Insert string `json:"insert,omitempty"`
	// CreatedAt/UpdatedAt/CompletedAt are minted automatically by the store —
	// never settable through the agent-facing tool (TaskInput/TaskUpdate carry
	// no timestamp fields). CreatedAt is stamped once when the task is added;
	// UpdatedAt advances on every mutation; CompletedAt is stamped when the task
	// transitions to done and cleared if it is later reopened. Pointers so an
	// unset stamp (and tasks persisted before timestamps existed) omit cleanly.
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TaskInput is the data needed to create a new task.
type TaskInput struct {
	Type            TaskType `json:"type"`                       // classification of the work
	Description     string   `json:"description"`                // short human-readable summary
	Prompt          string   `json:"prompt"`                     // full instruction for the task
	DependsOn       []int    `json:"depends_on,omitempty"`       // IDs of prerequisite tasks
	ReasoningEffort string   `json:"reasoning_effort,omitempty"` // per-task reasoning effort override
	Insert          string   `json:"insert,omitempty"`           // template-expansion marker (see Task.Insert)
}

// TaskUpdate is a status change for an existing task.
// DependsOn nil means no change; &[]int{} clears the dependency list.
// ReasoningEffort empty means no change; a non-empty value replaces it.
type TaskUpdate struct {
	ID              int        `json:"id"`                         // identifies the task to update
	Status          TaskStatus `json:"status"`                     // the new status to apply
	Notes           string     `json:"notes,omitempty"`            // a progress note to append (not replace)
	DependsOn       *[]int     `json:"depends_on,omitempty"`       // nil: no change; &[]int{}: clear
	ReasoningEffort string     `json:"reasoning_effort,omitempty"` // empty: no change; non-empty: replace
}

// TaskStore manages a persistent list of tasks stored as JSON.
type TaskStore struct {
	mu     sync.Mutex
	tasks  []Task
	nextID int
	path   string
	now    func() time.Time
	fs     afero.Fs
}

// NewTaskStore creates a TaskStore that persists to <stateDir>/tasks/<sessionID>.json.
// Each session (parent or subagent) gets its own task file, ensuring isolation.
func NewTaskStore(stateDir, sessionID string) *TaskStore {
	return &TaskStore{
		nextID: 1,
		path:   filepath.Join(stateDir, "tasks", sessionID+".json"),
		now:    time.Now,
		fs:     afero.NewOsFs(),
	}
}

// SetClock overrides the store's time source. Used by tests for deterministic
// timestamps; returns the store for call chaining.
func (s *TaskStore) SetClock(now func() time.Time) *TaskStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	return s
}

// SetFs overrides the store's filesystem. Production defaults to afero.NewOsFs()
// (identical to direct os calls); tests and fuzzers inject an in-memory or
// sandboxed filesystem. Returns the store for call chaining.
func (s *TaskStore) SetFs(fs afero.Fs) *TaskStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fs = fs
	return s
}

// stamp returns a pointer to the current store time.
func (s *TaskStore) stamp() *time.Time {
	t := s.now()
	return &t
}

// Load reads the task list from disk. If the file does not exist, the store starts empty.
func (s *TaskStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := afero.ReadFile(s.fs, s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tasks: %w", err)
	}

	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return fmt.Errorf("unmarshal tasks: %w", err)
	}
	s.tasks = tasks

	// Set nextID to max existing ID + 1.
	maxID := 0
	for _, t := range tasks {
		if t.ID > maxID {
			maxID = t.ID
		}
	}
	s.nextID = maxID + 1
	return nil
}

// save writes the task list to disk atomically.
func (s *TaskStore) save() error {
	dir := filepath.Dir(s.path)
	if err := s.fs.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tasks dir: %w", err)
	}

	data, err := json.MarshalIndent(s.tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tasks: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := afero.WriteFile(s.fs, tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := s.fs.Rename(tmp, s.path); err != nil {
		_ = s.fs.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// View returns a copy of all tasks.
func (s *TaskStore) View() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Task{}, s.tasks...)
}

// validateDependencies checks that all IDs in deps exist (in s.tasks or pending)
// and that adding them does not create a cycle in the dependency graph.
// taskID is the task being created or modified.
// pending holds tasks being appended in the same batch (not yet in s.tasks).
func (s *TaskStore) validateDependencies(taskID int, deps []int, pending []Task) error {
	// Build known ID set from existing tasks + pending.
	known := make(map[int]bool, len(s.tasks)+len(pending))
	for _, t := range s.tasks {
		known[t.ID] = true
	}
	for _, t := range pending {
		known[t.ID] = true
	}

	for _, dep := range deps {
		if dep == taskID {
			return fmt.Errorf("task %d cannot depend on itself", taskID)
		}
		if !known[dep] {
			return fmt.Errorf("task %d depends on unknown task %d", taskID, dep)
		}
	}

	// Build adjacency map for cycle detection.
	// Include all existing tasks, all pending tasks, and the task being validated.
	adj := make(map[int][]int)
	for _, t := range s.tasks {
		if t.ID == taskID {
			// Will be replaced by the new deps below.
			continue
		}
		adj[t.ID] = t.DependsOn
	}
	for _, t := range pending {
		if t.ID == taskID {
			continue
		}
		adj[t.ID] = t.DependsOn
	}
	adj[taskID] = deps

	if hasCycle(adj) {
		return fmt.Errorf("task %d would create a dependency cycle", taskID)
	}
	return nil
}

// hasCycle returns true if the directed graph contains a cycle (DFS with white/gray/black coloring).
func hasCycle(adj map[int][]int) bool {
	// 0 = white (unvisited), 1 = gray (in stack), 2 = black (done)
	color := make(map[int]int, len(adj))

	var dfs func(node int) bool
	dfs = func(node int) bool {
		color[node] = 1 // gray
		for _, neighbor := range adj[node] {
			if color[neighbor] == 1 {
				return true // back edge → cycle
			}
			if color[neighbor] == 0 {
				if dfs(neighbor) {
					return true
				}
			}
		}
		color[node] = 2 // black
		return false
	}

	for node := range adj {
		if color[node] == 0 {
			if dfs(node) {
				return true
			}
		}
	}
	return false
}

// Append adds new tasks with auto-assigned IDs and status=open. Returns the created tasks.
func (s *TaskStore) Append(items []TaskInput) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	savedNextID := s.nextID

	// Build all tasks first without committing to s.tasks.
	var added []Task
	for _, item := range items {
		taskType := item.Type
		if taskType == "" {
			taskType = TaskTypeImplement // default to implement
		}
		ts := s.stamp()
		t := Task{
			ID:              s.nextID,
			Type:            taskType,
			Description:     item.Description,
			Prompt:          item.Prompt,
			Status:          TaskOpen,
			DependsOn:       item.DependsOn,
			ReasoningEffort: item.ReasoningEffort,
			Insert:          item.Insert,
			CreatedAt:       ts,
			UpdatedAt:       ts,
		}
		s.nextID++
		added = append(added, t)
	}

	// Validate all dependency constraints before committing.
	for _, t := range added {
		if len(t.DependsOn) == 0 {
			continue
		}
		// pending = the other tasks in this batch (not the task itself).
		var pending []Task
		for _, p := range added {
			if p.ID != t.ID {
				pending = append(pending, p)
			}
		}
		if err := s.validateDependencies(t.ID, t.DependsOn, pending); err != nil {
			s.nextID = savedNextID
			return nil, err
		}
	}

	s.tasks = append(s.tasks, added...)

	if err := s.save(); err != nil {
		return added, fmt.Errorf("save: %w", err)
	}
	return added, nil
}

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
	return total, done
}

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

	var result []Task
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
			result = append(result, t)
		}
	}
	return result
}

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

// PopulateFromTemplates initializes the task store from agent definition templates.
// If parentTasks is non-nil and non-empty, they replace the template with Insert=="parent_tasks".
// The first task is auto-started (set to in_progress).
// No-op if the store already has tasks.
func (s *TaskStore) PopulateFromTemplates(templates []TaskTemplate, parentTasks []TaskTemplate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.tasks) > 0 {
		return nil // already populated
	}

	// Build the effective template list by expanding the insert placeholder.
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
		ts := s.stamp()
		t := Task{
			ID:              s.nextID,
			Type:            taskType,
			Description:     tt.Title,
			Prompt:          tt.Prompt,
			Status:          TaskOpen,
			ReasoningEffort: tt.ReasoningEffort,
			Insert:          tt.Insert,
			CreatedAt:       ts,
			UpdatedAt:       ts,
		}
		s.nextID++
		s.tasks = append(s.tasks, t)
	}

	// Auto-start the first task.
	if len(s.tasks) > 0 {
		s.tasks[0].Status = TaskInProgress
		s.tasks[0].UpdatedAt = s.stamp()
	}

	return s.save()
}

// Update changes the status of existing tasks.
func (s *TaskStore) Update(updates []TaskUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate status values up front so a bad status doesn't half-apply.
	for _, u := range updates {
		switch u.Status {
		case TaskOpen, TaskInProgress, TaskDone, TaskCancelled:
			// valid
		default:
			return fmt.Errorf("invalid status %q for task %d: must be open, in_progress, done, or cancelled", u.Status, u.ID)
		}
	}

	// Enforce the single-in_progress invariant: simulate the resulting state
	// and reject the whole batch if more than one task would be in_progress.
	projected := make(map[int]TaskStatus, len(s.tasks))
	for _, t := range s.tasks {
		projected[t.ID] = t.Status
	}
	for _, u := range updates {
		if _, exists := projected[u.ID]; !exists {
			return fmt.Errorf("unknown task ID %d", u.ID)
		}
		projected[u.ID] = u.Status
	}
	inProgressCount := 0
	for _, status := range projected {
		if status == TaskInProgress {
			inProgressCount++
		}
	}
	if inProgressCount > 1 {
		return fmt.Errorf("only one task may be in_progress at a time; update would result in %d", inProgressCount)
	}

	// Validate dependency changes against the PROJECTED graph up front — like the
	// status and single-in_progress checks above — so a bad or cycle-forming
	// dependency in any update can't leave earlier updates half-applied. Validating
	// inside the apply loop (the previous behavior) both mutated before validating
	// AND could miss a cycle formed jointly by several updates in one batch, because
	// it saw only the dep changes applied so far.
	known := make(map[int]bool, len(s.tasks))
	projDeps := make(map[int][]int, len(s.tasks))
	for _, t := range s.tasks {
		known[t.ID] = true
		projDeps[t.ID] = t.DependsOn
	}
	for _, u := range updates {
		if u.DependsOn != nil {
			projDeps[u.ID] = *u.DependsOn
		}
	}
	for _, u := range updates {
		if u.DependsOn == nil {
			continue
		}
		for _, dep := range *u.DependsOn {
			if dep == u.ID {
				return fmt.Errorf("task %d cannot depend on itself", u.ID)
			}
			if !known[dep] {
				return fmt.Errorf("task %d depends on unknown task %d", u.ID, dep)
			}
		}
	}
	if hasCycle(projDeps) {
		return errors.New("update would create a dependency cycle")
	}

	for _, u := range updates {
		found := false
		for i := range s.tasks {
			if s.tasks[i].ID == u.ID {
				s.tasks[i].Status = u.Status
				if u.Notes != "" {
					s.tasks[i].Notes = append(s.tasks[i].Notes, u.Notes)
				}
				if u.DependsOn != nil {
					s.tasks[i].DependsOn = *u.DependsOn
				}
				if u.ReasoningEffort != "" {
					s.tasks[i].ReasoningEffort = u.ReasoningEffort
				}
				// Mint timestamps: every update advances UpdatedAt; reaching done
				// stamps CompletedAt, and reopening a done task clears it.
				ts := s.stamp()
				s.tasks[i].UpdatedAt = ts
				if u.Status == TaskDone {
					s.tasks[i].CompletedAt = ts
				} else {
					s.tasks[i].CompletedAt = nil
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown task ID %d", u.ID)
		}
	}

	return s.save()
}

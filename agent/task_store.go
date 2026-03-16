package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TaskStatus represents the state of a task.
type TaskStatus string

const (
	TaskOpen       TaskStatus = "open"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskCancelled  TaskStatus = "cancelled"
)

// Task is a single work item in the agent's task list.
type Task struct {
	ID          int        `json:"id"`
	Description string     `json:"description"`
	Prompt      string     `json:"prompt"`
	Status      TaskStatus `json:"status"`
	DependsOn   []int      `json:"depends_on,omitempty"`
	Notes       []string   `json:"notes,omitempty"`
}

// TaskInput is the data needed to create a new task.
type TaskInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	DependsOn   []int  `json:"depends_on,omitempty"`
}

// TaskUpdate is a status change for an existing task.
// DependsOn nil means no change; &[]int{} clears the dependency list.
type TaskUpdate struct {
	ID        int        `json:"id"`
	Status    TaskStatus `json:"status"`
	Notes     string     `json:"notes,omitempty"`
	DependsOn *[]int     `json:"depends_on,omitempty"`
}

// TaskStore manages a persistent list of tasks stored as JSON.
type TaskStore struct {
	mu     sync.Mutex
	tasks  []Task
	nextID int
	path   string
}

// NewTaskStore creates a TaskStore that persists to <stateDir>/tasks/<sessionID>.json.
// Each session (parent or subagent) gets its own task file, ensuring isolation.
func NewTaskStore(stateDir, sessionID string) *TaskStore {
	return &TaskStore{
		nextID: 1,
		path:   filepath.Join(stateDir, "tasks", sessionID+".json"),
	}
}

// Load reads the task list from disk. If the file does not exist, the store starts empty.
func (s *TaskStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tasks dir: %w", err)
	}

	data, err := json.MarshalIndent(s.tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tasks: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
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

// Update changes the status of existing tasks.
func (s *TaskStore) Update(updates []TaskUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range updates {
		switch u.Status {
		case TaskOpen, TaskInProgress, TaskDone, TaskCancelled:
			// valid
		default:
			return fmt.Errorf("invalid status %q for task %d: must be open, in_progress, done, or cancelled", u.Status, u.ID)
		}
		found := false
		for i := range s.tasks {
			if s.tasks[i].ID == u.ID {
				s.tasks[i].Status = u.Status
				if u.Notes != "" {
					s.tasks[i].Notes = append(s.tasks[i].Notes, u.Notes)
				}
				if u.DependsOn != nil {
					if err := s.validateDependencies(u.ID, *u.DependsOn, nil); err != nil {
						return err
					}
					s.tasks[i].DependsOn = *u.DependsOn
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

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
	TaskUndone    TaskStatus = "undone"
	TaskDone      TaskStatus = "done"
	TaskCancelled TaskStatus = "cancelled"
)

// Task is a single work item in the agent's task list.
type Task struct {
	ID          int        `json:"id"`
	Description string     `json:"description"`
	Prompt      string     `json:"prompt"`
	Status      TaskStatus `json:"status"`
}

// TaskInput is the data needed to create a new task.
type TaskInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// TaskUpdate is a status change for an existing task.
type TaskUpdate struct {
	ID     int        `json:"id"`
	Status TaskStatus `json:"status"`
}

// TaskStore manages a persistent list of tasks stored as JSON.
type TaskStore struct {
	mu     sync.Mutex
	tasks  []Task
	nextID int
	path   string
}

const tasksFile = ".serf/tasks.json"

// NewTaskStore creates a TaskStore that persists to <workDir>/.serf/tasks.json.
func NewTaskStore(workDir string) *TaskStore {
	return &TaskStore{
		nextID: 1,
		path:   filepath.Join(workDir, tasksFile),
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
		os.Remove(tmp)
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

// Append adds new tasks with auto-assigned IDs and status=undone. Returns the created tasks.
func (s *TaskStore) Append(items []TaskInput) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var added []Task
	for _, item := range items {
		t := Task{
			ID:          s.nextID,
			Description: item.Description,
			Prompt:      item.Prompt,
			Status:      TaskUndone,
		}
		s.nextID++
		s.tasks = append(s.tasks, t)
		added = append(added, t)
	}

	if err := s.save(); err != nil {
		return added, fmt.Errorf("save: %w", err)
	}
	return added, nil
}

// Update changes the status of existing tasks. Only done/undone/cancelled are valid.
func (s *TaskStore) Update(updates []TaskUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range updates {
		if u.Status != TaskDone && u.Status != TaskUndone && u.Status != TaskCancelled {
			return fmt.Errorf("invalid status %q for task %d: must be done, undone, or cancelled", u.Status, u.ID)
		}
		found := false
		for i := range s.tasks {
			if s.tasks[i].ID == u.ID {
				s.tasks[i].Status = u.Status
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

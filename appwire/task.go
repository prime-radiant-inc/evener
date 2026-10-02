package appwire

import "time"

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

// Task is a task-list row shared by persistence and evener/tasks/list.
// Its existing snake_case field names are part of the wire contract.
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
	// never settable through the agent-facing task tool. CreatedAt is stamped once when the task is added;
	// UpdatedAt advances on every mutation; CompletedAt is stamped when the task
	// transitions to a terminal status (done or cancelled) and cleared if it
	// is later reopened. Pointers so an
	// unset stamp (and tasks persisted before timestamps existed) omit cleanly.
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

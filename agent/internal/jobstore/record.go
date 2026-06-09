// Package jobstore provides the pure, Session-free durable substrate for job records.
package jobstore

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// JobType identifies the runtime that owns a job.
type JobType string

const (
	JobShell    JobType = "shell"
	JobDelegate JobType = "delegate"
)

// Status identifies the lifecycle state of a job.
type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusStopped   Status = "stopped"
)

// IsTerminal reports whether the status means no further runtime work is expected.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusStopped:
		return true
	default:
		return false
	}
}

// NotifyState identifies terminal-notification delivery progress.
type NotifyState string

const (
	NotifyNotArmed  NotifyState = "not_armed"
	NotifyPending   NotifyState = "pending"
	NotifyDelivered NotifyState = "delivered"
)

// JobRecord is the durable storage shape reconstructed from the job event log.
type JobRecord struct {
	JobID            string      `json:"job_id"`
	Type             JobType     `json:"type"`
	Status           Status      `json:"status"`
	Reason           string      `json:"reason,omitempty"`
	Description      string      `json:"description,omitempty"`
	Command          string      `json:"command,omitempty"`
	Task             string      `json:"task,omitempty"`
	ParentSessionID  string      `json:"parent_session_id,omitempty"`
	OwnerSessionID   string      `json:"owner_session_id"`
	VisibleToSession string      `json:"visible_to_session_id"`
	ParentJobID      string      `json:"parent_job_id,omitempty"`
	OriginTurnID     string      `json:"origin_turn_id,omitempty"`
	OriginToolCallID string      `json:"origin_tool_call_id,omitempty"`
	TranscriptRef    string      `json:"transcript_ref,omitempty"`
	Resumable        *bool       `json:"resumable,omitempty"`
	NotResumableWhy  string      `json:"not_resumable_reason,omitempty"`
	StartedAt        time.Time   `json:"started_at"`
	EndedAt          *time.Time  `json:"ended_at,omitempty"`
	ExitCode         *int        `json:"exit_code,omitempty"`
	OutputPath       string      `json:"output_path,omitempty"`
	OutputBytes      int64       `json:"output_bytes"`
	TerminalGen      string      `json:"terminal_generation,omitempty"`
	NotifyState      NotifyState `json:"terminal_notification_state"`
}

func NewJobID() string {
	return "job_" + ulid.Make().String()
}

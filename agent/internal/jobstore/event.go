package jobstore

import "time"

// EventKind identifies a durable job-lifecycle event in jobs.jsonl.
type EventKind string

const (
	EventJobStarted               EventKind = "job_started"
	EventJobSessionAssigned       EventKind = "job_session_assigned"
	EventJobFinished              EventKind = "job_finished"
	EventJobMessageSent           EventKind = "job_message_sent"
	EventJobNotificationPending   EventKind = "job_notification_pending"
	EventJobNotificationDelivered EventKind = "job_notification_delivered"
)

// Event is one line in the append-only jobs.jsonl log. It carries a flat union
// of every payload field used by any event kind; Fold applies the present
// fields onto the JobRecord. Seq is assigned by the Store at append time.
type Event struct {
	Kind EventKind `json:"kind"`
	Seq  int64     `json:"seq"`
	TS   time.Time `json:"ts"`

	JobID string `json:"job_id"`

	// job_started payload
	Type             JobType    `json:"type,omitempty"`
	Command          string     `json:"command,omitempty"`
	Task             string     `json:"task,omitempty"`
	Description      string     `json:"description,omitempty"`
	ParentSessionID  string     `json:"parent_session_id,omitempty"`
	OwnerSessionID   string     `json:"owner_session_id,omitempty"`
	VisibleToSession string     `json:"visible_to_session_id,omitempty"`
	ParentJobID      string     `json:"parent_job_id,omitempty"`
	OriginTurnID     string     `json:"origin_turn_id,omitempty"`
	OriginToolCallID string     `json:"origin_tool_call_id,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	OutputPath       string     `json:"output_path,omitempty"`

	// job_session_assigned payload
	TranscriptRef   string `json:"transcript_ref,omitempty"`
	Resumable       *bool  `json:"resumable,omitempty"`
	NotResumableWhy string `json:"not_resumable_reason,omitempty"`

	// job_finished payload
	Status      Status     `json:"status,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	OutputBytes int64      `json:"output_bytes,omitempty"`
	TerminalGen string     `json:"terminal_generation,omitempty"`

	// job_message_sent payload
	Target string `json:"target,omitempty"`
	Action string `json:"action,omitempty"`
}

// Package jobstore provides the pure, Session-free durable substrate for Serf's job-control system.
package jobstore

import (
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/agent/provenance"
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

// DelegateStatus identifies the lifecycle state of a durable delegate handle.
type DelegateStatus string

const (
	DelegateRunning      DelegateStatus = "running"
	DelegateDriving      DelegateStatus = "driving"
	DelegateIdle         DelegateStatus = "idle"
	DelegateStopped      DelegateStatus = "stopped"
	DelegateNotResumable DelegateStatus = "not_resumable"
)

// DelegateRestoreDescriptor carries the durable state needed to restore a delegate job.
type DelegateRestoreDescriptor struct {
	Version             int                `json:"version"`
	ChildSessionID      string             `json:"child_session_id"`
	TranscriptRef       string             `json:"transcript_ref"`
	ParentSessionID     string             `json:"parent_session_id,omitempty"`
	ParentJobID         string             `json:"parent_job_id,omitempty"`
	OwnerSessionID      string             `json:"owner_session_id,omitempty"`
	VisibleSessionID    string             `json:"visible_session_id,omitempty"`
	OriginTurnID        string             `json:"origin_turn_id,omitempty"`
	OriginToolCallID    string             `json:"origin_tool_call_id,omitempty"`
	Task                string             `json:"task,omitempty"`
	AgentType           string             `json:"agent_type,omitempty"`
	RequestedModel      string             `json:"requested_model,omitempty"`
	ResolvedProfileID   string             `json:"resolved_profile_id,omitempty"`
	ResolvedModel       string             `json:"resolved_model,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	AgentName           string             `json:"agent_name,omitempty"`
	FrozenRolePrompt    string             `json:"frozen_role_prompt,omitempty"`
	FrozenTaskPrompt    string             `json:"frozen_task_prompt,omitempty"`
	FrozenToolNames     []string           `json:"frozen_tool_names,omitempty"`
	FrozenSkillNames    []string           `json:"frozen_skill_names,omitempty"`
	FrozenSkillBodies   []string           `json:"frozen_skill_bodies,omitempty"`
	WorkingDir          string             `json:"working_dir,omitempty"`
	LocalEnvPolicy      string             `json:"local_env_policy,omitempty"`
	ResultSchema        any                `json:"result_schema,omitempty"`
	ExplicitToolGrants  []string           `json:"explicit_tool_grants,omitempty"`
	DelegationAllowance int                `json:"delegation_allowance,omitempty"`
	Provenance          *provenance.Causal `json:"provenance,omitempty"`
}

// DelegateRecord is the folded durable state for a delegate handle.
type DelegateRecord struct {
	DelegateID          string         `json:"delegate_id"`
	ChildSessionID      string         `json:"child_session_id"`
	TranscriptRef       string         `json:"transcript_ref"`
	OwnerSessionID      string         `json:"owner_session_id,omitempty"`
	VisibleSessionID    string         `json:"visible_session_id,omitempty"`
	ParentDelegateID    string         `json:"parent_delegate_id,omitempty"`
	AgentType           string         `json:"agent_type,omitempty"`
	Status              DelegateStatus `json:"status"`
	Resumable           bool           `json:"resumable"`
	NotResumableWhy     string         `json:"not_resumable_reason,omitempty"`
	CurrentJobID        string         `json:"current_job_id,omitempty"`
	LatestJobID         string         `json:"latest_job_id,omitempty"`
	Generation          string         `json:"generation,omitempty"`
	StopGateClosed      bool           `json:"stop_gate_closed,omitempty"`
	StopGateClosedJobID string         `json:"stop_gate_closed_job_id,omitempty"`
}

type WatchRecord struct {
	WatchID          string `json:"watch_id"`
	Generation       string `json:"generation"`
	OwnerSessionID   string `json:"owner_session_id"`
	VisibleSessionID string `json:"visible_session_id"`
	Target           string `json:"target"`
	SendTo           string `json:"send_to,omitempty"`
	ConfigHash       string `json:"config_hash"`
	Condition        string `json:"condition,omitempty"`
	Deliveries       int    `json:"deliveries,omitempty"`
	Active           bool   `json:"active"`
	EndReason        string `json:"end_reason,omitempty"`
}

type WatchConfigSnapshot struct {
	Target             string   `json:"target"`
	OutputMatch        string   `json:"output_match,omitempty"`
	ProgressIntervalMS int      `json:"progress_interval_ms,omitempty"`
	Events             []string `json:"events,omitempty"`
	Every              int      `json:"every,omitempty"`
	SendTo             string   `json:"send_to,omitempty"`
	SendMessage        string   `json:"send_message,omitempty"`
	IncludeExcerpt     bool     `json:"include_excerpt,omitempty"`
}

// WatchSendKey identifies the coalescing slot for a durable watch-send frame.
type WatchSendKey struct {
	VisibleSessionID        string `json:"visible_session_id"`
	WatchID                 string `json:"watch_id,omitempty"`
	WatchTarget             string `json:"watch_target"`
	ResolvedWatchedIdentity string `json:"resolved_watched_identity"`
	ResolvedSendTo          string `json:"resolved_send_to"`
	WatchGeneration         string `json:"watch_generation"`
}

// WatchSendState is the durable payload for a pending or terminal watch-send
// delivery state.
type WatchSendState struct {
	Key                WatchSendKey       `json:"key"`
	DeliveryID         string             `json:"delivery_id"`
	UpdateSeq          uint64             `json:"update_seq,omitempty"`
	Message            string             `json:"message,omitempty"`
	Frame              string             `json:"frame,omitempty"`
	TriggerIdentity    string             `json:"trigger_identity,omitempty"`
	TriggerReason      string             `json:"trigger_reason,omitempty"`
	CoalescedCount     int                `json:"coalesced_count,omitempty"`
	DelegateGeneration string             `json:"delegate_generation,omitempty"`
	DiagnosticReason   string             `json:"diagnostic_reason,omitempty"`
	CreatedAt          time.Time          `json:"created_at,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at,omitempty"`
	Provenance         *provenance.Causal `json:"provenance,omitempty"`
}

// WatchSendRecord is the folded durable state for pending watch-send frames.
type WatchSendRecord struct {
	Pending map[WatchSendKey]*WatchSendState
}

// JobRecord is the durable storage shape reconstructed from the job event log.
type JobRecord struct {
	JobID            string                     `json:"job_id"`
	Type             JobType                    `json:"type"`
	Status           Status                     `json:"status"`
	Reason           string                     `json:"reason,omitempty"`
	Description      string                     `json:"description,omitempty"`
	Command          string                     `json:"command,omitempty"`
	Task             string                     `json:"task,omitempty"`
	ParentSessionID  string                     `json:"parent_session_id,omitempty"`
	OwnerSessionID   string                     `json:"owner_session_id"`
	VisibleToSession string                     `json:"visible_to_session_id"`
	ParentJobID      string                     `json:"parent_job_id,omitempty"`
	DelegateID       string                     `json:"delegate_id,omitempty"`
	OriginTurnID     string                     `json:"origin_turn_id,omitempty"`
	OriginToolCallID string                     `json:"origin_tool_call_id,omitempty"`
	DelegateRestore  *DelegateRestoreDescriptor `json:"delegate_restore,omitempty"`
	TranscriptRef    string                     `json:"transcript_ref,omitempty"`
	Resumable        *bool                      `json:"resumable,omitempty"`
	NotResumableWhy  string                     `json:"not_resumable_reason,omitempty"`
	StartedAt        time.Time                  `json:"started_at"`
	// LastActivity is the in-memory timestamp of the job's most recent
	// parent-observable activity (output append or start). It is a supervision
	// signal for RUNNING jobs and is intentionally NOT folded from a durable
	// event: a record reloaded from the store has it nil, and the projection
	// falls back to EndedAt (then StartedAt). See projectJobRecord.
	LastActivity           *time.Time         `json:"-"`
	EndedAt                *time.Time         `json:"ended_at,omitempty"`
	ExitCode               *int               `json:"exit_code,omitempty"`
	OutputPath             string             `json:"output_path,omitempty"`
	OutputBytes            int64              `json:"output_bytes"`
	StructuredResult       any                `json:"structured_result,omitempty"`
	StructuredResultValid  *bool              `json:"structured_result_valid,omitempty"`
	StructuredResultReason string             `json:"structured_result_reason,omitempty"`
	TerminalGen            string             `json:"terminal_generation,omitempty"`
	NotifyState            NotifyState        `json:"terminal_notification_state"`
	Provenance             *provenance.Causal `json:"provenance,omitempty"`
	NotificationProvenance *provenance.Causal `json:"notification_provenance,omitempty"`
}

func NewJobID() string {
	return "job_" + ulid.Make().String()
}

func NewDelegateID() string {
	return "dlg_" + ulid.Make().String()
}

func NewDelegateGeneration() string {
	return "dg_" + ulid.Make().String()
}

func NewWatchID() string {
	return "watch_" + ulid.Make().String()
}

func NewWatchGeneration() string {
	return "wg_" + ulid.Make().String()
}

func NewWatchSendDeliveryID() string {
	return "wd_" + ulid.Make().String()
}

// Package jobstore provides the pure, Session-free durable substrate for Evener's job-control system.
package jobstore

import (
	"time"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/identifier"
)

// JobType identifies the runtime that owns a job.
type JobType string

const (
	JobShell JobType = "shell"
)

// Status identifies the lifecycle state of a job.
type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusStopped   Status = "stopped"
	StatusExhausted Status = "exhausted"
)

// IsTerminal reports whether the status means no further runtime work is expected.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusStopped, StatusExhausted:
		return true
	default:
		return false
	}
}

// NotifyState identifies terminal-notification delivery progress.
type NotifyState string

// Authority identifies which retained source justified a record.
type Authority string

const (
	AuthorityOwner             Authority = "owner"
	AuthorityForwardedFallback Authority = "forwarded_fallback"
	AuthorityLegacyUnknown     Authority = "legacy_unknown"
)

const (
	NotifyNotArmed  NotifyState = "not_armed"
	NotifyPending   NotifyState = "pending"
	NotifyDelivered NotifyState = "delivered"
	// NotifyConsumed means the caller learned the job ended by reading its
	// terminal job_status, not by being shown the notification. The caller was
	// told either way — which is why it settles the pending notification — but
	// it is a separate value from NotifyDelivered so the durable ledger and
	// evener-doctor keep saying which of the two actually happened.
	NotifyConsumed NotifyState = "consumed"
)

type WatchRecord struct {
	WatchID                  string `json:"watch_id"`
	Generation               string `json:"generation"`
	OwnerSessionID           string `json:"owner_session_id"`
	VisibleSessionID         string `json:"visible_session_id"`
	Target                   string `json:"target"`
	SendTo                   string `json:"send_to,omitempty"`
	ConfigHash               string `json:"config_hash"`
	Condition                string `json:"condition,omitempty"`
	Deliveries               int    `json:"deliveries,omitempty"`
	Active                   bool   `json:"active"`
	EndReason                string `json:"end_reason,omitempty"`
	Source                   string `json:"source,omitempty"`
	SourceDelegateID         string `json:"source_delegate_id,omitempty"`
	SourceDelegateGeneration uint64 `json:"source_delegate_generation,omitempty"`
	StableReceiver           bool   `json:"stable_receiver,omitempty"`
	// Receiver identity, folded out of the registration's config snapshot: WHO
	// this watch reports to, which owner/visible cannot say because a receiver
	// watch installs on the owner's manager and names the owner in both. Empty on
	// an owner's own watch and on any row written before the snapshot carried it.
	ReceiverSessionID  string `json:"receiver_session_id,omitempty"`
	ReceiverDelegateID string `json:"receiver_delegate_id,omitempty"`
}

type WatchConfigSnapshot struct {
	Target             string                    `json:"target"`
	OutputMatch        string                    `json:"output_match,omitempty"`
	ProgressIntervalMS int                       `json:"progress_interval_ms,omitempty"`
	Events             []string                  `json:"events,omitempty"`
	Every              int                       `json:"every,omitempty"`
	EventFilter        *WatchEventFilterSnapshot `json:"event_filter,omitempty"`
	SendTo             string                    `json:"send_to,omitempty"`
	SendMessage        string                    `json:"send_message,omitempty"`
	IncludeExcerpt     bool                      `json:"include_excerpt,omitempty"`
	// Receiver identity is part of the watch's configured identity — two watches
	// that differ only in who receives them are different watches — so it belongs
	// in the hashed snapshot rather than beside it. These stay LAST because the
	// config hash is the snapshot's JSON encoding: appending here reproduces the
	// exact bytes the hash was computed over when they rode a wrapper struct, so
	// every already-durable config hash keeps matching.
	ReceiverSessionID        string `json:"receiver_session_id,omitempty"`
	ReceiverDelegateID       string `json:"receiver_delegate_id,omitempty"`
	Source                   string `json:"source,omitempty"`
	SourceDelegateID         string `json:"source_delegate_id,omitempty"`
	SourceDelegateGeneration uint64 `json:"source_delegate_generation,omitempty"`
	StableReceiver           bool   `json:"stable_receiver,omitempty"`
}

type WatchEventFilterSnapshot struct {
	ToolName string `json:"tool_name,omitempty"`
	Status   string `json:"status,omitempty"`
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
	Key                      WatchSendKey       `json:"key"`
	DeliveryID               string             `json:"delivery_id"`
	UpdateSeq                uint64             `json:"update_seq,omitempty"`
	Message                  string             `json:"message,omitempty"`
	Frame                    string             `json:"frame,omitempty"`
	TriggerIdentity          string             `json:"trigger_identity,omitempty"`
	TriggerReason            string             `json:"trigger_reason,omitempty"`
	CoalescedCount           int                `json:"coalesced_count,omitempty"`
	ReceiverSessionID        string             `json:"receiver_session_id,omitempty"`
	ReceiverDelegateID       string             `json:"receiver_delegate_id,omitempty"`
	SourceDelegateID         string             `json:"source_delegate_id,omitempty"`
	SourceDelegateGeneration uint64             `json:"source_delegate_generation,omitempty"`
	StableReceiver           bool               `json:"stable_receiver,omitempty"`
	NotificationJobID        string             `json:"notification_job_id,omitempty"`
	DiagnosticReason         string             `json:"diagnostic_reason,omitempty"`
	SelfInfluenceDepth       int                `json:"self_influence_depth,omitempty"`
	CreatedAt                time.Time          `json:"created_at"`
	UpdatedAt                time.Time          `json:"updated_at"`
	Provenance               *provenance.Causal `json:"provenance,omitempty"`
	// EndNotice marks the teardown frame a watch sends when it ends without ever
	// having fired: the watch is telling its watcher the condition can no longer
	// match. It is not a condition fire, and the runtime does not count it as a
	// delivery, so readers of the log must not either. Marked at the source
	// because the distinction is otherwise only legible in the trigger prose.
	EndNotice bool `json:"end_notice,omitempty"`
}

// WatchSendRecord is the folded durable state for pending watch-send frames.
type WatchSendRecord struct {
	Pending map[WatchSendKey]*WatchSendState
}

// JobRecord is the durable storage shape reconstructed from the job event log.
type JobRecord struct {
	JobID            string  `json:"job_id"`
	DurableSeq       int64   `json:"-"`
	Type             JobType `json:"type"`
	Status           Status  `json:"status"`
	Reason           string  `json:"reason,omitempty"`
	ExhaustionBudget string  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int     `json:"exhaustion_limit,omitempty"`
	Description      string  `json:"description,omitempty"`
	Command          string  `json:"command,omitempty"`
	// Background reports that a shell job is running in the background rather
	// than being waited on inline: stamped at launch for mode:"background", and
	// at promotion when a foreground command outlives its block timeout. No
	// event carries it, so it lives only on the in-memory record: a record
	// folded from the store always reads false, whatever the job did.
	// Live-only, like LastActivity.
	Background bool `json:"-"`
	// WorkingDir is the launch-time working directory of a background shell
	// job, recorded so manage_worktree remove/prune's live-work guard
	// (liveWorkUnder) can refuse deleting a worktree a shell job is running
	// under. Usually the executing env's WorkingDirectory() when the shell
	// tool call started it, but the shell tool's optional `cwd` argument lets
	// the model choose a different (validated, sandbox-confined) starting
	// directory instead — so this may be model-chosen, not just inherited.
	// Best-effort: a command
	// that itself `cd`s elsewhere after launch (e.g. `cd elsewhere && foo`) is
	// invisible to it — only the initial value is tracked.
	WorkingDir       string    `json:"working_dir,omitempty"`
	Task             string    `json:"task,omitempty"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	OwnerSessionID   string    `json:"owner_session_id"`
	VisibleToSession string    `json:"visible_to_session_id"`
	ParentJobID      string    `json:"parent_job_id,omitempty"`
	ParentDelegateID string    `json:"parent_delegate_id,omitempty"`
	OriginTurnID     string    `json:"origin_turn_id,omitempty"`
	OriginToolCallID string    `json:"origin_tool_call_id,omitempty"`
	OriginItemID     string    `json:"origin_item_id,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	// Phase is the running job's finer-grained supervision state (starting,
	// process_running, awaiting_model, model_streaming, tool_running),
	// restamped as the runtime observes activity. No event carries it, so a
	// record folded from the store has it empty and defaultJobPhase substitutes
	// a per-type default. Live-only, like LastActivity.
	Phase string `json:"-"`
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
	Authority              Authority          `json:"-"`
	Incomplete             bool               `json:"-"`
	IntegrityReasons       []string           `json:"-"`
	Provenance             *provenance.Causal `json:"provenance,omitempty"`
	NotificationProvenance *provenance.Causal `json:"notification_provenance,omitempty"`
}

func NewJobID(ownerSessionID string) string {
	return identifier.MustNewJobID(ownerSessionID)
}

func NewWatchID() string {
	return identifier.MustNewWatchID()
}

func NewWatchGeneration() string {
	return identifier.MustNewWatchGeneration()
}

func NewWatchSendDeliveryID() string {
	return identifier.MustNewWatchDeliveryID()
}

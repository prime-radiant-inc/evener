// Package jobstore provides the pure, Session-free durable substrate for Serf's job-control system.
package jobstore

import (
	"time"

	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/identifier"
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

const (
	NotifyNotArmed  NotifyState = "not_armed"
	NotifyPending   NotifyState = "pending"
	NotifyDelivered NotifyState = "delivered"
	// NotifyConsumed means the caller learned the job ended by reading its
	// terminal job_status, not by being shown the notification. The caller was
	// told either way — which is why it settles the pending notification — but
	// it is a separate value from NotifyDelivered so the durable ledger and
	// serf-doctor keep saying which of the two actually happened.
	NotifyConsumed NotifyState = "consumed"
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
	Version             int      `json:"version"`
	ChildSessionID      string   `json:"child_session_id"`
	TranscriptRef       string   `json:"transcript_ref"`
	ParentSessionID     string   `json:"parent_session_id,omitempty"`
	ParentJobID         string   `json:"parent_job_id,omitempty"`
	OwnerSessionID      string   `json:"owner_session_id,omitempty"`
	VisibleSessionID    string   `json:"visible_session_id,omitempty"`
	OriginTurnID        string   `json:"origin_turn_id,omitempty"`
	OriginToolCallID    string   `json:"origin_tool_call_id,omitempty"`
	OriginItemID        string   `json:"origin_item_id,omitempty"`
	Task                string   `json:"task,omitempty"`
	AgentType           string   `json:"agent_type,omitempty"`
	RequestedModel      string   `json:"requested_model,omitempty"`
	ResolvedProfileID   string   `json:"resolved_profile_id,omitempty"`
	ResolvedModel       string   `json:"resolved_model,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
	AgentName           string   `json:"agent_name,omitempty"`
	FrozenRolePrompt    string   `json:"frozen_role_prompt,omitempty"`
	FrozenTaskPrompt    string   `json:"frozen_task_prompt,omitempty"`
	FrozenToolNames     []string `json:"frozen_tool_names,omitempty"`
	FrozenSkillNames    []string `json:"frozen_skill_names,omitempty"`
	FrozenSkillBodies   []string `json:"frozen_skill_bodies,omitempty"`
	WorkingDir          string   `json:"working_dir,omitempty"`
	LocalEnvPolicy      string   `json:"local_env_policy,omitempty"`
	ResultSchema        any      `json:"result_schema,omitempty"`
	ExplicitToolGrants  []string `json:"explicit_tool_grants,omitempty"`
	DelegationAllowance int      `json:"delegation_allowance,omitempty"`
	ParentWatchGranted  bool     `json:"parent_watch_granted,omitempty"`
	// Isolation is "worktree" when this delegate was spawned with
	// delegate(isolation:"worktree") — WorkingDir then points at the
	// delegate's own managed worktree lane rather than the parent's plain
	// cwd (native worktree tools spec §9 lifecycle step 1). Both the spawn
	// path and the restore path (session_init.go) key the unconditional
	// manage_worktree deny off this field; empty for an ordinary delegate.
	Isolation  string             `json:"isolation,omitempty"`
	Provenance *provenance.Causal `json:"provenance,omitempty"`
	// Sandbox is the delegate's sandbox policy INPUTS (mode/net/denylist-deltas/
	// extra-roots), captured at spawn and persisted so a resumed delegate can
	// RE-RESOLVE its confinement against its own worktree lane plus freshly-probed
	// host facts on restore — never the worktree-anchored resolved roots (a config
	// that loosened between serf runs must not widen a live delegate's box). nil for
	// an unsandboxed (off) delegate, so its descriptor is byte-identical to today.
	Sandbox *SandboxSnapshot `json:"sandbox,omitempty"`
}

// SandboxSnapshot is the durable, live-type-decoupled mirror of a delegate's
// sandbox policy INPUTS (the WatchConfigSnapshot house pattern): the mode name,
// the network tri-state, and the user denylist/root extensions. It carries
// serializable inputs, NOT resolved roots — restore re-resolves it against the
// delegate's lane and re-probed host facts, honoring the immutable-across-restart
// guarantee. An off delegate persists no snapshot.
type SandboxSnapshot struct {
	Mode               string   `json:"mode"`
	Network            *bool    `json:"network,omitempty"`
	DenylistAdd        []string `json:"denylist_add,omitempty"`
	DenylistRemove     []string `json:"denylist_remove,omitempty"`
	ExtraWritableRoots []string `json:"extra_writable_roots,omitempty"`
	ExtraReadRoots     []string `json:"extra_read_roots,omitempty"`
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
	// Disposed is set when this delegate's isolation worktree lane was disposed
	// mid-life or at its creator session's close (delegate-lane disposal spec §P1).
	// It is additive and monotonic: folded from a delegate_disposed event keyed by
	// delegate id, no event un-disposes a delegate. Consumers (doctor tree, job
	// listing) treat a disposed delegate as non-resumable.
	Disposed bool `json:"disposed,omitempty"`
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
	ReceiverSessionID  string `json:"receiver_session_id,omitempty"`
	ReceiverDelegateID string `json:"receiver_delegate_id,omitempty"`
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
	Key                    WatchSendKey       `json:"key"`
	DeliveryID             string             `json:"delivery_id"`
	UpdateSeq              uint64             `json:"update_seq,omitempty"`
	Message                string             `json:"message,omitempty"`
	Frame                  string             `json:"frame,omitempty"`
	TriggerIdentity        string             `json:"trigger_identity,omitempty"`
	TriggerReason          string             `json:"trigger_reason,omitempty"`
	CoalescedCount         int                `json:"coalesced_count,omitempty"`
	DelegateGeneration     string             `json:"delegate_generation,omitempty"`
	ReceiverSessionID      string             `json:"receiver_session_id,omitempty"`
	ReceiverDelegateID     string             `json:"receiver_delegate_id,omitempty"`
	NotificationJobID      string             `json:"notification_job_id,omitempty"`
	NotificationDelegateID string             `json:"notification_delegate_id,omitempty"`
	DiagnosticReason       string             `json:"diagnostic_reason,omitempty"`
	SelfInfluenceDepth     int                `json:"self_influence_depth,omitempty"`
	CreatedAt              time.Time          `json:"created_at"`
	UpdatedAt              time.Time          `json:"updated_at"`
	Provenance             *provenance.Causal `json:"provenance,omitempty"`
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
	Type             JobType `json:"type"`
	Status           Status  `json:"status"`
	Reason           string  `json:"reason,omitempty"`
	ExhaustionBudget string  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int     `json:"exhaustion_limit,omitempty"`
	Description      string  `json:"description,omitempty"`
	Command          string  `json:"command,omitempty"`
	// Background reports that a shell job was launched to run in the background
	// rather than waited on inline. No event carries it, so it is stamped on the
	// in-memory record at launch and nowhere else: a record folded from the store
	// always reads false, whatever the job did. Live-only, like LastActivity.
	Background bool `json:"-"`
	// WorkingDir is the launch-time working directory of a background shell
	// job (the executing env's WorkingDirectory() when the shell tool call
	// started it), recorded so manage_worktree remove/prune's live-work guard
	// (liveWorkUnder) can refuse deleting a worktree a shell job is running
	// under. Empty for delegate jobs, which record their working dir in
	// DelegateRestore.WorkingDir instead. Best-effort: a command that `cd`s
	// after launch is invisible to it.
	WorkingDir       string                     `json:"working_dir,omitempty"`
	Task             string                     `json:"task,omitempty"`
	ParentSessionID  string                     `json:"parent_session_id,omitempty"`
	OwnerSessionID   string                     `json:"owner_session_id"`
	VisibleToSession string                     `json:"visible_to_session_id"`
	ParentJobID      string                     `json:"parent_job_id,omitempty"`
	DelegateID       string                     `json:"delegate_id,omitempty"`
	OriginTurnID     string                     `json:"origin_turn_id,omitempty"`
	OriginToolCallID string                     `json:"origin_tool_call_id,omitempty"`
	OriginItemID     string                     `json:"origin_item_id,omitempty"`
	DelegateRestore  *DelegateRestoreDescriptor `json:"delegate_restore,omitempty"`
	TranscriptRef    string                     `json:"transcript_ref,omitempty"`
	Resumable        *bool                      `json:"resumable,omitempty"`
	NotResumableWhy  string                     `json:"not_resumable_reason,omitempty"`
	// Disposed is set when this delegate's isolation worktree lane was disposed
	// at its creator session's close (native worktree tools spec §9 step 4-5).
	// assessDelegateResumability treats a disposed delegate as not-resumable so
	// delegate_send cannot revive the child into a removed lane. Folded from a
	// delegate_disposed event keyed by delegate id, so every job record for the
	// delegate — across resumes — carries it, independent of which job the
	// resumability check happens to resolve.
	Disposed  bool      `json:"disposed,omitempty"`
	StartedAt time.Time `json:"started_at"`
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
	Provenance             *provenance.Causal `json:"provenance,omitempty"`
	NotificationProvenance *provenance.Causal `json:"notification_provenance,omitempty"`
}

func NewJobID(ownerSessionID string) string {
	return identifier.MustNewJobID(ownerSessionID)
}

func NewDelegateID() string {
	return identifier.MustNewDelegateID()
}

func NewDelegateGeneration() string {
	return identifier.MustNewDelegateGeneration()
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

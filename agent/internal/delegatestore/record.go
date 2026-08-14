package delegatestore

import (
	"encoding/json"
	"time"

	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
)

type State map[string]*Aggregate

type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseRunning  Phase = "running"
	PhaseSettling Phase = "settling"
	PhaseStopping Phase = "stopping"
	PhaseClosed   Phase = "closed"
)

type RunTrigger string

const (
	TriggerInitial    RunTrigger = "initial"
	TriggerOwnerInput RunTrigger = "owner_input"
	TriggerAttention  RunTrigger = "attention"
)

type RunDisposition string

const (
	DispositionReported          RunDisposition = "reported"
	DispositionTerminalError     RunDisposition = "terminal_error"
	DispositionCompletedNoAction RunDisposition = "completed_no_action"
)

type OutcomeStatus string

const (
	OutcomeCompleted OutcomeStatus = "completed"
	OutcomeFailed    OutcomeStatus = "failed"
	OutcomeExhausted OutcomeStatus = "exhausted"
	OutcomeCancelled OutcomeStatus = "cancelled"
	OutcomeStopped   OutcomeStatus = "stopped"
)

type PacketKind string

const (
	PacketReported      PacketKind = "reported"
	PacketTerminalError PacketKind = "terminal_error"
)

type Descriptor struct {
	ChildSessionID                string                `json:"child_session_id"`
	TranscriptRef                 string                `json:"transcript_ref"`
	ParentDelegateID              string                `json:"parent_delegate_id,omitempty"`
	OwnerSessionID                string                `json:"owner_session_id"`
	VisibleSessionID              string                `json:"visible_session_id,omitempty"`
	OriginTurnID                  string                `json:"origin_turn_id,omitempty"`
	OriginToolCallID              string                `json:"origin_tool_call_id,omitempty"`
	OriginItemID                  string                `json:"origin_item_id,omitempty"`
	Task                          string                `json:"task"`
	Description                   string                `json:"description,omitempty"`
	AgentType                     string                `json:"agent_type"`
	RequestedModel                string                `json:"requested_model,omitempty"`
	ResolvedProfileID             string                `json:"resolved_profile_id,omitempty"`
	ResolvedModel                 string                `json:"resolved_model,omitempty"`
	FrozenRolePrompt              string                `json:"frozen_role_prompt,omitempty"`
	TaskTemplates                 []task.TaskTemplate   `json:"task_templates,omitempty"`
	ToolNameCeiling               []string              `json:"tool_name_ceiling,omitempty"`
	FrozenSkillNames              []string              `json:"frozen_skill_names,omitempty"`
	FrozenSkillBodies             []string              `json:"frozen_skill_bodies,omitempty"`
	WorkingDir                    string                `json:"working_dir,omitempty"`
	LocalEnvPolicy                string                `json:"local_env_policy,omitempty"`
	ResultSchema                  json.RawMessage       `json:"result_schema,omitempty"`
	ExplicitToolGrants            []string              `json:"explicit_tool_grants,omitempty"`
	DelegationAllowance           int                   `json:"delegation_allowance,omitempty"`
	Isolation                     string                `json:"isolation,omitempty"`
	Sandbox                       *SandboxSnapshot      `json:"sandbox,omitempty"`
	Config                        schema.ConfigSnapshot `json:"config"`
	SharedTaskStoreOwnerSessionID string                `json:"shared_task_store_owner_session_id,omitempty"`
	Provenance                    *provenance.Causal    `json:"provenance,omitempty"`
	Resumable                     bool                  `json:"resumable"`
}

type SandboxSnapshot struct {
	Mode               string   `json:"mode"`
	Network            *bool    `json:"network,omitempty"`
	DenylistAdd        []string `json:"denylist_add,omitempty"`
	DenylistRemove     []string `json:"denylist_remove,omitempty"`
	ExtraWritableRoots []string `json:"extra_writable_roots,omitempty"`
	ExtraReadRoots     []string `json:"extra_read_roots,omitempty"`
}

type Aggregate struct {
	DelegateID         string            `json:"delegate_id"`
	Descriptor         Descriptor        `json:"descriptor"`
	Generation         uint64            `json:"generation"`
	Trigger            RunTrigger        `json:"trigger,omitempty"`
	Phase              Phase             `json:"phase"`
	CurrentRunOpen     bool              `json:"current_run_open"`
	PreparedTerminal   *TerminalPacket   `json:"prepared_terminal,omitempty"`
	Resumable          bool              `json:"resumable"`
	NotResumableReason string            `json:"not_resumable_reason,omitempty"`
	RunStartedAt       time.Time         `json:"run_started_at,omitempty"`
	LatestActivityAt   time.Time         `json:"latest_activity_at,omitempty"`
	LatestOutcome      *Outcome          `json:"latest_outcome,omitempty"`
	PendingDeliveries  []PendingDelivery `json:"pending_deliveries,omitempty"`
	PendingStopSeq     uint64            `json:"pending_stop_seq,omitempty"`
	ProjectionRevision uint64            `json:"projection_revision"`
}

type Outcome struct {
	Status  OutcomeStatus `json:"status"`
	Reason  string        `json:"reason,omitempty"`
	EndedAt time.Time     `json:"ended_at"`
}

type TerminalPacket struct {
	Kind                   PacketKind      `json:"kind"`
	Message                json.RawMessage `json:"message"`
	StructuredResult       json.RawMessage `json:"structured_result,omitempty"`
	StructuredResultValid  *bool           `json:"structured_result_valid,omitempty"`
	StructuredResultReason string          `json:"structured_result_reason,omitempty"`
	Warnings               []string        `json:"warnings,omitempty"`
	Metadata               json.RawMessage `json:"metadata,omitempty"`
}

type PendingDelivery struct {
	DeliveryID      string         `json:"delivery_id"`
	Generation      uint64         `json:"generation"`
	OwnerDelegateID string         `json:"owner_delegate_id,omitempty"`
	Packet          TerminalPacket `json:"packet"`
}

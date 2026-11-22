package delegatestore

import "time"

const CurrentVersion = 1

type EventKind string

const (
	EventDelegateCreated               EventKind = "delegate_created"
	EventDelegateRunStarted            EventKind = "delegate_run_started"
	EventDelegateTerminalPrepared      EventKind = "delegate_terminal_prepared"
	EventDelegateRunFinished           EventKind = "delegate_run_finished"
	EventDelegateResumabilityClosed    EventKind = "delegate_resumability_closed"
	EventDelegateSubtreeStopRequested  EventKind = "delegate_subtree_stop_requested"
	EventDelegateSubtreeStopCompleted  EventKind = "delegate_subtree_stop_completed"
	EventDelegateDeliveryAcknowledged  EventKind = "delegate_delivery_acknowledged"
	EventDelegateAttentionChanged      EventKind = "delegate_attention_changed"
	EventDelegateUpdatePosted          EventKind = "delegate_update_posted"
	EventDelegateIntentStated          EventKind = "delegate_intent_stated"
	EventDelegateAttentionHoldReleased EventKind = "delegate_attention_hold_released"
	EventDelegateAttentionQueued       EventKind = "delegate_attention_queued"
	EventDelegateAttentionResolved     EventKind = "delegate_attention_resolved"
	EventDelegateAttentionHeld         EventKind = "delegate_attention_held"
)

type Event struct {
	Kind       EventKind `json:"kind"`
	Seq        uint64    `json:"seq"`
	TS         time.Time `json:"ts,omitzero"`
	DelegateID string    `json:"delegate_id"`

	Created               *DelegateCreated          `json:"created,omitempty"`
	RunStarted            *RunStarted               `json:"run_started,omitempty"`
	TerminalPrepared      *TerminalPrepared         `json:"terminal_prepared,omitempty"`
	RunFinished           *RunFinished              `json:"run_finished,omitempty"`
	ResumabilityClosed    *ResumabilityClosed       `json:"resumability_closed,omitempty"`
	SubtreeStopRequested  *SubtreeStopRequested     `json:"subtree_stop_requested,omitempty"`
	SubtreeStopCompleted  *SubtreeStopCompleted     `json:"subtree_stop_completed,omitempty"`
	DeliveryAcknowledged  *DeliveryAcknowledged     `json:"delivery_acknowledged,omitempty"`
	AttentionChanged      *DelegateAttentionChanged `json:"attention_changed,omitempty"`
	UpdatePosted          *UpdatePosted             `json:"update_posted,omitempty"`
	IntentStated          *IntentStated             `json:"intent_stated,omitempty"`
	AttentionHoldReleased *AttentionHoldReleased    `json:"attention_hold_released,omitempty"`
	AttentionQueued       *AttentionQueued          `json:"attention_queued,omitempty"`
	AttentionResolved     *AttentionResolved        `json:"attention_resolved,omitempty"`
	AttentionHeld         *AttentionHeld            `json:"attention_held,omitempty"`
}

type DelegateCreated struct {
	Descriptor Descriptor `json:"descriptor"`
}

type RunStarted struct {
	Generation uint64     `json:"generation"`
	Trigger    RunTrigger `json:"trigger"`
	StartedAt  time.Time  `json:"started_at"`
}

type TerminalPrepared struct {
	Generation uint64         `json:"generation"`
	Packet     TerminalPacket `json:"packet"`
}

type RunFinished struct {
	Generation  uint64          `json:"generation"`
	Outcome     Outcome         `json:"outcome"`
	Disposition RunDisposition  `json:"disposition"`
	DeliveryID  string          `json:"delivery_id,omitempty"`
	Packet      *TerminalPacket `json:"packet,omitempty"`
	// ObserverCallbackDelivered marks a report a watch-origin observer already
	// handed to its parent itself, so the fold must not also queue a delivery
	// for it. Nothing in this process sets it any more: its only writer ran on
	// an EntryWatchDelivery turn, and that kind was deleted with the rest of the
	// dormant delegate job schema's residue (kata z5fm). The field and the fold
	// arms that honour it stay because a delegate journal written before then
	// can still carry it, and a durable record must fold the same way it did
	// when it was written.
	ObserverCallbackDelivered bool `json:"observer_callback_delivered,omitempty"`
}

type ResumabilityClosed struct {
	Reason string `json:"reason"`
}

type SubtreeStopRequested struct {
	TargetDelegateID string `json:"target_delegate_id"`
}

type SubtreeStopCompleted struct {
	RequestSeq uint64 `json:"request_seq"`
}

type DeliveryAcknowledged struct {
	DeliveryID string `json:"delivery_id"`
}

type DelegateAttentionChanged struct {
	NeedsAttention bool `json:"needs_attention"`
}

// UpdatePosted is a running generation's mid-work message to its owner. The
// fold queues it as a pending delivery behind the delegate's earlier
// deliveries and ahead of the report the same generation settles.
//
// This journal records and orders updates only. Which tool call posts one
// belongs to the delegate controller that appends the event. The controller
// also owns its delivery shape: an update head never claims the generation's
// inline waiter (newHeadDeliveryPlanLocked in package agent). How an update
// is presented, as a notification frame, as an earlier result carried into a
// delegate_send reply, or in a client, belongs to those renderers.
type UpdatePosted struct {
	Generation uint64 `json:"generation"`
	Message    string `json:"message"`
}

// IntentStated is a running generation's newest tool-call intent, journaled
// so retained rows show the last intent of a run a restart interrupted. An
// empty Intent folds as the turn-start clear.
type IntentStated struct {
	Generation uint64 `json:"generation"`
	Intent     string `json:"intent"`
}

// AttentionHoldReleased records that a newer event ended the quiet on this
// delegate's pending rows for every hold through Generation, inclusive. The
// quiet itself is in-memory, so without this record a cold restore cannot tell
// a stop whose quiet is still current from one a newer event already released.
// Generation is the newest settled generation at admission, carried unchanged
// by a wake or an ack. The fold accepts any nonzero sample at or below it.
type AttentionHoldReleased struct {
	Generation uint64 `json:"generation"`
}

// AttentionQueued adds an owed attention row to a delegate's durable queue, or
// updates the row already queued under the same identity. It is idempotent by
// AttentionID, so a replay or a repeated arm adds nothing.
//
// Committed marks the wake as published. AdmittedGeneration is the settled
// generation the wake superseded: a fresh wake ends every stop quiet at or
// below it, exactly as appendAttentionWakeAtLocked journals the release it
// carries.
type AttentionQueued struct {
	AttentionID        string `json:"attention_id"`
	Committed          bool   `json:"committed,omitempty"`
	AdmittedGeneration uint64 `json:"admitted_generation,omitempty"`
}

// AttentionResolved removes one attention row from the queue, consumed or
// discarded. Removal is by identity and out of order: the row need not be the
// queue head, and resolving a row that is already gone is a no-op so a
// journal replay of a resolution stays valid.
type AttentionResolved struct {
	AttentionID string `json:"attention_id"`
}

// AttentionHeld installs or releases a user-stop quiet on queued attention.
// With AttentionID set, it stamps that pending row's HeldThrough with
// Generation. With AttentionID empty, it is a release: every row held through
// Generation or earlier becomes presentable again, and the aggregate's
// AttentionHoldReleasedGeneration floor advances, so a cold restore cannot
// rebuild a quiet a newer event already ended. Generation is always a settled
// generation: a stop's own quiet or a wake's newer sample, never an open run's.
type AttentionHeld struct {
	AttentionID string `json:"attention_id,omitempty"`
	Generation  uint64 `json:"generation"`
}

type versionRecord struct {
	Version int `json:"version"`
}

type batchRecord struct {
	Events []Event `json:"events"`
}

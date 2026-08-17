package delegatestore

import "time"

const CurrentVersion = 1

type EventKind string

const (
	EventDelegateCreated              EventKind = "delegate_created"
	EventDelegateRunStarted           EventKind = "delegate_run_started"
	EventDelegateTerminalPrepared     EventKind = "delegate_terminal_prepared"
	EventDelegateRunFinished          EventKind = "delegate_run_finished"
	EventDelegateResumabilityClosed   EventKind = "delegate_resumability_closed"
	EventDelegateSubtreeStopRequested EventKind = "delegate_subtree_stop_requested"
	EventDelegateSubtreeStopCompleted EventKind = "delegate_subtree_stop_completed"
	EventDelegateDeliveryAcknowledged EventKind = "delegate_delivery_acknowledged"
)

type Event struct {
	Kind       EventKind `json:"kind"`
	Seq        uint64    `json:"seq"`
	TS         time.Time `json:"ts,omitzero"`
	DelegateID string    `json:"delegate_id"`

	Created              *DelegateCreated      `json:"created,omitempty"`
	RunStarted           *RunStarted           `json:"run_started,omitempty"`
	TerminalPrepared     *TerminalPrepared     `json:"terminal_prepared,omitempty"`
	RunFinished          *RunFinished          `json:"run_finished,omitempty"`
	ResumabilityClosed   *ResumabilityClosed   `json:"resumability_closed,omitempty"`
	SubtreeStopRequested *SubtreeStopRequested `json:"subtree_stop_requested,omitempty"`
	SubtreeStopCompleted *SubtreeStopCompleted `json:"subtree_stop_completed,omitempty"`
	DeliveryAcknowledged *DeliveryAcknowledged `json:"delivery_acknowledged,omitempty"`
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

type versionRecord struct {
	Version int `json:"version"`
}

type batchRecord struct {
	Events []Event `json:"events"`
}

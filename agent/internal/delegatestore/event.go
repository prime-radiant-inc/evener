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
	TS         time.Time `json:"ts,omitempty"`
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

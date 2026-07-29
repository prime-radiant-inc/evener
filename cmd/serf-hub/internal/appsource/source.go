package appsource

import (
	"context"

	"primeradiant.com/serf/appwire"
)

type Source interface {
	ID() string
	ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error)
	ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
	ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)
	StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error)
	ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error)
	ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error)
	StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error)
	ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error
	InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
	QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error)
	DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error)
	PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error)
	CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error)
	CompactThread(context.Context, appwire.ThreadCompactStartParams) error
	ShutdownThread(context.Context, appwire.ThreadShutdownParams) error
	SetThreadModel(context.Context, appwire.ThreadModelSetParams) error
	SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error
	SetThreadName(context.Context, appwire.ThreadNameSetParams) error
	GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error)
	ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error)
	ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error)
	ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error)
	SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error)
}

// RelaySessionSource is implemented by sources that can preserve one ordered
// upstream snapshot-to-live stream. Codex deliberately does not implement this
// interface because it converges through authoritative full-state replacement.
type RelaySessionSource interface {
	AcquireRelaySession(appwire.ThreadReadParams) (RelaySessionLease, error)
}

type RelaySessionLease interface {
	Read(context.Context, appwire.ThreadReadParams) (RelayReadResult, error)
	Listen(context.Context) (<-chan RelayDelivery, error)
	Close()
}

type RelayReadResult struct {
	Response appwire.ThreadReadResponse
	Handoff  RelayHandoff
}

type RelayHandoff interface {
	Commit() bool
	Abort() bool
}

type RelayDelivery struct {
	Notification appwire.Notification
	Acknowledge  func()
}

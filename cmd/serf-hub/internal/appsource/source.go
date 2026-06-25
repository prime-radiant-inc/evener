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
	SteerTurn(context.Context, appwire.TurnSteerParams) error
	InterruptTurn(context.Context, appwire.TurnInterruptParams) error
	QueueTurn(context.Context, appwire.TurnQueueParams) error
	DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error
	CompactThread(context.Context, appwire.ThreadCompactStartParams) error
	ShutdownThread(context.Context, appwire.ThreadShutdownParams) error
	SetThreadModel(context.Context, appwire.ThreadModelSetParams) error
	SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error
	GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error)
	ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error)
	ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error)
	ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error)
	SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error)
}

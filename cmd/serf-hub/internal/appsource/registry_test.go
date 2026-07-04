package appsource

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

type fakeSource struct{ id string }

func (f fakeSource) ID() string { return f.id }
func (f fakeSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
}
func (f fakeSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}
func (f fakeSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, nil
}
func (f fakeSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, nil
}
func (f fakeSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, nil
}
func (f fakeSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, nil
}
func (f fakeSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{}, nil
}
func (f fakeSource) SteerTurn(context.Context, appwire.TurnSteerParams) error { return nil }
func (f fakeSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) error {
	return nil
}
func (f fakeSource) QueueTurn(context.Context, appwire.TurnQueueParams) error { return nil }
func (f fakeSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return nil
}
func (f fakeSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return nil
}
func (f fakeSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return nil
}
func (f fakeSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return nil
}

func (f fakeSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return nil
}

func (f fakeSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return nil
}
func (f fakeSource) GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return appwire.GoalSetResponse{}, nil
}
func (f fakeSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, nil
}
func (f fakeSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{}, nil
}
func (f fakeSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, nil
}
func (f fakeSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	return nil, nil
}

func TestRegistryRoutesByRefSource(t *testing.T) {
	reg := NewRegistry()
	reg.Add(fakeSource{id: "local"})
	src, err := reg.SourceForRef("local:th_1")
	if err != nil {
		t.Fatalf("SourceForRef: %v", err)
	}
	if src.ID() != "local" {
		t.Fatalf("source=%q", src.ID())
	}
}

func TestRegistryRejectsMissingSource(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.SourceForRef("remote:th_1"); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestRegistryAllReturnsSourcesInIDOrder(t *testing.T) {
	reg := NewRegistry()
	// Insert in non-lexicographic order; "local" sorts between "codex" and "serf",
	// so all three positions must be correct — a random permutation matches in only 1/6 runs.
	reg.Add(fakeSource{id: "serf"})
	reg.Add(fakeSource{id: "codex"})
	reg.Add(fakeSource{id: "local"})

	sources := reg.All()
	if len(sources) != 3 {
		t.Fatalf("sources=%d", len(sources))
	}
	if sources[0].ID() != "codex" || sources[1].ID() != "local" || sources[2].ID() != "serf" {
		t.Fatalf("source order=%s,%s,%s", sources[0].ID(), sources[1].ID(), sources[2].ID())
	}
}

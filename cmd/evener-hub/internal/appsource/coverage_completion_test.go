package appsource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func fuzzScenarioLocalDaemonSourceRPCSurface(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadTurnsList, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return appwire.ThreadTurnsListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, emptyHandler[appwire.TurnSteerParams])
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerSandboxEscalationResolve, emptyHandler[appwire.SandboxEscalationResolveParams])
	appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, emptyHandler[appwire.TurnInterruptParams])
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, emptyHandler[appwire.TurnQueueParams])
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, emptyHandler[appwire.TurnDrainAsSteerParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, emptyHandler[appwire.ThreadCompactStartParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadShutdown, emptyHandler[appwire.ThreadShutdownParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, emptyHandler[appwire.ThreadModelSetParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadReasoningEffortSet, emptyHandler[appwire.ThreadReasoningEffortSetParams])
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerThreadNameSet, emptyHandler[appwire.ThreadNameSetParams])
	appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		return appwire.GoalSetResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		return appwire.ThreadClearResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, func(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
		return appwire.TaskListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerJobsList, func(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
		return appwire.JobsListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerJobsOutput, func(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
		return appwire.JobsOutputResponse{}, nil
	})
	server := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	t.Cleanup(server.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(server.URL, "http"), SourceID: "local", ThreadID: "thread", SessionID: "session", WorkspaceRef: "local:thread", InstanceID: "session"}
	source := NewLocalDaemonSourceWithEntries("", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, server.Client())
	ctx := context.Background()
	ref := "local:thread"

	if source.ID() != "local" {
		t.Fatalf("ID = %q", source.ID())
	}
	if _, err := source.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "session", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "session", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.ResolveSandboxEscalation(ctx, appwire.SandboxEscalationResolveParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "session", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.QueueTurn(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "session", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "session", ExpectedQueueRevision: 0, Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.CompactThread(ctx, appwire.ThreadCompactStartParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.ShutdownThread(ctx, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.SetThreadModel(ctx, appwire.ThreadModelSetParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.SetThreadReasoningEffort(ctx, appwire.ThreadReasoningEffortSetParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.SetThreadName(ctx, appwire.ThreadNameSetParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.GoalSet(ctx, appwire.GoalSetParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ClearThread(ctx, appwire.ThreadClearParams{Ref: ref, ClientMutationID: "clear-mutation", ExpectedInstanceID: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ListModels(ctx, appwire.ModelListParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ListTasks(ctx, appwire.TaskListParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ListJobs(ctx, appwire.JobsListParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.JobOutput(ctx, appwire.JobsOutputParams{Ref: ref, JobID: "job_1"}); err != nil {
		t.Fatal(err)
	}
}

func emptyHandler[T any](context.Context, T) (appwire.EmptyResponse, error) {
	return appwire.EmptyResponse{}, nil
}

func fuzzScenarioLocalDaemonErrorMappingRemainingBranches(t *testing.T) {
	for _, err := range []error{errors.New("I/O TIMEOUT"), appwire.InternalError("failed to get reader"), appwire.InternalError("semantic failure")} {
		if got := localDaemonCallError(err); got == nil {
			t.Fatalf("mapped %v to nil", err)
		}
	}
	if got := localDaemonInitializeError(io.EOF); got == nil {
		t.Fatal("initialize EOF mapped nil")
	}
	if got := localDaemonSubscribeReadError(io.EOF); got == nil {
		t.Fatal("subscribe EOF mapped nil")
	}
}

func fuzzScenarioLocalDaemonSourceRejectsUnknownReferenceAcrossRPCSurface(t *testing.T) {
	s := NewLocalDaemonSource("local", nil, nil)
	ctx := context.Background()
	ref := "local:missing"
	calls := map[string]func() error{
		"read":  func() error { _, err := s.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref}); return err },
		"turns": func() error { _, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: ref}); return err },
		"start turn": func() error {
			_, err := s.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		"steer": func() error {
			_, err := s.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		"resolve": func() error { return s.ResolveSandboxEscalation(ctx, appwire.SandboxEscalationResolveParams{Ref: ref}) },
		"interrupt": func() error {
			_, err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		"queue": func() error {
			_, err := s.QueueTurn(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		"drain": func() error {
			_, err := s.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedQueueRevision: 0, Ref: ref})
			return err
		},
		"compact":  func() error { return s.CompactThread(ctx, appwire.ThreadCompactStartParams{Ref: ref}) },
		"shutdown": func() error { return s.ShutdownThread(ctx, appwire.ThreadShutdownParams{Ref: ref}) },
		"model":    func() error { return s.SetThreadModel(ctx, appwire.ThreadModelSetParams{Ref: ref}) },
		"effort":   func() error { return s.SetThreadReasoningEffort(ctx, appwire.ThreadReasoningEffortSetParams{Ref: ref}) },
		"name":     func() error { return s.SetThreadName(ctx, appwire.ThreadNameSetParams{Ref: ref}) },
		"goal":     func() error { _, err := s.GoalSet(ctx, appwire.GoalSetParams{Ref: ref}); return err },
		"clear": func() error {
			_, err := s.ClearThread(ctx, appwire.ThreadClearParams{Ref: ref, ClientMutationID: "clear-mutation", ExpectedInstanceID: "session"})
			return err
		},
		"tasks": func() error { _, err := s.ListTasks(ctx, appwire.TaskListParams{Ref: ref}); return err },
		"jobs":  func() error { _, err := s.ListJobs(ctx, appwire.JobsListParams{Ref: ref}); return err },
		"jobOutput": func() error {
			_, err := s.JobOutput(ctx, appwire.JobsOutputParams{Ref: ref, JobID: "job_1"})
			return err
		},
		"subscribe": func() error { _, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: ref}); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := s.entryForRef("broken", ""); err == nil {
		t.Fatal("malformed ref accepted")
	}
	if _, err := s.entryForRef("other:thread", ""); err == nil {
		t.Fatal("foreign ref accepted")
	}
}

func fuzzScenarioLocalDaemonFilteringAndRegistryInvalidRef(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: "wrong", Endpoint: "ws://x", ThreadID: "a"}},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, ThreadID: "b"}},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://x"}},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://x", ThreadID: "c", SourceID: "other"}},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://x", ThreadID: "ok"}},
		}
	}, nil)
	if got := s.liveEntries(); len(got) != 1 {
		t.Fatalf("live entries = %+v", got)
	}
	if _, err := NewRegistry().SourceForRef("bad"); err == nil {
		t.Fatal("invalid registry ref accepted")
	}
}

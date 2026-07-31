package appsource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
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
	appserver.HandleTyped(app.Router(), appwire.MethodSerfSandboxEscalationResolve, emptyHandler[appwire.SandboxEscalationResolveParams])
	appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, emptyHandler[appwire.TurnInterruptParams])
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, emptyHandler[appwire.TurnQueueParams])
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, emptyHandler[appwire.TurnDrainAsSteerParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, emptyHandler[appwire.ThreadCompactStartParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadShutdown, emptyHandler[appwire.ThreadShutdownParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, emptyHandler[appwire.ThreadModelSetParams])
	appserver.HandleTyped(app.Router(), appwire.MethodThreadReasoningEffortSet, emptyHandler[appwire.ThreadReasoningEffortSetParams])
	appserver.HandleTyped(app.Router(), appwire.MethodSerfThreadNameSet, emptyHandler[appwire.ThreadNameSetParams])
	appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		return appwire.GoalSetResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		return appwire.ThreadClearResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodSerfTasksList, func(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
		return appwire.TaskListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodSerfJobsList, func(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
		return appwire.JobsListResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodSerfJobsOutput, func(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
		return appwire.JobsOutputResponse{}, nil
	})
	server := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	t.Cleanup(server.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(server.URL, "http"), SourceID: "local", ThreadID: "thread", SessionID: "session"}
	source := NewLocalDaemonSourceWithEntries("", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, server.Client())
	ctx := context.Background()
	ref := "local:thread"

	if source.ID() != "local" {
		t.Fatalf("ID = %q", source.ID())
	}
	if _, err := source.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := source.ResolveSandboxEscalation(ctx, appwire.SandboxEscalationResolveParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", Ref: ref, ExpectedTurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.QueueTurn(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", ExpectedQueueRevision: 0, Ref: ref}); err != nil {
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
	if _, err := source.ClearThread(ctx, appwire.ThreadClearParams{Ref: ref}); err != nil {
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

func fuzzScenarioLocalDaemonSourceUnavailableSurface(t *testing.T) {
	source := NewLocalDaemonSource("", nil, nil)
	ctx := context.Background()
	if got, err := source.ListModels(ctx, appwire.ModelListParams{}); err != nil || len(got.Data) != 0 {
		t.Fatalf("ListModels = %+v, %v", got, err)
	}
	for name, err := range map[string]error{
		"start":  func() error { _, err := source.StartThread(ctx, appwire.ThreadStartParams{}); return err }(),
		"resume": func() error { _, err := source.ResumeThread(ctx, appwire.ThreadResumeParams{}); return err }(),
		"fork":   func() error { _, err := source.ForkThread(ctx, appwire.ThreadForkParams{}); return err }(),
	} {
		if err == nil {
			t.Fatalf("%s returned nil", name)
		}
	}
	if err := source.restInterrupt(ctx, rendezvous.Entry{}); err == nil {
		t.Fatal("restInterrupt returned nil")
	}
}

func fuzzScenarioLocalDaemonRESTInterruptFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		http.Error(w, " denied ", http.StatusForbidden)
	}))
	defer server.Close()
	entry := rendezvous.Entry{Address: server.Listener.Addr().String(), HubToken: "token"}
	source := NewLocalDaemonSource("local", nil, server.Client())
	if err := source.restInterrupt(context.Background(), entry); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.restInterrupt(canceled, entry); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func fuzzScenarioCodexLiveThreadRemainingLifecycleBranches(t *testing.T) {
	var closed atomic.Int32
	closedSignal := make(chan struct{})
	live := &codexLiveThread{close: func() error { closed.Add(1); close(closedSignal); return nil }, done: make(chan struct{}), subscribers: map[chan appwire.Notification]struct{}{}}
	live.publish(appwire.Notification{Method: "one"})
	live.publish(appwire.Notification{Method: "two"})
	ctx, cancel := context.WithCancel(context.Background())
	out := live.subscribe(ctx)
	if (<-out).Method != "one" || (<-out).Method != "two" {
		t.Fatal("backlog order changed")
	}
	cancel()
	<-closedSignal
	if _, ok := <-out; ok {
		t.Fatal("subscriber remained open")
	}
	if closed.Load() != 1 {
		t.Fatalf("close count = %d", closed.Load())
	}
	live.publish(appwire.Notification{Method: "ignored"})
	live.finish()
	closedOut := live.subscribe(context.Background())
	if _, ok := <-closedOut; ok {
		t.Fatal("closed subscription remained open")
	}
	live.retireIfNoSubscriber(0)
	live.retireIfNoSubscriber(time.Hour)
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
			_, err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: ref})
			return err
		},
		"queue": func() error {
			_, err := s.QueueTurn(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: ref})
			return err
		},
		"drain": func() error {
			_, err := s.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", ExpectedQueueRevision: 0, Ref: ref})
			return err
		},
		"compact":   func() error { return s.CompactThread(ctx, appwire.ThreadCompactStartParams{Ref: ref}) },
		"shutdown":  func() error { return s.ShutdownThread(ctx, appwire.ThreadShutdownParams{Ref: ref}) },
		"model":     func() error { return s.SetThreadModel(ctx, appwire.ThreadModelSetParams{Ref: ref}) },
		"effort":    func() error { return s.SetThreadReasoningEffort(ctx, appwire.ThreadReasoningEffortSetParams{Ref: ref}) },
		"name":      func() error { return s.SetThreadName(ctx, appwire.ThreadNameSetParams{Ref: ref}) },
		"goal":      func() error { _, err := s.GoalSet(ctx, appwire.GoalSetParams{Ref: ref}); return err },
		"clear":     func() error { _, err := s.ClearThread(ctx, appwire.ThreadClearParams{Ref: ref}); return err },
		"tasks":     func() error { _, err := s.ListTasks(ctx, appwire.TaskListParams{Ref: ref}); return err },
		"jobs":      func() error { _, err := s.ListJobs(ctx, appwire.JobsListParams{Ref: ref}); return err },
		"jobOutput": func() error { _, err := s.JobOutput(ctx, appwire.JobsOutputParams{Ref: ref, JobID: "job_1"}); return err },
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

func fuzzScenarioCodexSourceRemainingRPCSurface(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{"id": "turn", "status": "completed"}}, "nextCursor": "next"}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnSteer, emptyHandler[map[string]any])
	appserver.HandleTyped(server.Router(), appwire.MethodTurnInterrupt, emptyHandler[map[string]any])
	appserver.HandleTyped(server.Router(), appwire.MethodThreadCompactStart, emptyHandler[map[string]any])
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{"id": "fallback"}, {"id": "id", "model": "preferred"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	s := NewCodexSource(CodexSourceConfig{Endpoint: "ws" + strings.TrimPrefix(httpServer.URL, "http")}, httpServer.Client())
	ctx := context.Background()
	ref := "codex:thread"
	turns, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: ref, Cursor: "cursor", Limit: 2, ItemsView: "full"})
	if err != nil || len(turns.Data) != 1 || turns.NextCursor != "next" {
		t.Fatalf("turns = %+v, %v", turns, err)
	}
	if _, err := s.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Ref: ref}); err == nil {
		t.Fatal("steer without turn accepted")
	}
	if _, err := s.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Ref: ref, ExpectedTurnID: "turn", Input: []appwire.InputItem{{Type: "text", Text: "hi"}}}); err == nil {
		t.Fatal("codex steer unexpectedly supported")
	}
	if _, err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: ref}); err == nil {
		t.Fatal("interrupt without turn accepted")
	}
	if _, err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", Ref: ref, ExpectedTurnID: "turn"}); err == nil {
		t.Fatal("codex interrupt unexpectedly supported")
	}
	if err := s.CompactThread(ctx, appwire.ThreadCompactStartParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	models, err := s.ListModels(ctx, appwire.ModelListParams{})
	if err != nil || len(models.Data) != 2 || models.Data[0].Model != "fallback" || models.Data[1].Model != "preferred" {
		t.Fatalf("models = %+v, %v", models, err)
	}
	unsupported := []error{
		s.ResolveSandboxEscalation(ctx, appwire.SandboxEscalationResolveParams{}), s.SetThreadName(ctx, appwire.ThreadNameSetParams{}),
	}
	for _, err := range unsupported {
		if err == nil {
			t.Fatal("unsupported call returned nil")
		}
	}
}

func fuzzScenarioCodexSourceRejectsInvalidReferencesAcrossRPCSurface(t *testing.T) {
	s := newTestCodexSource()
	ctx := context.Background()
	calls := []func() error{
		func() error { _, err := s.ReadThread(ctx, appwire.ThreadReadParams{}); return err },
		func() error { _, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{}); return err },
		func() error { _, err := s.ResumeThread(ctx, appwire.ThreadResumeParams{}); return err },
		func() error { _, err := s.ForkThread(ctx, appwire.ThreadForkParams{}); return err },
		func() error {
			_, err := s.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation"})
			return err
		},
		func() error {
			_, err := s.startTurnWithClient(ctx, nil, appwire.TurnStartParams{ClientMutationID: "test-mutation"})
			return err
		},
		func() error {
			_, err := s.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation"})
			return err
		},
		func() error {
			_, err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation"})
			return err
		},
		func() error { return s.CompactThread(ctx, appwire.ThreadCompactStartParams{}) },
		func() error { _, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{}); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("call %d returned nil", i)
		}
	}
	if _, _, err := s.connect(ctx); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	s.endpoint = "ws://unused"
	s.bearerTokenFile = t.TempDir()
	if _, _, err := s.connect(ctx); err == nil {
		t.Fatal("unreadable token accepted")
	}
}

func fuzzScenarioCodexLiveThreadBacklogLimitFinishAndReplacement(t *testing.T) {
	live := &codexLiveThread{close: func() error { return nil }, done: make(chan struct{}), subscribers: map[chan appwire.Notification]struct{}{}}
	for i := 0; i <= codexLiveBacklogLimit; i++ {
		live.publish(appwire.Notification{})
	}
	if len(live.backlog) != codexLiveBacklogLimit {
		t.Fatalf("backlog = %d", len(live.backlog))
	}
	subscriber := make(chan appwire.Notification, 1)
	live.subscribers[subscriber] = struct{}{}
	live.finish()
	if _, ok := <-subscriber; ok {
		t.Fatal("finish did not close subscriber")
	}

	s := newTestCodexSource()
	s.setLiveThread("", live)
	retired := false
	old := &codexLiveThread{close: func() error { retired = true; return nil }, done: make(chan struct{}), subscribers: map[chan appwire.Notification]struct{}{}}
	newer := &codexLiveThread{close: func() error { return nil }, done: make(chan struct{}), subscribers: map[chan appwire.Notification]struct{}{}}
	s.setLiveThread("thread", old)
	s.setLiveThread("thread", newer)
	if !retired {
		t.Fatal("replaced live thread was not retired")
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

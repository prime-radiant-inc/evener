package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// retirementResumeFixture stands up a retiring daemon (a scripted appserver
// peer that answers the prober contract but refuses mutations with the typed
// lifecycle error), a confirmed roster entry for it, and a hub whose spawner
// builds the replacement daemon: a real server.Server over a restored
// agent.Session sharing the seed session's state dir, so the durable mutation
// journal survives the daemon replacement exactly as in production.
type retirementResumeFixture struct {
	t *testing.T

	stateDir  string
	runDir    string
	sessionID string
	ref       string
	meta      schema.SessionMeta
	oldEntry  rendezvous.Entry

	peer *httptest.Server

	locks   *hubcore.ResumeLocks
	roster  *hubcore.Roster
	spawner *fakeRPCSpawner
	hub     *httptest.Server

	launches      atomic.Int32
	accepted      atomic.Int32
	providerCalls atomic.Int32

	waitOnce    sync.Once
	waitEntered chan struct{}
	exit        chan struct{}
	exitOnce    sync.Once

	replacement *httptest.Server
	sess        *agent.Session
	adapter     *retirementAdapter

	providerGate    chan struct{}
	providerEntered chan struct{}

	turnEnded chan struct{}

	peerMutationGate    chan struct{}
	peerMutationEntered chan struct{}
}

type retirementResumeOptions struct {
	peerSessionID string // reported owner; defaults to the seeded session id
	mutationGate  bool   // peer blocks inside the mutation handler until released
	deletionStore *hubcore.DeletionStore
	providerGate  bool // replacement adapter blocks the provider call until released
}

type retirementStartResult struct {
	resp appwire.TurnStartResponse
	err  error
}

func newRetirementResumeFixture(t *testing.T, opts retirementResumeOptions) *retirementResumeFixture {
	t.Helper()
	f := &retirementResumeFixture{
		t:           t,
		stateDir:    t.TempDir(),
		runDir:      t.TempDir(),
		waitEntered: make(chan struct{}),
		exit:        make(chan struct{}),
		turnEnded:   make(chan struct{}, 8),
	}

	// Seed a saved session the replacement daemon will restore.
	seed, err := agent.NewSession(
		llm.NewClient(),
		provider.NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(f.stateDir),
		agent.SessionConfig{StateDir: f.stateDir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	f.sessionID = seed.ID()
	f.ref = "local:" + f.sessionID
	f.meta = seed.Meta()
	seed.Close()

	peerSessionID := opts.peerSessionID
	if peerSessionID == "" {
		peerSessionID = f.sessionID
	}

	if opts.mutationGate {
		f.peerMutationGate = make(chan struct{})
		f.peerMutationEntered = make(chan struct{}, 8)
	}
	if opts.providerGate {
		f.providerGate = make(chan struct{})
		f.providerEntered = make(chan struct{}, 8)
	}

	// The retiring owner: answers the prober contract (thread/list, thread/read,
	// evener/daemon/status reporting phase=retiring) and refuses every mutation
	// with the typed lifecycle-unavailable error, like a daemon draining for
	// idle retirement.
	thread := appwire.Thread{
		ID:        f.sessionID,
		SessionID: peerSessionID,
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener:    appwire.EvenerThread{Ref: f.ref},
	}
	peerApp := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{thread}}, nil
	})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, peerSessionID)
		return appwire.ThreadReadResponse{Thread: thread}, nil
	})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodEvenerDaemonStatus, func(context.Context, appwire.DaemonStatusParams) (appwire.DaemonStatusResponse, error) {
		return appwire.DaemonStatusResponse{Lifecycle: appwire.DaemonLifecycle{
			Phase:         "retiring",
			TimeoutMillis: int64(15 * time.Minute / time.Millisecond),
			Blockers:      []appwire.DaemonBlocker{},
		}}, nil
	})
	retireMutation := func(ctx context.Context) error {
		if f.peerMutationGate != nil {
			select {
			case f.peerMutationEntered <- struct{}{}:
			default:
			}
			select {
			case <-f.peerMutationGate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return appwire.LifecycleUnavailable("retiring")
	}
	appserver.HandleTyped(peerApp.Router(), appwire.MethodTurnStart, func(ctx context.Context, _ appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, retireMutation(ctx)
	})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodTurnQueue, func(ctx context.Context, _ appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, retireMutation(ctx)
	})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodTurnSteer, func(ctx context.Context, _ appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, retireMutation(ctx)
	})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodThreadShutdown, func(ctx context.Context, _ appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, retireMutation(ctx)
	})
	appserver.HandleTyped(peerApp.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, retireMutation(ctx)
	})
	f.peer = httptest.NewServer(http.HandlerFunc(peerApp.ServeWebSocket))
	t.Cleanup(f.peer.Close)

	f.oldEntry = rendezvous.Entry{
		PID:          4242,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + strings.TrimPrefix(f.peer.URL, "http"),
		SourceID:     "local",
		ThreadID:     f.sessionID,
		SessionID:    f.sessionID,
		WorkspaceRef: f.ref,
		StateDir:     f.stateDir,
		StartedAt:    time.Now(),
	}
	writeRendezvous(t, f.runDir, f.oldEntry)
	f.roster = hubcore.NewRoster(f.runDir, &hubcore.StatusProber{})
	f.roster.Refresh()
	if peerSessionID == f.sessionID && !f.roster.HasConfirmedEntry(f.oldEntry) {
		t.Fatalf("retiring peer was not confirmed live on the roster")
	}

	f.locks = hubcore.NewResumeLocks()
	f.adapter = &retirementAdapter{f: f, name: "openai"}
	f.spawner = &fakeRPCSpawner{resume: f.buildReplacement}
	controller := forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
		if target.PID != f.oldEntry.PID {
			return nil, daemonprocess.ErrExited
		}
		return &retirementProcess{f: f}, nil
	})
	f.hub = newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:          f.runDir,
		Roster:          f.roster,
		Spawner:         f.spawner,
		ResumeLocks:     f.locks,
		DaemonProcesses: controller,
		DeletionStore:   opts.deletionStore,
		StateDir:        f.stateDir,
	})
	t.Cleanup(f.hub.Close)
	t.Cleanup(func() {
		if f.replacement != nil {
			f.replacement.Close()
		}
		if f.sess != nil {
			f.sess.Close()
		}
	})
	return f
}

// buildReplacement plays the spawner: it restores the seeded session over the
// same state dir, serves it through a real server.Server, and writes the
// replacement's rendezvous file, exactly like a resumed serve process would.
func (f *retirementResumeFixture) buildReplacement(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
	f.launches.Add(1)
	llmClient := llm.NewClient()
	llmClient.Register(f.adapter)
	// The background session namer runs on the cheap model; give it a dedicated
	// scripted provider so it never consumes the turn adapter's calls (the
	// agent package's withTestSessionNamer establishes this pattern).
	llmClient.Register(&retirementNamerAdapter{})
	sess, err := agent.RestoreSessionFromMetaWithConfig(
		llmClient,
		provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), retirementNamerProvider+"/namer"),
		execenv.NewLocalExecutionEnvironment(f.stateDir),
		f.meta,
		agent.RestoreSessionConfig{StateDir: f.stateDir},
	)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", f.sessionID)
	srv.SetState(appwire.ThreadStatusIdle)
	srv.SetRetrySafeTurnFunctions(server.RetrySafeTurnFunctions{
		Start: func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			resp, err := sess.AcceptClientMutationStart(params)
			if err == nil && resp.Receipt.Disposition == appwire.MutationDispositionApplied {
				f.accepted.Add(1)
			}
			return resp, err
		},
		Queue: sess.AcceptClientMutationQueue,
		Steer: sess.AcceptClientMutationSteer,
	})
	// Emulate the serve loop's drain (cmd/evener/serve.go's
	// SubmitClientMutationStart): an accepted start only reserves the durable
	// intent — without this wake the runner never claims or executes it.
	sess.SetClientMutationStartWakeFunc(func() {
		go func() { _, _, _ = sess.ProcessClientMutationStart(context.Background(), nil) }()
	})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		server.BridgeEvent(srv, ev, f.observeEvent)
	}, func() {})
	repl := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	f.replacement = repl
	f.sess = sess
	entry := rendezvous.Entry{
		PID:          5252,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + strings.TrimPrefix(repl.URL, "http") + "/rpc",
		SourceID:     "local",
		ThreadID:     f.sessionID,
		SessionID:    f.sessionID,
		WorkspaceRef: f.ref,
		StateDir:     f.stateDir,
		StartedAt:    time.Now(),
	}
	if _, err := rendezvous.Write(f.runDir, entry); err != nil {
		return rendezvous.Entry{}, err
	}
	return entry, nil
}

func (f *retirementResumeFixture) observeEvent(ev events.SessionEvent) {
	if ev.Kind == events.EventTurnEnded {
		select {
		case f.turnEnded <- struct{}{}:
		default:
		}
	}
}

func (f *retirementResumeFixture) dial(t *testing.T) *appwire.Client {
	t.Helper()
	client := dialHubRPC(t, f.hub)
	t.Cleanup(func() { client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return client
}

func (f *retirementResumeFixture) params(clientMutationID, text string) appwire.TurnStartParams {
	return appwire.TurnStartParams{
		Ref:                f.ref,
		ClientMutationID:   clientMutationID,
		ExpectedInstanceID: f.sessionID,
		Input:              []appwire.InputItem{{Type: "text", Text: text}},
	}
}

func (f *retirementResumeFixture) startAsync(client *appwire.Client, ctx context.Context, clientMutationID, text string) <-chan retirementStartResult {
	done := make(chan retirementStartResult, 1)
	go func() {
		resp, err := client.TurnStart(ctx, f.params(clientMutationID, text))
		done <- retirementStartResult{resp: resp, err: err}
	}()
	return done
}

// awaitWaitBehindOwner blocks until the hub is waiting on the retiring owner's
// exit. A turn/start that returns first proves the hub surfaced the lifecycle
// error to the caller instead of resolving the retirement.
func (f *retirementResumeFixture) awaitWaitBehindOwner(t *testing.T, ctx context.Context, done <-chan retirementStartResult) {
	t.Helper()
	select {
	case <-f.waitEntered:
	case result := <-done:
		t.Fatalf("turn/start returned before the retiring owner exited: resp=%+v err=%v", result.resp, result.err)
	case <-ctx.Done():
		t.Fatalf("timed out before the hub began waiting on the retiring owner: %v", ctx.Err())
	}
}

// confirmExit plays the retiring daemon's confirmed exit: the process wait
// returns, the peer stops serving, and its rendezvous file disappears.
func (f *retirementResumeFixture) confirmExit(t *testing.T) {
	t.Helper()
	f.peer.Close()
	if err := rendezvous.Remove(f.runDir, f.oldEntry.PID); err != nil {
		t.Fatalf("remove rendezvous: %v", err)
	}
	f.roster.Refresh()
	// The process wait unblocks last: a waiter released before the teardown
	// could still confirm the old owner live on the roster.
	f.exitOnce.Do(func() { close(f.exit) })
}

func (f *retirementResumeFixture) awaitTurnEnd(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-f.turnEnded:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for turn settlement: %v", ctx.Err())
	}
}

func (f *retirementResumeFixture) waitWasEntered() bool {
	select {
	case <-f.waitEntered:
		return true
	default:
		return false
	}
}

type retirementMutationRecord struct {
	ClientMutationID string          `json:"client_mutation_id"`
	Method           string          `json:"method"`
	Payload          json.RawMessage `json:"payload"`
	StableTurnID     string          `json:"stable_turn_id"`
	ExecutionState   string          `json:"execution_state"`
}

func (f *retirementResumeFixture) journal(t *testing.T) map[string]retirementMutationRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.stateDir, "mutations", f.sessionID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read mutation journal: %v", err)
	}
	var parsed struct {
		Journal map[string]retirementMutationRecord `json:"journal"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse mutation journal: %v", err)
	}
	return parsed.Journal
}

// retirementProcess plays the retiring daemon's process handle: Wait blocks
// until the fixture confirms exit or the waiter's context is canceled.
type retirementProcess struct {
	f *retirementResumeFixture
}

func (p *retirementProcess) Kill() error { return nil }
func (p *retirementProcess) Wait(ctx context.Context) error {
	p.f.waitOnce.Do(func() { close(p.f.waitEntered) })
	select {
	case <-p.f.exit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *retirementProcess) Close() error { return nil }

// retirementAdapter answers the replacement session's provider call,
// optionally holding it open behind a gate so a concurrent mutation provably
// conflicts with the in-flight turn.
type retirementAdapter struct {
	f    *retirementResumeFixture
	name string
}

func (a *retirementAdapter) Name() string { return a.name }

func (a *retirementAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.f.providerCalls.Add(1)
	if a.f.providerGate != nil {
		select {
		case a.f.providerEntered <- struct{}{}:
		default:
		}
		select {
		case <-a.f.providerGate:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	// The runner ends a turn only when the model calls communicate with
	// end_turn=true; a plain assistant text is treated as mid-turn work and
	// re-prompted until the round cap (agent/communicate_test_helpers_test.go's
	// communicateResponse is the canonical shape).
	args, _ := json.Marshal(map[string]any{
		"message":  "ok",
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return llm.Response{
		Provider: a.name,
		Model:    req.Model,
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "communicate_test_call",
						Name:      "communicate",
						Arguments: args,
						Type:      "function",
					},
				},
			},
		},
		Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls},
	}, nil
}

const retirementNamerProvider = "retirement-namer"

// retirementNamerAdapter answers only the background session-naming call so the
// turn adapter's call counts stay exact.
type retirementNamerAdapter struct{}

func (a *retirementNamerAdapter) Name() string { return retirementNamerProvider }

func (a *retirementNamerAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Provider: retirementNamerProvider,
		Model:    req.Model,
		Message:  llm.Assistant(`{"name":"Retirement Resume Fixture"}`),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
	}, nil
}

func (a *retirementNamerAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *retirementAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestRetirementResumeDoesNotReplaceLiveOwner(t *testing.T) {
	f := newRetirementResumeFixture(t, retirementResumeOptions{})
	client := f.dial(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	rpcCtx, rpcCancel := context.WithCancel(ctx)
	defer rpcCancel()

	done := f.startAsync(client, rpcCtx, "live-owner-start", "hello")
	f.awaitWaitBehindOwner(t, ctx, done)
	if got := f.launches.Load(); got != 0 {
		t.Fatalf("replacement launches while the owner is alive = %d, want 0", got)
	}

	// Cancellation abandons the wait: no replacement may be launched for a
	// caller that is no longer listening.
	rpcCancel()
	result := <-done
	if result.err == nil {
		t.Fatal("canceled turn/start returned a nil error")
	}
	if got := f.launches.Load(); got != 0 {
		t.Fatalf("replacement launches after cancel = %d, want 0", got)
	}
}

func TestRetirementResumeSameIDReplaysOneStableTurn(t *testing.T) {
	f := newRetirementResumeFixture(t, retirementResumeOptions{})
	first := f.dial(t)
	second := f.dial(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	doneFirst := f.startAsync(first, ctx, "shared-mutation", "send exactly once")
	f.awaitWaitBehindOwner(t, ctx, doneFirst)
	// A second connection retries the same client mutation id behind the same
	// unexited owner; the retry must not race a second daemon or a second turn.
	doneSecond := f.startAsync(second, ctx, "shared-mutation", "send exactly once")

	f.confirmExit(t)
	resultFirst, resultSecond := <-doneFirst, <-doneSecond
	if resultFirst.err != nil || resultSecond.err != nil {
		t.Fatalf("retried mutation after confirmed exit: first err=%v second err=%v", resultFirst.err, resultSecond.err)
	}
	if resultFirst.resp.Turn.ID == "" || resultFirst.resp.Turn.ID != resultSecond.resp.Turn.ID {
		t.Fatalf("same mutation id produced different stable turns: %q vs %q", resultFirst.resp.Turn.ID, resultSecond.resp.Turn.ID)
	}
	applied := 0
	for _, result := range []retirementStartResult{resultFirst, resultSecond} {
		switch result.resp.Receipt.Disposition {
		case appwire.MutationDispositionApplied:
			applied++
		case appwire.MutationDispositionReplayed:
		default:
			t.Fatalf("disposition = %q, want applied or replayed", result.resp.Receipt.Disposition)
		}
	}
	if applied != 1 || f.accepted.Load() != 1 {
		t.Fatalf("same mutation id applied %d times (accepted=%d), want exactly one", applied, f.accepted.Load())
	}
	if got := f.launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want 1", got)
	}

	// The replacement owns the thread now: a subscribed read binds to it even
	// though the thread ref and session id are unchanged.
	read, err := first.ThreadRead(ctx, appwire.ThreadReadParams{Ref: f.ref, Subscribe: true})
	if err != nil {
		t.Fatalf("subscribed read after replacement: %v", err)
	}
	if read.Thread.SessionID != f.sessionID {
		t.Fatalf("replacement thread session = %q, want %q", read.Thread.SessionID, f.sessionID)
	}
}

func TestRetirementResumeConcurrentDistinctIDsConflict(t *testing.T) {
	f := newRetirementResumeFixture(t, retirementResumeOptions{providerGate: true})
	first := f.dial(t)
	second := f.dial(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	doneFirst := f.startAsync(first, ctx, "mutation-a", "first concurrent text")
	f.awaitWaitBehindOwner(t, ctx, doneFirst)
	doneSecond := f.startAsync(second, ctx, "mutation-b", "second concurrent text")

	f.confirmExit(t)
	// The winner's provider call is held open, so the loser provably meets an
	// active turn instead of squeezing in after it finishes.
	select {
	case <-f.providerEntered:
	case <-ctx.Done():
		t.Fatalf("no provider call started after confirmed exit: %v", ctx.Err())
	}
	results := []retirementStartResult{<-doneFirst, <-doneSecond}

	var winner *retirementStartResult
	conflicts := 0
	for i := range results {
		result := &results[i]
		if result.err == nil && result.resp.Receipt.Disposition == appwire.MutationDispositionApplied && result.resp.Turn.ID != "" {
			if winner != nil {
				t.Fatal("both concurrent mutations were applied")
			}
			winner = result
			continue
		}
		var wire appwire.WireError
		if errors.As(result.err, &wire) && wire.Code == appwire.CodeConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent loser: resp=%+v err=%v, want a CodeConflict", result.resp, result.err)
	}
	if winner == nil || conflicts != 1 {
		t.Fatalf("concurrent distinct ids: winner=%v conflicts=%d, want exactly one of each", winner != nil, conflicts)
	}

	journal := f.journal(t)
	if len(journal) != 2 {
		t.Fatalf("journal records = %d, want the winner's applied start and the loser's durable rejection: %+v", len(journal), journal)
	}
	record, ok := journal[winner.resp.Receipt.ClientMutationID]
	if !ok || record.StableTurnID == "" || record.StableTurnID != winner.resp.Turn.ID ||
		record.ExecutionState == "rejected" || len(record.Payload) == 0 {
		t.Fatalf("winner journal record = %+v (present=%v), want stable turn %q with its payload; journal = %+v", record, ok, winner.resp.Turn.ID, journal)
	}
	// The conflicted mutation is journaled too — as durable notAccepted proof
	// (operationState=rejected, payload and stable turn dropped) — never as a
	// second executed turn.
	loserID := "mutation-a"
	if winner.resp.Receipt.ClientMutationID == "mutation-a" {
		loserID = "mutation-b"
	}
	loser, loserOK := journal[loserID]
	if !loserOK || loser.ExecutionState != "rejected" || loser.StableTurnID != "" || len(loser.Payload) != 0 {
		t.Fatalf("loser journal record = %+v (present=%v), want a payload-free rejection with no stable turn; journal = %+v", loser, loserOK, journal)
	}

	close(f.providerGate)
	f.awaitTurnEnd(t, ctx)
	if got := f.launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want 1", got)
	}
	if got := f.accepted.Load(); got != 1 {
		t.Fatalf("applied mutations on the replacement = %d, want 1", got)
	}
}

func TestRetirementResumeSequentialDistinctIDsAccepted(t *testing.T) {
	f := newRetirementResumeFixture(t, retirementResumeOptions{providerGate: true})
	client := f.dial(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	done := f.startAsync(client, ctx, "sequential-first", "first sequential text")
	f.awaitWaitBehindOwner(t, ctx, done)
	f.confirmExit(t)
	first := <-done
	if first.err != nil || first.resp.Receipt.Disposition != appwire.MutationDispositionApplied || first.resp.Turn.ID == "" {
		t.Fatalf("first sequential mutation: resp=%+v err=%v, want one applied stable turn", first.resp, first.err)
	}
	// Hold the first turn inside its provider call so the journal read sees the
	// durable record exactly as accepted: payload is pruned when the record
	// reaches its terminal state (agent/session_client_mutation.go), so only a
	// still-running turn carries it.
	select {
	case <-f.providerEntered:
	case <-ctx.Done():
		t.Fatalf("first turn never reached the provider: %v", ctx.Err())
	}
	journal := f.journal(t)
	record, ok := journal["sequential-first"]
	if !ok || record.StableTurnID != first.resp.Turn.ID || record.ExecutionState == "rejected" ||
		!strings.Contains(string(record.Payload), "first sequential text") {
		t.Fatalf("journal after first acceptance = %+v (present=%v), want stable turn %q with its payload; journal = %+v", record, ok, first.resp.Turn.ID, journal)
	}
	close(f.providerGate)
	f.awaitTurnEnd(t, ctx)

	// The replacement is the live owner now: a distinct id starts a fresh turn
	// without any wait, and both records stay intact in the shared journal.
	second, err := client.TurnStart(ctx, f.params("sequential-second", "second sequential text"))
	if err != nil || second.Receipt.Disposition != appwire.MutationDispositionApplied || second.Turn.ID == "" || second.Turn.ID == first.resp.Turn.ID {
		t.Fatalf("second sequential mutation: resp=%+v err=%v, want a distinct applied turn", second, err)
	}
	f.awaitTurnEnd(t, ctx)

	journal = f.journal(t)
	firstRecord, firstOK := journal["sequential-first"]
	secondRecord, secondOK := journal["sequential-second"]
	if !firstOK || !secondOK {
		t.Fatalf("journal records after both turns = %+v, want both mutation ids", journal)
	}
	if firstRecord.StableTurnID != first.resp.Turn.ID || secondRecord.StableTurnID != second.Turn.ID {
		t.Fatalf("stable turns changed: first=%q want %q, second=%q want %q",
			firstRecord.StableTurnID, first.resp.Turn.ID, secondRecord.StableTurnID, second.Turn.ID)
	}
	if firstRecord.ExecutionState == "rejected" || secondRecord.ExecutionState == "rejected" {
		t.Fatalf("accepted turns journaled as rejected: first=%+v second=%+v", firstRecord, secondRecord)
	}
	if got := f.providerCalls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if got := f.accepted.Load(); got != 2 {
		t.Fatalf("applied mutations = %d, want 2", got)
	}
	if got := f.launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want 1", got)
	}
}

func TestRetirementResumeFences(t *testing.T) {
	t.Run("resume required blocks the automatic path", func(t *testing.T) {
		f := newRetirementResumeFixture(t, retirementResumeOptions{})
		if err := f.locks.PersistForceStop([]string{f.sessionID}, f.sessionID); err != nil {
			t.Fatalf("PersistForceStop: %v", err)
		}
		client := f.dial(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		_, err := client.TurnStart(ctx, f.params("fenced-start", "must not run"))
		if err == nil || !strings.Contains(err.Error(), "explicit thread/resume") {
			t.Fatalf("err = %v, want the explicit-resume admission error", err)
		}
		if got := f.launches.Load(); got != 0 {
			t.Fatalf("launches = %d, want 0", got)
		}
		if f.waitWasEntered() {
			t.Fatal("recovery-required session entered the retirement wait")
		}
	})

	t.Run("stale epoch cancels the pending action", func(t *testing.T) {
		f := newRetirementResumeFixture(t, retirementResumeOptions{mutationGate: true})
		client := f.dial(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		done := f.startAsync(client, ctx, "stale-epoch-start", "must not resume")
		select {
		case <-f.peerMutationEntered:
		case <-ctx.Done():
			t.Fatalf("mutation never reached the peer: %v", ctx.Err())
		}
		// The request was admitted before this recovery began; finishing the
		// stop without a recovery requirement still invalidates its epoch.
		abort := f.locks.BeginForceStop([]string{f.sessionID})
		abort(false)
		close(f.peerMutationGate)

		result := <-done
		if result.err == nil || !strings.Contains(result.err.Error(), "canceled this pending action") {
			t.Fatalf("err = %v, want the stale-epoch admission error", result.err)
		}
		if got := f.launches.Load(); got != 0 {
			t.Fatalf("launches = %d, want 0", got)
		}
		if f.waitWasEntered() {
			t.Fatal("stale-epoch request entered the retirement wait")
		}
	})

	t.Run("deleted target stays fenced", func(t *testing.T) {
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewDeletionStore: %v", err)
		}
		projectsRoot := t.TempDir()
		projectID := filepath.Base(hubtest.ProjectDir(t, projectsRoot, "alpha"))
		f := newRetirementResumeFixture(t, retirementResumeOptions{deletionStore: store})
		record, err := store.Begin(projectID, []hubcore.DeletionTarget{{Ref: f.ref, ThreadID: f.sessionID}})
		if err != nil {
			t.Fatalf("deletion Begin: %v", err)
		}
		if err := store.MarkDeleted(projectID, record.Generation); err != nil {
			t.Fatalf("MarkDeleted: %v", err)
		}
		client := f.dial(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		_, err = client.TurnStart(ctx, f.params("deleted-start", "must not run"))
		if err == nil || !strings.Contains(err.Error(), "deleted") {
			t.Fatalf("err = %v, want the deletion fence error", err)
		}
		if got := f.launches.Load(); got != 0 {
			t.Fatalf("launches = %d, want 0", got)
		}
		if f.waitWasEntered() {
			t.Fatal("deleted session entered the retirement wait")
		}
	})

	t.Run("incompatible owner never triggers a resume", func(t *testing.T) {
		f := newRetirementResumeFixture(t, retirementResumeOptions{peerSessionID: "different-owner"})
		client := f.dial(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		done := f.startAsync(client, ctx, "incompatible-start", "must not resume")
		result := <-done
		if result.err == nil {
			t.Fatal("turn/start against an incompatible owner returned a nil error")
		}
		if got := f.launches.Load(); got != 0 {
			t.Fatalf("launches = %d, want 0", got)
		}
		if f.waitWasEntered() {
			t.Fatal("incompatible owner entered the retirement wait")
		}
	})

	t.Run("non-start actions never wait or spawn", func(t *testing.T) {
		f := newRetirementResumeFixture(t, retirementResumeOptions{})
		client := f.dial(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		// Reads still work against a retiring daemon; the rest lose the
		// lifecycle race. None of them may wait on exit or launch a replacement.
		_, _ = client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: f.ref})
		_ = client.ThreadShutdown(ctx, appwire.ThreadShutdownParams{Ref: f.ref})
		_ = client.TurnQueue(ctx, appwire.TurnQueueParams{
			Ref:                f.ref,
			ClientMutationID:   "queue-while-retiring",
			ExpectedInstanceID: f.sessionID,
			Input:              []appwire.InputItem{{Type: "text", Text: "must not run"}},
		})
		_ = client.TurnSteer(ctx, appwire.TurnSteerParams{
			Ref:                f.ref,
			ClientMutationID:   "steer-while-retiring",
			ExpectedInstanceID: f.sessionID,
			Input:              []appwire.InputItem{{Type: "text", Text: "must not run"}},
		})
		_ = client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{Ref: f.ref, Model: "gpt-5.2"})

		if got := f.launches.Load(); got != 0 {
			t.Fatalf("launches = %d, want 0", got)
		}
		if f.waitWasEntered() {
			t.Fatal("non-start action entered the retirement wait")
		}
	})
}

func TestRetirementResumeUnreadableDiscoveryFails(t *testing.T) {
	// A run dir that is a regular file makes rendezvous discovery unreadable;
	// turn/start must fail instead of guessing at a replacement.
	runFile := filepath.Join(t.TempDir(), "run-dir-as-file")
	if err := os.WriteFile(runFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	launches := 0
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			launches++
			return rendezvous.Entry{}, errors.New("must not be called")
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:      runFile,
		Roster:      hubcore.NewRoster(runFile, nil),
		Spawner:     spawner,
		ResumeLocks: hubcore.NewResumeLocks(),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:              "local:any-session",
		ClientMutationID: "unreadable-discovery",
		Input:            []appwire.InputItem{{Type: "text", Text: "must not run"}},
	})
	if err == nil {
		t.Fatal("turn/start with unreadable discovery returned a nil error")
	}
	if launches != 0 {
		t.Fatalf("launches = %d, want 0", launches)
	}
}

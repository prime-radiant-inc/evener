package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

	// wakeWG tracks the turn goroutines the replacement's wake func spawns.
	// They call back into the session from OUTSIDE it, so sess.Close() cannot
	// join them; the fixture must, before t.TempDir's removal runs.
	wakeWG sync.WaitGroup

	waitOnce    sync.Once
	waitEntered chan struct{}
	exit        chan struct{}
	exitOnce    sync.Once

	replacement *httptest.Server
	sess        *agent.Session
	adapter     *retirementAdapter

	// eventsDrained closes when the replacement session's event consumer has
	// drained the closed events channel (ConsumeEventsLossless's onDrained).
	eventsDrained chan struct{}

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
		// Deterministic shutdown before the temp dirs (created above, removed
		// last under LIFO) are reclaimed: stop the RPC surface so no new
		// mutation can be accepted — and no new wake fired — once the session
		// is closing; close the session so its internal writers (transcript,
		// journal, namer) are joined; then join the wake-spawned turn
		// goroutines and the event consumer, which call in from outside the
		// session and are the writers sess.Close() cannot wait for. Without
		// these joins a turn still settling meta/journal files under stateDir
		// outlives the test body and races t.TempDir's RemoveAll
		// ("directory not empty").
		if f.replacement != nil {
			f.replacement.Close()
		}
		if f.sess != nil {
			f.sess.Close()
			<-f.eventsDrained
		}
		f.wakeWG.Wait()
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
		f.wakeWG.Go(func() {
			_, _, _ = sess.ProcessClientMutationStart(context.Background(), nil)
		})
	})
	f.eventsDrained = make(chan struct{})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		server.BridgeEvent(srv, ev, f.observeEvent)
	}, func() { close(f.eventsDrained) })
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

func (f *retirementResumeFixture) startAsync(ctx context.Context, client *appwire.Client, clientMutationID, text string) <-chan retirementStartResult {
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
func (f *retirementResumeFixture) awaitWaitBehindOwner(ctx context.Context, t *testing.T, done <-chan retirementStartResult) {
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

func (f *retirementResumeFixture) awaitTurnEnd(ctx context.Context, t *testing.T) {
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
	OperationState   string          `json:"operation_state"`
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

// Close has nothing to join: the handle starts no goroutine and holds no
// resource. Wait is driven entirely by the fixture's exit channel (closed by
// the test's confirmExit) and the waiter's context, so no waiter outlives the
// test through this handle. The writers that DID outlive it are the
// wake-spawned turn goroutines, joined by the fixture's wakeWG instead.
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

	done := f.startAsync(rpcCtx, client, "live-owner-start", "hello")
	f.awaitWaitBehindOwner(ctx, t, done)
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

	doneFirst := f.startAsync(ctx, first, "shared-mutation", "send exactly once")
	f.awaitWaitBehindOwner(ctx, t, doneFirst)
	// A second connection retries the same client mutation id behind the same
	// unexited owner; the retry must not race a second daemon or a second turn.
	doneSecond := f.startAsync(ctx, second, "shared-mutation", "send exactly once")

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

	doneFirst := f.startAsync(ctx, first, "mutation-a", "first concurrent text")
	f.awaitWaitBehindOwner(ctx, t, doneFirst)
	doneSecond := f.startAsync(ctx, second, "mutation-b", "second concurrent text")

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
	f.awaitTurnEnd(ctx, t)
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

	done := f.startAsync(ctx, client, "sequential-first", "first sequential text")
	f.awaitWaitBehindOwner(ctx, t, done)
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
	f.awaitTurnEnd(ctx, t)

	// The turn-ended event can precede the journal's terminal transition, which
	// is what clears the session's active-turn fence
	// (agent/session_client_mutation.go: OperationState=terminal alongside
	// ActiveTurnID=""). The second distinct start must wait for that reflection
	// — the plan's "settlement event and journal reflection" — or a loaded host
	// can hand it a "turn is already active" conflict.
	for {
		if rec, ok := f.journal(t)["sequential-first"]; ok && rec.OperationState == "terminal" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("first mutation never reached terminal reflection: %v; journal = %+v", ctx.Err(), f.journal(t))
		case <-time.After(5 * time.Millisecond):
		}
	}

	// The replacement is the live owner now: a distinct id starts a fresh turn
	// without any wait, and both records stay intact in the shared journal.
	second, err := client.TurnStart(ctx, f.params("sequential-second", "second sequential text"))
	if err != nil || second.Receipt.Disposition != appwire.MutationDispositionApplied || second.Turn.ID == "" || second.Turn.ID == first.resp.Turn.ID {
		t.Fatalf("second sequential mutation: resp=%+v err=%v, want a distinct applied turn", second, err)
	}
	f.awaitTurnEnd(ctx, t)

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

		done := f.startAsync(ctx, client, "stale-epoch-start", "must not resume")
		select {
		case <-f.peerMutationEntered:
		case <-ctx.Done():
			t.Fatalf("mutation never reached the peer: %v", ctx.Err())
		}
		// The request was admitted before this recovery began; finishing the
		// stop without a recovery requirement still invalidates its epoch.
		abort := f.locks.BeginForceStop([]string{f.sessionID})
		abort.Finish(false)
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

		done := f.startAsync(ctx, client, "incompatible-start", "must not resume")
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

// TestResumeAfterConfirmedRetirementSpawnsResolvedTarget pins H5-2: when
// resumeOwnership resolves the requested alias to a different current target
// (a session cleared via thread/clear), the spawn half must name the RESOLVED
// target. Before the fix it received the pre-resolution requested id, so the
// replacement was discovered and launched for the obsolete alias — resurrecting
// stale state and dropping the cleared session's state.
func TestResumeAfterConfirmedRetirementSpawnsResolvedTarget(t *testing.T) {
	const (
		requested = "session-stale-alias"
		target    = "session-resolved-target"
	)
	locks := hubcore.NewResumeLocks()
	// A completed clear: the requested alias and its replacement shared one
	// ownership group, explicit recovery acknowledged it, and the resolved
	// routing records requested -> target. resumeOwnershipStep reads exactly
	// this state to return a target distinct from the request.
	if err := locks.PersistForceStop([]string{requested, target}, target); err != nil {
		t.Fatalf("PersistForceStop: %v", err)
	}
	epoch := locks.RecoveryState(requested).Epoch
	if err := locks.ExplicitResumeCompleted(target, epoch); err != nil {
		t.Fatalf("ExplicitResumeCompleted: %v", err)
	}
	locks.RecordResolvedSession(requested, target, epoch)
	resolveCfg := hubcore.WebConfig{ResumeLocks: locks, RunDir: t.TempDir()}
	if got, _, err := resumeOwnership(resolveCfg, requested, requested); err != nil || got != target {
		t.Fatalf("resumeOwnership(%q) = (%q, err=%v), want the resolved target %q", requested, got, err, target)
	}

	var spawned string
	spawner := &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawned = req.SessionID
		return rendezvous.Entry{}, nil
	}}
	runDir := t.TempDir()
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      hubcore.NewRoster(runDir, nil),
		ResumeLocks: locks,
		Spawner:     spawner,
	}
	// The spawn identity is the assertion; the zero rendezvous entry cannot
	// project a responsive thread, so the call's own error is not the subject.
	_ = resumeAfterConfirmedRetirement(t.Context(), cfg, nil, appwire.TurnStartParams{Ref: "local:" + requested})
	if spawned != target {
		t.Fatalf("spawn request session = %q, want the resolved target %q (not the requested alias %q)", spawned, target, requested)
	}
}

// TestRetirementResumeSiblingAliasDeletionFencesOwnershipGroup pins the same
// ownership-group fence on the retirement path: the deletion record names only
// a sibling alias in the resolved group while the path validated only the
// resolved target, so the whole group must be fenced under the alias locks
// before live-owner reuse or replacement.
func TestRetirementResumeSiblingAliasDeletionFencesOwnershipGroup(t *testing.T) {
	requested := hubtest.SessionID(t)
	target := hubtest.SessionID(t)
	sibling := hubtest.SessionID(t)
	locks := hubcore.NewResumeLocks()
	// A completed clear over a three-alias ownership group, with the resolved
	// routing recorded requested -> target. The fence names only the sibling.
	if err := locks.PersistForceStop([]string{requested, target, sibling}, target); err != nil {
		t.Fatalf("PersistForceStop: %v", err)
	}
	epoch := locks.RecoveryState(requested).Epoch
	if err := locks.ExplicitResumeCompleted(target, epoch); err != nil {
		t.Fatalf("ExplicitResumeCompleted: %v", err)
	}
	locks.RecordResolvedSession(requested, target, epoch)
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(filepath.Base(hubtest.ProjectDir(t, t.TempDir(), "deleted")), []hubcore.DeletionTarget{{Ref: "local:" + sibling, ThreadID: sibling}}); err != nil {
		t.Fatal(err)
	}
	launches := 0
	runDir := t.TempDir()
	prevRefresh := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
	t.Cleanup(func() { hubRosterRefresh = prevRefresh })
	cfg := hubcore.WebConfig{
		RunDir:        runDir,
		Roster:        hubcore.NewRoster(runDir, nil),
		ResumeLocks:   locks,
		DeletionStore: store,
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			launches++
			return rendezvous.Entry{}, errors.New("sibling-deleted group reached launcher")
		}},
	}
	err = resumeAfterConfirmedRetirement(t.Context(), cfg, nil, appwire.TurnStartParams{Ref: "local:" + requested})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("sibling deletion fence error=%v", err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeTargetDeleted {
		t.Errorf("deletion outcome=%#v", wire.Data)
	}
	if launches != 0 {
		t.Fatalf("sibling-deleted group launch count=%d", launches)
	}
}

// TestResumeAfterConfirmedRetirementRecordsResolvedSession is the round-16
// regression: a successful retirement recovery must record the replacement the
// alias resolved to, or the alias keeps walking a stale hop. The recorded chain
// is stale by construction — a completed explicit resume left A -> B
// (ResumeSessionID cleared, ResolvedSessionID(A) == B) — while the live
// rendezvous entry names C as the current session and still claims A, so
// resumeClaimTarget resolves the target to C from live evidence. C appears in no
// recorded hop, so without RecordResolvedSession the alias still names the older
// session B and a later fork/resume branches it.
func TestResumeAfterConfirmedRetirementRecordsResolvedSession(t *testing.T) {
	const (
		alias  = "session-record-alias"
		stale  = "session-record-stale"
		target = "session-record-target"
	)
	locks := hubcore.NewResumeLocks()
	// A completed redirect A -> B, exactly as production leaves one after an
	// explicit resume: the durable per-alias ResumeSessionID is cleared, and the
	// only surviving record is the group's resolved routing.
	if err := locks.PersistForceStop([]string{alias, stale}, stale); err != nil {
		t.Fatalf("PersistForceStop: %v", err)
	}
	epoch := locks.RecoveryState(alias).Epoch
	if err := locks.ExplicitResumeCompleted(stale, epoch); err != nil {
		t.Fatalf("ExplicitResumeCompleted: %v", err)
	}
	locks.RecordResolvedSession(alias, stale, epoch)
	if got := locks.ResolvedSessionID(alias); got != stale {
		t.Fatalf("seeded ResolvedSessionID(%q) = %q, want the stale hop %q", alias, got, stale)
	}

	// The live daemon serves C but still owns the alias A. The run dir holds the
	// rendezvous entry resumeOwnership reads as live evidence; the roster holds
	// the same entry so the owner lookups resolve it as the already-live
	// replacement.
	entry := rendezvous.Entry{
		PID:          6101,
		Address:      "127.0.0.1:6101",
		Endpoint:     "ws://127.0.0.1:6101/rpc",
		Protocol:     appwire.ProtocolVersion,
		SourceID:     "local",
		ThreadID:     alias,
		SessionID:    target,
		WorkspaceRef: "local:" + alias,
		InstanceID:   "record-instance",
		StateDir:     t.TempDir(),
		StartedAt:    time.Unix(1700002000, 0).UTC(),
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, entry)
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:          entry,
		SessionID:      target,
		Status:         appwire.ThreadStatusIdle,
		Lifecycle:      &appwire.DaemonLifecycle{Phase: "resident", Blockers: []appwire.DaemonBlocker{}},
		LifecycleFresh: true,
	})
	prevRefresh := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
	t.Cleanup(func() { hubRosterRefresh = prevRefresh })

	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		ResumeLocks: locks,
	}
	if err := resumeAfterConfirmedRetirement(t.Context(), cfg, nil, appwire.TurnStartParams{Ref: "local:" + alias}); err != nil {
		t.Fatalf("resumeAfterConfirmedRetirement: %v", err)
	}
	if got := locks.ResolvedSessionID(alias); got != target {
		t.Fatalf("ResolvedSessionID(%q) = %q after a successful retirement resume, want the live replacement %q (the stale hop is %q)", alias, got, target, stale)
	}
}

// captureHubStderr redirects the hub's lifecycle log destination (os.Stderr)
// into a pipe for the duration of fn and returns what was written. Both pipe
// ends are closed and os.Stderr restored through t.Cleanup, so an early
// t.Fatalf or panic cannot leak a descriptor or leave stderr redirected.
func captureHubStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stderr = original
		_ = writeEnd.Close()
		_ = readEnd.Close()
	})
	os.Stderr = writeEnd
	fn()
	_ = writeEnd.Close()
	os.Stderr = original
	data, _ := io.ReadAll(readEnd)
	return string(data)
}

// TestResumeAfterConfirmedRetirementRecordsLifecycle requires that a resume
// triggered by daemon retirement carries the same correlated lifecycle trace as
// an explicit resume: an operation=resume stream whose records carry the
// requested identity in session_id and, for every stage at or after ownership
// resolution, the resolved target in resolved_session_id. The log destination
// is the hub's own stderr, not a configurable seam, so capture it around the
// call.
func TestResumeAfterConfirmedRetirementRecordsLifecycle(t *testing.T) {
	requested := hubtest.SessionID(t)
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	target := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	if requested == target {
		t.Fatal("fixture requires distinct requested and resolved session IDs")
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        target,
			SessionID: target,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: params.Ref, InstanceID: target},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
		if req.SessionID != target {
			t.Fatalf("resume request session = %q, want the resolved target %q", req.SessionID, target)
		}
		entry := rendezvous.Entry{
			PID:        106,
			Protocol:   appwire.ProtocolVersion,
			Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
			SourceID:   "local",
			ThreadID:   target,
			SessionID:  target,
			WorkingDir: workingDir,
		}
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	locks := hubcore.NewResumeLocks()
	if err := locks.PersistForceStop([]string{requested, target}, target); err != nil {
		t.Fatalf("PersistForceStop: %v", err)
	}
	epoch := locks.RecoveryState(requested).Epoch
	if err := locks.ExplicitResumeCompleted(target, epoch); err != nil {
		t.Fatalf("ExplicitResumeCompleted: %v", err)
	}
	locks.RecordResolvedSession(requested, target, epoch)

	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      hubcore.NewRoster(runDir, nil),
		ResumeLocks: locks,
		Spawner:     spawner,
		Past:        past,
	}

	var resumeErr error
	data := captureHubStderr(t, func() {
		resumeErr = resumeAfterConfirmedRetirement(t.Context(), cfg, newHubSourceRegistry(cfg), appwire.TurnStartParams{Ref: "local:" + requested})
	})
	if resumeErr != nil {
		t.Fatalf("resumeAfterConfirmedRetirement: %v", resumeErr)
	}
	records := assertThreadLifecycleRecords(t, data)
	for _, record := range records {
		if record["operation"] != "resume" || record["session_id"] != requested {
			t.Fatalf("lost retirement-resume correlation: %#v, want operation=resume session_id=%s", record, requested)
		}
	}
	assertThreadLifecycleOutcome(t, records, "request", "success", "none")
	assertThreadLifecycleOutcome(t, records, "ownership", "success", "none")
	// Only the stages recorded after resumeOwnership resolves the alias carry the
	// resolved identity, exactly as the explicit path's post-resolution stages do.
	for _, stage := range []string{"lock_wait", "lock_held", "discovery", "protocol_check", "owner_lookup", "request_preparation", "spawner_resume", "post_launch_discovery", "daemon_read"} {
		assertThreadLifecycleOutcome(t, records, stage, "success", "none")
		for _, record := range records {
			if record["stage"] == stage && record["resolved_session_id"] != target {
				t.Fatalf("%s lost resolved identity: %#v, want resolved_session_id=%s", stage, record, target)
			}
		}
	}
}

// TestResumeAfterConfirmedRetirementRecordsAdmissionFailure requires that a
// retirement resume refused by an admission fence, before it ever reaches the
// spawn half, still emits the request pair: the path must remain observable
// exactly when it fails.
func TestResumeAfterConfirmedRetirementRecordsAdmissionFailure(t *testing.T) {
	requested := hubtest.SessionID(t)
	locks := hubcore.NewResumeLocks()
	// An in-flight force stop leaves Stopping > 0, so the admission re-check
	// refuses the retirement resume. finish.Finish(false) is the caller acknowledging
	// the refused force stop; the fence itself is what the test needs held.
	finish := locks.BeginForceStop([]string{requested})
	t.Cleanup(func() { finish.Finish(false) })
	runDir := t.TempDir()
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      hubcore.NewRoster(runDir, nil),
		ResumeLocks: locks,
	}

	var resumeErr error
	data := captureHubStderr(t, func() {
		resumeErr = resumeAfterConfirmedRetirement(t.Context(), cfg, nil, appwire.TurnStartParams{Ref: "local:" + requested})
	})
	if resumeErr == nil {
		t.Fatal("resumeAfterConfirmedRetirement unexpectedly succeeded under an admission fence")
	}
	records := assertThreadLifecycleRecords(t, data)
	for _, record := range records {
		if record["operation"] != "resume" || record["session_id"] != requested {
			t.Fatalf("lost refused-resume correlation: %#v, want operation=resume session_id=%s", record, requested)
		}
	}
	assertThreadLifecycleOutcome(t, records, "request", "error", "failed")
}

// TestResumeAfterConfirmedRetirementRecordsLiveReplacementIdentity requires
// that the reuse-a-live-replacement early return records the resolved identity:
// ownership resolves the alias before that return, so its deferred request
// completion must carry resolved_session_id=target like every other
// post-resolution outcome.
func TestResumeAfterConfirmedRetirementRecordsLiveReplacementIdentity(t *testing.T) {
	alias := hubtest.SessionID(t)
	stale := hubtest.SessionID(t)
	target := hubtest.SessionID(t)

	locks := hubcore.NewResumeLocks()
	// A completed redirect alias -> stale is on record; the live entry below
	// names target as the current session while still claiming alias, so
	// resumeOwnership resolves target from live evidence.
	if err := locks.PersistForceStop([]string{alias, stale}, stale); err != nil {
		t.Fatalf("PersistForceStop: %v", err)
	}
	epoch := locks.RecoveryState(alias).Epoch
	if err := locks.ExplicitResumeCompleted(stale, epoch); err != nil {
		t.Fatalf("ExplicitResumeCompleted: %v", err)
	}
	locks.RecordResolvedSession(alias, stale, epoch)

	entry := rendezvous.Entry{
		PID:          6101,
		Address:      "127.0.0.1:6101",
		Endpoint:     "ws://127.0.0.1:6101/rpc",
		Protocol:     appwire.ProtocolVersion,
		SourceID:     "local",
		ThreadID:     alias,
		SessionID:    target,
		WorkspaceRef: "local:" + alias,
		InstanceID:   "live-replacement-instance",
		StateDir:     t.TempDir(),
		StartedAt:    time.Unix(1700003000, 0).UTC(),
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, entry)
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:          entry,
		SessionID:      target,
		Status:         appwire.ThreadStatusIdle,
		Lifecycle:      &appwire.DaemonLifecycle{Phase: "resident", Blockers: []appwire.DaemonBlocker{}},
		LifecycleFresh: true,
	})
	prevRefresh := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
	t.Cleanup(func() { hubRosterRefresh = prevRefresh })

	cfg := hubcore.WebConfig{RunDir: runDir, Roster: roster, ResumeLocks: locks}

	var resumeErr error
	data := captureHubStderr(t, func() {
		resumeErr = resumeAfterConfirmedRetirement(t.Context(), cfg, nil, appwire.TurnStartParams{Ref: "local:" + alias})
	})
	if resumeErr != nil {
		t.Fatalf("resumeAfterConfirmedRetirement: %v", resumeErr)
	}
	records := assertThreadLifecycleRecords(t, data)
	for _, record := range records {
		if record["operation"] != "resume" || record["session_id"] != alias {
			t.Fatalf("lost live-replacement correlation: %#v, want operation=resume session_id=%s", record, alias)
		}
	}
	complete := false
	for _, record := range records {
		if record["stage"] != "request" || record["state"] != "complete" {
			continue
		}
		complete = true
		if record["result"] != "success" || record["resolved_session_id"] != target {
			t.Fatalf("request completion: %#v, want success resolved_session_id=%s", record, target)
		}
	}
	if !complete {
		t.Fatalf("missing request completion: %#v", records)
	}
}

// TestResumeAfterConfirmedRetirementRecordsLockWait pins that lock waiting is
// observable on the retirement path: the lock_wait begin record must be
// emitted before the alias mutex is acquired, so a resume blocked behind a
// concurrent force stop or explicit resume is attributable to lock contention
// rather than to a slow daemon exit or spawn.
func TestResumeAfterConfirmedRetirementRecordsLockWait(t *testing.T) {
	id := hubtest.SessionID(t)
	locks := hubcore.NewResumeLocks()
	lock := locks.For(id)
	lock.Lock()
	release := sync.OnceFunc(lock.Unlock)
	defer release()

	w := &threadLifecycleWaitWriter{waiting: make(chan struct{})}
	ctx, _ := withThreadLifecycleLog(t.Context(), "resume", id, w)
	runDir := t.TempDir()
	cfg := hubcore.WebConfig{RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil), ResumeLocks: locks}

	done := make(chan error, 1)
	go func() {
		done <- resumeAfterConfirmedRetirement(ctx, cfg, nil, appwire.TurnStartParams{Ref: "local:" + id})
	}()

	select {
	case <-w.waiting:
		// lock_wait/begin was recorded while the alias mutex is still held.
	case <-time.After(10 * time.Second):
		t.Fatal("lock_wait begin was not recorded before lock acquisition while the alias mutex was held")
	}
	release()

	finished := false
	defer func() {
		release()
		if !finished {
			<-done
		}
	}()
	// The spawner is unconfigured, so the resume fails after the lock section;
	// this test asserts the lock stages, not the outcome.
	<-done
	finished = true

	records := assertThreadLifecycleRecords(t, w.String())
	assertThreadLifecycleOutcome(t, records, "lock_wait", "success", "none")
	assertThreadLifecycleOutcome(t, records, "lock_held", "success", "none")
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

// TestLifecycleRetiringGateExcludesPreparing pins the gate that admits the
// retirement-resume path: only the terminal "retiring" reason routes into
// resumeAfterConfirmedRetirement. A refusal typed "preparing" (or "resident")
// is an ordinary retryable race, so sameAsRefused is never evaluated for a
// daemon that has settled back to resident — a committed retirement cannot
// return to resident (agent/retirement.go: Commit is terminal, Abort is
// preparing-only).
func TestLifecycleRetiringGateExcludesPreparing(t *testing.T) {
	if !isLifecycleRetiringError(appwire.LifecycleUnavailable("retiring")) {
		t.Fatal(`isLifecycleRetiringError("retiring") = false, want true`)
	}
	for _, reason := range []string{"preparing", "resident"} {
		if isLifecycleRetiringError(appwire.LifecycleUnavailable(reason)) {
			t.Fatalf("isLifecycleRetiringError(%q) = true, want false: a non-terminal phase must not route into the retirement wait", reason)
		}
	}
}

// TestSameDaemonIdentityUsesExactOwnership proves sameDaemonIdentity treats any
// change to a hashed ownership field as a different daemon. A narrower subset
// (PID/StartedAt/InstanceID/Endpoint) misclassified a replacement differing only
// in Address/Protocol/SourceID/ThreadID/SessionID/WorkspaceRef/StateDir/WorkingDir
// as the refused owner, so resumeAfterConfirmedRetirement waited on the wrong
// process instead of treating it as a replacement.
func TestSameDaemonIdentityUsesExactOwnership(t *testing.T) {
	base := rendezvous.Entry{
		PID:          4242,
		Address:      "127.0.0.1:5000",
		Endpoint:     "ws://127.0.0.1:5000/rpc",
		Protocol:     appwire.ProtocolVersion,
		SourceID:     "local",
		ThreadID:     "thread-a",
		SessionID:    "session-a",
		WorkspaceRef: "local:session-a",
		InstanceID:   "instance-a",
		WorkingDir:   "/work/a",
		StateDir:     "/state/a",
		StartedAt:    time.Unix(1700000000, 0).UTC(),
	}
	if !sameDaemonIdentity(base, base) {
		t.Fatal("identical entries must be the same daemon")
	}
	// Probe-irrelevant settings never enter the ownership fingerprint, so a
	// re-published copy that differs only in them stays the same owner.
	nonOwned := base
	nonOwned.Agent = "other"
	nonOwned.Model = "other"
	nonOwned.Provider = "other"
	nonOwned.HubToken = "secret"
	nonOwned.SpawnedBy = "someone-else"
	if !sameDaemonIdentity(base, nonOwned) {
		t.Fatal("non-ownership fields must not change the daemon identity")
	}
	drift := map[string]func(*rendezvous.Entry){
		"pid":           func(e *rendezvous.Entry) { e.PID = 4243 },
		"address":       func(e *rendezvous.Entry) { e.Address = "127.0.0.1:5001" },
		"endpoint":      func(e *rendezvous.Entry) { e.Endpoint = "ws://127.0.0.1:5001/rpc" },
		"protocol":      func(e *rendezvous.Entry) { e.Protocol = "evener-appwire-v1" },
		"source id":     func(e *rendezvous.Entry) { e.SourceID = "remote" },
		"thread id":     func(e *rendezvous.Entry) { e.ThreadID = "thread-b" },
		"session id":    func(e *rendezvous.Entry) { e.SessionID = "session-b" },
		"workspace ref": func(e *rendezvous.Entry) { e.WorkspaceRef = "local:session-b" },
		"instance id":   func(e *rendezvous.Entry) { e.InstanceID = "instance-b" },
		"working dir":   func(e *rendezvous.Entry) { e.WorkingDir = "/work/b" },
		"state dir":     func(e *rendezvous.Entry) { e.StateDir = "/state/b" },
		"started at":    func(e *rendezvous.Entry) { e.StartedAt = e.StartedAt.Add(time.Second) },
	}
	for name, mutate := range drift {
		mutated := base
		mutate(&mutated)
		if sameDaemonIdentity(base, mutated) {
			t.Errorf("a replacement differing in %s was classified as the refused owner", name)
		}
	}
}

// TestAwaitRetiredOwnerFallsBackToThreadID is the M7 regression: a rendezvous
// entry that identifies its session only through ThreadID (SessionID empty)
// must still be opened and waited on. Without the fallback, controller.Open
// rejects the empty session identity with a non-ErrExited error and
// awaitRetiredOwner returns LifecycleUnavailable("retiring") immediately
// instead of awaiting the process exit.
func TestAwaitRetiredOwnerFallsBackToThreadID(t *testing.T) {
	entry := rendezvous.Entry{PID: 4331, ThreadID: "thread-only", StateDir: t.TempDir(), StartedAt: time.Now()}
	proc := &waitingForceStopProcess{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proc.release) }) }
	defer release()

	var openedSessionID string
	controller := forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
		openedSessionID = target.SessionID
		if target.SessionID == "" {
			// daemonprocess.controller.Open rejects an empty session identity
			// with a non-ErrExited error; the caller reports that as retiring.
			return nil, errors.New("missing or invalid daemon session identity")
		}
		if target.PID != entry.PID || !target.StartedAt.Equal(entry.StartedAt) {
			return nil, errors.New("unexpected process target")
		}
		return proc, nil
	})
	cfg := hubcore.WebConfig{DaemonProcesses: controller}
	result := make(chan error, 1)
	go func() { result <- awaitRetiredOwner(t.Context(), cfg, entry) }()
	select {
	case <-proc.entered:
		// It reached Wait: the retired owner's exit is what is awaited.
	case err := <-result:
		t.Fatalf("returned before awaiting the retired owner's exit: %v", err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("confirmed exit not accepted: %v", err)
	}
	if openedSessionID != entry.ThreadID {
		t.Fatalf("opened session identity = %q, want the ThreadID fallback %q", openedSessionID, entry.ThreadID)
	}
}

// TestAwaitRetiredOwnerOpensTheOwnerAsRetiring pins the flake behind
// TestE2E_SendPromptToARetiredSession: a committed-retiring daemon releases its
// session API log before the process exits, and the force-stop verifier treats
// a process without that log as unverified. Opened as an ordinary target, the
// owner in that window was refused and awaitRetiredOwner returned
// LifecycleUnavailable("retiring") at once, so a prompt sent during retirement
// failed instead of resuming. The owner must be opened as a Retiring target,
// whose handle confirms exit without requiring the log.
func TestAwaitRetiredOwnerOpensTheOwnerAsRetiring(t *testing.T) {
	entry := rendezvous.Entry{PID: 4332, SessionID: "retiring-session", StateDir: t.TempDir(), StartedAt: time.Now()}
	var opened daemonprocess.Target
	controller := forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
		opened = target
		return nil, daemonprocess.ErrExited
	})
	if err := awaitRetiredOwner(t.Context(), hubcore.WebConfig{DaemonProcesses: controller}, entry); err != nil {
		t.Fatalf("awaitRetiredOwner: %v", err)
	}
	if !opened.Retiring {
		t.Fatalf("opened the retiring owner as %+v; want Retiring so its released API log does not refuse the exit wait", opened)
	}
}

// TestResumeAfterConfirmedRetirementFreshReplacementIsNotAwaited is the round-7
// regression for the replacement-daemon hang. A concurrent resume can replace
// the retiring daemon before this retry samples the entry-time owner at
// app_retirement_resume.go:139, so ownerBefore and owner both resolve to the
// SAME healthy replacement. sameDaemonIdentity then matches, sameAsRefused is
// true, and the pre-fix code fell through to awaitRetiredOwner: it opened the
// live replacement's process, waited on it for the caller's whole context
// deadline, and held the session's alias locks for that entire wait. A fresh
// lifecycle probe reporting a NON-retiring phase proves the current owner is an
// active replacement already serving the session, so the retry must resolve
// against it and return without ever opening its process.
//
// The same fixture pins the branches the fix must preserve: a fresh "retiring"
// owner is still the daemon that refused the mutation and must still be awaited,
// and an unfresh lifecycle reports the capability as unknown (never as a
// replacement), so it must still be awaited.
func TestResumeAfterConfirmedRetirementFreshReplacementIsNotAwaited(t *testing.T) {
	const sessionID = "session-retirement-replacement"
	entry := rendezvous.Entry{
		PID:          7201,
		Address:      "127.0.0.1:7201",
		Endpoint:     "ws://127.0.0.1:7201/rpc",
		Protocol:     appwire.ProtocolVersion,
		SourceID:     "local",
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: "local:" + sessionID,
		InstanceID:   "replacement-instance",
		WorkingDir:   "/work/" + sessionID,
		StateDir:     "/state/" + sessionID,
		StartedAt:    time.Unix(1700001000, 0).UTC(),
	}
	// The roster snapshot is the current owner: no rendezvous scan is needed, and
	// the same entry answers both the entry-time sample and the post-lock sample,
	// exactly as it does when a concurrent resume already replaced the refuser.
	prevRefresh := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
	t.Cleanup(func() { hubRosterRefresh = prevRefresh })

	cases := []struct {
		name      string
		lifecycle *appwire.DaemonLifecycle
		fresh     bool
		wantWait  bool
	}{
		{
			name:      "fresh resident replacement resolves the retry",
			lifecycle: &appwire.DaemonLifecycle{Phase: "resident", Blockers: []appwire.DaemonBlocker{}},
			fresh:     true,
		},
		{
			name:      "fresh retiring owner is still awaited",
			lifecycle: &appwire.DaemonLifecycle{Phase: "retiring", Blockers: []appwire.DaemonBlocker{}},
			fresh:     true,
			wantWait:  true,
		},
		{
			name:      "unfresh lifecycle is not read as a replacement",
			lifecycle: &appwire.DaemonLifecycle{Phase: "resident", Blockers: []appwire.DaemonBlocker{}},
			fresh:     false,
			wantWait:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
				Entry:          entry,
				SessionID:      sessionID,
				Status:         appwire.ThreadStatusIdle,
				Lifecycle:      tc.lifecycle,
				LifecycleFresh: tc.fresh,
			})
			var events []string
			controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
				return &forceStopProcess{events: &events, waitErr: errors.New("live owner must not be awaited")}, nil
			})
			cfg := hubcore.WebConfig{
				RunDir:          t.TempDir(),
				Roster:          roster,
				ResumeLocks:     hubcore.NewResumeLocks(),
				DaemonProcesses: controller,
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := resumeAfterConfirmedRetirement(ctx, cfg, nil, appwire.TurnStartParams{Ref: "local:" + sessionID})
			if tc.wantWait {
				if len(events) == 0 {
					t.Fatalf("owner was not awaited (err=%v); a refusing owner must still be awaited", err)
				}
				if !isLifecycleRetiringError(err) {
					t.Fatalf("awaiting owner returned %v, want the retryable retiring lifecycle error", err)
				}
				return
			}
			if len(events) != 0 {
				t.Fatalf("opened the live replacement's process (%v); a fresh non-retiring owner must resolve the retry immediately", events)
			}
			if err != nil {
				t.Fatalf("resumeAfterConfirmedRetirement with a healthy replacement owner: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatalf("recovery did not return promptly: %v", ctx.Err())
			}
		})
	}
}

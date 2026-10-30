package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// The proxy holds only the first real answer-continuation response after the daemon
// produced it. Every other frame, including notifications, can pass. The query
// and response must carry the exact saved execution turns observed by the test.
type readinessContinuationGate struct {
	mu                  sync.RWMutex
	ancestor, candidate string
	entered             chan appwire.ThreadReadResponse
	cancelled           chan appwire.SessionRuntimeCancel
	release             chan struct{}
	settled             chan error
	once                sync.Once
	held                bool
}

func newReadinessContinuationGate() *readinessContinuationGate {
	return &readinessContinuationGate{entered: make(chan appwire.ThreadReadResponse, 1), cancelled: make(chan appwire.SessionRuntimeCancel, 1), release: make(chan struct{}), settled: make(chan error, 1)}
}

func (g *readinessContinuationGate) open() { g.once.Do(func() { close(g.release) }) }

func (g *readinessContinuationGate) setIDs(ancestor, candidate string) {
	g.mu.Lock()
	g.ancestor, g.candidate = ancestor, candidate
	g.mu.Unlock()
}

func (g *readinessContinuationGate) ids() (string, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ancestor, g.candidate
}

func (g *readinessContinuationGate) proxy(t *testing.T, daemon *httptest.Server, manager bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		front, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept proxy websocket: %v", err)
			return
		}
		defer front.CloseNow()
		headers := http.Header{}
		headers.Set("Authorization", r.Header.Get("Authorization"))
		back, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(daemon.URL, "http"), &websocket.DialOptions{HTTPClient: daemon.Client(), HTTPHeader: headers})
		if err != nil {
			t.Errorf("dial daemon websocket: %v", err)
			return
		}
		defer back.CloseNow()
		var pending sync.Map // AppWire request ID -> real continuation query, only for this socket.
		var invoke sync.Map  // Negative reverse request ID -> specific continuation operation.
		var forwarding sync.WaitGroup
		forward := func(src, dst *websocket.Conn, requests bool) {
			defer forwarding.Done()
			for {
				kind, frame, err := src.Read(ctx)
				if err != nil {
					cancel()
					return
				}
				var msg appwire.Message
				if err := json.Unmarshal(frame, &msg); err != nil {
					t.Errorf("proxy decoded frame: %v", err)
					cancel()
					return
				}
				ancestor, candidate := g.ids()
				if requests && msg.Request != nil && msg.Request.Method == appwire.MethodThreadRead {
					var read appwire.ThreadReadParams
					if err := json.Unmarshal(msg.Request.Params, &read); err == nil && read.AnswerContinuation != nil && read.AnswerContinuation.AncestorTurnID == ancestor && read.AnswerContinuation.CandidateTurnID == candidate {
						pending.Store(msg.IDString(), true)
					}
				}
				if manager && !requests && msg.Request != nil {
					if msg.Request.Method == appwire.MethodRuntimeSessionInvoke {
						var operation appwire.SessionRuntimeRequest
						if json.Unmarshal(msg.Request.Params, &operation) == nil && operation.Operation != nil && operation.Operation.Read != nil {
							query := operation.Operation.Read.AnswerContinuation
							if query != nil && query.AncestorTurnID == ancestor && query.CandidateTurnID == candidate {
								invoke.Store(msg.Request.ID.Int64(), operation.BindingID)
							}
						}
					}
					if msg.Request.Method == appwire.MethodRuntimeSessionCancel {
						var p appwire.SessionRuntimeCancel
						if json.Unmarshal(msg.Request.Params, &p) == nil {
							if binding, ok := invoke.LoadAndDelete(p.RequestID); ok && binding == p.BindingID {
								g.cancelled <- p
							}
						}
					}
				}
				if !requests && msg.Response != nil {
					if _, ok := pending.LoadAndDelete(msg.IDString()); ok {
						var read appwire.ThreadReadResponse
						raw, ok := msg.Response.Result.(json.RawMessage)
						if !ok || json.Unmarshal(raw, &read) != nil || read.AnswerContinuation == nil || read.AnswerContinuation.AncestorTurnID != ancestor || read.AnswerContinuation.CandidateTurnID != candidate || !read.AnswerContinuation.Matches {
							t.Errorf("daemon returned no matching real continuation verdict: %+v", read.AnswerContinuation)
							cancel()
							return
						}
						g.mu.Lock()
						hold := !g.held
						g.held = true
						g.mu.Unlock()
						if !hold {
							if err := dst.Write(ctx, kind, frame); err != nil {
								cancel()
								return
							}
							continue
						}
						g.entered <- read
						forwarding.Go(func() {
							<-g.release
							writeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second) // Teardown tripwire for an unresponsive transport peer.
							defer stop()
							g.settled <- dst.Write(writeCtx, kind, frame)
						})
						continue
					}
				}
				if err := dst.Write(ctx, kind, frame); err != nil {
					cancel()
					return
				}
			}
		}
		forwarding.Add(2)
		go forward(front, back, true)
		go forward(back, front, false)
		<-ctx.Done()
		// Only the test releases the held response: a canceled request
		// must not accidentally turn the gate into a successful reply.
		forwarding.Wait()
	}))
}

type readinessWorkerProvider struct {
	mu   sync.Mutex
	step int
}

func (*readinessWorkerProvider) Name() string { return "openai" }
func (*readinessWorkerProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p *readinessWorkerProvider) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	if len(request.Tools) == 0 { // Session naming is a separate external model call.
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "READINESS-WORKER-NAME"}}}}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.step++
	if p.step == 1 {
		return activityRelayTool("ask_user", map[string]any{"questions": []any{map[string]any{"question": "READINESS-QUESTION", "options": []any{map[string]any{"label": "Yes", "detail": "continue"}, map[string]any{"label": "No", "detail": "stop"}}}}}), nil
	}
	return activityRelayTool("communicate", map[string]any{"message": "READINESS-DONE", "end_turn": true}), nil
}

type readinessManagerProvider struct {
	ref           string
	mu            sync.Mutex
	step          int
	clearID       string
	clearConsumed bool
}

func (*readinessManagerProvider) Name() string { return "openai" }
func (*readinessManagerProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p *readinessManagerProvider) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	if len(request.Tools) == 0 {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "READINESS-MANAGER-NAME"}}}}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.step++
	if p.step == 1 {
		return activityRelayTool("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"ref": p.ref, "events": []string{"question"}, "completion": true}}), nil
	}
	if p.clearID != "" && !p.clearConsumed {
		p.clearConsumed = true
		return activityRelayTool("job_watch", map[string]any{"operation": "clear", "watch_id": p.clearID}), nil
	}
	return activityRelayTool("communicate", map[string]any{"message": "READINESS-MANAGER-REST", "end_turn": true}), nil
}

// Both daemons, sessions, mutation admission, watch journal and history belong
// to Evener. Only the external model and external WebSocket transport are scripted.
type readinessInterruptionFixture struct {
	ctx                   context.Context
	worker, manager       *agent.Session
	workerDaemon          *daemonserver.Server
	workerRef, managerRef string
	workerProvider        *readinessWorkerProvider
	managerProvider       *readinessManagerProvider
	human                 *appwire.Client
	gate                  *readinessContinuationGate
}

func newReadinessInterruptionFixture(t *testing.T) *readinessInterruptionFixture {
	t.Helper()
	// Tripwire for fixture startup, question/answer execution and the existing
	// two-second operation budget plus recovery spacing, not a pacing mechanism.
	ctx, cancel := context.WithTimeout(t.Context(), 18*time.Second)
	workerProvider, managerProvider := &readinessWorkerProvider{}, &readinessManagerProvider{}
	newSession := func(adapter llm.ProviderAdapter, manager bool) *agent.Session {
		t.Helper()
		client := llm.NewClient()
		client.Register(adapter)
		working := t.TempDir()
		session, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(working), agent.SessionConfig{StateDir: t.TempDir(), Sandbox: "off", Manager: manager})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(session.Close)
		return session
	}
	worker, manager := newSession(workerProvider, false), newSession(managerProvider, true)
	workerRef, managerRef := "local:"+worker.WorkspaceID(), "local:"+manager.WorkspaceID()
	managerProvider.ref = workerRef
	gate := newReadinessContinuationGate()
	makeDaemon := func(session *agent.Session, ref string, manager bool) (*daemonserver.Server, *httptest.Server) {
		t.Helper()
		daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
		t.Cleanup(daemon.Close)
		identity, err := daemonserver.PrepareAppIdentityForRef("local", session.ID(), ref, session.TranscriptPath())
		if err != nil {
			t.Fatal(err)
		}
		daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@readiness"), nil)
		daemon.UpdateSessionInfo(session.ID(), "", "")
		daemon.SetState("idle")
		daemon.SetSessionTools(func() *agent.Session { return session })
		daemon.SetThreadWatchesListFunc(session.ListActivityWatches)
		daemon.WireTranscriptHistory(session)
		session.ConsumeEventsLossless(func(ev events.SessionEvent) { daemonserver.BridgeEvent(daemon, ev, nil) }, func() {})
		if !manager {
			daemon.SetRetrySafeTurnFunctions(daemonserver.RetrySafeTurnFunctions{Start: session.AcceptClientMutationStart, StartAs: session.AcceptClientMutationStartAs})
		}
		daemon.SetNotesHumanSetFunc(session.SetHumanNote)
		daemon.SetUrlsRemoveFunc(session.RemoveSessionURL)
		httpServer := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
		t.Cleanup(httpServer.Close)
		return daemon, httpServer
	}
	workerDaemon, workerHTTP := makeDaemon(worker, workerRef, false)
	_, managerHTTP := makeDaemon(manager, managerRef, true)
	workerProxy := gate.proxy(t, workerHTTP, false)
	t.Cleanup(workerProxy.Close)
	managerProxy := gate.proxy(t, managerHTTP, true)
	t.Cleanup(managerProxy.Close)
	// LIFO cleanup must release any held frame before either proxy closes.
	t.Cleanup(gate.open)
	entries := []appsource.LocalDaemonEntry{}
	for _, current := range []struct {
		session  *agent.Session
		ref, url string
		manager  bool
	}{{worker, workerRef, workerProxy.URL, false}, {manager, managerRef, managerProxy.URL, true}} {
		entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: current.url, SourceID: "local", ThreadID: current.session.ID(), SessionID: current.session.ID(), WorkspaceRef: current.ref, InstanceID: current.session.ID(), HubToken: "test-token"}
		entries = append(entries, appsource.LocalDaemonEntry{Entry: entry, SessionID: current.session.ID(), Manager: current.manager})
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry { return entries }, workerHTTP.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test", Roster: hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: entries[0].Entry, SessionID: worker.ID(), Status: "active"}, hubcore.LiveEntry{Entry: entries[1].Entry, SessionID: manager.ID(), Status: "active", Manager: true})}
	web := NewWebServer(cfg)
	hub := newHubAppServerWithNavigation(cfg, sources, web.navigation, web.resolveTopLevelSessionRef)
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second) // Teardown tripwire independent of test's expired operation context.
		defer done()
		if err := hub.Shutdown(shutdown); err != nil {
			t.Errorf("hub shutdown: %v", err)
		}
	})
	hubHTTP := httptest.NewServer(http.HandlerFunc(hub.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	var workers sync.WaitGroup
	life, stop := context.WithCancel(ctx)
	startSessionTools(life, sources, func(f func()) { workers.Go(f) })
	t.Cleanup(func() { stop(); workers.Wait(); cancel() })
	human := dialHubRPC(t, hubHTTP)
	t.Cleanup(func() { _ = human.Close() })
	if _, err := human.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := human.ThreadRead(ctx, appwire.ThreadReadParams{Ref: workerRef, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	return &readinessInterruptionFixture{ctx: ctx, worker: worker, manager: manager, workerDaemon: workerDaemon, workerRef: workerRef, managerRef: managerRef, workerProvider: workerProvider, managerProvider: managerProvider, human: human, gate: gate}
}

func (f *readinessInterruptionFixture) prepare(t *testing.T) string {
	t.Helper()
	if _, err := f.worker.ProcessInput(f.ctx, "READINESS-WORKER", nil); err != nil {
		t.Fatalf("question turn: %v", err)
	}
	if f.worker.PendingQuestion() == nil {
		t.Fatalf("real worker did not ask a question: provider steps=%d session state=%s", f.workerProvider.step, f.worker.State())
	}
	if _, err := f.manager.ProcessInput(f.ctx, "READINESS-WATCH", nil); err != nil {
		t.Fatalf("manager registration turn: %v", err)
	}
	watches, err := f.human.ThreadWatchesList(f.ctx, appwire.SessionActivityListParams{Ref: f.managerRef})
	if err != nil {
		t.Fatal(err)
	}
	watchID := ""
	for _, row := range watches.Watches {
		if row.State == appwire.SessionWatchStateArmed && row.Watch.Target == f.workerRef {
			watchID = row.Watch.ID
		}
	}
	if watchID == "" {
		t.Fatalf("no armed real Manager watch: %+v", watches.Watches)
	}
	// A real human turn/start admits the answer into the worker's mutation
	// journal; ProcessClientMutationStart executes that exact accepted input.
	return watchID
}

func (f *readinessInterruptionFixture) answer(t *testing.T) (string, string) {
	t.Helper()
	turns, err := readTranscriptTurns(f.ctx, f.human, f.workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	question := ""
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.ToolName == "ask_user" {
				question = turn.ID
			}
		}
	}
	if question == "" {
		t.Fatalf("question turn missing from saved history: %+v", turns)
	}
	answer, err := f.human.TurnStart(f.ctx, appwire.TurnStartParams{Ref: f.workerRef, ClientMutationID: newMutationID(t), ExpectedInstanceID: f.worker.ID(), Input: []appwire.InputItem{{Type: "text", Text: "READINESS-ANSWER"}}})
	if err != nil || answer.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("real answer admission=%+v err=%v", answer, err)
	}
	f.gate.setIDs(question, answer.Receipt.TurnID)
	if _, ran, err := f.worker.ProcessClientMutationStart(f.ctx, nil); err != nil || !ran {
		t.Fatalf("real admitted answer execution ran=%v err=%v", ran, err)
	}
	turns, err = readTranscriptTurns(f.ctx, f.human, f.workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	savedAnswer := false
	for _, turn := range turns {
		if turn.ID != answer.Receipt.TurnID {
			continue
		}
		for _, item := range turn.Items {
			if item.Text == "READINESS-ANSWER" {
				savedAnswer = true
			}
		}
	}
	if !savedAnswer || question == answer.Receipt.TurnID {
		t.Fatalf("admitted answer not saved as separate execution turn: question=%s receipt=%s", question, answer.Receipt.TurnID)
	}
	if !workerTerminalAccepted(turns, "READINESS-DONE") {
		t.Fatal("worker has no real accepted terminal result")
	}
	return question, answer.Receipt.TurnID
}

func (f *readinessInterruptionFixture) watch(t *testing.T, id string) appwire.SessionWatchesResponse {
	t.Helper()
	watches, err := f.human.ThreadWatchesList(f.ctx, appwire.SessionActivityListParams{Ref: f.managerRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range watches.Watches {
		if row.Watch.ID == id && row.Watch.Target != f.workerRef {
			t.Fatalf("watch target changed: %+v", row)
		}
	}
	return watches
}

func (f *readinessInterruptionFixture) awaitGate(t *testing.T) {
	t.Helper()
	select {
	case read := <-f.gate.entered:
		if read.AnswerContinuation == nil || !read.AnswerContinuation.Matches || read.Thread.Evener.Ref != f.workerRef || read.Thread.Evener.InstanceID != f.worker.ID() || read.BootGeneration != "7@readiness" {
			t.Fatalf("daemon continuation verdict or bound generation: verdict=%+v ref=%s instance=%s boot=%s", read.AnswerContinuation, read.Thread.Evener.Ref, read.Thread.Evener.InstanceID, read.BootGeneration)
		}
	case <-f.ctx.Done():
		t.Fatalf("real continuation response never entered gate: %v", f.ctx.Err())
	}
}

func (f *readinessInterruptionFixture) awaitEnded(t *testing.T, id string) appwire.SessionWatch {
	t.Helper()
	for {
		for _, row := range f.watch(t, id).Watches {
			if row.Watch.ID == id && row.State == appwire.SessionWatchStateEnded && !row.Watch.Active {
				if row.OwnerRef != f.managerRef || row.SourceRef != f.managerRef || row.Watch.Target != f.workerRef {
					t.Fatalf("terminal watch changed owner or target: %+v", row)
				}
				return row
			}
		}
		select {
		case <-f.ctx.Done():
			t.Fatalf("real watch %s did not end: %v", id, f.ctx.Err())
		case <-time.After(10 * time.Millisecond): // State API has no settlement notification channel.
		}
	}
}

func (f *readinessInterruptionFixture) awaitCancelFrame(t *testing.T) {
	t.Helper()
	select {
	case cancel := <-f.gate.cancelled:
		if cancel.BindingID == "" || cancel.RequestID >= 0 {
			t.Fatalf("invalid cancellation of correlated reverse request: %+v", cancel)
		}
	case <-f.ctx.Done():
		t.Fatalf("in-flight continuation reverse request was not canceled: %v", f.ctx.Err())
	}
}

func (f *readinessInterruptionFixture) lateAndHuman(t *testing.T, id string, reason string) {
	t.Helper()
	f.gate.open()
	select {
	case <-f.gate.settled: // The real late frame may be rejected or ignored after cancellation.
	case <-f.ctx.Done():
		t.Fatalf("late real transport frame did not settle: %v", f.ctx.Err())
	}
	row := f.awaitEnded(t, id)
	if row.Watch.EndReason != reason || (reason != "completed" && acceptanceWatchCompleted(f.watch(t, id), id)) {
		t.Fatalf("late response resurrected or completed watch: %+v", row)
	}
	// A real human note mutation produces a content-bearing, NEW target update.
	// An older buffered frame cannot carry this note.
	const note = "READINESS-HUMAN-AFTER"
	changed, err := f.human.NotesHumanSet(f.ctx, appwire.NotesHumanSetParams{Ref: f.workerRef, ClientMutationID: newMutationID(t), ExpectedInstanceID: f.worker.ID(), Note: note})
	if err != nil || changed.Receipt.Disposition != appwire.MutationDispositionApplied || changed.Note != note {
		t.Fatalf("real later target note mutation=%+v err=%v", changed, err)
	}
	for {
		select {
		case notification := <-f.human.Notifications():
			if notification.Method == appwire.NotifyEvenerNotesUpdated {
				var update appwire.NotesUpdatedParams
				if err := json.Unmarshal(notification.Params, &update); err != nil {
					t.Fatal(err)
				}
				if update.Ref == f.workerRef && update.HumanNote == note {
					read, err := f.human.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: f.workerRef})
					if err != nil {
						t.Fatal(err)
					}
					if read.Thread.Evener.HumanNote != note {
						t.Fatalf("human read after notification lacks committed note: %q", read.Thread.Evener.HumanNote)
					}
					if latest := f.awaitEnded(t, id); latest.Watch.EndReason != reason {
						t.Fatalf("late completion changed durable watch: %+v", latest)
					}
					return
				}
			}
		case <-f.ctx.Done():
			t.Fatalf("human lost live subscribed update or read: %v", f.ctx.Err())
		}
	}
}

func TestManagerReadinessInterruptionCallerCancel(t *testing.T) {
	f := newReadinessInterruptionFixture(t)
	id := f.prepare(t)
	f.answer(t)
	f.awaitGate(t)
	f.managerProvider.mu.Lock()
	f.managerProvider.clearID = id
	f.managerProvider.mu.Unlock()
	if _, err := f.manager.ProcessInput(f.ctx, "READINESS-CLEAR", nil); err != nil {
		t.Fatalf("clear via real Manager tool: %v", err)
	}
	f.awaitCancelFrame(t)
	row := f.awaitEnded(t, id)
	if row.Watch.EndReason != "cleared" {
		t.Fatalf("watch clear did not cancel intended observation: %+v", row)
	}
	f.lateAndHuman(t, id, row.Watch.EndReason)
}

func TestManagerReadinessInterruptionDeadline(t *testing.T) {
	f := newReadinessInterruptionFixture(t)
	id := f.prepare(t)
	// The answer can trigger the lookup before its response reaches the gate.
	// Start before admission so the lower bound includes the full read budget.
	started := time.Now()
	f.answer(t)
	f.awaitGate(t)
	f.awaitCancelFrame(t)
	row := f.awaitEnded(t, id)
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Fatalf("existing two-second operation budget did not expire: %v", elapsed)
	}
	// Only the first real response is held. The original watch must retry the
	// same saved-answer verdict after correlated cancellation, not re-register.
	if row.Watch.EndReason != "completed" || !acceptanceWatchCompleted(f.watch(t, id), id) {
		t.Fatalf("deadline lost original completion watch: %+v", row)
	}
	f.lateAndHuman(t, id, row.Watch.EndReason)
}

func TestManagerReadinessInterruptionHealthyControl(t *testing.T) {
	f := newReadinessInterruptionFixture(t)
	watchID := f.prepare(t)
	question, answer := f.answer(t)
	ancestor, candidate := f.gate.ids()
	if ancestor != question || candidate != answer {
		t.Fatalf("correlated request=%s/%s saved turns=%s/%s", ancestor, candidate, question, answer)
	}
	select {
	case read := <-f.gate.entered:
		if read.AnswerContinuation == nil || !read.AnswerContinuation.Matches {
			t.Fatalf("real continuation verdict: %+v", read.AnswerContinuation)
		}
	case <-f.ctx.Done():
		t.Fatalf("continuation response never entered gate: %v", f.ctx.Err())
	}
	f.gate.open()
	select {
	case err := <-f.gate.settled:
		if err != nil {
			t.Fatalf("healthy real response transport: %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("healthy response transport did not settle")
	}
	for !acceptanceWatchCompleted(f.watch(t, watchID), watchID) {
		select {
		case <-f.ctx.Done():
			t.Fatal("healthy answer completion did not end watch")
		case <-time.After(10 * time.Millisecond): // No public watch-settlement channel; bounded state-read tripwire.
		}
	}
}

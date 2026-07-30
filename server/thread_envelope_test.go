package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

// feedBridge runs the real Bridge over evs and returns when it has drained
// them. Tests use it rather than calling RecordAppEvent directly whenever the
// thing under test is the envelope, because the refresh lives in the bridge:
// RecordAppEvent alone would prove nothing about freshness.
func feedBridge(srv *Server, evs ...events.SessionEvent) {
	ch := make(chan events.SessionEvent, len(evs))
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	Bridge(srv, ch)
}

// readThreadOverWire performs a thread/read through a real connection and
// returns the wire response. Every envelope assertion goes through here rather
// than calling appThread() directly: a field dropped between the envelope and
// the wire is exactly the defect an internal round trip cannot see.
func readThreadOverWire(t *testing.T, srv *Server, ref string) appwire.Thread {
	t.Helper()
	conn := srv.AppServer().NewConnection("envelope-test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("initialize: %v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("thread/read: %v", resp.Kind())
	}
	read, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read result type = %T", resp.Response.Result)
	}
	return read.Thread
}

// TestThreadEnvelopeFacetsRefreshOnTheEventsThatMoveThem is the freshness
// contract for facetsByEvent, asserted one producer at a time against the wire.
//
// Each case moves a value in the session source AFTER the envelope was seeded,
// drives the real event that announces it through the real Bridge, and reads the
// thread back over a real connection. Delete that event's row from
// facetsByEvent and the case fails with the value the session moved to still
// absent from the wire, which is precisely the permanent staleness a pushed
// envelope risks.
func TestThreadEnvelopeFacetsRefreshOnTheEventsThatMoveThem(t *testing.T) {
	measured := 7
	for _, tc := range []struct {
		name  string
		move  func(*stubThreadEnvelopeSource)
		event events.SessionEvent
		want  func(*testing.T, appwire.Thread)
	}{
		{
			name: "queue on QUEUE_CHANGED",
			move: func(e *stubThreadEnvelopeSource) {
				e.queue = appwire.QueueState{Depth: 2, Revision: 5, Preview: []string{"alpha", "bravo"}}
			},
			event: events.SessionEvent{Kind: events.EventQueueChanged, SessionID: "th_1", Data: events.QueueChangedData{Depth: 2}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.Queue.Depth != 2 || thread.Serf.Queue.Revision != 5 {
					t.Fatalf("queue = %+v, want the depth/revision the session moved to", thread.Serf.Queue)
				}
			},
		},
		{
			name: "tasks on TASK_UPDATED",
			move: func(e *stubThreadEnvelopeSource) {
				e.tasks = &appwire.TaskAggregate{Total: 4, Done: 3}
			},
			event: events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "th_1", Data: events.TaskUpdatedData{Total: 4, Done: 3}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.Tasks == nil || thread.Serf.Tasks.Total != 4 || thread.Serf.Tasks.Done != 3 {
					t.Fatalf("tasks = %+v, want 4/3", thread.Serf.Tasks)
				}
			},
		},
		{
			name: "escalations on SANDBOX_ESCALATION_REQUESTED",
			move: func(e *stubThreadEnvelopeSource) {
				e.escalations = []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", DeniedPath: "/x"}}
			},
			event: events.SessionEvent{
				Kind: events.EventSandboxEscalationRequested, SessionID: "th_1",
				Data: events.SandboxEscalationRequestedData{EscalationID: "esc_1", DeniedPath: "/x"},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				got := thread.Serf.PendingEscalations
				if len(got) != 1 || got[0].EscalationID != "esc_1" {
					t.Fatalf("pendingEscalations = %+v, want the blocked card", got)
				}
				// Stamped at the write, so the read hands out the stored slice
				// without touching it.
				if got[0].ThreadID != "th_1" || got[0].Ref != "local:th_1" {
					t.Fatalf("escalation card = %+v, want this thread's id and ref", got[0])
				}
			},
		},
		{
			name: "name on SESSION_NAME_CHANGED",
			move: func(e *stubThreadEnvelopeSource) {
				e.meta = schema.SessionMeta{ID: "th_1", Name: "Fix the envelope", NameSource: "prompt"}
			},
			event: events.SessionEvent{
				Kind: events.EventSessionNameChanged, SessionID: "th_1",
				Data: events.SessionNameChangedData{Name: "Fix the envelope"},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Name != "Fix the envelope" {
					t.Fatalf("thread.Name = %q, want the renamed session's name", thread.Name)
				}
			},
		},
		{
			name: "reasoning on MODEL_CHANGED",
			move: func(e *stubThreadEnvelopeSource) {
				e.reasoningEffort = "high"
				e.reasoningLevels = []string{"low", "high"}
				e.supportsReason = true
			},
			event: events.SessionEvent{
				Kind: events.EventModelChanged, SessionID: "th_1",
				Data: events.ModelChangedData{NewModel: "m2", SupportsReasoning: true},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.ReasoningEffort != "high" || !thread.Serf.SupportsReasoning {
					t.Fatalf("reasoning = (%q, %v), want the new profile's settings",
						thread.Serf.ReasoningEffort, thread.Serf.SupportsReasoning)
				}
			},
		},
		{
			name: "reasoning effort on REASONING_EFFORT_CHANGED",
			move: func(e *stubThreadEnvelopeSource) { e.reasoningEffort = "minimal" },
			event: events.SessionEvent{
				Kind: events.EventReasoningEffortChanged, SessionID: "th_1",
				Data: events.ReasoningEffortChangedData{ReasoningEffort: "minimal"},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.ReasoningEffort != "minimal" {
					t.Fatalf("reasoningEffort = %q, want minimal", thread.Serf.ReasoningEffort)
				}
			},
		},
		{
			name: "goal on GOAL_ENDED",
			move: func(e *stubThreadEnvelopeSource) {
				e.goalStatus, e.goalIterations, e.goalSet = "completed", 3, true
			},
			event: events.SessionEvent{Kind: events.EventGoalEnded, SessionID: "th_1", Data: events.GoalEndedData{}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.Goal == nil || thread.Serf.Goal.Status != "completed" || thread.Serf.Goal.Iterations != 3 {
					t.Fatalf("goal = %+v, want completed/3", thread.Serf.Goal)
				}
			},
		},
		{
			name: "failure count on TOOL_CALL_END",
			move: func(e *stubThreadEnvelopeSource) {
				e.failedToolCalls, e.failuresMeasured = measured, true
			},
			event: events.SessionEvent{
				Kind: events.EventToolCallEnd, SessionID: "th_1",
				Data: events.ToolCallEndData{CallID: "call_1", ToolName: "shell"},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.FailedToolCalls == nil || *thread.Serf.FailedToolCalls != measured {
					t.Fatalf("failedToolCalls = %v, want %d", thread.Serf.FailedToolCalls, measured)
				}
			},
		},
		{
			name:  "diagnostics on JOB_STARTED",
			move:  func(e *stubThreadEnvelopeSource) { e.detailedStatus = DetailedStatus{Agents: []string{"scout"}} },
			event: events.SessionEvent{Kind: events.EventJobStarted, SessionID: "th_1", Data: events.JobStartedData{JobID: "job_1"}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.Diagnostics == nil || len(thread.Serf.Diagnostics.Agents) != 1 {
					t.Fatalf("diagnostics = %+v, want the job-bearing status", thread.Serf.Diagnostics)
				}
			},
		},
		{
			name: "context on ASSISTANT_TEXT_END",
			move: func(e *stubThreadEnvelopeSource) {
				e.contextPressure = 0.75
				e.contextMetrics = ContextMetrics{Used: 75, Window: 100, Remaining: 25}
			},
			event: events.SessionEvent{
				Kind: events.EventAssistantTextEnd, SessionID: "th_1",
				Data: events.AssistantTextEndData{Text: "done"},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.ContextPressure != 0.75 || thread.Serf.ContextUsed != 75 {
					t.Fatalf("context = (%v, %d), want the post-response figures",
						thread.Serf.ContextPressure, thread.Serf.ContextUsed)
				}
			},
		},
		{
			name: "work metrics on TURN_ENDED",
			move: func(e *stubThreadEnvelopeSource) {
				e.workMillis = 9000
				e.usage = &appwire.SerfUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
			},
			event: events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_1", Data: events.TurnEndedData{}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Serf.WorkMillis != 9000 || thread.Serf.Usage == nil || thread.Serf.Usage.TotalTokens != 30 {
					t.Fatalf("work = (%d, %+v), want the turn's accumulated figures",
						thread.Serf.WorkMillis, thread.Serf.Usage)
				}
			},
		},
		{
			name:  "ask pending on STEERING_INJECTED",
			move:  func(e *stubThreadEnvelopeSource) { e.askPending = true },
			event: events.SessionEvent{Kind: events.EventSteeringInjected, SessionID: "th_1", Data: events.SteeringInjectedData{Text: "answer"}},
			want: func(t *testing.T, thread appwire.Thread) {
				if !thread.Serf.AskPending {
					t.Fatal("askPending = false, want true once the session is blocked on a question")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_1")
			src := publishEnvelope(srv, &stubThreadEnvelopeSource{})

			// The session moves its state, then emits the event announcing it.
			// Nothing has told the daemon yet.
			tc.move(src)
			feedBridge(srv, tc.event)

			tc.want(t, readThreadOverWire(t, srv, "local:th_1"))
		})
	}
}

// TestThreadReadUnderTheCutReachesNoSession is the structural claim, asserted
// behaviorally: a subscribing thread/read touches the materialized envelope and
// nothing else.
//
// It matters because that read runs inside CaptureSubscription, holding
// projectionMu AND deliveryMu. Any call into the session from there blocks the
// event bridge, every projection commit and every other connection's capture --
// and four of the sixteen callbacks this replaced could block behind a
// transcript fsync, a jobs.jsonl read, a task-store save, or the session mutex
// SetModel holds across rendering the system prompt.
//
// The counting source is the production seam, so this measures the real thing
// rather than a stand-in for it.
func TestThreadReadUnderTheCutReachesNoSession(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	counter := &countingThreadEnvelopeSource{}
	srv.SetThreadEnvelopeSource(counter)
	srv.RefreshThreadEnvelope()

	seeded := counter.calls()
	if seeded == 0 {
		t.Fatal("seeding the envelope called nothing on the source; the fixture proves nothing")
	}

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test transport teardown
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:th_1", Subscribe: true, IncludeTurns: true, TurnLimit: 40,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	if got := counter.calls() - seeded; got != 0 {
		t.Fatalf("a subscribing thread/read made %d calls into the session while holding the projection gate, want 0", got)
	}
}

// TestReplacingTheIdentityReplacesTheEnvelope pins the invariant that no
// envelope value can outlive the session it described.
//
// The old thread's figures reaching the new thread's snapshot is a torn
// identity: a client that reduced thread/started and read back would attribute
// the retired session's failures, queue and title to its replacement, with
// nothing afterwards to correct it. Asserted on the wire, over a real
// connection, because that is where the harm lands.
func TestReplacingTheIdentityReplacesTheEnvelope(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	failures := 9
	publishEnvelope(srv, &stubThreadEnvelopeSource{
		queue:            appwire.QueueState{Depth: 3, Revision: 2},
		failedToolCalls:  failures,
		failuresMeasured: true,
		meta:             schema.SessionMeta{ID: "th_1", Name: "the retired session"},
		escalations:      []appwire.SandboxEscalationRequested{{EscalationID: "esc_old"}},
	})
	before := readThreadOverWire(t, srv, "local:th_1")
	if before.Name != "the retired session" || before.Serf.Queue.Depth != 3 {
		t.Fatalf("fixture did not publish the outgoing session's envelope: %+v", before.Serf)
	}

	// The daemon's session is replaced. Nothing has published the replacement's
	// state yet, which is the window this guards.
	srv.SetAppIdentity("local", "th_2")

	after := readThreadOverWire(t, srv, "local:th_2")
	if after.Name != "" {
		t.Fatalf("thread.Name = %q on the replacement thread, want the retired session's title gone", after.Name)
	}
	if after.Serf.Queue.Depth != 0 || after.Serf.Queue.Revision != 0 {
		t.Fatalf("queue = %+v on the replacement thread, want the retired session's queue gone", after.Serf.Queue)
	}
	if after.Serf.FailedToolCalls != nil {
		t.Fatalf("failedToolCalls = %d on the replacement thread, want absent: nobody has counted this session",
			*after.Serf.FailedToolCalls)
	}
	if len(after.Serf.PendingEscalations) != 0 {
		t.Fatalf("pendingEscalations = %+v on the replacement thread, want the retired session's cards gone",
			after.Serf.PendingEscalations)
	}
}

// TestEnvelopeCommittedBeforeTheCutIsInTheResponse is THE TRAP, asserted for the
// envelope rather than the turns.
//
// A subscribing read installs its cut at the sequence current when it captures,
// and Subscriptions.Release discards everything at or below it. So a change
// committed while the read waits at the projection gate reaches the client by
// exactly one route: the response body. Sample the envelope before entering
// CaptureSubscription -- the fix two reviewers proposed -- and that change is in
// neither the response nor the delivered stream, permanently.
//
// The read is parked at the gate, a real bridge pass runs to completion while it
// waits, and the assertion reads the response. Nothing consults the notifier.
func TestEnvelopeCommittedBeforeTheCutIsInTheResponse(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{})

	atGate := make(chan struct{})
	openGate := make(chan struct{})
	var once sync.Once
	srv.AppServer().SetBeforeSubscriptionGate(func() {
		once.Do(func() {
			close(atGate)
			<-openGate
		})
	})

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test transport teardown
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	type outcome struct {
		response appwire.ThreadReadResponse
		err      error
	}
	reads := make(chan outcome, 1)
	go func() {
		response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
			Ref: "local:th_1", Subscribe: true, IncludeTurns: true, TurnLimit: 40,
		})
		reads <- outcome{response: response, err: err}
	}()
	<-atGate

	// A tool call fails while the read waits for the gate. The session writes
	// its count, then emits the event; the bridge refreshes and commits. This
	// runs to completion here: the read holds no lock at the gate barrier.
	src.failedToolCalls, src.failuresMeasured = 5, true
	src.queue = appwire.QueueState{Depth: 1, Revision: 8}
	feedBridge(srv, events.SessionEvent{
		Kind: events.EventToolCallEnd, SessionID: "th_1",
		Data: events.ToolCallEndData{CallID: "call_1", ToolName: "shell", Error: "boom"},
	})
	close(openGate)

	got := <-reads
	if got.err != nil {
		t.Fatalf("ThreadRead: %v", got.err)
	}
	// The response is on the cut's side of that commit. An envelope sampled
	// before the gate would report no failures while the notification that
	// announced them is discarded as pre-cut, leaving the pane permanently
	// wrong with nothing left to correct it.
	if got.response.Thread.Serf.FailedToolCalls == nil {
		t.Fatal("response carried no failure count: the envelope is not on the cut's side of the commit")
	}
	if n := *got.response.Thread.Serf.FailedToolCalls; n != 5 {
		t.Fatalf("response failedToolCalls = %d, want 5: the envelope was sampled before the commit", n)
	}
}

// countingThreadEnvelopeSource counts every call made into it. It reports the
// zero value for everything: what is under test is whether it is called at all.
type countingThreadEnvelopeSource struct {
	mu sync.Mutex
	n  int
}

func (c *countingThreadEnvelopeSource) hit() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *countingThreadEnvelopeSource) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *countingThreadEnvelopeSource) ContextPressure() float64 { c.hit(); return 0 }
func (c *countingThreadEnvelopeSource) ContextMetrics() ContextMetrics {
	c.hit()
	return ContextMetrics{}
}
func (c *countingThreadEnvelopeSource) DetailedStatus() DetailedStatus {
	c.hit()
	return DetailedStatus{}
}
func (c *countingThreadEnvelopeSource) TaskAggregate() *appwire.TaskAggregate { c.hit(); return nil }
func (c *countingThreadEnvelopeSource) AskPending() bool                      { c.hit(); return false }
func (c *countingThreadEnvelopeSource) SessionMeta() schema.SessionMeta {
	c.hit()
	return schema.SessionMeta{}
}
func (c *countingThreadEnvelopeSource) GoalStatus() (string, int, bool) { c.hit(); return "", 0, false }
func (c *countingThreadEnvelopeSource) FailedToolCalls() (int, bool)    { c.hit(); return 0, false }
func (c *countingThreadEnvelopeSource) ReasoningInfo() (string, []string, bool) {
	c.hit()
	return "", nil, false
}
func (c *countingThreadEnvelopeSource) WorkMetrics() (int64, *appwire.SerfUsage, int64) {
	c.hit()
	return 0, nil, 0
}
func (c *countingThreadEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	c.hit()
	return nil
}
func (c *countingThreadEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	c.hit()
	return appwire.QueueState{}, nil
}

// TestClearingTheGoalClearsItOnTheWire pins the one state change that reaches
// the daemon through a handler rather than through the session's event stream.
//
// goal/set with an empty objective is the documented clear path, and the web
// palette offers it as one ("objective... (empty to clear)"). It runs
// Session.ClearGoal -> goal.Store.Clear, which nils the goal and emits nothing:
// the goal store has no event handle at all. Nothing in facetsByEvent can
// therefore observe a clear, so without a refresh at the handler a cleared goal
// stays on every thread/read for the life of the identity, and the status bar
// keeps reporting an objective the user explicitly abandoned.
//
// Setting a goal is silent for the same reason (SetGoal returns without an event
// whenever a turn is running, no kick is wired, or an ask is pending), so the
// refresh covers both directions of the one handler.
func TestClearingTheGoalClearsItOnTheWire(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{
		goalStatus: "active", goalIterations: 2, goalSet: true,
	})
	// The daemon's goal callback is the session's; here it stands in for
	// ClearGoal/SetGoal, which both mutate the store and emit nothing.
	srv.SetGoalFunc(func(objective string) (bool, error) {
		if objective == "" {
			src.goalStatus, src.goalIterations, src.goalSet = "", 0, false
			return false, nil
		}
		src.goalStatus, src.goalIterations, src.goalSet = "active", 0, true
		return true, nil
	})

	if got := readThreadOverWire(t, srv, "local:th_1").Serf.Goal; got == nil || got.Status != "active" {
		t.Fatalf("fixture did not publish a goal to clear: %+v", got)
	}

	conn := srv.AppServer().NewConnection("goal-clear")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(2), appwire.MethodGoalSet, appwire.GoalSetParams{Objective: ""}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("goal/set: %v", resp.Kind())
	}

	// Traffic keeps flowing afterwards. None of these events samples facetGoal,
	// which is what makes the staleness permanent rather than transient.
	//
	// The events are chosen deliberately: a turn boundary WOULD rescue the goal,
	// because TURN_ENDED re-reads every facet. But clearing a goal is something
	// you do when you have stopped, and an idle session produces no turn
	// boundary at all -- so leaning on one here would prove the handler refresh
	// unnecessary when it is exactly what that session depends on.
	feedBridge(srv,
		events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"},
		events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "x"}},
		events.SessionEvent{Kind: events.EventQueueChanged, SessionID: "th_1", Data: events.QueueChangedData{}},
	)

	if got := readThreadOverWire(t, srv, "local:th_1").Serf.Goal; got != nil {
		t.Fatalf("thread/read still carries goal %+v after goal/set cleared it", got)
	}
}

// TestSampleLandingAfterAnIdentityReplacementIsDropped pins the window this
// change opened.
//
// refreshFacets deliberately samples OUTSIDE s.mu, because a sample can read
// jobs.jsonl or take the session's mutex and holding s.mu across that would put
// the I/O back under a lock the read path takes. It also takes no projection
// lock, so unlike the pull it replaced it is no longer mutually excluded from
// ReplaceAppIdentity's commit. In production that race is not theoretical: the
// identity commit for /clear runs while the OLD session's bridge is still
// draining, and only closes it afterwards.
//
// A sample that started before the replacement therefore describes the retired
// session. Storing it would publish the retired session's title, queue, failure
// count and escalation cards under the replacement's identity, with nothing
// afterwards to correct it. The ordering here is a real happens-before: the
// sample is parked in its last facet while the replacement runs to completion.
func TestSampleLandingAfterAnIdentityReplacementIsDropped(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	src := &stubThreadEnvelopeSource{
		queue:            appwire.QueueState{Depth: 3, Revision: 2},
		failedToolCalls:  9,
		failuresMeasured: true,
		meta:             schema.SessionMeta{ID: "th_1", Name: "the retired session"},
		escalations:      []appwire.SandboxEscalationRequested{{EscalationID: "esc_old"}},
	}
	srv.SetThreadEnvelopeSource(src)
	src.parkOnMeta = func() {
		once.Do(func() {
			close(parked)
			<-release
		})
	}

	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		srv.RefreshThreadEnvelope()
	}()
	<-parked

	// The daemon's session is replaced while that sample is in flight.
	srv.SetAppIdentity("local", "th_2")
	close(release)
	<-sampled

	thread := readThreadOverWire(t, srv, "local:th_2")
	if thread.Name != "" {
		t.Fatalf("thread.Name = %q on th_2: a sample of the retired session landed after the replacement", thread.Name)
	}
	if thread.Serf.Queue.Depth != 0 {
		t.Fatalf("queue = %+v on th_2, want the retired session's queue dropped", thread.Serf.Queue)
	}
	if thread.Serf.FailedToolCalls != nil {
		t.Fatalf("failedToolCalls = %d on th_2, want absent: that count belongs to th_1", *thread.Serf.FailedToolCalls)
	}
	if len(thread.Serf.PendingEscalations) != 0 {
		t.Fatalf("pendingEscalations = %+v on th_2, want th_1's cards dropped", thread.Serf.PendingEscalations)
	}
}

// TestTurnStartPublishesItsPendingMutation pins the other change point that
// reaches the daemon through a handler rather than an event.
//
// The six sibling mutation handlers all reflect the durable queue, which emits
// QUEUE_CHANGED. turn/start does not: it records a pending execution and emits
// nothing until the serve loop later CLAIMS it. A client reading in between
// would not see its own in-flight mutation, which is the single thing the
// retry-safe projection exists to show a reconnecting client.
func TestTurnStartPublishesItsPendingMutation(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Start: func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			// Stands in for AcceptClientMutationStart: the durable intent is
			// recorded, and nothing is emitted.
			src.pendingMutations = []appwire.PendingMutation{{
				ClientMutationID: params.ClientMutationID,
				Method:           "turn/start",
				ExecutionState:   "accepted",
			}}
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
		},
	})

	conn := srv.AppServer().NewConnection("turn-start")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
			Ref:              "local:th_1",
			ClientMutationID: "cm_1",
			Input:            []appwire.InputItem{{Type: "text", Text: "go"}},
		}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("turn/start: %v", resp.Kind())
	}

	got := readThreadOverWire(t, srv, "local:th_1").Serf.PendingMutations
	if len(got) != 1 || got[0].ClientMutationID != "cm_1" {
		t.Fatalf("pendingMutations = %+v after turn/start, want the accepted intent", got)
	}
}

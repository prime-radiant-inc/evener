package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
			// A turn opening moves ActiveTurnStartedAt, and nothing else
			// announces it: without this row thread/read reports an active
			// thread whose turn started at zero for the notification turn's
			// whole first round.
			name: "work metrics on TURN_STARTED",
			move: func(e *stubThreadEnvelopeSource) {
				e.workMillis = 4200
				e.turnStartedAt = 1750000000
			},
			event: events.SessionEvent{Kind: events.EventTurnStarted, SessionID: "th_1", Data: events.TurnStartedData{TurnID: "turn_m4"}},
			want: func(t *testing.T, thread appwire.Thread) {
				// ActiveTurnStartedAt is the field the facetWork row exists for
				// — it is what a turn OPENING moves, and nothing else announces
				// it. Assert it directly rather than inferring it from a
				// sibling in the same facet.
				if thread.Evener.ActiveTurnStartedAt != 1750000000 {
					t.Fatalf("activeTurnStartedAt = %d, want the moment the session recorded; an active thread whose turn started at zero is what this row prevents", thread.Evener.ActiveTurnStartedAt)
				}
				if thread.Evener.WorkMillis != 4200 {
					t.Fatalf("workMillis = %d, want the figure the session moved to", thread.Evener.WorkMillis)
				}
			},
		},
		{
			name: "queue on QUEUE_CHANGED",
			move: func(e *stubThreadEnvelopeSource) {
				e.queue = appwire.QueueState{Depth: 2, Revision: 5, Preview: []string{"alpha", "bravo"}}
			},
			event: events.SessionEvent{Kind: events.EventQueueChanged, SessionID: "th_1", Data: events.QueueChangedData{Depth: 2}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.Queue.Depth != 2 || thread.Evener.Queue.Revision != 5 {
					t.Fatalf("queue = %+v, want the depth/revision the session moved to", thread.Evener.Queue)
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
				if thread.Evener.Tasks == nil || thread.Evener.Tasks.Total != 4 || thread.Evener.Tasks.Done != 3 {
					t.Fatalf("tasks = %+v, want 4/3", thread.Evener.Tasks)
				}
			},
		},
		{
			name: "goal on GOAL_UPDATED",
			move: func(e *stubThreadEnvelopeSource) {
				e.meta.Goal = &schema.GoalSnapshot{Objective: "stale sampled objective", Status: "blocked", Iterations: 9}
			},
			event: events.SessionEvent{
				Kind: events.EventGoalUpdated, SessionID: "th_1",
				Data: events.GoalUpdatedData{Goal: &events.GoalStateData{Objective: "ship focus sentence", Status: "active", Iterations: 1}},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "ship focus sentence" || thread.Evener.Goal.Status != "active" || thread.Evener.Goal.Iterations != 1 {
					t.Fatalf("goal = %+v, want ship focus sentence/active/1", thread.Evener.Goal)
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
				got := thread.Evener.PendingEscalations
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
				if thread.Evener.ReasoningEffort != "high" || !thread.Evener.SupportsReasoning {
					t.Fatalf("reasoning = (%q, %v), want the new profile's settings",
						thread.Evener.ReasoningEffort, thread.Evener.SupportsReasoning)
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
				if thread.Evener.ReasoningEffort != "minimal" {
					t.Fatalf("reasoningEffort = %q, want minimal", thread.Evener.ReasoningEffort)
				}
			},
		},
		{
			name: "goal on GOAL_ENDED",
			move: func(e *stubThreadEnvelopeSource) {
				e.meta.Goal = &schema.GoalSnapshot{Objective: "checkpoint objective", Status: "completed", Iterations: 3}
			},
			event: events.SessionEvent{Kind: events.EventGoalEnded, SessionID: "th_1", Data: events.GoalEndedData{}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.Goal == nil || thread.Evener.Goal.Status != "completed" || thread.Evener.Goal.Iterations != 3 {
					t.Fatalf("goal = %+v, want completed/3", thread.Evener.Goal)
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
				if thread.Evener.FailedToolCalls == nil || *thread.Evener.FailedToolCalls != measured {
					t.Fatalf("failedToolCalls = %v, want %d", thread.Evener.FailedToolCalls, measured)
				}
			},
		},
		{
			name:  "diagnostics on JOB_STARTED",
			move:  func(e *stubThreadEnvelopeSource) { e.detailedStatus = DetailedStatus{Agents: []string{"scout"}} },
			event: events.SessionEvent{Kind: events.EventJobStarted, SessionID: "th_1", Data: events.JobStartedData{JobID: "job_1"}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.Diagnostics == nil || len(thread.Evener.Diagnostics.Agents) != 1 {
					t.Fatalf("diagnostics = %+v, want the job-bearing status", thread.Evener.Diagnostics)
				}
			},
		},
		{
			name: "diagnostics on DELEGATE_UPDATED",
			move: func(e *stubThreadEnvelopeSource) {
				e.detailedStatus = DetailedStatus{Delegates: []DelegateStatusInfo{{DelegateID: "dlg_1", ProjectionRevision: 3}}}
			},
			event: events.SessionEvent{Kind: events.EventDelegateUpdated, SessionID: "th_1", Data: events.DelegateUpdatedData{
				DelegateID: "dlg_1", OwnerSessionID: "th_1", ProjectionRevision: 3,
			}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.Diagnostics == nil || len(thread.Evener.Diagnostics.Delegates) != 1 || thread.Evener.Diagnostics.Delegates[0].DelegateID != "dlg_1" {
					t.Fatalf("diagnostics = %+v, want the stable delegate status", thread.Evener.Diagnostics)
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
				if thread.Evener.ContextPressure != 0.75 || thread.Evener.ContextUsed != 75 {
					t.Fatalf("context = (%v, %d), want the post-response figures",
						thread.Evener.ContextPressure, thread.Evener.ContextUsed)
				}
			},
		},
		{
			name: "work metrics on TURN_ENDED",
			move: func(e *stubThreadEnvelopeSource) {
				e.workMillis = 9000
				e.usage = &appwire.EvenerUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
			},
			event: events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_1", Data: events.TurnEndedData{}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.WorkMillis != 9000 || thread.Evener.Usage == nil || thread.Evener.Usage.TotalTokens != 30 {
					t.Fatalf("work = (%d, %+v), want the turn's accumulated figures",
						thread.Evener.WorkMillis, thread.Evener.Usage)
				}
			},
		},
		{
			name:  "ask pending on STEERING_INJECTED",
			move:  func(e *stubThreadEnvelopeSource) { e.askPending = true },
			event: events.SessionEvent{Kind: events.EventSteeringInjected, SessionID: "th_1", Data: events.SteeringInjectedData{Text: "answer"}},
			want: func(t *testing.T, thread appwire.Thread) {
				if !thread.Evener.AskPending {
					t.Fatal("askPending = false, want true once the session is blocked on a question")
				}
			},
		},
		{
			// Steering lands in history the moment it is injected, so the
			// pressure it added is readable before any model round follows.
			name: "context on STEERING_INJECTED",
			move: func(e *stubThreadEnvelopeSource) {
				e.contextPressure = 0.4
				e.contextMetrics = ContextMetrics{Used: 40, Window: 100, Remaining: 60}
			},
			event: events.SessionEvent{Kind: events.EventSteeringInjected, SessionID: "th_1", Data: events.SteeringInjectedData{Text: "answer"}},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Evener.ContextPressure != 0.4 || thread.Evener.ContextUsed != 40 {
					t.Fatalf("context = (%v, %d), want the figures the injected steering moved to",
						thread.Evener.ContextPressure, thread.Evener.ContextUsed)
				}
			},
		},
		{
			// An unnamed thread's preview falls back to meta.OriginalPrompt,
			// which is derived from the first user turn. Drop facetMeta here and
			// a new thread lists as its raw session id for its whole first turn.
			name: "preview on USER_INPUT",
			move: func(e *stubThreadEnvelopeSource) {
				e.meta = schema.SessionMeta{ID: "th_1", OriginalPrompt: "make the envelope authoritative"}
			},
			event: events.SessionEvent{
				Kind: events.EventUserInput, SessionID: "th_1",
				Data: events.UserInputData{Text: "make the envelope authoritative"},
			},
			want: func(t *testing.T, thread appwire.Thread) {
				if thread.Preview != "make the envelope authoritative" {
					t.Fatalf("thread.Preview = %q, want the first user turn's prompt: an unnamed "+
						"thread otherwise lists as its raw session id for its whole first turn",
						thread.Preview)
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

func TestThreadEnvelopeSeedUsesTaskAggregateAndStructuredMetaGoal(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	source := &stubThreadEnvelopeSource{}
	source.tasks = &appwire.TaskAggregate{
		Total:   2,
		Current: &appwire.TaskSummary{ID: 2, Description: "wire current work"},
	}
	source.meta.Goal = &schema.GoalSnapshot{Objective: "wire goal objective", Status: "active", Iterations: 2}
	publishEnvelope(srv, source)

	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.ID != 2 || thread.Evener.Tasks.Current.Description != "wire current work" {
		t.Fatalf("thread.Evener.Tasks.Current = %+v, want task 2", thread.Evener.Tasks)
	}
	if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "wire goal objective" || thread.Evener.Goal.Status != "active" || thread.Evener.Goal.Iterations != 2 {
		t.Fatalf("thread.Evener.Goal = %+v, want wire goal objective/active/2", thread.Evener.Goal)
	}
}

func TestSessionStartSeedTriStatePreservesUnknownAndAppliesExplicitClear(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "legacy-root")
	source := publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "pre-bridge task"}},
		meta:  schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "pre-bridge goal", Status: "active", Iterations: 4}},
	})

	feedBridge(srv, events.SessionEvent{Kind: events.EventSessionStart, SessionID: "legacy-root", Data: events.SessionStartData{}})
	legacy := readThreadOverWire(t, srv, "local:legacy-root")
	if legacy.Evener.Tasks == nil || legacy.Evener.Tasks.Current == nil || legacy.Evener.Tasks.Current.Description != "pre-bridge task" {
		t.Fatalf("legacy start tasks = %+v, want pre-bridge seed preserved", legacy.Evener.Tasks)
	}
	if legacy.Evener.Goal == nil || legacy.Evener.Goal.Objective != "pre-bridge goal" {
		t.Fatalf("legacy start goal = %+v, want pre-bridge seed preserved", legacy.Evener.Goal)
	}

	srv.SetAppIdentity("local", "seeded-root")
	source.tasks = &appwire.TaskAggregate{Total: 2, Current: &appwire.TaskSummary{ID: 2, Description: "replacement pre-seed"}}
	source.meta = schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "goal to clear", Status: "active", Iterations: 1}}
	srv.RefreshThreadEnvelope()
	feedBridge(srv, events.SessionEvent{Kind: events.EventSessionStart, SessionID: "seeded-root", Data: events.SessionStartData{
		CurrentWork: &events.CurrentWorkSeedData{Tasks: nil, Goal: nil},
	}})
	seeded := readThreadOverWire(t, srv, "local:seeded-root")
	if seeded.Evener.Tasks == nil || seeded.Evener.Tasks.Current == nil || seeded.Evener.Tasks.Current.Description != "replacement pre-seed" {
		t.Fatalf("present seed with nil Tasks replaced task state: %+v", seeded.Evener.Tasks)
	}
	if seeded.Evener.Goal != nil {
		t.Fatalf("present seed with nil Goal did not clear: %+v", seeded.Evener.Goal)
	}
}

func TestTaskAndGoalCarrierEventsDoNotRepullEnvelopeStores(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	source := publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1},
		meta:  schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "stale goal", Status: "active"}},
	})
	taskCalls, metaCalls := source.taskCalls, source.metaCalls

	feedBridge(srv,
		events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "th_1", Data: events.TaskUpdatedData{
			Total: 2, Done: 1, TaskStoreOwnerSessionID: "th_1",
		}},
		events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "th_1", Data: events.GoalUpdatedData{
			Goal: &events.GoalStateData{Objective: "carrier goal", Status: "active", Iterations: 2},
		}},
	)
	if source.taskCalls != taskCalls || source.metaCalls != metaCalls {
		t.Fatalf("carrier events re-pulled stores: task calls %d→%d, meta calls %d→%d", taskCalls, source.taskCalls, metaCalls, source.metaCalls)
	}
}

func TestTaskAndGoalCarriersReplaceSeededRootState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "old task"}},
		meta:  schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "old goal", Status: "active", Iterations: 1}},
	})

	feedBridge(srv,
		events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "th_1", Data: events.TaskUpdatedData{
			Total: 3, Done: 2, Current: &events.TaskSummaryData{ID: 3, Description: "new carrier task"},
			TaskStoreOwnerSessionID: "th_1",
		}},
		events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "th_1", Data: events.GoalUpdatedData{Goal: nil}},
	)
	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Total != 3 || thread.Evener.Tasks.Done != 2 || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "new carrier task" {
		t.Fatalf("root carrier tasks = %+v, want complete replacement", thread.Evener.Tasks)
	}
	if thread.Evener.Goal != nil {
		t.Fatalf("root carrier goal = %+v, want explicit clear", thread.Evener.Goal)
	}
}

func TestSessionTurnCheckpointRecoversOldTaskAndGoalProducer(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	source := publishEnvelope(srv, &stubThreadEnvelopeSource{})
	// An old producer changes its stores without emitting structured carriers.
	source.tasks = &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "checkpoint task"}}
	source.meta.Goal = &schema.GoalSnapshot{Objective: "checkpoint goal", Status: "active", Iterations: 5}
	feedBridge(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_1", Data: events.TurnEndedData{}})

	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "checkpoint task" {
		t.Fatalf("checkpoint tasks = %+v", thread.Evener.Tasks)
	}
	if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "checkpoint goal" || thread.Evener.Goal.Status != "active" || thread.Evener.Goal.Iterations != 5 {
		t.Fatalf("checkpoint goal = %+v", thread.Evener.Goal)
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
	if before.Name != "the retired session" || before.Evener.Queue.Depth != 3 {
		t.Fatalf("fixture did not publish the outgoing session's envelope: %+v", before.Evener)
	}

	// The daemon's session is replaced. Nothing has published the replacement's
	// state yet, which is the window this guards.
	srv.SetAppIdentity("local", "th_2")

	after := readThreadOverWire(t, srv, "local:th_2")
	if after.Name != "" {
		t.Fatalf("thread.Name = %q on the replacement thread, want the retired session's title gone", after.Name)
	}
	if after.Evener.Queue.Depth != 0 || after.Evener.Queue.Revision != 0 {
		t.Fatalf("queue = %+v on the replacement thread, want the retired session's queue gone", after.Evener.Queue)
	}
	if after.Evener.FailedToolCalls != nil {
		t.Fatalf("failedToolCalls = %d on the replacement thread, want absent: nobody has counted this session",
			*after.Evener.FailedToolCalls)
	}
	if len(after.Evener.PendingEscalations) != 0 {
		t.Fatalf("pendingEscalations = %+v on the replacement thread, want the retired session's cards gone",
			after.Evener.PendingEscalations)
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
	if got.response.Thread.Evener.FailedToolCalls == nil {
		t.Fatal("response carried no failure count: the envelope is not on the cut's side of the commit")
	}
	if n := *got.response.Thread.Evener.FailedToolCalls; n != 5 {
		t.Fatalf("response failedToolCalls = %d, want 5: the envelope was sampled before the commit", n)
	}
}

// TestEnvelopeLeadsTheCommitThatAnnouncesIt pins the ordering in BridgeEvent.
//
// The refresh must run BEFORE RecordAppEvent's commit. Swap them and a read
// that cuts between the commit and the later refresh snapshots the STALE
// envelope and then discards, as pre-cut, the very notification that announced
// the new value: neither side carries it, and nothing afterwards corrects it.
// That is the response-cut invariant inverted, which is the defect this whole
// line of work exists to remove.
//
// TestEnvelopeCommittedBeforeTheCutIsInTheResponse looks like this pin and is
// not: feedBridge returns only after the whole bridge pass, so it pins "refresh
// before the reader's gate", which the inverted order still satisfies. This one
// observes from INSIDE the commit, through the insideAppProjectionCommitHook,
// where the difference is the whole question. No parking and no second
// goroutine: the commit runs on feedBridge's own.
//
// One row is enough. The ordering is a property of BridgeEvent, not of a facet.
func TestEnvelopeLeadsTheCommitThatAnnouncesIt(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{})

	var atCommit *int
	var sawCommit bool
	setInsideAppProjectionCommitHook(t, func() {
		sawCommit = true
		srv.mu.RLock()
		if srv.appEnvelope.FailedToolCalls != nil {
			v := *srv.appEnvelope.FailedToolCalls
			atCommit = &v
		}
		srv.mu.RUnlock()
	})

	// A tool call fails: the session writes its count, then emits the event
	// announcing it. That is the production ordering the bridge samples under.
	src.failedToolCalls, src.failuresMeasured = 5, true
	feedBridge(srv, events.SessionEvent{
		Kind: events.EventToolCallEnd, SessionID: "th_1",
		Data: events.ToolCallEndData{CallID: "call_1", ToolName: "shell", Error: "boom"},
	})

	if !sawCommit {
		t.Fatal("the commit never ran; this test proves nothing")
	}
	if atCommit == nil {
		t.Fatal("the envelope carried no failure count at commit time: the refresh runs " +
			"AFTER the commit, so a read cutting between them snapshots the stale value " +
			"and then discards the notification that announced the new one")
	}
	if *atCommit != 5 {
		t.Fatalf("envelope failure count at commit time = %d, want 5", *atCommit)
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
func (c *countingThreadEnvelopeSource) FailedToolCalls() (int, bool) { c.hit(); return 0, false }
func (c *countingThreadEnvelopeSource) ReasoningInfo() (string, []string, bool) {
	c.hit()
	return "", nil, false
}

func (c *countingThreadEnvelopeSource) VisionModel() string { c.hit(); return "" }
func (c *countingThreadEnvelopeSource) WorkMetrics() (int64, *appwire.EvenerUsage, int64) {
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

// TestClearingTheGoalClearsItOnTheWire pins the successful callback's structured
// carrier as the authority for goal/set. The handler must not perform a second
// source pull after the callback emits the update.
func TestClearingTheGoalClearsItOnTheWire(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{
		meta: schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "old goal", Status: "active", Iterations: 2}},
	})
	srv.SetGoalFunc(func(objective string) (bool, error) {
		if objective == "" {
			feedBridge(srv, events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "th_1", Data: events.GoalUpdatedData{Goal: nil}})
			return false, nil
		}
		feedBridge(srv, events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "th_1", Data: events.GoalUpdatedData{Goal: &events.GoalStateData{Objective: objective, Status: "active"}}})
		return true, nil
	})

	if got := readThreadOverWire(t, srv, "local:th_1").Evener.Goal; got == nil || got.Status != "active" {
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

	// Leave the source deliberately stale: the direct carrier must win.
	feedBridge(srv,
		events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"},
		events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "x"}},
		events.SessionEvent{Kind: events.EventQueueChanged, SessionID: "th_1", Data: events.QueueChangedData{}},
	)

	if got := readThreadOverWire(t, srv, "local:th_1").Evener.Goal; got != nil {
		t.Fatalf("thread/read still carries goal %+v after goal/set cleared it", got)
	}
	if src.meta.Goal == nil {
		t.Fatal("fixture source unexpectedly changed; test no longer proves carrier-first behavior")
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
// identity commit for thread/clear runs while the OLD session's bridge is still
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
	if thread.Evener.Queue.Depth != 0 {
		t.Fatalf("queue = %+v on th_2, want the retired session's queue dropped", thread.Evener.Queue)
	}
	if thread.Evener.FailedToolCalls != nil {
		t.Fatalf("failedToolCalls = %d on th_2, want absent: that count belongs to th_1", *thread.Evener.FailedToolCalls)
	}
	if len(thread.Evener.PendingEscalations) != 0 {
		t.Fatalf("pendingEscalations = %+v on th_2, want th_1's cards dropped", thread.Evener.PendingEscalations)
	}
}

// TestEveryMutationHandlerPublishesItsQueueChange pins the change points that
// reach the daemon through a HANDLER rather than through the event stream.
//
// All seven turn/* handlers commit to the durable client-mutation store and then
// refresh the queue facet. Only turn/start's need was obvious: it records a
// pending execution and emits nothing until the serve loop later CLAIMS it, so a
// client reading in between could not see its own in-flight mutation -- the one
// thing the retry-safe projection exists to show a reconnecting client. Move 1's
// re-review then measured the other six on a real session and found turn/steer
// and turn/interrupt emit nothing either, which is why the fix was made uniform
// instead of targeted.
//
// Uniformity that nothing enforces is the failure mode this branch keeps
// finding, so each handler is pinned on its own row: delete one handler's
// refreshFacets and exactly that subtest fails, with the mutation the client
// just committed still absent from the wire.
//
// Every case drives the real RPC over a real connection and reads the thread
// back over another one. The retry-safe callback stands in for the session's
// accept step: it records the durable intent in the source and emits NOTHING, so
// the handler's own refresh is the only thing that can put the value on the
// wire.
func TestEveryMutationHandlerPublishesItsQueueChange(t *testing.T) {
	// record builds the callback body every case shares: stamp the accepted
	// intent onto the source, silently, exactly as the durable store does.
	record := func(src *stubThreadEnvelopeSource, method, mutationID string) {
		src.pendingMutations = []appwire.PendingMutation{{
			ClientMutationID: mutationID,
			Method:           method,
			ExecutionState:   "accepted",
		}}
	}

	cases := []struct {
		method     string
		mutationID string
		install    func(src *stubThreadEnvelopeSource, mutationID string) RetrySafeTurnFunctions
		params     func(mutationID string) any
	}{
		{
			method:     appwire.MethodTurnStart,
			mutationID: "cm_start",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Start: func(p appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
					record(src, appwire.MethodTurnStart, p.ClientMutationID)
					return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnStartParams{
					Ref:                "local:th_1",
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
					Input:              []appwire.InputItem{{Type: "text", Text: "go"}},
				}
			},
		},
		{
			method:     appwire.MethodTurnSteer,
			mutationID: "cm_steer",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Steer: func(p appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
					record(src, appwire.MethodTurnSteer, p.ClientMutationID)
					return appwire.TurnSteerResponse{}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnSteerParams{
					Ref:                "local:th_1",
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
					Input:              []appwire.InputItem{{Type: "text", Text: "steer"}},
				}
			},
		},
		{
			method:     appwire.MethodTurnQueue,
			mutationID: "cm_queue",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Queue: func(p appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
					record(src, appwire.MethodTurnQueue, p.ClientMutationID)
					return appwire.TurnQueueResponse{}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnQueueParams{
					Ref:                "local:th_1",
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
					Input:              []appwire.InputItem{{Type: "text", Text: "later"}},
				}
			},
		},
		{
			method:     appwire.MethodTurnInterrupt,
			mutationID: "cm_interrupt",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Interrupt: func(_ context.Context, p appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
					record(src, appwire.MethodTurnInterrupt, p.ClientMutationID)
					return appwire.TurnInterruptResponse{}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnInterruptParams{
					Ref:                "local:th_1",
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
				}
			},
		},
		{
			method:     appwire.MethodTurnDrainAsSteer,
			mutationID: "cm_drain",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Drain: func(p appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
					record(src, appwire.MethodTurnDrainAsSteer, p.ClientMutationID)
					return appwire.TurnDrainAsSteerResponse{}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnDrainAsSteerParams{
					Ref:                "local:th_1",
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
				}
			},
		},
		{
			method:     appwire.MethodTurnPromoteQueuedAsSteer,
			mutationID: "cm_promote",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Promote: func(p appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
					record(src, appwire.MethodTurnPromoteQueuedAsSteer, p.ClientMutationID)
					return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnPromoteQueuedAsSteerParams{
					Ref:                "local:th_1",
					Index:              0,
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
					ExpectedEntryID:    "entry_1",
				}
			},
		},
		{
			method:     appwire.MethodTurnCancelQueued,
			mutationID: "cm_cancel",
			install: func(src *stubThreadEnvelopeSource, id string) RetrySafeTurnFunctions {
				return RetrySafeTurnFunctions{Cancel: func(p appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
					record(src, appwire.MethodTurnCancelQueued, p.ClientMutationID)
					return appwire.TurnCancelQueuedResponse{}, nil
				}}
			},
			params: func(id string) any {
				return appwire.TurnCancelQueuedParams{
					Ref:                "local:th_1",
					Index:              0,
					ClientMutationID:   id,
					ExpectedInstanceID: "th_1",
					ExpectedEntryID:    "entry_1",
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_1")
			src := publishEnvelope(srv, &stubThreadEnvelopeSource{})
			srv.SetRetrySafeTurnFunctions(tc.install(src, tc.mutationID))

			conn := srv.AppServer().NewConnection("mutation-handler")
			conn.HandleMessage(context.Background(), appwire.RequestMessage(
				appwire.NewIntID(1), appwire.MethodInitialize,
				appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
			resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
				appwire.NewIntID(2), tc.method, tc.params(tc.mutationID)))
			if resp.Kind() != appwire.MessageResponse {
				t.Fatalf("%s: %v (%+v)", tc.method, resp.Kind(), resp.Response)
			}
			// The callback ran, so the durable intent exists. Anything missing
			// from the wire below is the handler's refresh, not the accept.
			if len(src.pendingMutations) != 1 {
				t.Fatalf("%s: callback did not record the intent; the case proves nothing", tc.method)
			}

			got := readThreadOverWire(t, srv, "local:th_1").Evener.PendingMutations
			if len(got) != 1 || got[0].ClientMutationID != tc.mutationID {
				t.Fatalf("pendingMutations = %+v after %s, want the accepted intent %q on the wire",
					got, tc.method, tc.mutationID)
			}
		})
	}
}

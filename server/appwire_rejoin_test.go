package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/llm"
)

func TestAtomicProjectionCommitPreservesProducerOrderAcrossSequenceAllocation(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("prompt")))
	srv := st.srv
	srv.RecordAppEvent(threadEvent("th_1", events.RoundStartedData{RoundID: "r_1"}))
	before := srv.appNotifier.RetainedWindow("th_1").UpperSeq

	projected := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	setInsideAppProjectionCommitHook(t, func() {
		once.Do(func() {
			close(projected)
			<-release
		})
	})

	completed := make(chan struct{})
	go func() {
		srv.RecordAppEvent(threadEvent("th_1", events.ToolCallStartData{ToolName: "shell", CallID: "call_1"}))
		close(completed)
	}()
	<-projected

	laterStarted := make(chan struct{})
	srv.mu.Lock()
	srv.beforeAppProjectionCommit = func() {
		close(laterStarted)
	}
	srv.mu.Unlock()
	laterCompleted := make(chan struct{})
	go func() {
		srv.RecordAppEvent(threadEvent("th_1", events.ToolCallOutputDeltaData{CallID: "call_1", Delta: "second"}))
		close(laterCompleted)
	}()
	<-laterStarted
	close(release)
	<-completed
	<-laterCompleted

	records := srv.AppNotificationsAfter(before, "th_1")
	if len(records) != 2 {
		t.Fatalf("committed notifications = %d, want the tool's upsert and its later output delta", len(records))
	}
	if records[0].Notification.Method != appwire.NotifyOverlayUpserted || records[1].Notification.Method != appwire.NotifyOverlayDelta {
		t.Fatalf("commit order = [%s, %s], want [%s, %s]",
			records[0].Notification.Method, records[1].Notification.Method,
			appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta)
	}
}

func TestAtomicRejoinProjectsDurablePendingMutationsAndQueueRevision(t *testing.T) {
	sess := newMutationReplaySession(t)
	acceptMutationReplayActiveTurn(t, sess)
	queueParams := appwire.TurnQueueParams{
		ClientMutationID:   "queued-for-rejoin",
		ExpectedInstanceID: sess.ID(),
		Ref:                "local:" + sess.ID(),
		Input:              []appwire.InputItem{{Type: "text", Text: "durable queued input"}},
	}
	if _, err := sess.AcceptClientMutationQueue(queueParams); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	publishSessionQueueEnvelope(srv, sess)

	response, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("thread/read: %v", err)
	}
	if response.Thread.Evener.Queue.Revision != 1 {
		t.Fatalf("queue revision = %d, want durable revision 1", response.Thread.Evener.Queue.Revision)
	}
	for _, pending := range response.Thread.Evener.PendingMutations {
		if pending.ClientMutationID == queueParams.ClientMutationID {
			if len(pending.Input) != 1 || pending.Input[0].Text != "durable queued input" {
				t.Fatalf("pending queue payload = %#v", pending.Input)
			}
			return
		}
	}
	t.Fatalf("pending mutations = %#v, want %q", response.Thread.Evener.PendingMutations, queueParams.ClientMutationID)
}

func TestAtomicRejoinExcludesTranscriptIncorporatedMutationsFromPending(t *testing.T) {
	tests := []struct {
		name    string
		blockAt int
		accept  func(*testing.T, *agent.Session) string
	}{
		{
			name:    "start",
			blockAt: 1,
			accept: func(t *testing.T, sess *agent.Session) string {
				params := appwire.TurnStartParams{
					ClientMutationID:   "incorporated-start",
					ExpectedInstanceID: sess.ID(),
					Input:              []appwire.InputItem{{Type: "text", Text: "durable start"}},
				}
				if _, err := sess.AcceptClientMutationStart(params); err != nil {
					t.Fatalf("AcceptClientMutationStart: %v", err)
				}
				return params.ClientMutationID
			},
		},
		{
			name:    "queue",
			blockAt: 2,
			accept: func(t *testing.T, sess *agent.Session) string {
				// Started for its side effect: the queue needs a turn to sit behind.
				_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
					ClientMutationID:   "queue-parent-start",
					ExpectedInstanceID: sess.ID(),
					Input:              []appwire.InputItem{{Type: "text", Text: "parent start"}},
				})
				if err != nil {
					t.Fatalf("AcceptClientMutationStart: %v", err)
				}
				params := appwire.TurnQueueParams{
					ClientMutationID:   "incorporated-queue",
					ExpectedInstanceID: sess.ID(),
					Input:              []appwire.InputItem{{Type: "text", Text: "durable queue"}},
				}
				if _, err := sess.AcceptClientMutationQueue(params); err != nil {
					t.Fatalf("AcceptClientMutationQueue: %v", err)
				}
				return params.ClientMutationID
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &mutationProjectionAdapter{
				blockAt: test.blockAt,
				blocked: make(chan struct{}),
			}
			sess := newMutationReplaySessionWithAdapter(t, adapter)
			mutationID := test.accept(t, sess)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, _, err := sess.ProcessClientMutationStart(ctx, nil)
				done <- err
			}()
			<-adapter.blocked

			srv := NewServer(ServerConfig{})
			installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
			publishSessionQueueEnvelope(srv, sess)
			response, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{IncludeTurns: true})
			if err != nil {
				cancel()
				<-done
				t.Fatalf("thread/read: %v", err)
			}

			assertMutationIdentityInTranscriptNotPending(t, response, mutationID)
			cancel()
			<-done
		})
	}
}

func assertMutationIdentityInTranscriptNotPending(
	t *testing.T,
	response appwire.ThreadReadResponse,
	mutationID string,
) {
	t.Helper()
	for _, turn := range response.Thread.Turns {
		for _, item := range turn.Items {
			if item.ClientMutationID == mutationID {
				for _, pending := range response.Thread.Evener.PendingMutations {
					if pending.ClientMutationID == mutationID {
						t.Fatalf(
							"mutation %q appears in transcript identity and pending mutations: %#v",
							mutationID,
							response.Thread.Evener.PendingMutations,
						)
					}
				}
				return
			}
		}
	}
	t.Fatalf("transcript has no item with client mutation identity %q", mutationID)
}

type mutationProjectionAdapter struct {
	mu        sync.Mutex
	mainCalls int
	blockAt   int
	blocked   chan struct{}
}

func (a *mutationProjectionAdapter) Name() string { return "openai" }

func (a *mutationProjectionAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if !requestHasTool(req, "communicate") {
		return llm.Response{
			Provider: a.Name(),
			Model:    req.Model,
			Message:  llm.Assistant(`{"name":"rejoin projection"}`),
		}, nil
	}

	a.mu.Lock()
	a.mainCalls++
	call := a.mainCalls
	a.mu.Unlock()
	if call < a.blockAt {
		args, _ := json.Marshal(map[string]any{
			"message":  "done",
			"end_turn": true,
			"output": map[string]any{
				"message":   "",
				"data":      map[string]any{},
				"artifacts": []string{},
			},
		})
		return llm.Response{
			Provider: a.Name(),
			Model:    req.Model,
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "communicate-rejoin",
						Name:      "communicate",
						Arguments: args,
						Type:      "function",
					},
				}},
			},
		}, nil
	}

	close(a.blocked)
	<-ctx.Done()
	return llm.Response{Provider: a.Name(), Model: req.Model}, ctx.Err()
}

func (a *mutationProjectionAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func requestHasTool(req llm.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// TestAtomicRejoinShowsARecordedAnswerOnce pins the rejoin boundary between a
// streamed answer and the entry that records it. The read captures the
// overlay's stream inside the cut; the answer's ASSISTANT entry is recorded
// after the response, and its history/updated arrives on the subscription. A
// client that merges history by version and drops a stream once a history
// item of its round is held shows the answer once.
func TestAtomicRejoinShowsARecordedAnswerOnce(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", inExecution("t_1", true, schema.NewTurn(schema.TurnUserInput, llm.User("question"))))
	srv := st.srv
	srv.RecordAppEvent(threadEvent("th_1", events.RoundStartedData{RoundID: "r_answer"}))
	srv.RecordAppEvent(threadEvent("th_1", events.AssistantTextDeltaData{Delta: "answer"}))

	client := dialServerAppWire(t, srv)
	cut := srv.appNotifier.CurrentSequence()
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true, IncludeTurns: true})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(response.Overlay) != 1 || response.Overlay[0].Kind != appwire.OverlayStream {
		t.Fatalf("response overlay = %+v, want the answer's stream", response.Overlay)
	}

	answer := inExecution("t_1", false, schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")))
	answer.RoundID = "r_answer"
	st.record(t, answer)
	st.settle(t)

	held := newHistoryClient(response)
	for _, n := range srv.AppNotificationsAfter(cut, "th_1") {
		if n.Notification.Method == appwire.NotifyHistoryUpdated {
			held.apply(t, notificationParams[appwire.HistoryUpdatedParams](t, n))
		}
	}
	answers := 0
	coveredRounds := map[string]bool{}
	for _, item := range held.items {
		if item.RoundID != "" {
			coveredRounds[item.RoundID] = true
		}
		if item.Text == "answer" {
			answers++
		}
	}
	for _, item := range response.Overlay {
		if item.Kind == appwire.OverlayStream && !coveredRounds[item.RoundID] && item.Item.Text == "answer" {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("the answer is shown %d times after rejoin, want exactly 1\nhistory: %+v\noverlay: %+v", answers, held.items, response.Overlay)
	}
}

func TestAtomicProjectionCommitStampsAuthoritativeNotificationTarget(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "authoritative")
	srv.mu.Lock()
	srv.appProjector = appprojector.NewAppEventProjector("stale", "remote:stale")
	srv.mu.Unlock()

	srv.RecordAppEvent(events.SessionEvent{
		Kind: events.EventNotesUpdated,
		Data: events.NotesUpdatedData{HumanNote: "qualified"},
	})

	records := srv.AppNotificationsAfter(0, "authoritative")
	if len(records) != 1 {
		t.Fatalf("authoritative notifications = %d, want 1", len(records))
	}
	record := records[0]
	if record.ThreadID != "local:authoritative" {
		t.Fatalf("record routing key = %q, want local:authoritative", record.ThreadID)
	}
	if record.Notification.Method != appwire.NotifyEvenerNotesUpdated {
		t.Fatalf("notification method = %q, want %q", record.Notification.Method, appwire.NotifyEvenerNotesUpdated)
	}
	var params appwire.NotesUpdatedParams
	if err := json.Unmarshal(record.Notification.Params, &params); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if params.ThreadID != "authoritative" || params.Ref != "local:authoritative" {
		t.Fatalf("notification target = (%q, %q), want authoritative identity", params.ThreadID, params.Ref)
	}
}

// TestServerAppWireReadCutTakesTheSnapshotInsideTheSubscription pins the one
// thing that makes a subscribing thread/read authoritative: the response and
// the cut are produced inside the same projection transition that registers
// the subscription. Take the envelope beside the registration rather than with
// it and a commit landing in between is in neither the response nor the
// delivered stream, which is a pane that is permanently wrong with nothing
// left to correct it. History is read after the cut, up to the recorded
// length, so an entry recorded before the cut is in the response too.
//
// The read is parked at the projection gate, a commit is driven to completion
// while it waits there, and the assertions read the response and the frames the
// SUBSCRIPTION actually delivered. Nothing consults the notifier's replay.
func TestServerAppWireReadCutTakesTheSnapshotInsideTheSubscription(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1")
	srv := st.srv

	atGate := make(chan struct{})
	openGate := make(chan struct{})
	var once sync.Once
	srv.AppServer().SetBeforeSubscriptionGate(func() {
		once.Do(func() {
			close(atGate)
			<-openGate
		})
	})
	client := dialServerAppWire(t, srv)

	type readOutcome struct {
		response appwire.ThreadReadResponse
		err      error
	}
	reads := make(chan readOutcome, 1)
	go func() {
		response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
			Ref:          "local:th_1",
			Subscribe:    true,
			IncludeTurns: true,
		})
		reads <- readOutcome{response: response, err: err}
	}()
	<-atGate

	// A turn opens while the read waits for the gate: its execution is
	// published and its first entry recorded. These run to completion here:
	// the read holds no lock at the gate barrier.
	srv.SetProcessingTurn("t_1")
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("question")))
	close(openGate)

	outcome := <-reads
	if outcome.err != nil {
		t.Fatalf("ThreadRead: %v", outcome.err)
	}
	// The response is on the cut's side of that commit, envelope included. An
	// envelope sampled before the commit would report no active turn, and
	// nothing after the cut would ever say otherwise.
	if got := outcome.response.Thread.Evener.ActiveTurnID; got != "t_1" {
		t.Fatalf("response activeTurnId = %q, want t_1: the envelope is not on the cut's side of the commit", got)
	}
	if got := readTexts(outcome.response); len(got) != 1 || got[0] != "question" {
		t.Fatalf("response items = %q, want the entry recorded before the cut", got)
	}

	// The subscription is live and post-cut history reaches it. This reads the
	// delivered stream, not the notifier's replay window.
	st.record(t, schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")))
	held := newHistoryClient(outcome.response)
	for answered := false; !answered; {
		notification := <-client.Notifications()
		if notification.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		var update appwire.HistoryUpdatedParams
		if err := json.Unmarshal(notification.Params, &update); err != nil {
			t.Fatal(err)
		}
		held.apply(t, update)
		for _, item := range update.Items {
			answered = answered || item.Text == "answer"
		}
	}
	counts := map[string]int{}
	for _, item := range held.items {
		counts[item.Text]++
	}
	if counts["question"] != 1 || counts["answer"] != 1 {
		t.Fatalf("held items %v, want the question and the answer once each", counts)
	}
}

// TestReloadMidStreamResumesTheSameStream (kata 5xk6) reloads a session while
// it is working -- in the middle of one assistant item's delta run -- and
// requires the reloaded client to land on exactly the state a client that
// never reloaded holds.
//
// "Streaming breaks after a reload" is what a torn response boundary looks
// like from the outside. There are three ways to tear it, and a test that
// reads BETWEEN turns cannot see any of them:
//
//   - the response's in-progress item stops short of the cut and the missing
//     chunks are never replayed (a gap);
//   - the response carries chunks that are replayed after it as well (a
//     repeat);
//   - the response carries no partial item at all, so every delta that
//     follows has nothing to append to.
//
// The daemon's answer to all three is one boundary: CaptureSubscription clones
// the installed snapshot and takes the notifier cut inside a single projection
// transition, so the response and the records after it partition the stream
// exactly. The gate barrier here is what makes that partition load-bearing
// rather than incidental: four deltas commit while the read is parked at the
// subscription gate, which is AFTER a snapshot sampled outside the capture
// would have been taken and BEFORE the cut. They therefore have exactly one
// correct home -- inside the response -- and a snapshot hoisted above
// CaptureSubscription loses them in both directions at once, since the cut
// then discards them as already-reflected. Without those deltas the test is
// sequential and a hoisted snapshot survives it.
//
// The comparison is whole-snapshot rather than text-only on purpose: a
// reload-specific divergence in item status, timing or turn state fails here
// without anyone having to predict which field it lands in.
func TestReloadMidStreamResumesTheSameStream(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1",
		schema.NewTurn(schema.TurnUserInput, llm.User("in")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("out")),
		schema.NewTurn(schema.TurnUserInput, llm.User("question")),
	)
	srv := st.srv

	stream := func(deltas []string) {
		for _, delta := range deltas {
			srv.RecordAppEvent(threadEvent("th_1", events.AssistantTextDeltaData{Delta: delta}))
		}
	}
	srv.RecordAppEvent(threadEvent("th_1", events.RoundStartedData{RoundID: "r_stream"}))

	beforeRead := []string{"chunk-0 ", "chunk-1 ", "chunk-2 ", "chunk-3 "}
	atTheGate := []string{"chunk-4 ", "chunk-5 ", "chunk-6 ", "chunk-7 "}
	afterRead := []string{"chunk-8 ", "chunk-9 ", "chunk-10 ", "chunk-11"}
	stream(beforeRead)

	atGate := make(chan struct{})
	openGate := make(chan struct{})
	var once sync.Once
	srv.AppServer().SetBeforeSubscriptionGate(func() {
		once.Do(func() {
			close(atGate)
			<-openGate
		})
	})
	client := dialServerAppWire(t, srv)

	// The reload: one subscribing read, issued with the turn still streaming.
	type outcome struct {
		response appwire.ThreadReadResponse
		err      error
	}
	reads := make(chan outcome, 1)
	go func() {
		response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
			Ref:          "local:th_1",
			Subscribe:    true,
			IncludeTurns: true,
		})
		reads <- outcome{response: response, err: err}
	}()
	<-atGate

	// The model keeps talking while the read waits at the gate. The read holds
	// no lock there, so these commit to completion.
	stream(atTheGate)
	close(openGate)

	got := <-reads
	if got.err != nil {
		t.Fatalf("ThreadRead: %v", got.err)
	}
	// This goroutine is the only committer and it was blocked on the read, so
	// nothing has committed since the response's own cut: sampling here samples
	// that cut.
	cut := srv.appNotifier.CurrentSequence()

	// The round is deliberately left streaming: "while it's working" is the
	// state the kata reports.
	stream(afterRead)

	reloaded := map[string]appwire.OverlayItem{}
	var order []string
	for _, item := range got.response.Overlay {
		reloaded[item.Key] = item
		order = append(order, item.Key)
	}
	for _, record := range srv.AppNotificationsAfter(cut, "th_1") {
		switch record.Notification.Method {
		case appwire.NotifyOverlayUpserted:
			item := notificationParams[appwire.OverlayUpsertedParams](t, record).Item
			if _, held := reloaded[item.Key]; !held {
				order = append(order, item.Key)
			}
			reloaded[item.Key] = item
		case appwire.NotifyOverlayDelta:
			delta := notificationParams[appwire.OverlayDeltaParams](t, record)
			item, held := reloaded[delta.Key]
			if !held {
				t.Fatalf("a delta for %s arrived with nothing to append to", delta.Key)
			}
			item.Item.Text += delta.Delta
			reloaded[delta.Key] = item
		}
	}

	var wantParts []string
	for _, deltas := range [][]string{beforeRead, atTheGate, afterRead} {
		wantParts = append(wantParts, deltas...)
	}
	wantText := strings.Join(wantParts, "")
	if len(order) != 1 {
		t.Fatalf("reload reduced the streaming answer into %d overlay items %v, want exactly 1", len(order), order)
	}
	if text := reloaded[order[0]].Item.Text; text != wantText {
		t.Fatalf("streamed text across the reload =\n  %q\nwant\n  %q", text, wantText)
	}

	// The whole state against a client that never reloaded: a fresh read.
	uninterrupted := st.read(t)
	if len(uninterrupted.Overlay) != 1 || !reflect.DeepEqual(uninterrupted.Overlay[0], reloaded[order[0]]) {
		t.Fatalf("reloaded overlay diverged from a fresh read\n reloaded: %+v\n   direct: %+v", reloaded[order[0]], uninterrupted.Overlay)
	}
	if !reflect.DeepEqual(readTexts(got.response), readTexts(uninterrupted)) {
		t.Fatalf("reloaded history %q diverged from a fresh read %q", readTexts(got.response), readTexts(uninterrupted))
	}
}

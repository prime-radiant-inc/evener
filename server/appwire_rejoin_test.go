package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/llm"
)

func TestAtomicProjectionCommitPreservesProducerOrderAcrossSequenceAllocation(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "prompt"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
	})
	before := srv.appNotifier.RetainedWindow("th_1").UpperSeq

	projected := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.failuresMeasured = true })
	srv.mu.Lock()
	srv.insideAppProjectionCommit = func() {
		once.Do(func() {
			close(projected)
			<-release
		})
	}
	srv.mu.Unlock()

	completed := make(chan struct{})
	go func() {
		srv.RecordAppEvent(events.SessionEvent{
			Kind:      events.EventAssistantTextEnd,
			SessionID: "th_1",
			Data:      events.AssistantTextEndData{Text: "first"},
		})
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
		srv.RecordAppEvent(events.SessionEvent{
			Kind:      events.EventAssistantTextDelta,
			SessionID: "th_1",
			Data:      events.AssistantTextDeltaData{Delta: " second"},
		})
		close(laterCompleted)
	}()
	<-laterStarted
	close(release)
	<-completed
	<-laterCompleted

	records := srv.AppNotificationsAfter(before, "th_1")
	// Three, not two: the later delta opens its own agent-message item (the
	// completion above closed the previous one) and so commits item/started
	// alongside the delta itself.
	if len(records) != 3 {
		t.Fatalf("committed notifications = %d, want item completion and later delta", len(records))
	}
	last := len(records) - 1
	if records[0].Notification.Method != appwire.NotifyItemCompleted ||
		records[last].Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf(
			"commit order = [%s, %s], want [%s, %s]",
			records[0].Notification.Method,
			records[last].Notification.Method,
			appwire.NotifyItemCompleted,
			appwire.NotifyAgentMessageDelta,
		)
	}
}

func TestAtomicRejoinProjectsDurablePendingMutationsAndQueueRevision(t *testing.T) {
	sess := newMutationReplaySession(t)
	turnID := acceptMutationReplayActiveTurn(t, sess)
	queueParams := appwire.TurnQueueParams{
		ClientMutationID: "queued-for-rejoin",
		ExpectedTurnID:   turnID,
		Input:            []appwire.InputItem{{Type: "text", Text: "durable queued input"}},
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
	if response.Thread.Serf.Queue.Revision != 1 {
		t.Fatalf("queue revision = %d, want durable revision 1", response.Thread.Serf.Queue.Revision)
	}
	for _, pending := range response.Thread.Serf.PendingMutations {
		if pending.ClientMutationID == queueParams.ClientMutationID {
			if len(pending.Input) != 1 || pending.Input[0].Text != "durable queued input" {
				t.Fatalf("pending queue payload = %#v", pending.Input)
			}
			return
		}
	}
	t.Fatalf("pending mutations = %#v, want %q", response.Thread.Serf.PendingMutations, queueParams.ClientMutationID)
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
					ClientMutationID: "incorporated-start",
					Input:            []appwire.InputItem{{Type: "text", Text: "durable start"}},
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
				start, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
					ClientMutationID: "queue-parent-start",
					Input:            []appwire.InputItem{{Type: "text", Text: "parent start"}},
				})
				if err != nil {
					t.Fatalf("AcceptClientMutationStart: %v", err)
				}
				params := appwire.TurnQueueParams{
					ClientMutationID: "incorporated-queue",
					ExpectedTurnID:   start.Turn.ID,
					Input:            []appwire.InputItem{{Type: "text", Text: "durable queue"}},
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
				for _, pending := range response.Thread.Serf.PendingMutations {
					if pending.ClientMutationID == mutationID {
						t.Fatalf(
							"mutation %q appears in transcript identity and pending mutations: %#v",
							mutationID,
							response.Thread.Serf.PendingMutations,
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

// TestAtomicRejoinDoesNotReadTranscriptAheadOfBlockedEvent pins the response cut
// against the file the daemon must not consult while holding it.
//
// Transcript persistence can lead live event delivery: the entry for an
// assistant answer can already be durable while the matching event is still
// short of its projection commit. A read that parses the file under the cut
// therefore answers with that output under TRANSCRIPT identity, and the same
// output arrives again after the cut under the LIVE projector's identity. The
// client reduces one answer into two items and the pane shows it twice.
func TestAtomicRejoinDoesNotReadTranscriptAheadOfBlockedEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "th_1",
		CreatedAt: time.Unix(1700000000, 0),
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User("question"))); err != nil {
		t.Fatalf("append user: %v", err)
	}

	// Install the snapshot from the transcript as it stands NOW -- before the
	// assistant entry exists.
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)
	// A restored session seeds the live projector above the persisted entry
	// count, so the live turn id cannot collide with the reload path's.
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Restored: true, TranscriptEntries: 1, Profile: "openai", Model: "gpt-5"},
	})

	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.mu.Lock()
	srv.beforeAppProjectionCommit = func() {
		once.Do(func() {
			close(blocked)
			<-release
		})
	}
	srv.mu.Unlock()

	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		srv.RecordAppEvent(events.SessionEvent{
			Kind:      events.EventAssistantTextEnd,
			SessionID: "th_1",
			Data:      events.AssistantTextEndData{Text: "answer"},
		})
	}()
	<-blocked

	// The answer is durable on disk before its event reaches the commit.
	if err := writer.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer"))); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
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

	cut := srv.appNotifier.CurrentSequence()
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "local:th_1",
		Subscribe:    true,
		IncludeTurns: true,
		TurnLimit:    40,
	})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	// Nothing committed between the sampled sequence and the response, so cut
	// is the response's own cut and everything after it is post-cut delivery.
	if pending := srv.AppNotificationsAfter(cut, "th_1"); len(pending) != 0 {
		t.Fatalf("sampled cut is not the response cut: %d record(s) already committed", len(pending))
	}

	close(release)
	<-recorded

	// Reduce exactly what a rejoining client reduces: the response snapshot,
	// then the post-cut records in wire order.
	rejoined := &appTurnSnapshot{threadID: "th_1"}
	rejoined.Seed(response.Thread.Turns)
	rejoined.Apply(srv.AppNotificationsAfter(cut, "th_1"))

	answers := 0
	for _, turn := range rejoined.Snapshot() {
		for _, item := range turn.Items {
			if strings.Contains(item.Text, "answer") {
				answers++
			}
		}
	}
	if answers != 1 {
		t.Fatalf("assistant answer reduced into %d items after rejoin, want exactly 1\nturns: %+v", answers, rejoined.Snapshot())
	}
}

// TestPreparedAppIdentityFencesLiveTurnIDsAboveSeededTranscript proves a live
// turn cannot land on top of a seeded one.
//
// Both id spaces are "turn_%d": the transcript projection numbers a turn by its
// ENTRY INDEX (internal/apptranscript), and a fresh projector mints turn_1 for
// the first live turn. The snapshot is now the daemon's only turn authority, so
// a collision is not a display glitch that the next read repairs -- the live
// turn merges into the seeded entry and REPLACES its content for the life of
// the session.
//
// Nothing orders SessionStart (which carries the persisted entry count) ahead
// of the first turn-starting request: bridgeSession only spawns the drain
// goroutine. So the fence has to be established when the identity is prepared,
// not when an event happens to arrive. This drives the collision deterministically
// by recording the live input with no SessionStart at all -- the worst case that
// ordering leaves open.
func TestPreparedAppIdentityFencesLiveTurnIDsAboveSeededTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	writeTranscriptPairs(t, path, 3)

	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)
	seeded := srv.appAllTurns("th_1")
	if len(seeded) == 0 {
		t.Fatal("transcript seeded no turns")
	}

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "LIVE-INPUT"},
	})

	after := map[string]appwire.Turn{}
	for _, turn := range srv.appAllTurns("th_1") {
		after[turn.ID] = turn
	}
	for _, before := range seeded {
		got, ok := after[before.ID]
		if !ok {
			t.Fatalf("seeded turn %s vanished from the snapshot", before.ID)
		}
		if !reflect.DeepEqual(before, got) {
			t.Fatalf("live turn overwrote seeded turn %s\n before: %+v\n  after: %+v", before.ID, before, got)
		}
	}
	if len(after) != len(seeded)+1 {
		t.Fatalf("snapshot holds %d turns, want the %d seeded turns plus one live turn", len(after), len(seeded))
	}
}

func TestAtomicProjectionCommitStampsAuthoritativeNotificationTarget(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "authoritative")
	srv.mu.Lock()
	srv.appProjector = appprojector.NewAppEventProjector("stale", "remote:stale")
	srv.mu.Unlock()

	srv.RecordAppEvent(events.SessionEvent{
		Kind: events.EventAssistantTextDelta,
		Data: events.AssistantTextDeltaData{Delta: "qualified"},
	})

	// Three: the delta opens its own turn and its own agent-message item before
	// carrying the text. Every record is the commit's, so every one of them must
	// name the authoritative thread.
	records := srv.AppNotificationsAfter(0, "authoritative")
	if len(records) != 3 {
		t.Fatalf("authoritative notifications = %d, want 3", len(records))
	}
	for _, record := range records {
		if record.ThreadID != "authoritative" {
			t.Fatalf("record thread ID = %q, want authoritative", record.ThreadID)
		}
	}
	delta := records[len(records)-1]
	if delta.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("last notification method = %q, want %q", delta.Notification.Method, appwire.NotifyAgentMessageDelta)
	}
	var params appwire.AgentMessageDeltaParams
	if err := json.Unmarshal(delta.Notification.Params, &params); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if params.ThreadID != "authoritative" || params.Ref != "local:authoritative" {
		t.Fatalf("notification target = (%q, %q), want authoritative identity", params.ThreadID, params.Ref)
	}
}

// TestServerAppWireReadCutTakesTheSnapshotInsideTheSubscription pins the one
// thing that makes a subscribing thread/read authoritative: the response and
// the cut are produced inside the same projection transition that registers
// the subscription. Take the snapshot beside the registration rather than with
// it -- the shape this branch replaced -- and a commit landing in between is in
// neither the response nor the delivered stream, which is a pane that is
// permanently wrong with nothing left to correct it.
//
// The read is parked at the projection gate, a commit is driven to completion
// while it waits there, and the assertions read the response and the frames the
// SUBSCRIPTION actually delivered. Nothing consults the notifier's replay.
func TestServerAppWireReadCutTakesTheSnapshotInsideTheSubscription(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

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
			TurnLimit:    40,
		})
		reads <- readOutcome{response: response, err: err}
	}()
	<-atGate

	// A turn opens while the read waits for the gate. This commit runs to
	// completion here: the read holds no lock at the gate barrier, in either
	// the atomic shape or the one it replaced.
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "question"},
	})
	close(openGate)

	outcome := <-reads
	if outcome.err != nil {
		t.Fatalf("ThreadRead: %v", outcome.err)
	}

	// The response is on the cut's side of that commit, envelope included. An
	// envelope sampled before the commit would report no active turn while
	// listing the turn it opened, and nothing after the cut would ever say
	// otherwise.
	if got := outcome.response.Thread.Serf.ActiveTurnID; got != "turn_1" {
		t.Fatalf("response activeTurnId = %q, want turn_1: the snapshot is not on the cut's side of the commit", got)
	}
	if turns := outcome.response.Thread.Turns; len(turns) != 1 || turns[0].ID != "turn_1" {
		t.Fatalf("response turns = %+v, want the one turn opened before the cut", turns)
	}

	// The subscription is live and post-cut frames reach it. This reads the
	// delivered stream, not the notifier's replay window.
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "answer"},
	})

	rejoined := &appTurnSnapshot{threadID: "th_1"}
	rejoined.Seed(outcome.response.Thread.Turns)
	delivered := uint64(0)
	deltas := 0
	for deltas == 0 {
		notification := <-client.Notifications()
		delivered++
		rejoined.Apply([]appserver.SequencedNotification{{
			Seq:          delivered,
			ThreadID:     "th_1",
			Notification: notification,
		}})
		if notification.Method == appwire.NotifyAgentMessageDelta {
			deltas++
		}
	}

	answers := 0
	for _, turn := range rejoined.Snapshot() {
		for _, item := range turn.Items {
			if strings.Contains(item.Text, "answer") {
				answers++
			}
		}
	}
	if answers != 1 {
		t.Fatalf("assistant answer reduced into %d items, want exactly 1\nturns: %+v", answers, rejoined.Snapshot())
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
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	writeTranscriptPairs(t, path, 1)

	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)

	stream := func(deltas []string) {
		for _, delta := range deltas {
			srv.RecordAppEvent(events.SessionEvent{
				Kind:      events.EventAssistantTextDelta,
				SessionID: "th_1",
				Data:      events.AssistantTextDeltaData{Delta: delta},
			})
		}
	}

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "question"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{Model: "gpt-5.5"},
	})

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
			ItemsView:    "full",
			TurnLimit:    40,
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

	// The turn is deliberately left streaming. An ASSISTANT_TEXT_END settle
	// stamp carries the whole accumulated answer, so completing the item would
	// overwrite whatever the reload had reduced and heal a gap this test exists
	// to catch -- and "while it's working" is the state the kata reports.
	stream(afterRead)

	reloaded := &appTurnSnapshot{threadID: "th_1"}
	reloaded.Seed(got.response.Thread.Turns)
	reloaded.Apply(srv.AppNotificationsAfter(cut, "th_1"))

	// Only the LIVE turn is under test here; the seeded transcript contributes
	// an older assistant turn of its own, which the whole-snapshot comparison
	// below covers.
	var wantParts []string
	for _, deltas := range [][]string{beforeRead, atTheGate, afterRead} {
		wantParts = append(wantParts, deltas...)
	}
	wantText := strings.Join(wantParts, "")
	reloadedTurns := reloaded.Snapshot()
	liveTurn := reloadedTurns[len(reloadedTurns)-1]
	gotText := ""
	assistantItems := 0
	for _, item := range liveTurn.Items {
		if item.Type != "agentMessage" {
			continue
		}
		assistantItems++
		gotText = item.Text
	}
	if assistantItems != 1 {
		t.Fatalf("reload reduced the streaming answer into %d agentMessage items on turn %s, want exactly 1\nturn: %+v",
			assistantItems, liveTurn.ID, liveTurn)
	}
	if gotText != wantText {
		t.Fatalf("streamed text across the reload =\n  %q\nwant\n  %q", gotText, wantText)
	}

	uninterrupted := srv.appAllTurns("th_1")
	if !reflect.DeepEqual(reloadedTurns, uninterrupted) {
		t.Fatalf("reloaded state diverged from the never-reloaded snapshot\n reloaded: %+v\n   direct: %+v", reloadedTurns, uninterrupted)
	}
}

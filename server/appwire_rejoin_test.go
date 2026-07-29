package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
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
	srv.SetFailedToolCallsFunc(func() (int, bool) {
		once.Do(func() {
			close(projected)
			<-release
		})
		return 0, true
	})

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
	if len(records) != 2 {
		t.Fatalf("committed notifications = %d, want item completion and later delta", len(records))
	}
	if records[0].Notification.Method != appwire.NotifyItemCompleted ||
		records[1].Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf(
			"commit order = [%s, %s], want [%s, %s]",
			records[0].Notification.Method,
			records[1].Notification.Method,
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
	srv.SetQueueDepthFunc(sess.QueueDepth)
	srv.SetQueuePreviewFunc(sess.QueuePreview)
	srv.SetQueueIDsFunc(sess.QueueIDs)
	srv.SetQueueTextsFunc(sess.QueueTexts)
	srv.SetClientMutationProjectionFunc(sess.ClientMutationProjection)

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
			srv.SetAppIdentity("local", sess.ID())
			srv.SetTranscriptPathFunc(sess.TranscriptPath)
			srv.SetClientMutationProjectionFunc(sess.ClientMutationProjection)
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

	records := srv.AppNotificationsAfter(0, "authoritative")
	if len(records) != 1 {
		t.Fatalf("authoritative notifications = %d, want 1", len(records))
	}
	if records[0].ThreadID != "authoritative" {
		t.Fatalf("record thread ID = %q, want authoritative", records[0].ThreadID)
	}
	var params appwire.AgentMessageDeltaParams
	if err := json.Unmarshal(records[0].Notification.Params, &params); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if params.ThreadID != "authoritative" || params.Ref != "local:authoritative" {
		t.Fatalf("notification target = (%q, %q), want authoritative identity", params.ThreadID, params.Ref)
	}
}

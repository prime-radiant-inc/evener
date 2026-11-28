package server

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestServerAppWireReadReplaysRecordedReasoningWithTerminalIdentity(t *testing.T) {
	answer := llm.Assistant("answer")
	answer.Content = append([]llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "first thought"}}}, answer.Content...)
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_reasoning",
		schema.NewTurn(schema.TurnUserInput, llm.User("question")),
		schema.NewTurn(schema.TurnAssistant, answer),
	)
	client := dialServerAppWire(t, st.srv)
	read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_reasoning", IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", read.Thread.Turns)
	}
	turn := read.Thread.Turns[0]
	if turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("turn status %q, want completed", turn.Status)
	}
	if len(turn.Items) != 3 {
		t.Fatalf("items=%+v", turn.Items)
	}
	var reasoningCount, answerCount int
	for _, item := range turn.Items {
		switch item.Type {
		case "reasoning":
			reasoningCount++
			if item.TurnID != turn.ID || item.ID == "" || item.Text != "first thought" || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("reasoning=%+v", item)
			}
		case "agentMessage":
			answerCount++
			if item.TurnID != turn.ID || item.ID == "" || item.Text != "answer" || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("answer=%+v", item)
			}
		}
	}
	if reasoningCount != 1 || answerCount != 1 {
		t.Fatalf("counts reasoning=%d answer=%d", reasoningCount, answerCount)
	}
}

func TestServerAppWireExecutionStartedConsumesPendingIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_execution_carrier")
	srv.SetProcessingTurn("turn_steer")
	cursor := srv.appNotifier.CurrentSequence()

	srv.RecordAppEvent(threadEvent("th_execution_carrier", events.ExecutionStartedData{TurnID: "turn_steer"}))
	srv.RecordAppEvent(threadEvent("th_execution_carrier", events.ExecutionEndedData{TurnID: "turn_steer", Status: "completed"}))
	// The lossless consumer can project completion before the input runner
	// returns and clears processing. Clients still need the terminal frame.
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_execution_carrier", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}}, nil)

	notifications := srv.AppNotificationsAfter(cursor, "th_execution_carrier")
	var sawIdle bool
	for _, notification := range notifications {
		if notification.Notification.Method == appwire.NotifyThreadStatusChanged {
			var params appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(notification.Notification.Params, &params); err == nil && params.Status.Type == appwire.ThreadStatusIdle {
				sawIdle = true
			}
		}
	}
	if !sawIdle {
		t.Fatalf("terminal notifications=%+v, want idle status", notifications)
	}

	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_execution_carrier"})
	if read.Thread.Evener.ActiveTurnID != "" || read.Thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("read state=(active %q, status %q), want empty/idle", read.Thread.Evener.ActiveTurnID, read.Thread.Status.Type)
	}
}

func TestServerAppWireCleanupBeforeSessionEndDoesNotLeaveLateCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_late_cleanup")
	srv.SetProcessingTurn("turn_abandoned")
	srv.SetProcessing(false)
	cursor := srv.appNotifier.CurrentSequence()

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_late_cleanup", Data: events.SessionEndData{State: "idle"}})

	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_late_cleanup"}).Thread
	if read.Status.Type != appwire.ThreadStatusIdle || read.Evener.ActiveTurnID != "" {
		t.Fatalf("cleanup-before-end read=(status %q, active %q), want idle/empty", read.Status.Type, read.Evener.ActiveTurnID)
	}
	for _, notification := range srv.AppNotificationsAfter(cursor, "th_late_cleanup") {
		if notification.Notification.Method == appwire.NotifyThreadStatusChanged {
			var params appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(notification.Notification.Params, &params); err != nil {
				t.Fatalf("decode status: %v", err)
			}
			if params.Status.Type == appwire.ThreadStatusIdle {
				return
			}
		}
	}
	t.Fatal("cleanup-before-end emitted no idle status notification")
}

func TestServerAppWireOldCarrierDoesNotConsumeNewPendingIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_old_carrier")
	srv.SetProcessingTurn("turn_old")
	srv.SetProcessingTurn("turn_new")
	srv.RecordAppEvent(threadEvent("th_old_carrier", events.ExecutionStartedData{TurnID: "turn_old"}))
	srv.mu.RLock()
	pending := srv.appPendingStableTurnID
	srv.mu.RUnlock()
	if pending != "turn_new" {
		t.Fatalf("the old execution's start consumed the pending identity; pending = %q", pending)
	}
	srv.RecordAppEvent(threadEvent("th_old_carrier", events.ExecutionStartedData{TurnID: "turn_new"}))

	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_old_carrier"}).Thread
	if read.Status.Type != appwire.ThreadStatusActive || read.Evener.ActiveTurnID != "turn_new" {
		t.Fatalf("after old and new carriers read=(status %q, active %q), want active/turn_new", read.Status.Type, read.Evener.ActiveTurnID)
	}
}

func TestServerAppWireAbandonedCarrierReplaysDeferredTerminalStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_abandoned_carrier")
	client := dialServerAppWire(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_abandoned_carrier", Subscribe: true}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	srv.SetProcessingTurn("abandoned-turn")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_abandoned_carrier", Data: events.SessionEndData{Reason: "turn_failed", State: "idle"}}, nil)
	srv.SetProcessing(false)

	deadline := time.After(time.Second)
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var params appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode status: %v", err)
			}
			if params.Status.Type == appwire.ThreadStatusIdle {
				read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_abandoned_carrier"})
				if err != nil {
					t.Fatalf("read after deferred terminal status: %v", err)
				}
				if read.Thread.Status.Type != appwire.ThreadStatusIdle || read.Thread.Evener.ActiveTurnID != "" {
					t.Fatalf("read after deferred terminal status=(status %q, active %q), want idle/empty", read.Thread.Status.Type, read.Thread.Evener.ActiveTurnID)
				}
				return
			}
		case <-deadline:
			t.Fatal("subscribed client received no idle terminal status after abandoned carrier")
		}
	}
}

func TestServerAppWireCarrierDiscardsDeferredPriorTerminalStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_carrier_wins")
	srv.SetProcessingTurn("new-turn")
	cursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_carrier_wins", Data: events.SessionEndData{Reason: "turn_failed", State: "idle"}}, nil)
	srv.RecordAppEvent(threadEvent("th_carrier_wins", events.ExecutionStartedData{TurnID: "new-turn"}))

	for _, notification := range srv.AppNotificationsAfter(cursor, "th_carrier_wins") {
		if notification.Notification.Method == appwire.NotifyThreadClosed {
			t.Fatalf("deferred prior terminal closed the new carrier: %+v", notification)
		}
		if notification.Notification.Method != appwire.NotifyThreadStatusChanged {
			continue
		}
		var params appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(notification.Notification.Params, &params); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if params.Status.Type == appwire.ThreadStatusIdle {
			t.Fatalf("deferred prior terminal idled the new carrier: %+v", params)
		}
	}

	// Processing ending is the new execution's own end: exactly one idle.
	cursor = srv.appNotifier.CurrentSequence()
	srv.SetProcessing(false)
	idles := 0
	for _, notification := range srv.AppNotificationsAfter(cursor, "th_carrier_wins") {
		var params appwire.ThreadStatusChangedParams
		if notification.Notification.Method == appwire.NotifyThreadStatusChanged && json.Unmarshal(notification.Notification.Params, &params) == nil && params.Status.Type == appwire.ThreadStatusIdle {
			idles++
		}
	}
	if idles != 1 {
		t.Fatalf("processing end published %d idle statuses, want 1", idles)
	}
}

func TestServerAppWireAbandonedCarrierReplaysDeferredClosedStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_abandoned_closed")
	client := dialServerAppWire(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_abandoned_closed", Subscribe: true}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	srv.SetProcessingTurn("abandoned-closed-turn")
	// An interrupted SESSION_END is the shape that genuinely defers: an
	// uninterrupted closed one closes the session in the same commit and
	// publishes its terminal frames immediately (sessionEventClosesSession).
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_abandoned_closed", Data: events.SessionEndData{Interrupted: true, State: "closed", Reason: "interrupted"}})
	srv.SetProcessing(false)

	deadline := time.After(time.Second)
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyThreadClosed {
				continue
			}
			var params map[string]any
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode closed params: %v", err)
			}
			if params["threadId"] != "th_abandoned_closed" {
				t.Fatalf("deferred closed threadId=%v, want th_abandoned_closed", params["threadId"])
			}
			if params["reason"] != "interrupted" {
				t.Fatalf("deferred closed reason=%v, want interrupted", params["reason"])
			}
			read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_abandoned_closed"})
			if err != nil {
				t.Fatalf("read after deferred closed status: %v", err)
			}
			if read.Thread.Status.Type != appwire.ThreadStatusClosed {
				t.Fatalf("read after deferred closed status=%q, want closed", read.Thread.Status.Type)
			}
			return
		case <-deadline:
			t.Fatal("subscribed client received no deferred thread/closed notification")
		}
	}
}

// A malformed entry in the deferred terminal list must not be published: the
// replay validates every entry, so only the well-formed closed frame the
// projector deferred reaches subscribers.
func TestServerAppWireDropsMalformedDeferredTerminalNotification(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_malformed_deferred")
	srv.SetProcessingTurn("malformed-deferred-turn")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_malformed_deferred", Data: events.SessionEndData{Interrupted: true, State: "closed", Reason: "interrupted"}})

	srv.mu.Lock()
	rootThreadID, rootRef := srv.appRootIdentityLocked()
	if len(srv.appDeferredTerminalNotifications) == 0 {
		srv.mu.Unlock()
		t.Fatal("the interrupted SESSION_END deferred no terminal notification")
	}
	srv.appDeferredTerminalNotifications = append(srv.appDeferredTerminalNotifications, pendingAppNotification{
		threadID: rootThreadID,
		ref:      rootRef,
		method:   appwire.NotifyThreadClosed,
		params: map[string]any{
			// Missing "ref" is what makes the frame malformed. The bogus marker
			// also rides in "reason" because the egress target stamp rewrites
			// threadId (and adds ref) on a map, so a bogus threadId alone would
			// not survive into the published bytes.
			"threadId": "bogus-malformed-thread",
			"reason":   "bogus-malformed-thread",
		},
	})
	srv.mu.Unlock()

	cursor := srv.appNotifier.CurrentSequence()
	srv.SetProcessing(false)

	closed := 0
	for _, record := range srv.AppNotificationsAfter(cursor, "th_malformed_deferred") {
		if bytes.Contains(record.Notification.Params, []byte("bogus-malformed-thread")) {
			t.Fatalf("published a malformed deferred terminal notification: %s %s", record.Notification.Method, record.Notification.Params)
		}
		if record.Notification.Method != appwire.NotifyThreadClosed {
			continue
		}
		closed++
		params := notificationParams[map[string]any](t, record)
		if params["threadId"] != rootThreadID || params["reason"] != "interrupted" {
			t.Fatalf("deferred closed params=%v, want threadId %q reason interrupted", params, rootThreadID)
		}
	}
	if closed != 1 {
		t.Fatalf("published %d thread/closed notifications, want only the valid deferred one", closed)
	}
}

func TestAppDeferredTerminalParams(t *testing.T) {
	const threadID = "th_deferred_params"
	const ref = "local:th_deferred_params"
	status := appwire.ThreadStatusChangedParams{
		ThreadID: threadID,
		Ref:      ref,
		Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
	}
	closedTyped := appwire.ThreadClosedParams{ThreadID: threadID, Ref: ref, Reason: "interrupted"}
	for _, tc := range []struct {
		name   string
		method string
		params any
		wantOK bool
	}{
		{name: "status struct", method: appwire.NotifyThreadStatusChanged, params: status, wantOK: true},
		{name: "status struct missing threadId", method: appwire.NotifyThreadStatusChanged, params: appwire.ThreadStatusChangedParams{Ref: ref, Status: status.Status}},
		{name: "status struct missing ref", method: appwire.NotifyThreadStatusChanged, params: appwire.ThreadStatusChangedParams{ThreadID: threadID, Status: status.Status}},
		{name: "status struct missing status type", method: appwire.NotifyThreadStatusChanged, params: appwire.ThreadStatusChangedParams{ThreadID: threadID, Ref: ref}},
		{name: "closed map", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": threadID, "ref": ref, "reason": "interrupted"}, wantOK: true},
		{name: "closed map empty reason", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": threadID, "ref": ref, "reason": ""}, wantOK: true},
		{name: "closed typed", method: appwire.NotifyThreadClosed, params: closedTyped, wantOK: true},
		{name: "closed typed missing threadId", method: appwire.NotifyThreadClosed, params: appwire.ThreadClosedParams{Ref: ref, Reason: "interrupted"}},
		{name: "closed typed missing ref", method: appwire.NotifyThreadClosed, params: appwire.ThreadClosedParams{ThreadID: threadID, Reason: "interrupted"}},
		{name: "closed map missing threadId", method: appwire.NotifyThreadClosed, params: map[string]any{"ref": ref, "reason": "interrupted"}},
		{name: "closed map empty threadId", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": "", "ref": ref, "reason": "interrupted"}},
		{name: "closed map missing ref", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": threadID, "reason": "interrupted"}},
		{name: "closed map empty ref", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": threadID, "ref": "", "reason": "interrupted"}},
		{name: "closed map non-string reason", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": threadID, "ref": ref, "reason": 7}},
		{name: "closed map missing reason", method: appwire.NotifyThreadClosed, params: map[string]any{"threadId": threadID, "ref": ref}},
		{name: "status method carrying a closed map", method: appwire.NotifyThreadStatusChanged, params: map[string]any{"threadId": threadID, "ref": ref, "reason": "interrupted"}},
		{name: "closed method carrying a status struct", method: appwire.NotifyThreadClosed, params: status},
		{name: "unknown method", method: "thread/started", params: status},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appDeferredTerminalParamsValid(tc.method, tc.params); got != tc.wantOK {
				t.Fatalf("appDeferredTerminalParamsValid(%q, %#v)=%v, want %v", tc.method, tc.params, got, tc.wantOK)
			}
		})
	}
}

func TestServerAppWireAbandonedCarrierUsesStatusIdentityBeforeAppIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
	}{
		{name: "idle", state: "idle"},
		{name: "closed", state: "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const threadID = "status-only-abandoned"
			srv := NewServer(ServerConfig{})
			seedStatus(srv, StatusInfo{SessionID: threadID, State: "active"})
			client := dialServerAppWire(t, srv)
			if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + threadID, Subscribe: true}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			srv.SetProcessingTurn("abandoned-turn")
			BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: threadID, Data: events.SessionEndData{Reason: "abandoned", State: tc.state}}, nil)
			srv.SetProcessing(false)

			deadline := time.After(time.Second)
			for {
				select {
				case notification := <-client.Notifications():
					if (tc.state == "idle" && notification.Method != appwire.NotifyThreadStatusChanged) ||
						(tc.state == "closed" && notification.Method != appwire.NotifyThreadClosed) {
						continue
					}
					if tc.state == "idle" {
						var params appwire.ThreadStatusChangedParams
						if err := json.Unmarshal(notification.Params, &params); err != nil {
							t.Fatalf("decode status: %v", err)
						}
						if params.Status.Type != appwire.ThreadStatusIdle {
							continue
						}
					}
					read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + threadID})
					if err != nil {
						t.Fatalf("read after deferred %s status: %v", tc.state, err)
					}
					if read.Thread.Status.Type != tc.state {
						t.Fatalf("read status=%q, want %q", read.Thread.Status.Type, tc.state)
					}
					return
				case <-deadline:
					t.Fatalf("status-only identity received no deferred %s notification", tc.state)
				}
			}
		})
	}
}

// TestServerAppWireConcurrentCarrierAndCleanupPublishOneTerminal races the
// published execution's EXECUTION_STARTED against the end of processing while
// the input before it has a deferred terminal status: whichever wins, the
// thread ends with no active turn, exactly one terminal naming the cleared
// turn (the settlement finishProcessing emits beside the deferred replay),
// and the deferred frame — when it publishes — naming only the older turn.
func TestServerAppWireConcurrentCarrierAndCleanupPublishOneTerminal(t *testing.T) {
	for _, state := range []string{"idle", "closed"} {
		t.Run(state, func(t *testing.T) {
			for attempt := range 1000 {
				const threadID = "concurrent-carrier"
				srv := NewServer(ServerConfig{})
				setAppIdentity(t, srv, "local", threadID)
				srv.SetProcessingTurn("new-turn")
				cursor := srv.appNotifier.CurrentSequence()
				BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: threadID, Data: events.SessionEndData{State: state}}, nil)
				start := make(chan struct{})
				var finished sync.WaitGroup
				finished.Add(2)
				go func() {
					defer finished.Done()
					<-start
					srv.SetProcessing(false)
				}()
				go func() {
					defer finished.Done()
					<-start
					srv.RecordAppEvent(threadEvent(threadID, events.ExecutionStartedData{TurnID: "new-turn"}))
				}()
				close(start)
				finished.Wait()
				read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:" + threadID}).Thread
				if read.Evener.ActiveTurnID != "" {
					t.Fatalf("attempt %d: read active turn %q after processing ended", attempt, read.Evener.ActiveTurnID)
				}
				terminals := 0
				cleared := 0
				for _, record := range srv.AppNotificationsAfter(cursor, threadID) {
					var params appwire.ThreadStatusChangedParams
					if record.Notification.Method == appwire.NotifyThreadStatusChanged && json.Unmarshal(record.Notification.Params, &params) == nil && params.Status.Type == state {
						terminals++
						if params.EndedTurnID == "new-turn" {
							cleared++
						}
					}
				}
				if state == "idle" {
					if cleared != 1 {
						t.Fatalf("attempt %d: %d %s statuses name the cleared turn, want exactly 1", attempt, cleared, state)
					}
					if terminals > 2 {
						t.Fatalf("attempt %d: %d %s statuses published, want the settlement and at most the deferred replay", attempt, terminals, state)
					}
				} else if terminals != 1 {
					// A closing end drops every deferred frame and clears
					// processing inside its own commit, so the projector's own
					// closed status is the one terminal and no settlement
					// fires: the closed frame owns the thread's end the way
					// turn/completed does, not as a turn's Stop.
					t.Fatalf("attempt %d: %d %s statuses published, want the closed terminal alone", attempt, terminals, state)
				}
			}
		})
	}
}

func TestServerAppWireClosedTerminalRejectsLateCarriers(t *testing.T) {
	for _, turnID := range []string{"accepted-turn", "stale-turn"} {
		srv := NewServer(ServerConfig{})
		setAppIdentity(t, srv, "local", "closed-late-carrier")
		srv.SetProcessingTurn("accepted-turn")
		BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "closed-late-carrier", Data: events.SessionEndData{State: "closed"}}, nil)
		BridgeEvent(srv, threadEvent("closed-late-carrier", events.ExecutionStartedData{TurnID: turnID}), nil)
		read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:closed-late-carrier"}).Thread
		if read.Status.Type != appwire.ThreadStatusClosed || read.Evener.ActiveTurnID != "" {
			t.Fatalf("turn=%q read=(%q,%q), want closed/empty", turnID, read.Status.Type, read.Evener.ActiveTurnID)
		}
	}
}

func TestServerAppWireIdentityReplacementDiscardsDeferredPriorTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		newID  string
		newRef string
	}{
		{name: "different ref", newID: "replacement", newRef: "local:replacement"},
		{name: "same ref new projection", newID: "old", newRef: "local:old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			setAppIdentity(t, srv, "local", "old")
			srv.SetProcessingTurn("pending-old")
			cursor := srv.appNotifier.CurrentSequence()
			BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "old", Data: events.SessionEndData{Reason: "turn_failed", State: "idle"}}, nil)

			prepared, err := PrepareAppIdentityForRef("local", tc.newID, tc.newRef, "")
			if err != nil {
				t.Fatalf("PrepareAppIdentityForRef: %v", err)
			}
			srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
			srv.SetState("awaiting")
			srv.SetProcessing(false)

			read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: tc.newRef})
			if read.Thread.Status.Type != appwire.ThreadStatusAwaiting || read.Thread.Evener.ActiveTurnID != "" {
				t.Fatalf("replacement read=(status %q, active %q), want awaiting/empty", read.Thread.Status.Type, read.Thread.Evener.ActiveTurnID)
			}
			for _, notification := range srv.AppNotificationsAfter(cursor, tc.newRef) {
				var params appwire.ThreadStatusChangedParams
				retiredStatus := notification.Notification.Method == appwire.NotifyThreadStatusChanged &&
					(json.Unmarshal(notification.Notification.Params, &params) != nil || params.Status.Type != appwire.ThreadStatusAwaiting)
				if retiredStatus || notification.Notification.Method == appwire.NotifyThreadClosed {
					t.Fatalf("retired terminal notification crossed replacement boundary: %+v", notification)
				}
			}
		})
	}
}

// A SESSION_END still queued from the input before a published execution
// publishes no thread state over it; the execution's own EXECUTION_STARTED
// and SESSION_END then carry the thread to active and back to idle.
func TestServerAppWireQueuedSessionEndDoesNotEndThePublishedExecution(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_reasoning_boundary")
	srv.SetProcessingTurn("turn_new")
	queuedSessionEndCursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_reasoning_boundary", Data: events.SessionEndData{State: "idle"}}, nil)
	for _, notification := range srv.AppNotificationsAfter(queuedSessionEndCursor, "th_reasoning_boundary") {
		if notification.Notification.Method == appwire.NotifyThreadStatusChanged || notification.Notification.Method == appwire.NotifyThreadClosed {
			t.Fatalf("queued session end published stale thread state: %+v", notification)
		}
	}
	queuedSessionEndRead := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary"})
	if got := queuedSessionEndRead.Thread.Evener.ActiveTurnID; got != "turn_new" {
		t.Fatalf("durable active turn after queued session end = %q, want turn_new", got)
	}
	if got := queuedSessionEndRead.Thread.Status.Type; got != appwire.ThreadStatusActive {
		t.Fatalf("status after queued session end = %q, want active", got)
	}
	srv.RecordAppEvent(threadEvent("th_reasoning_boundary", events.ExecutionStartedData{TurnID: "turn_new"}))
	if got := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary"}).Thread.Status.Type; got != appwire.ThreadStatusActive {
		t.Fatalf("status after the execution started = %q, want active", got)
	}
	completedCursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_reasoning_boundary", Data: events.SessionEndData{State: "idle"}}, nil)
	completedNotifications := srv.AppNotificationsAfter(completedCursor, "th_reasoning_boundary")
	var sawCompletedStatus bool
	for _, notification := range completedNotifications {
		if notification.Notification.Method != appwire.NotifyThreadStatusChanged {
			continue
		}
		var params appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(notification.Notification.Params, &params); err == nil && params.Status.Type == appwire.ThreadStatusIdle {
			sawCompletedStatus = true
		}
	}
	if !sawCompletedStatus {
		t.Fatalf("new completion notifications=%+v, want idle thread status", completedNotifications)
	}
	completedRead := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary"})
	if completedRead.Thread.Status.Type != appwire.ThreadStatusIdle || completedRead.Thread.Evener.ActiveTurnID != "" {
		t.Fatalf("completed new state=(%q, %q), want idle/empty", completedRead.Thread.Status.Type, completedRead.Thread.Evener.ActiveTurnID)
	}
}

func TestServerAppWireUnclaimedStableTurnDoesNotKeepSessionBusy(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_unclaimed")
	// The execution is published before its claim; a failed claim clears
	// processing without the execution ever starting.
	srv.SetProcessingTurn("turn_unclaimed")
	srv.SetProcessing(false)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_unclaimed", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_unclaimed"})
	if read.Thread.Evener.ActiveTurnID != "" || read.Thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("read = (active %q, status %q), want idle after the failed claim", read.Thread.Evener.ActiveTurnID, read.Thread.Status.Type)
	}
}

func TestServerAppWireFailedClaimSessionEndClearsState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_failed_claim")
	srv.SetProcessingTurn("turn_unclaimed")
	srv.SetProcessing(false)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_failed_claim", Data: events.SessionEndData{State: "idle"}}, nil)
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_failed_claim"})
	if read.Thread.Status.Type != appwire.ThreadStatusIdle || read.Thread.Evener.ActiveTurnID != "" {
		t.Fatalf("failed claim state=(%q, %q), want idle/empty", read.Thread.Status.Type, read.Thread.Evener.ActiveTurnID)
	}
}

func TestServerAppWireQueuedClosedSessionEndClosesThePublishedExecution(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_closed_boundary")
	srv.SetProcessingTurn("turn_new")
	cursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_closed_boundary", Data: events.SessionEndData{State: "closed"}}, nil)
	var sawThreadClosed bool
	for _, notification := range srv.AppNotificationsAfter(cursor, "th_closed_boundary") {
		if notification.Notification.Method == appwire.NotifyThreadClosed {
			sawThreadClosed = true
		}
	}
	if !sawThreadClosed {
		t.Fatal("a closing session end did not close the thread")
	}
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_closed_boundary"}).Thread
	if read.Status.Type != appwire.ThreadStatusClosed || read.Evener.ActiveTurnID != "" {
		t.Fatalf("closed queued read=(%q,%q), want closed/empty", read.Status.Type, read.Evener.ActiveTurnID)
	}
}

// lastStatus is the newest thread/status/changed broadcast for threadID.
func lastStatus(t *testing.T, srv *Server, threadID string) string {
	t.Helper()
	statuses := statusNotifications(t, srv, threadID)
	if len(statuses) == 0 {
		t.Fatalf("no status broadcast for %s", threadID)
	}
	return statuses[len(statuses)-1].Status.Type
}

// A turn's end is still on the feed when serve finishes the pass that ran it:
// idle stands in until it lands, never the rest from before the turn.
func TestServerAppWireFinishBeforeATurnsEndPublishesIdleNotThePriorRest(t *testing.T) {
	for _, prior := range []string{appwire.ThreadStatusAwaiting, appwire.ThreadStatusSystemError} {
		srv := NewServer(ServerConfig{})
		setAppIdentity(t, srv, "local", "th_prior_"+prior)
		srv.SetState(prior)
		srv.SetProcessing(true)
		srv.SetProcessingTurn("t1")
		srv.SetProcessing(false)
		if got := lastStatus(t, srv, "th_prior_"+prior); got != appwire.ThreadStatusIdle {
			t.Fatalf("prior %s: finish broadcast %q, want the idle placeholder", prior, got)
		}
		if read := readThreadOverWire(t, srv, "local:th_prior_"+prior); read.Status.Type != appwire.ThreadStatusIdle {
			t.Fatalf("prior %s: read %q, want idle until the turn's end", prior, read.Status.Type)
		}
	}
}

// A pass that ran no turn leaves the session's state as its events stated it:
// a refused input over an awaiting rest still reads and broadcasts awaiting.
func TestServerAppWireFinishOfAPassWithNoTurnPublishesTheStoredRest(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_no_turn")
	srv.SetState(appwire.ThreadStatusAwaiting)
	srv.SetProcessing(true)
	srv.SetProcessing(false)
	if got := lastStatus(t, srv, "th_no_turn"); got != appwire.ThreadStatusAwaiting {
		t.Fatalf("finish broadcast %q, want awaiting", got)
	}
	if read := readThreadOverWire(t, srv, "local:th_no_turn"); read.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("read %q, want awaiting", read.Status.Type)
	}
}

// An interrupted turn's end states the resting state while the turn still
// unwinds; the finish publishes that state, not the placeholder, and a read
// agrees.
func TestServerAppWireFinishAfterAnInterruptedEndPublishesItsState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_interrupted_rest")
	srv.SetState(appwire.ThreadStatusIdle)
	srv.SetProcessing(true)
	srv.SetProcessingTurn("t1")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventExecutionStarted, SessionID: "th_interrupted_rest", Data: events.ExecutionStartedData{TurnID: "t1"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_interrupted_rest", Data: events.SessionEndData{Reason: "interrupted", State: appwire.ThreadStatusSystemError, Interrupted: true}}, nil)
	srv.SetProcessing(false)
	if read := readThreadOverWire(t, srv, "local:th_interrupted_rest"); read.Status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("read %q, want the interrupted end's systemError", read.Status.Type)
	}
	if got := lastStatus(t, srv, "th_interrupted_rest"); got != appwire.ThreadStatusSystemError {
		t.Fatalf("finish broadcast %q, want the interrupted end's systemError", got)
	}
}

// Nothing runs once processing ends, so a stored active, a forecast that
// work would follow (an interrupted end's, or a turn end's with notifications
// waiting), reads idle, stored and published alike, even when the pass ran no
// turn to restate it.
func TestServerAppWireFinishResolvesASpentActiveForecast(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_interrupted")
	srv.SetProcessing(true)
	srv.SetProcessingTurn("t1")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventExecutionStarted, SessionID: "th_interrupted", Data: events.ExecutionStartedData{TurnID: "t1"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_interrupted", Data: events.SessionEndData{Reason: "interrupted", State: appwire.ThreadStatusActive, Interrupted: true}}, nil)
	srv.SetProcessing(false)
	read := readThreadOverWire(t, srv, "local:th_interrupted")
	if read.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("read %q, want the spent forecast read as idle", read.Status.Type)
	}
	if got := lastStatus(t, srv, "th_interrupted"); got != appwire.ThreadStatusIdle {
		t.Fatalf("finish broadcast %q, want idle", got)
	}

	// A later pass that ran no turn (a notification wake the session filters
	// out) resolves a forecast its turn's end left stored.
	srv.SetState(appwire.ThreadStatusActive)
	srv.SetProcessing(true)
	srv.SetProcessing(false)
	if read := readThreadOverWire(t, srv, "local:th_interrupted"); read.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("after a no-op pass, read %q, want idle", read.Status.Type)
	}
}

// A turn published after an interrupted one starts with its end unstated, so
// a finish before its end lands publishes the idle placeholder, not the
// interrupted turn's state.
func TestServerAppWireANewTurnsEndStartsUnstated(t *testing.T) {
	srv := NewServer(ServerConfig{})
	setAppIdentity(t, srv, "local", "th_two_turns")
	srv.SetProcessing(true)
	srv.SetProcessingTurn("a")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventExecutionStarted, SessionID: "th_two_turns", Data: events.ExecutionStartedData{TurnID: "a"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_two_turns", Data: events.SessionEndData{Reason: "interrupted", State: appwire.ThreadStatusSystemError, Interrupted: true}}, nil)
	srv.SetProcessingTurn("b")
	srv.SetProcessing(false)
	if got := lastStatus(t, srv, "th_two_turns"); got != appwire.ThreadStatusIdle {
		t.Fatalf("finish broadcast %q, want the idle placeholder for turn b", got)
	}
}

package server

import (
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
	srv.SetAppIdentity("local", "th_execution_carrier")
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
	srv.SetAppIdentity("local", "th_late_cleanup")
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
	srv.SetAppIdentity("local", "th_old_carrier")
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
	srv.SetAppIdentity("local", "th_abandoned_carrier")
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
	srv.SetAppIdentity("local", "th_carrier_wins")
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
	srv.SetAppIdentity("local", "th_abandoned_closed")
	client := dialServerAppWire(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_abandoned_closed", Subscribe: true}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	srv.SetProcessingTurn("abandoned-closed-turn")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_abandoned_closed", Data: events.SessionEndData{Reason: "session_closed", State: "closed"}})
	srv.SetProcessing(false)

	deadline := time.After(time.Second)
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyThreadClosed {
				continue
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
			srv.SetStatus(StatusInfo{SessionID: threadID, State: "active"})
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
// thread ends with exactly one terminal status and no active turn.
func TestServerAppWireConcurrentCarrierAndCleanupPublishOneTerminal(t *testing.T) {
	for _, state := range []string{"idle", "closed"} {
		t.Run(state, func(t *testing.T) {
			for attempt := range 1000 {
				const threadID = "concurrent-carrier"
				srv := NewServer(ServerConfig{})
				srv.SetAppIdentity("local", threadID)
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
				for _, record := range srv.AppNotificationsAfter(cursor, threadID) {
					var params appwire.ThreadStatusChangedParams
					if record.Notification.Method == appwire.NotifyThreadStatusChanged && json.Unmarshal(record.Notification.Params, &params) == nil && params.Status.Type == state {
						terminals++
					}
				}
				if terminals != 1 {
					t.Fatalf("attempt %d: %d %s statuses published, want 1", attempt, terminals, state)
				}
			}
		})
	}
}

func TestServerAppWireClosedTerminalRejectsLateCarriers(t *testing.T) {
	for _, turnID := range []string{"accepted-turn", "stale-turn"} {
		srv := NewServer(ServerConfig{})
		srv.SetAppIdentity("local", "closed-late-carrier")
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
			srv.SetAppIdentity("local", "old")
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
	srv.SetAppIdentity("local", "th_reasoning_boundary")
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
	srv.SetAppIdentity("local", "th_unclaimed")
	// The execution is published before its claim; a failed claim clears
	// processing without the execution ever starting.
	srv.SetProcessingTurn("turn_unclaimed")
	srv.SetProcessing(false)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_unclaimed", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_unclaimed"})
	if read.Thread.Evener.ActiveTurnID != "" || read.Thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("read = (active %q, status %q), want idle after the failed claim", read.Thread.Evener.ActiveTurnID, read.Thread.Status.Type)
	}
	srv.mu.RLock()
	reserved := srv.appReservedTurnID
	srv.mu.RUnlock()
	if reserved != "" {
		t.Fatalf("admission reservation=%q, want empty after failed claim", reserved)
	}
}

func TestServerAppWireFailedClaimSessionEndClearsState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_failed_claim")
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
	srv.SetAppIdentity("local", "th_closed_boundary")
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

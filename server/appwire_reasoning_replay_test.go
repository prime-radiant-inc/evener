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

// A resting status that settles after the next turn has been published as
// running never reaches subscribers: the running turn's own end restates the
// state.
func TestServerAppWireStatusSettledDuringATurnIsNotPublished(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_settled_late")
	srv.SetProcessingTurn("next-turn")
	// The rest settled before the turn started, but the bridge reaches it only
	// after serve published the turn, and serve can finish the turn before the
	// bridge reaches the turn's own events.
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_settled_late", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	if read := readThreadOverWire(t, srv, "local:th_settled_late"); read.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("read status = %q, want the running turn's active", read.Status.Type)
	}
	srv.SetProcessing(false)

	statuses := statusNotifications(t, srv, "th_settled_late")
	for _, status := range statuses {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast the stale awaiting over the running turn: %+v", statuses)
		}
	}
	if len(statuses) == 0 || statuses[len(statuses)-1].Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("statuses = %+v, want the turn's end to settle idle", statuses)
	}
}

// A quiet-period timer that fires after the session closed must not reopen it
// on the wire: no awaiting frame follows the closing SESSION_END.
func TestServerAppWireStatusSettledAfterCloseIsNotPublished(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_settled_closed")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_settled_closed", Data: events.SessionEndData{Reason: "session_closed", State: "closed"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_settled_closed", Data: events.StatusSettledData{State: "awaiting"}}, nil)

	for _, status := range statusNotifications(t, srv, "th_settled_closed") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast awaiting after the session closed: %+v", status)
		}
	}
}

// A rest that settles while an input is being taken, one that never starts a
// turn, is held and published when processing ends: the broadcast and a read
// both say awaiting.
func TestServerAppWireStatusSettledDuringARefusedPassIsPublishedAtItsEnd(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_refused_pass")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_refused_pass", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	for _, status := range statusNotifications(t, srv, "th_refused_pass") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast awaiting while the input was still being taken: %+v", status)
		}
	}
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_refused_pass"); read.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("read status = %q, want awaiting", read.Status.Type)
	}
	statuses := statusNotifications(t, srv, "th_refused_pass")
	if len(statuses) == 0 || statuses[len(statuses)-1].Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("statuses = %+v, want the pass's end to broadcast awaiting", statuses)
	}
}

// A held rest belongs to the session that settled it: a thread/clear that
// replaces the root before the pass ends leaves the new root's state alone.
func TestServerAppWireHeldStatusSettledDoesNotReachAReplacementRoot(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_old_root")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_old_root", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	srv.SetAppIdentity("local", "th_new_root")
	srv.SetState("idle")
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_new_root"); read.Status.Type == appwire.ThreadStatusAwaiting {
		t.Fatalf("read status = %q on the replacement root, want the old root's rest dropped", read.Status.Type)
	}
	for _, status := range statusNotifications(t, srv, "th_new_root") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast the old root's awaiting on the replacement: %+v", status)
		}
	}
}

// Closed wins over a held rest.
func TestServerAppWireHeldStatusSettledYieldsToAClose(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_held_closed")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_held_closed", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_held_closed", Data: events.SessionEndData{Reason: "session_closed", State: "closed"}}, nil)
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_held_closed"); read.Status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("read status = %q, want closed", read.Status.Type)
	}
	for _, status := range statusNotifications(t, srv, "th_held_closed") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast awaiting after the close: %+v", status)
		}
	}
}

// A rest held while an input was being taken is dropped once a turn is
// published, even when serve finishes that turn before the bridge reaches it.
func TestServerAppWireHeldStatusSettledDropsWhenATurnIsPublished(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_held_turn")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_held_turn", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	srv.SetProcessingTurn("t1")
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_held_turn"); read.Status.Type == appwire.ThreadStatusAwaiting {
		t.Fatalf("read status = %q, want the held rest dropped by the turn", read.Status.Type)
	}
	for _, status := range statusNotifications(t, srv, "th_held_turn") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast the held rest after a turn: %+v", status)
		}
	}
}

// An input that ends on its own SESSION_END states the session's state, so a
// rest held during it is dropped.
func TestServerAppWireHeldStatusSettledDropsAtTheInputsSessionEnd(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_held_end")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_held_end", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_held_end", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}}, nil)
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_held_end"); read.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("read status = %q, want the SESSION_END's idle", read.Status.Type)
	}
	for _, status := range statusNotifications(t, srv, "th_held_end") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast the held rest over the SESSION_END: %+v", status)
		}
	}
}

// A closed session holds nothing, even if an input is marked after the close.
func TestServerAppWireStatusSettledAfterACloseIsNotHeld(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_closed_then_marked")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_closed_then_marked", Data: events.SessionEndData{Reason: "session_closed", State: "closed"}}, nil)
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_closed_then_marked", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_closed_then_marked"); read.Status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("read status = %q, want closed", read.Status.Type)
	}
}

// A settle the bridge meets while a published turn is running is dropped, also
// once that turn's EXECUTION_STARTED has been bridged.
func TestServerAppWireStatusSettledDuringAStartedTurnIsNotHeld(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_started_turn")
	srv.SetProcessingTurn("t1")
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventExecutionStarted, SessionID: "th_started_turn", Data: events.ExecutionStartedData{TurnID: "t1"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_started_turn", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	srv.SetProcessing(false)

	if read := readThreadOverWire(t, srv, "local:th_started_turn"); read.Status.Type == appwire.ThreadStatusAwaiting {
		t.Fatalf("read status = %q, want the running turn's settle dropped", read.Status.Type)
	}
	for _, status := range statusNotifications(t, srv, "th_started_turn") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast a settle held over a started turn: %+v", status)
		}
	}
}

// An interrupted SESSION_END, which changes no status itself, still drops a
// rest held during the input it ends.
func TestServerAppWireHeldStatusSettledDropsAtAnInterruptedSessionEnd(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_held_interrupt")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_held_interrupt", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_held_interrupt", Data: events.SessionEndData{Reason: "interrupted", State: "idle", Interrupted: true}}, nil)
	srv.SetProcessing(false)

	for _, status := range statusNotifications(t, srv, "th_held_interrupt") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast the held rest after an interrupt: %+v", status)
		}
	}
}

// A finish that runs after an input's SESSION_END ended processing, but
// before the bridge projects that SESSION_END, does not apply the rest held
// during the input: the SESSION_END's own status effect drops it.
func TestServerAppWireFinishBetweenASessionEndsEffectAndItsProjectionDropsTheHold(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_end_window")
	srv.SetProcessing(true)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_end_window", Data: events.StatusSettledData{State: "awaiting"}}, nil)
	end := events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_end_window", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}}
	srv.applySessionEventStatus(end)
	srv.SetProcessing(false)
	srv.RecordAppEvent(end)

	if read := readThreadOverWire(t, srv, "local:th_end_window"); read.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("read status = %q, want the SESSION_END's idle", read.Status.Type)
	}
	for _, status := range statusNotifications(t, srv, "th_end_window") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			t.Fatalf("broadcast the held rest over the SESSION_END: %+v", status)
		}
	}
}

// A settle is applied, held or dropped in the same lock hold that decides its
// broadcast, so a finish racing the bridge never publishes it twice.
func TestServerAppWireHeldStatusSettledIsBroadcastOnce(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_once")
	srv.SetProcessing(true)
	settled := events.SessionEvent{Kind: events.EventStatusSettled, SessionID: "th_once", Data: events.StatusSettledData{State: "awaiting"}}
	srv.applySessionEventStatus(settled)
	srv.SetProcessing(false)
	srv.RecordAppEvent(settled)

	awaiting := 0
	for _, status := range statusNotifications(t, srv, "th_once") {
		if status.Status.Type == appwire.ThreadStatusAwaiting {
			awaiting++
		}
	}
	if awaiting != 1 {
		t.Fatalf("awaiting broadcast %d times, want once", awaiting)
	}
	if read := readThreadOverWire(t, srv, "local:th_once"); read.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("read status = %q, want awaiting", read.Status.Type)
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
		srv.SetAppIdentity("local", "th_prior_"+prior)
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
// a refused input over a needs_response rest still reads and broadcasts
// awaiting.
func TestServerAppWireFinishOfAPassWithNoTurnPublishesTheStoredRest(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_no_turn")
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
	srv.SetAppIdentity("local", "th_interrupted_rest")
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
	srv.SetAppIdentity("local", "th_interrupted")
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
	srv.SetAppIdentity("local", "th_two_turns")
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

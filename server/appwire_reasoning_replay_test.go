package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestServerAppWireReadReplaysProjectedReasoningWithTerminalIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_reasoning")
	client := dialServerAppWire(t, srv)
	ctx := context.Background()
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_reasoning", Data: events.UserInputData{Text: "question", StableTurnID: "turn_stable_reasoning"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning", Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "first "}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning", Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "thought"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_reasoning", Data: events.AssistantTextEndData{Text: "answer"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_reasoning", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_reasoning", IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", read.Thread.Turns)
	}
	turn := read.Thread.Turns[0]
	if turn.ID != "turn_stable_reasoning" || turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("turn=(id %q, status %q), want stable identity and completed", turn.ID, turn.Status)
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

func TestServerAppWireSteeringCarrierTurnStartedConsumesPendingIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_steering_carrier")
	srv.SetProcessingTurn("turn_steer")
	cursor := srv.appNotifier.CurrentSequence()

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTurnStarted, SessionID: "th_steering_carrier", Data: events.TurnStartedData{TurnID: "turn_steer"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSteeringInjected, SessionID: "th_steering_carrier", Data: events.SteeringInjectedData{Text: "steering payload", Source: events.SteeringSourceUser}})
	// The lossless consumer can project completion before the input runner
	// returns and clears processing. Clients still need the terminal frame.
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_steering_carrier", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}}, nil)

	notifications := srv.AppNotificationsAfter(cursor, "th_steering_carrier")
	var sawCompleted, sawIdle bool
	for _, notification := range notifications {
		switch notification.Notification.Method {
		case appwire.NotifyTurnCompleted:
			sawCompleted = true
		case appwire.NotifyThreadStatusChanged:
			var params appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(notification.Notification.Params, &params); err == nil && params.Status.Type == appwire.ThreadStatusIdle {
				sawIdle = true
			}
		}
	}
	if !sawCompleted || !sawIdle {
		t.Fatalf("carrier terminal notifications=%+v, want completed turn and idle status", notifications)
	}

	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_steering_carrier", IncludeTurns: true})
	if read.Thread.Evener.ActiveTurnID != "" || read.Thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("carrier read state=(active %q, status %q), want empty/idle", read.Thread.Evener.ActiveTurnID, read.Thread.Status.Type)
	}
	if len(read.Thread.Turns) != 1 || read.Thread.Turns[0].ID != "turn_steer" || read.Thread.Turns[0].Status != appwire.TurnStatusCompleted {
		t.Fatalf("carrier turns=%+v, want completed turn_steer", read.Thread.Turns)
	}
}

func TestServerAppWireAbandonedCarrierReplaysDeferredTerminalStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_abandoned_carrier")
	client := dialServerAppWire(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_abandoned_carrier", Subscribe: true}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_abandoned_carrier", Data: events.UserInputData{Text: "old", StableTurnID: "old-turn"}})
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
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_carrier_wins", Data: events.UserInputData{Text: "old", StableTurnID: "old-turn"}})
	srv.SetProcessingTurn("new-turn")
	cursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_carrier_wins", Data: events.SessionEndData{Reason: "turn_failed", State: "idle"}}, nil)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTurnStarted, SessionID: "th_carrier_wins", Data: events.TurnStartedData{TurnID: "new-turn"}})
	srv.SetProcessing(false)

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
			srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: threadID, Data: events.UserInputData{Text: "old", StableTurnID: "old-turn"}})
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
			srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "old", Data: events.UserInputData{Text: "old", StableTurnID: "old-turn"}})
			srv.SetProcessingTurn("pending-old")
			cursor := srv.appNotifier.CurrentSequence()
			BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "old", Data: events.SessionEndData{Reason: "turn_failed", State: "idle"}}, nil)

			prepared, err := PrepareAppIdentityForRef("local", tc.newID, tc.newRef, "")
			if err != nil {
				t.Fatalf("PrepareAppIdentityForRef: %v", err)
			}
			srv.ReplaceAppIdentity(prepared, nil)
			srv.SetState("awaiting")
			srv.SetProcessing(false)

			read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: tc.newRef})
			if read.Thread.Status.Type != appwire.ThreadStatusAwaiting || read.Thread.Evener.ActiveTurnID != "" {
				t.Fatalf("replacement read=(status %q, active %q), want awaiting/empty", read.Thread.Status.Type, read.Thread.Evener.ActiveTurnID)
			}
			for _, notification := range srv.AppNotificationsAfter(cursor, tc.newRef) {
				if notification.Notification.Method == appwire.NotifyThreadStatusChanged || notification.Notification.Method == appwire.NotifyThreadClosed {
					t.Fatalf("retired terminal notification crossed replacement boundary: %+v", notification)
				}
			}
		})
	}
}

func TestServerAppWireSnapshotPreservesReasoningAcrossReservedTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_reasoning_boundary")
	srv.SetCostLookupFunc(func(string) *registry.Cost {
		return &registry.Cost{Input: 5, Output: 25}
	})
	client := dialServerAppWire(t, srv)
	ctx := context.Background()
	oldStart := time.Unix(100, 0)
	oldEnd := oldStart.Add(4200 * time.Millisecond)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_reasoning_boundary", Timestamp: oldStart, Data: events.UserInputData{Text: "question", StableTurnID: "turn_old"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning_boundary", Timestamp: oldStart.Add(time.Second), Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "old reasoning"}})
	srv.SetProcessingTurn("turn_new")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_reasoning_boundary", Timestamp: oldStart.Add(2 * time.Second), Data: events.AssistantTextEndData{Text: "old answer", Usage: llm.Usage{InputTokens: 1000, OutputTokens: 500}}})
	if got := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary"}).Thread.Evener.ActiveTurnID; got != "turn_new" {
		t.Fatalf("durable active turn after queued assistant end = %q, want turn_new", got)
	}
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning_boundary", Timestamp: oldStart.Add(3 * time.Second), Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "unfinished reasoning"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_reasoning_boundary", Timestamp: oldEnd, Data: events.TurnEndedData{TurnDurationMS: 4200}})
	if got := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary"}).Thread.Evener.ActiveTurnID; got != "turn_new" {
		t.Fatalf("durable active turn after queued turn end = %q, want turn_new", got)
	}
	queuedSessionEndCursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_reasoning_boundary", Data: events.SessionEndData{State: "idle"}}, nil)
	queuedSessionEndNotifications := srv.AppNotificationsAfter(queuedSessionEndCursor, "th_reasoning_boundary")
	var sawOldTurnCompleted, sawOldItemCompleted, sawStaleThreadStatus, sawStaleThreadClosed bool
	for _, notification := range queuedSessionEndNotifications {
		switch notification.Notification.Method {
		case appwire.NotifyTurnCompleted:
			sawOldTurnCompleted = true
		case appwire.NotifyItemCompleted:
			sawOldItemCompleted = true
		case appwire.NotifyThreadStatusChanged:
			sawStaleThreadStatus = true
		case appwire.NotifyThreadClosed:
			sawStaleThreadClosed = true
		}
	}
	if !sawOldTurnCompleted || !sawOldItemCompleted {
		t.Fatalf("queued session end notifications=%+v, want old turn/item completion", queuedSessionEndNotifications)
	}
	if sawStaleThreadStatus || sawStaleThreadClosed {
		t.Fatalf("queued session end published stale thread state: %+v", queuedSessionEndNotifications)
	}
	queuedSessionEndRead := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary"})
	if got := queuedSessionEndRead.Thread.Evener.ActiveTurnID; got != "turn_new" {
		t.Fatalf("durable active turn after queued session end = %q, want turn_new", got)
	}
	if got := queuedSessionEndRead.Thread.Status.Type; got != appwire.ThreadStatusActive {
		t.Fatalf("status after queued session end = %q, want active", got)
	}
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_reasoning_boundary", Timestamp: oldEnd.Add(time.Millisecond), Data: events.GoalContinuationData{Text: "new question", StableTurnID: "turn_new"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_reasoning_boundary", Timestamp: oldEnd.Add(time.Second)})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning_boundary", Timestamp: oldEnd.Add(2 * time.Second), Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "new reasoning"}})

	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary", IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Thread.Turns) != 2 {
		t.Fatalf("turns=%+v, want completed old and in-progress new turn", read.Thread.Turns)
	}
	old, current := read.Thread.Turns[0], read.Thread.Turns[1]
	if old.ID != "turn_old" || old.Status != appwire.TurnStatusCompleted {
		t.Fatalf("old turn=(id %q, status %q), want completed turn_old", old.ID, old.Status)
	}
	if old.Usage == nil || old.Usage.InputTokens != 1000 || old.Usage.OutputTokens != 500 {
		t.Fatalf("old usage=%+v, want input 1000/output 500", old.Usage)
	}
	if old.Cost != "~$0.02" {
		t.Fatalf("old cost=%q, want ~$0.02", old.Cost)
	}
	if old.DurationMS == nil || *old.DurationMS != 4200 || old.CompletedAt == nil || *old.CompletedAt != oldEnd.UnixMilli() {
		t.Fatalf("old timing=(duration %v, completed %v), want (4200, %d)", old.DurationMS, old.CompletedAt, oldEnd.UnixMilli())
	}
	reasoningTexts := make(map[string]bool)
	for _, item := range old.Items {
		if item.Type != "reasoning" {
			continue
		}
		if item.TurnID != "turn_old" || item.Status != appwire.TurnStatusCompleted {
			t.Fatalf("old reasoning=%+v, want completed item owned by turn_old", item)
		}
		reasoningTexts[item.Text] = true
	}
	if len(reasoningTexts) != 2 || !reasoningTexts["old reasoning"] || !reasoningTexts["unfinished reasoning"] {
		t.Fatalf("old reasoning text=%v, want both retained reasoning rounds", reasoningTexts)
	}
	if current.ID != "turn_new" || current.Status != appwire.TurnStatusInProgress {
		t.Fatalf("current turn=(id %q, status %q), want in-progress turn_new", current.ID, current.Status)
	}
	if got := read.Thread.Status.Type; got != appwire.ThreadStatusActive {
		t.Fatalf("status after stable carrier = %q, want active", got)
	}
	if len(current.Items) != 2 || current.Items[1].Type != "reasoning" || current.Items[1].Text != "new reasoning" || current.Items[1].TurnID != "turn_new" {
		t.Fatalf("current items=%+v, want new reasoning on turn_new", current.Items)
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

func TestServerAppWireQueuedEventsRetainOwnershipAfterFastProcessingClear(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_fast_clear")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_fast_clear", Data: events.UserInputData{Text: "old", StableTurnID: "turn_old"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_fast_clear", Data: events.ReasoningSummaryDeltaData{Delta: "old reasoning"}})
	srv.SetProcessingTurn("turn_new")
	srv.SetProcessing(false)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_fast_clear", Data: events.AssistantTextEndData{Text: "old answer"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_fast_clear", Data: events.TurnEndedData{TurnDurationMS: 1200}})
	// Processing returns before this buffered event stream is consumed. Idle is
	// now accurate, while the stable carrier still owns the queued new items.
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_fast_clear", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}}, nil)
	settled := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_fast_clear"}).Thread
	if settled.Status.Type != appwire.ThreadStatusIdle || settled.Evener.ActiveTurnID != "" {
		t.Fatalf("settled state=(%q, %q), want idle/empty after processing returned", settled.Status.Type, settled.Evener.ActiveTurnID)
	}
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_fast_clear", Data: events.GoalContinuationData{Text: "new", StableTurnID: "turn_new"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_fast_clear", Data: events.AssistantTextEndData{Text: "new answer"}})
	srv.mu.RLock()
	reserved := srv.appReservedTurnID
	active := srv.appActiveTurnID
	srv.mu.RUnlock()
	if reserved != "" || active != "turn_new" {
		t.Fatalf("after stable carrier reserved=%q active=%q, want empty/turn_new", reserved, active)
	}
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_fast_clear", IncludeTurns: true})
	if len(read.Thread.Turns) != 2 {
		t.Fatalf("turns=%+v, want old and new turns", read.Thread.Turns)
	}
	old, current := read.Thread.Turns[0], read.Thread.Turns[1]
	if old.ID != "turn_old" || len(old.Items) != 3 || old.Items[2].Text != "old answer" || old.Items[2].TurnID != "turn_old" {
		t.Fatalf("old turn=%+v, want old answer retained on turn_old", old)
	}
	if current.ID != "turn_new" || len(current.Items) != 2 || current.Items[1].Text != "new answer" || current.Items[1].TurnID != "turn_new" {
		t.Fatalf("new turn=%+v, want new answer on turn_new", current)
	}
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_fast_clear", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}}, nil)
	completed := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_fast_clear", IncludeTurns: true}).Thread
	if completed.Status.Type != appwire.ThreadStatusIdle || completed.Evener.ActiveTurnID != "" || completed.Turns[1].Status != appwire.TurnStatusCompleted {
		t.Fatalf("completed buffered turn=%+v, want idle, no active identity, and completed new turn", completed)
	}
}

func TestServerAppWireUnclaimedStableTurnDoesNotKeepSessionBusy(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_unclaimed")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_unclaimed", Data: events.UserInputData{Text: "old", StableTurnID: "turn_old"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_unclaimed", Data: events.ReasoningSummaryDeltaData{Delta: "old reasoning"}})
	// The runnable callback precedes claiming the durable input. A failed claim
	// clears processing without ever emitting the new turn's stable carrier.
	srv.SetProcessingTurn("turn_unclaimed")
	srv.SetProcessing(false)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_unclaimed", Data: events.AssistantTextEndData{Text: "old answer"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_unclaimed", Data: events.TurnEndedData{TurnDurationMS: 1200}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_unclaimed", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_unclaimed", IncludeTurns: true})
	if read.Thread.Evener.ActiveTurnID != "" {
		t.Fatalf("active turn=%q, want idle after failed claim and old completion", read.Thread.Evener.ActiveTurnID)
	}
	if len(read.Thread.Turns) != 1 || read.Thread.Turns[0].ID != "turn_old" || read.Thread.Turns[0].Status != appwire.TurnStatusCompleted {
		t.Fatalf("turns=%+v, want only the completed old turn", read.Thread.Turns)
	}
	srv.mu.RLock()
	reserved := srv.appReservedTurnID
	srv.mu.RUnlock()
	if reserved != "" {
		t.Fatalf("admission reservation=%q, want empty after failed claim", reserved)
	}
}

func TestServerAppWireNonstableGoalUpdatesActiveIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_nonstable_goal")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_nonstable_goal", Data: events.UserInputData{Text: "old", StableTurnID: "turn_old"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_nonstable_goal", Data: events.GoalContinuationData{Text: "new"}})
	srv.mu.RLock()
	active := srv.appActiveTurnID
	srv.mu.RUnlock()
	if active == "turn_old" || active == "" {
		t.Fatalf("nonstable goal active turn=%q, want a new identity", active)
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

func TestServerAppWireQueuedClosedSessionEndOmitsStaleThreadFrames(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_closed_boundary")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_closed_boundary", Data: events.UserInputData{Text: "old", StableTurnID: "turn_old"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_closed_boundary", Data: events.ReasoningSummaryDeltaData{Delta: "old reasoning"}})
	srv.SetProcessingTurn("turn_new")
	cursor := srv.appNotifier.CurrentSequence()
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_closed_boundary", Data: events.SessionEndData{State: "closed"}}, nil)
	notifications := srv.AppNotificationsAfter(cursor, "th_closed_boundary")
	var sawTurnCompleted, sawItemCompleted, sawThreadStatus, sawThreadClosed bool
	for _, notification := range notifications {
		switch notification.Notification.Method {
		case appwire.NotifyTurnCompleted:
			sawTurnCompleted = true
		case appwire.NotifyItemCompleted:
			sawItemCompleted = true
		case appwire.NotifyThreadStatusChanged:
			sawThreadStatus = true
		case appwire.NotifyThreadClosed:
			sawThreadClosed = true
		}
	}
	if !sawTurnCompleted || !sawItemCompleted {
		t.Fatalf("closed queued notifications=%+v, want turn/item completion", notifications)
	}
	if sawThreadStatus || sawThreadClosed {
		t.Fatalf("closed queued notifications=%+v, want no stale thread frames", notifications)
	}
}

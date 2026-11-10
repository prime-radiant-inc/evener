package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/llm"
)

// A delegate's update carries the lineage of the turn that produced it across
// the hop: the parent's turn that consumes the update adopts it, with an entry
// for the update itself, and the controller drops the cause once the update is
// consumed.
func TestDelegateUpdateCarriesItsCauseToTheParentTurn(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return agenttest.CommunicateResponse(false, "need the heading from you")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("summary written") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("noted") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	warmDelegateUnservedRoot(t, root, fixture)
	if pending := rootPendingAttention(root); len(pending) != 1 {
		t.Fatalf("root attention = %v, want the delegate's update alone", pending)
	}
	causes := &root.delegateController.attentionCauses
	if causes.size() != 1 {
		t.Fatalf("causes held = %d, want the update's", causes.size())
	}

	if _, err := root.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	if got := provenance.MessagesFrom(root.completedCausalProvenance(), fixture.childID); got != 1 {
		t.Fatalf("the parent turn's lineage holds %d messages from the delegate, want its update", got)
	}
	if causes.size() != 0 {
		t.Fatalf("causes held after the update was consumed = %d, want none", causes.size())
	}
}

// Closing the controller drops every cause it holds.
func TestDelegateAttentionCausesClearOnClose(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return agenttest.CommunicateResponse(false, "need the heading from you")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("summary written") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	warmDelegateUnservedRoot(t, root, fixture)
	causes := &root.delegateController.attentionCauses
	if causes.size() != 1 {
		t.Fatalf("causes held = %d, want the update's", causes.size())
	}
	if err := root.delegateController.Close(tripwireCtx(t)); err != nil {
		t.Fatalf("close controller: %v", err)
	}
	if causes.size() != 0 {
		t.Fatalf("causes held after close = %d, want none", causes.size())
	}
}

// A delegate that watches its parent and answers each fire with an update
// ping-pongs with the parent. With the update's lineage carried to the parent,
// each fire after the first is marked as caused by the delegate's own message,
// with the number of exchanges so far, and the runaway guard stops the chain at
// its cap. The delegate's turns are modeled the way they run: its notification
// turn adopts the notification's lineage, and its update records that lineage
// as the cause the parent's consuming turn adopts.
func TestDelegateParentPingPongIsMarkedAndStoppedByTheRunawayGuard(t *testing.T) {
	t.Parallel()
	parent := newTestJM(t)
	const delegateSession = "child-session"
	var toDelegate []jobNotification
	res, err := parent.configureWatch(watchArgs{
		Target: "*", Events: []string{"communicate"}, ReceiverSessionID: delegateSession,
		ReceiverNotify: func(n jobNotification) { toDelegate = append(toDelegate, n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveries := func() int {
		parent.mu.Lock()
		defer parent.mu.Unlock()
		_, cfg, _ := parent.watchConfigByIDLocked(res.WatchID)
		return cfg.deliveries
	}
	var causes delegateAttentionCauses
	var parentTurn *provenance.Causal
	var marks []int
	for exchange := range 4 * runawaySelfInfluenceDepth {
		before := deliveries()
		parent.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate, SessionID: parent.sessionID, Data: events.CommunicateData{Message: "parent reply"}, Provenance: parentTurn})
		if deliveries() == before {
			break
		}
		fire := toDelegate[len(toDelegate)-1]
		marks = append(marks, fire.ownMessageHops)
		if fire.ownMessageHops > 0 && !strings.Contains(formatJobNotificationBlock(fire, notificationExcerpt{}, false), llm.SystemNotificationOpenTag) {
			t.Fatalf("exchange %d: a fire caused by the delegate's own message carries no system notification", exchange)
		}
		attentionID := delegateAttentionID(fmt.Sprintf("dlg_x/update/%d", exchange))
		causes.remember(attentionID, provenance.WithMessage(fire.Provenance, delegateSession, attentionID))
		parentTurn = causes.union([]string{attentionID})
		causes.forget(attentionID)
	}
	want := make([]int, runawaySelfInfluenceDepth)
	for i := range want {
		want[i] = i
	}
	if !slices.Equal(marks, want) {
		t.Fatalf("own-message marks per delivered fire = %v, want %v and the chain stopped at the runaway cap", marks, want)
	}
}

// A watch whose receiver is another session never wakes it with that
// session's own event.
func TestCrossSessionWatchIgnoresItsReceiversOwnEvents(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	const receiver = "receiver-session"
	var woke []jobNotification
	if _, err := jm.configureWatch(watchArgs{
		Target: "*", Events: []string{"communicate"}, ReceiverSessionID: receiver,
		ReceiverNotify: func(n jobNotification) { woke = append(woke, n) },
	}); err != nil {
		t.Fatal(err)
	}
	jm.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate, SessionID: receiver, Data: events.CommunicateData{Message: "own"}})
	if len(woke) != 0 {
		t.Fatalf("the receiver's own event woke it %d times, want never", len(woke))
	}
	jm.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate, SessionID: jm.sessionID, Data: events.CommunicateData{Message: "source"}})
	if len(woke) != 1 {
		t.Fatalf("the source's event woke the receiver %d times, want once", len(woke))
	}
}

// A send watch's frame for an event caused by the watcher's own message
// carries the mark.
func TestSendWatchFrameMarksAnEventCausedByTheWatchersOwnMessage(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
	})
	cause := provenance.WithMessage(nil, jm.sessionID, "delegate:dlg_x/update/1")
	jm.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate, SessionID: jm.sessionID, Data: events.CommunicateData{Message: "reply"}, Provenance: cause})
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 || !strings.Contains(pending[0].state.Frame, llm.SystemNotificationOpenTag) {
		t.Fatalf("pending frames = %d, want one marked as caused by the watcher's own message", len(pending))
	}
}

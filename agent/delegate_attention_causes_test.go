package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

// Resolving a queued child attention forgets its cause. A resolution whose
// first journal append fails keeps it only until the retry lands: resolving or
// discarding an item must leave no cause behind for a later full-history turn
// to re-adopt after the item is spent.
func TestResolvingDelegateAttentionForgetsItsCause(t *testing.T) {
	t.Parallel()
	const (
		delegateID  = "dlg_cause"
		consumedID  = "delegate:dlg_cause/update/1"
		discardedID = "delegate:dlg_cause/update/2"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, delegateID, "")
	for _, attentionID := range []string{consumedID, discardedID} {
		if !c.noteDelegateAttentionForTest(delegateID, attentionID) {
			t.Fatalf("seed the queued attention row %q", attentionID)
		}
		c.attentionCauses.remember(attentionID, provenance.WithMessage(nil, "child-session", attentionID))
	}

	appendErr := errors.New("test: resolution append failure")
	c.mu.Lock()
	c.appendFault = func(events []delegatestore.Event) error {
		for _, event := range events {
			if event.Kind == delegatestore.EventDelegateAttentionResolved {
				return appendErr
			}
		}
		return nil
	}
	c.mu.Unlock()
	if err := c.forgetDelegateAttention(delegateID, consumedID); !errors.Is(err, appendErr) {
		t.Fatalf("failed resolution append = %v, want %v", err, appendErr)
	}
	if got := c.attentionCauses.union([]string{consumedID}); got == nil {
		t.Fatal("a resolution that never landed dropped the row's cause")
	}
	c.mu.Lock()
	c.appendFault = nil
	c.mu.Unlock()
	if err := c.forgetDelegateAttention(delegateID, consumedID); err != nil {
		t.Fatalf("retry the attention resolution: %v", err)
	}
	if got := c.attentionCauses.union([]string{consumedID}); got != nil {
		t.Fatal("consumed attention kept its cause, so a later turn could re-adopt spent lineage")
	}

	c.mu.Lock()
	err := c.forgetDelegateAttentionLocked(delegateID, discardedID)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("discard the queued attention: %v", err)
	}
	if got := c.attentionCauses.union([]string{discardedID}); got != nil {
		t.Fatal("discarded attention kept its cause")
	}
	for _, attentionID := range []string{consumedID, discardedID} {
		if _, resolved := c.durable[delegateID].ResolvedAttention[attentionID]; !resolved {
			t.Fatalf("attention %q is not recorded resolved in delegatestore", attentionID)
		}
	}
}

// A transferred attention keeps its cause for the root's receiving turn: the
// escalation resolves the source row but hands the same identity to the root,
// so only the root's own resolution may forget the cause.
func TestEscalatedDelegateAttentionKeepsItsCauseForTheRoot(t *testing.T) {
	t.Parallel()
	fenced := newFencedGrandchildAttention(t)
	root := fenced.root
	fenced.closeParent(t)
	// The grandchild's update remembered this cause when it was delivered; the
	// transfer must carry it to the root without the source resolution dropping
	// it first.
	root.delegateController.attentionCauses.remember(fenced.attentionID, provenance.WithMessage(nil, "grandchild-session", fenced.attentionID))

	plans := root.delegateController.permanentlyFencedDelegateAttention()
	if len(plans) != 1 {
		t.Fatalf("fenced escalation plans = %d, want exactly one", len(plans))
	}
	if err := root.escalateOneUnreachableDelegateAttention(plans[0], readDelegateAttentionFold); err != nil {
		t.Fatalf("escalate unreachable delegate attention: %v", err)
	}
	if got := root.delegateController.attentionCauses.union([]string{fenced.attentionID}); got == nil {
		t.Fatal("the transfer resolved the source row before the root's receiving turn could adopt its cause")
	}
	if err := root.resolveAttentionDurably([]string{fenced.attentionID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("root resolves the transferred attention: %v", err)
	}
	if got := root.delegateController.attentionCauses.union([]string{fenced.attentionID}); got != nil {
		t.Fatal("the root's own resolution kept the transferred cause")
	}
}

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
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller},
	})
	cause := provenance.WithMessage(nil, jm.sessionID, "delegate:dlg_x/update/1")
	jm.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate, SessionID: jm.sessionID, Data: events.CommunicateData{Message: "reply"}, Provenance: cause})
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 || !strings.Contains(pending[0].state.Frame, llm.SystemNotificationOpenTag) {
		t.Fatalf("pending frames = %d, want one marked as caused by the watcher's own message", len(pending))
	}
}

// An attention item the running turn's settled request presents joins that
// turn's lineage when its coverage is promoted, however late in the turn it
// arrived.
func TestPresentedAttentionCauseJoinsTheRunningTurn(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	const attentionID = "delegate:dlg_x/update/7"
	root.delegateController.attentionCauses.remember(attentionID, provenance.WithMessage(nil, "child-session", attentionID))
	root.attentionMu.Lock()
	root.attentionStagedIDs = map[string]struct{}{attentionID: {}}
	root.attentionMu.Unlock()

	root.promoteStagedAttentionCoverage(llm.Request{})
	if got := provenance.MessagesFrom(root.activeCausalProvenance(), "child-session"); got != 1 {
		t.Fatalf("the running turn's lineage holds %d messages from the delegate, want the presented update", got)
	}
}

// The trigger of an accepted attention generation is resolved before the
// generation runs; its cause still reaches the generation when its request
// presents the trigger.
func TestAcceptedTriggerCauseReachesTheGenerationItLaunches(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	const trigger = "watch:frame/1"
	causes := &root.delegateController.attentionCauses
	causes.remember(trigger, provenance.WithMessage(nil, "child-session", trigger))

	root.holdAcceptedTrigger(trigger) // accept latches the trigger, then resolves it
	causes.forget(trigger)            // resolution drops the controller's copy
	root.attentionMu.Lock()
	root.attentionStagedIDs = map[string]struct{}{trigger: {}}
	root.attentionMu.Unlock()
	root.promoteStagedAttentionCoverage(llm.Request{})

	if got := provenance.MessagesFrom(root.activeCausalProvenance(), "child-session"); got != 1 {
		t.Fatalf("the launched generation's lineage holds %d messages behind its trigger, want 1", got)
	}
}

// A presentation start whose commit's journal append failed retries the same
// accepted trigger. The first accept's resolution already dropped the
// controller's copy of the trigger's cause, so the retry's re-hold must keep
// the cause the first hold latched — the generation that finally runs still
// descends from what triggered it.
func TestAcceptedTriggerCauseSurvivesTheStickyCommitRetry(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	causes := &root.delegateController.attentionCauses
	const trigger = "watch:frame/1"
	causes.remember(trigger, provenance.WithMessage(nil, "child-session", trigger))

	root.holdAcceptedTrigger(trigger) // the accept latches the trigger
	causes.forget(trigger)            // its resolution drops the controller's copy
	root.holdAcceptedTrigger(trigger) // the sticky retry re-holds the same trigger

	root.attentionMu.Lock()
	root.attentionStagedIDs = map[string]struct{}{trigger: {}}
	root.attentionMu.Unlock()
	root.promoteStagedAttentionCoverage(llm.Request{})
	if got := provenance.MessagesFrom(root.activeCausalProvenance(), "child-session"); got != 1 {
		t.Fatalf("the retried generation's lineage holds %d messages behind its trigger, want 1", got)
	}
}

// Two updates per exchange share one fire's ancestry, and a turn that
// presents both merges the shared entries twice. However the diagnostic chain
// truncates, the runaway accounting must retain every distinct fire, so the
// guard still stops an endless parent/delegate loop.
func TestParentDelegateExchangesWithTwoUpdatesKeepTheRunawayFuseVisible(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	causes := &root.delegateController.attentionCauses
	const (
		watchID         = "watch_loop"
		watchGeneration = "wg_loop"
		delegateSession = "child-session"
	)
	// One exchange: the parent's reply fires the delegate's watch once, the
	// delegate answers with two updates whose causes share that lineage, and
	// the parent's next turn presents both updates.
	exchange := func(turn *provenance.Causal, n int) *provenance.Causal {
		fired := provenance.WithWatch(turn, watchID, watchGeneration, jobstore.NewWatchSendDeliveryID(), delegateSession, "")
		updates := []string{
			fmt.Sprintf("delegate:dlg_x/update/%d-a", n),
			fmt.Sprintf("delegate:dlg_x/update/%d-b", n),
		}
		for _, update := range updates {
			causes.remember(update, provenance.WithMessage(fired, delegateSession, update))
		}
		root.replaceActiveProvenance(nil)
		root.resetAttentionCoverage()
		for _, update := range updates {
			root.attentionMu.Lock()
			root.attentionStagedIDs = map[string]struct{}{update: {}}
			root.attentionMu.Unlock()
			root.promoteStagedAttentionCoverage(llm.Request{})
		}
		causes.forget(updates...)
		return root.activeCausalProvenance()
	}
	var turn *provenance.Causal
	for i := range runawaySelfInfluenceDepth {
		turn = exchange(turn, i)
	}
	depth := provenance.SelfInfluenceDepth(turn, watchID, "", func(string) bool { return true })
	if depth < runawaySelfInfluenceDepth {
		t.Fatalf("the parent turn's lineage exposes %d fires of the watch, want %d so the runaway guard trips", depth, runawaySelfInfluenceDepth)
	}
}

// A delegate that answers each of its parent's frames with a terminal report
// cannot restart the runaway count at zero: the report carries the producing
// generation's lineage to the parent turn that consumes it, so every
// exchange's fire counts toward the guard's depth and the loop stops at the
// cap.
func TestTerminalReportsCarryTheParentLoopToTheRunawayGuard(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	var causes *delegateAttentionCauses
	const exchanges = 4 * runawaySelfInfluenceDepth
	steps := make([]func(llm.Request) llm.Response, 0, 2*exchanges+1)
	steps = append(steps, func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") })
	for k := range exchanges {
		steps = append(steps,
			func(llm.Request) llm.Response {
				return agenttest.CommunicateResponse(true, fmt.Sprintf("child report %d", k))
			},
			func(llm.Request) llm.Response {
				return agenttest.CommunicateResponse(true, fmt.Sprintf("root reply %d", k))
			},
		)
	}
	fixture.adapter.setSteps(steps)
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)

	type frameFire struct {
		notification jobNotification
		depth        int
	}
	var fires []frameFire
	var res watchResult
	res, err := root.jobManager.configureWatch(watchArgs{
		Target:            "*",
		Events:            []string{"communicate"},
		ReceiverSessionID: fixture.childID,
		ReceiverNotify: func(n jobNotification) {
			fires = append(fires, frameFire{
				notification: n,
				depth:        provenance.SelfInfluenceDepth(n.Provenance, res.WatchID, "", func(string) bool { return true }),
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	frameID := func(k int) string { return fmt.Sprintf("attention:report-%d", k) }

	// Exchange 0 begins from outside the loop: the initial delegation. Each
	// later exchange's report answers the frame the previous reply fired.
	causes = &root.delegateController.attentionCauses
	armStableSupervisionAttention(t, sub, frameID(0), "begin the loop")
	root.drivePendingDelegateAttention()
	waitForStableSupervisionRun(t, root, fixture.childID)
	var depths []int
	for k := range exchanges {
		if len(fires) < k {
			break // the previous reply's fire was refused: the guard stopped the loop
		}
		testwait.Until(t, fmt.Sprintf("exchange %d's report to arm as root attention", k), func() bool {
			return len(rootPendingAttention(root)) == 1
		})
		if _, err := root.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
			t.Fatalf("root turn %d: %v", k, err)
		}
		if len(fires) <= k {
			continue // this reply's fire was refused; the loop drains and ends
		}
		depths = append(depths, fires[k].depth)
		causes.remember(frameID(k+1), fires[k].notification.Provenance)
		armStableSupervisionAttention(t, sub, frameID(k+1), "answer the frame")
		root.drivePendingDelegateAttention()
		waitForStableSupervisionRun(t, root, fixture.childID)
	}
	if got := len(fires); got != runawaySelfInfluenceDepth {
		t.Fatalf("the loop delivered %d fires, want it stopped at the runaway depth %d", got, runawaySelfInfluenceDepth)
	}
	// Each notification's lineage carries its own delivery, so a fire
	// counts itself: exchange k's fire reads depth k+1.
	want := make([]int, runawaySelfInfluenceDepth)
	for i := range want {
		want[i] = i + 1
	}
	if !slices.Equal(depths, want) {
		t.Fatalf("fire depths per exchange = %v, want %v with the chain stopped at the cap", depths, want)
	}
}

// A generation's report queues behind any delivery its parent has not taken,
// and the finish path makes no delivery plan for it then: the head is the
// earlier delivery. The report's cause must still be registered when it is
// queued, because the parent turn that later consumes it adopts that cause as
// its lineage. Registering only alongside a delivery plan loses the lineage
// exactly on the held path, and the parent's reply then restarts the runaway
// count at zero.
func TestHeldReportCarriesItsCauseWithoutADeliveryPlan(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(false, "PROGRESS: scanning") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("FINAL_REPORT") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	// The root is mid-turn, so the update stays queued and the report lands
	// behind it.
	root.mu.Lock()
	root.state = SessionProcessing
	root.mu.Unlock()
	// The frame is a watch fire: the generation it starts is watch-triggered,
	// and that fire is the lineage its report must carry onward.
	const watchID, watchGeneration = "watch_held_report", "wg_held_report"
	const frameID = "attention:frame-0"
	root.delegateController.attentionCauses.remember(frameID, provenance.WithWatch(nil, watchID, watchGeneration, jobstore.NewWatchSendDeliveryID(), root.ID(), ""))
	armStableSupervisionAttention(t, sub, frameID, "begin")
	root.drivePendingDelegateAttention()
	waitForStableSupervisionRun(t, root, fixture.childID)

	pending := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID).PendingDeliveries
	if len(pending) != 2 {
		t.Fatalf("queued deliveries = %#v, want the update and the report behind it", pending)
	}
	if pending[1].Packet.Kind == delegatestore.PacketUpdate {
		t.Fatalf("the queued second delivery is an update, not the report: %#v", pending)
	}
	reportID := delegateAttentionID(pending[1].DeliveryID)
	cause := root.delegateController.attentionCauses.union([]string{reportID})
	if cause == nil {
		t.Fatalf("queued report %q registered no cause", reportID)
	}
	if depth := provenance.SelfInfluenceDepth(cause, watchID, watchGeneration, func(string) bool { return true }); depth != 1 {
		t.Fatalf("queued report's cause exposes the watch fire at depth %d, want 1: %#v", depth, cause)
	}
}

// A parent/delegate exchange whose parent turn settles several requests keeps
// the runaway fuse visible: every exchange's fire still counts toward the
// depth, so the guard that stops an endless loop trips. Each settled request
// of a turn re-presents the update it handled, so the update's cause must
// join the turn's lineage once — merged per request it would fill the
// retained lineage head with copies and cap the fires the fuse can see.
func TestParentDelegateExchangesWithRepeatedSettledRequestsKeepTheFuseVisible(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	causes := &root.delegateController.attentionCauses
	const (
		watchID         = "watch_loop"
		watchGeneration = "wg_loop"
		delegateSession = "child-session"
	)
	// One exchange: the parent's reply fires the delegate's watch, the
	// delegate answers with an update whose cause carries that lineage, and
	// the parent's next turn consumes the update through the settled
	// requests that present it.
	exchange := func(turn *provenance.Causal, n int) *provenance.Causal {
		fired := provenance.WithWatch(turn, watchID, watchGeneration, jobstore.NewWatchSendDeliveryID(), delegateSession, "")
		update := fmt.Sprintf("delegate:dlg_x/update/%d", n)
		causes.remember(update, provenance.WithMessage(fired, delegateSession, update))
		root.replaceActiveProvenance(nil)
		root.resetAttentionCoverage()
		for range 4 {
			root.attentionMu.Lock()
			root.attentionStagedIDs = map[string]struct{}{update: {}}
			root.attentionMu.Unlock()
			root.promoteStagedAttentionCoverage(llm.Request{})
		}
		causes.forget(update)
		return root.activeCausalProvenance()
	}
	var turn *provenance.Causal
	for i := range runawaySelfInfluenceDepth {
		turn = exchange(turn, i)
	}
	depth := provenance.SelfInfluenceDepth(turn, watchID, "", func(string) bool { return true })
	if depth < runawaySelfInfluenceDepth {
		t.Fatalf("the parent turn's lineage exposes %d fires of the watch, want %d so the runaway guard trips", depth, runawaySelfInfluenceDepth)
	}
}

// A cause that arrives later in the turn still joins it: merging each item's
// cause once skips only items an earlier settled request already presented,
// never the ones a later request presents for the first time.
func TestLaterArrivingUpdateCauseStillJoinsTheTurn(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	causes := &root.delegateController.attentionCauses
	const first = "delegate:dlg_x/update/1"
	const late = "delegate:dlg_x/update/2"
	stage := func(ids ...string) {
		root.attentionMu.Lock()
		staged := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			staged[id] = struct{}{}
		}
		root.attentionStagedIDs = staged
		root.attentionMu.Unlock()
	}
	root.resetAttentionCoverage()
	causes.remember(first, provenance.WithMessage(nil, "child-session", first))
	stage(first)
	root.promoteStagedAttentionCoverage(llm.Request{})

	// The second update arrives mid-turn; a later request presents both.
	causes.remember(late, provenance.WithMessage(nil, "child-session", late))
	stage(first, late)
	root.promoteStagedAttentionCoverage(llm.Request{})

	if got := provenance.MessagesFrom(root.activeCausalProvenance(), "child-session"); got != 2 {
		t.Fatalf("the turn's lineage holds %d messages from the delegate, want both updates", got)
	}
}

// A settled round presents its items once. Re-presenting an item at a later
// request of the same turn must not merge its cause again: the chain keeps one
// entry per cause, so copies would spend the bounded head on one exchange and
// leave too few distinct watch fires for the runaway fuse to count.
//
// Two guards hold this: the merge-once set stops the second union, and Union
// collapses identical entries if one arrives. Neither alone is load-bearing,
// so this counts raw chain entries — a distinct-id count cannot see copies.
func TestRepresentingAnItemInTheSameTurnMergesItsCauseOnce(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	root := fixture.root
	causes := &root.delegateController.attentionCauses
	const update = "delegate:dlg_x/update/1"
	stage := func(ids ...string) {
		root.attentionMu.Lock()
		staged := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			staged[id] = struct{}{}
		}
		root.attentionStagedIDs = staged
		root.attentionMu.Unlock()
	}
	root.resetAttentionCoverage()
	causes.remember(update, provenance.WithMessage(nil, "child-session", update))

	// Four settled requests of one turn present the same item, the way one
	// delegate update surfaces on more than one request.
	for range 4 {
		stage(update)
		root.promoteStagedAttentionCoverage(llm.Request{})
	}

	// Count the chain itself: MessagesFrom reports distinct delivery ids, so it
	// cannot see a cause merged more than once.
	entries := 0
	for _, entry := range root.activeCausalProvenance().Chain {
		if entry.Kind == provenance.EntryKindMessage && entry.SessionID == "child-session" {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("the turn's lineage holds %d copies of one update, want 1: re-presenting an item must not merge its cause again", entries)
	}
}

// A held report a positive-wait delegate_send consumes inline spends its
// delivery in a tool-results turn, which carries no attention ID: the reply's
// earlier_results are the only trace of what the parent's request presented.
// The report's cause must still join the parent's turn — otherwise the reply
// that descends from a watch fire loses that lineage and restarts the runaway
// count — and its registry entry must go once incorporated, since no attention
// turn remains to resolve it.
func TestInlineHeldReportJoinsTheTurnAndRetiresItsCause(t *testing.T) {
	const (
		watchID         = "watch_inline"
		watchGeneration = "wg_inline"
	)
	// A positive-wait delegate_send is consumed inline: the controller hands its
	// caller a real delivery commit, and the caller's tool-results append is the
	// only record that the round presented it. Drive that append, not a
	// hand-built turn, so the test crosses the boundary the reviewer named.
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "HELD_REPORT").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("deliver held report inline: %v", err)
	}
	resolution := <-waiter.resolution
	if resolution.commit == nil {
		t.Fatalf("inline delivery resolution carries no commit: %#v", resolution)
	}
	deliveryID := plan.deliveryID
	reportID := delegateAttentionID(deliveryID)
	c.attentionCauses.remember(reportID, provenance.WithWatch(nil, watchID, watchGeneration, deliveryID, "child-session", ""))
	sess := newDelegateToolResultPersistenceSession(t, c, afero.NewMemMapFs())
	sess.queueDelegateDeliveryCommit("send", resolution.commit)
	if err := appendDelegateToolResultFixture(sess, "send"); err != nil {
		t.Fatalf("append inline result: %v", err)
	}

	// The next request presents the resident history, tool-results turn
	// included. A full-history round carries every staged turn; a responses
	// delta carries only the turns it names, so it must be told which of those
	// is the tool-results turn. Both carry it here, so both must adopt the
	// report's lineage and retire its cause.
	presented := currentHistory(t, sess)
	for _, settled := range []llm.Request{
		{HistoryMode: llm.HistoryModeFullHistory},
		{HistoryMode: llm.HistoryModeResponsesDelta},
	} {
		sess.replaceActiveProvenance(nil)
		c.attentionCauses.remember(reportID, provenance.WithWatch(nil, watchID, watchGeneration, deliveryID, "child-session", ""))
		sess.resetAttentionCoverage()
		sess.stagePresentedAttentionCoverage(context.Background(), presented, presented)
		sess.promoteStagedAttentionCoverage(settled)

		turn := sess.activeCausalProvenance()
		if depth := provenance.SelfInfluenceDepth(turn, watchID, watchGeneration, func(string) bool { return true }); depth != 1 {
			t.Fatalf("%s: the turn that consumed the held report exposes its watch fire at depth %d, want 1: %#v", settled.HistoryMode, depth, turn)
		}
		if cause := c.attentionCauses.union([]string{reportID}); cause != nil {
			t.Fatalf("%s: the consumed report's cause is still registered after incorporation: %#v", settled.HistoryMode, cause)
		}
	}
}

// A responses delta carries only the turns after its continuation anchor. An
// inline delivery staged from a tool-results turn BEFORE that anchor was not
// presented by this call, so a delta that settles must not adopt its cause:
// that would credit the model with seeing a result it never received AND
// forget the cause for good, since an inline result is spent on adoption.
func TestDeltaDoesNotAdoptInlineDeliveryItDidNotCarry(t *testing.T) {
	const (
		watchID         = "watch_delta_scope"
		watchGeneration = "wg_delta_scope"
	)
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "HELD_REPORT").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("deliver held report inline: %v", err)
	}
	resolution := <-waiter.resolution
	if resolution.commit == nil {
		t.Fatalf("inline delivery resolution carries no commit: %#v", resolution)
	}
	deliveryID := plan.deliveryID
	reportID := delegateAttentionID(deliveryID)
	sess := newDelegateToolResultPersistenceSession(t, c, afero.NewMemMapFs())
	sess.queueDelegateDeliveryCommit("send", resolution.commit)
	if err := appendDelegateToolResultFixture(sess, "send"); err != nil {
		t.Fatalf("append inline result: %v", err)
	}

	// A later user message is the delta-eligible suffix: the continuation anchor
	// sits after the tool-results turn, so the delta does not carry it.
	sess.appendTurn(schema.TurnUserInput, llm.User("a later message past the anchor"))
	presented := currentHistory(t, sess)
	inlineAt := -1
	for i, turn := range presented {
		if len(turn.DelegateDeliveryCommits) != 0 {
			inlineAt = i
			break
		}
	}
	if inlineAt < 0 {
		t.Fatalf("no presented turn carries the inline delivery commit: %#v", presented)
	}
	deltaTurns := presented[inlineAt+1:]
	if len(deltaTurns) == 0 {
		t.Fatal("test has no turn past the inline one for the delta to carry")
	}

	sess.replaceActiveProvenance(nil)
	c.attentionCauses.remember(reportID, provenance.WithWatch(nil, watchID, watchGeneration, deliveryID, "child-session", ""))
	sess.resetAttentionCoverage()
	sess.stagePresentedAttentionCoverage(context.Background(), presented, deltaTurns)
	sess.promoteStagedAttentionCoverage(llm.Request{HistoryMode: llm.HistoryModeResponsesDelta})

	if turn := sess.activeCausalProvenance(); turn != nil {
		if depth := provenance.SelfInfluenceDepth(turn, watchID, watchGeneration, func(string) bool { return true }); depth != 0 {
			t.Fatalf("the delta adopted the watch fire of an inline delivery it never carried (depth %d): %#v", depth, turn)
		}
	}
	if cause := c.attentionCauses.union([]string{reportID}); cause == nil {
		t.Fatal("the delta spent an inline delivery it never carried: its cause is gone, so no later full-history round can present it")
	}

	// The full-history round that does carry it adopts and retires it, which is
	// the behavior the delta case above must not preempt.
	sess.replaceActiveProvenance(nil)
	sess.resetAttentionCoverage()
	sess.stagePresentedAttentionCoverage(context.Background(), presented, deltaTurns)
	sess.promoteStagedAttentionCoverage(llm.Request{HistoryMode: llm.HistoryModeFullHistory})
	if turn := sess.activeCausalProvenance(); turn == nil {
		t.Fatal("the full-history round that carried the inline delivery adopted nothing")
	} else if depth := provenance.SelfInfluenceDepth(turn, watchID, watchGeneration, func(string) bool { return true }); depth != 1 {
		t.Fatalf("the full-history round that carried the inline delivery exposes its watch fire at depth %d, want 1: %#v", depth, turn)
	}
	if cause := c.attentionCauses.union([]string{reportID}); cause != nil {
		t.Fatalf("the full-history round left the consumed report's cause registered: %#v", cause)
	}
}

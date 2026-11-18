package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// A finalizing stable delegate whose only pending work is a watch notice must
// not wait forever for it.
//
// The generation it is finalizing under is the reason a successor cannot start:
// the attention drive refuses while the generation is open. So the drain that
// already holds that generation is the only actor that can present the row. It
// used to count only queued notifications, root attention and delegate updates,
// so a delegate waiting on a shell while a progress or output-match notice
// about that shell sat in durable attention would park indefinitely, and the
// notice that was supposed to tell it what to do about the shell was the thing
// it was waiting for (#2340 review M2).
//
// The oracle is the drain's own ending, not the row afterwards. The drain is
// driven on a fixture where nothing else can finish it: if the watch row is not
// presented, the loop never returns, so the bounded wait below times out. That
// is the blocked state being shown rather than asserted.
func TestFinalizationDrainPresentsPendingWatchAttention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
		return agenttest.FinalResponse("acted on the watch notice")
	})
	client.Register(adapter)
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "compact-cheap/model")
	sess := newSession(t, withClient(client), withProfile(profile), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
	}))
	drainSessionEvents(sess)

	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_draining", "")
	sess.mu.Lock()
	ownController := sess.delegateController
	ownOwner := sess.owningDelegateID
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_draining"
	sess.mu.Unlock()
	t.Cleanup(func() {
		sess.mu.Lock()
		sess.delegateController = ownController
		sess.owningDelegateID = ownOwner
		sess.mu.Unlock()
	})
	controller.mu.Lock()
	controller.live["dlg_draining"].runtime = sess
	controller.live["dlg_draining"].binding.runtime = sess
	controller.mu.Unlock()

	// One durable, unresolved watch row. It is the ONLY pending work: no queued
	// notification, no root attention, no delegate update.
	watchID := stableWatchAttentionIDPrefix + "watch:drain:job:drain"
	if _, err := sess.appendDelegateNotificationDurably(watchID, "unresolved watch notice"); err != nil {
		t.Fatalf("persist watch attention: %v", err)
	}
	pending, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	if len(pending) != 1 || pending[0] != watchID {
		t.Fatalf("setup: pending attention = %v, want exactly the watch row", pending)
	}
	if sess.peekNotifications() != 0 || sess.pendingRootDelegateAttention() || sess.hasPendingDelegateUpdateAttention() {
		t.Fatal("setup: something other than the watch row is pending, so the drain could finish for another reason")
	}

	// The lease the run finalizes under. The drain holds this in production.
	leaseCtx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_draining", generation: 1})
	ctx, cancel := context.WithCancel(leaseCtx)
	defer cancel()

	ran := make(chan EntryKind, 4)
	process := func(ctx context.Context, _ string, _ []ImageAttachment, kind EntryKind) (string, error) {
		ran <- kind
		// Present the row the way a leased attention turn does: the request
		// stages it and turn-end resolution retires it.
		sess.stagePresentedAttentionCoverage(ctx, currentHistory(t, sess), nil)
		sess.promoteStagedAttentionCoverage(llm.Request{})
		if err := sess.finishAttentionTurn(nil, nil); err != nil {
			return "", err
		}
		return "", nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := sess.drainJobTreeWith(ctx, nil, func(context.Context) error { return nil }, process)
		done <- err
	}()

	// The drain must reach its turn rather than park. A drain that never
	// presents the row blocks here, which is the finding.
	select {
	case kind := <-ran:
		if kind != EntryDelegateAttention {
			t.Fatalf("the drain opened a %v turn for watch attention, want the attention entry: the notification entry stands down on it", kind)
		}
	case <-time.After(2 * time.Second): // TRIPWIRE: in-process drain on a scripted adapter with no I/O; only fires when the drain is genuinely parked.
		t.Fatal("the finalization drain never presented its pending watch attention: it is waiting for a generation that cannot start until this drain finishes")
	}

	// And the row it presented must be retired, or the drain would re-open the
	// turn for the same row forever.
	remaining, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention after the drain turn: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("the drain presented the watch row but left it pending (%v): the next pass runs another turn for it", remaining)
	}

	// The drain has no other work, so it now ends.
	cancel()
	select {
	case err := <-done:
		if err != nil && ctx.Err() == nil {
			t.Fatalf("drain returned %v", err)
		}
	case <-time.After(2 * time.Second): // TRIPWIRE: same in-process fixture; a hang here means the drain spun after settling.
		t.Fatal("the drain did not finish after presenting and settling its only pending work")
	}
}

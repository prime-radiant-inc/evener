package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// The teardown paths mark a watch rejecting while its end is persisted. When the
// persist fails the watch has to be live and deliverable again, and a retry
// after the store recovers has to complete the teardown.

func teardownFailureConfig(id, target, sendTo string) (watchKey, *watchConfig) {
	key := watchKey{VisibleSessionID: testOwnerSessionID, Target: target, SendTo: sendTo}
	cfg := &watchConfig{
		watchID:          id,
		sourcePublic:     "self",
		target:           target,
		send:             &watchSendArgs{To: sendTo, Message: "observe"},
		generation:       "gen-" + id,
		outputMatch:      "ready",
		createdAt:        frozenTestTime,
		pending:          make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
		settledUpdateSeq: make(map[jobstore.WatchSendKey]uint64),
	}
	return key, cfg
}

func addTeardownFailurePending(cfg *watchConfig, wsKey jobstore.WatchSendKey, seq uint64) {
	cfg.pending[wsKey] = &jobstore.WatchSendState{
		Key:        wsKey,
		DeliveryID: "delivery-" + cfg.watchID + string(rune('0'+seq)),
		UpdateSeq:  seq,
		CreatedAt:  frozenTestTime,
		UpdatedAt:  frozenTestTime,
	}
	cfg.pendingOrder = append(cfg.pendingOrder, wsKey)
}

func installTeardownFailureConfig(jm *jobManager, key watchKey, cfg *watchConfig) {
	jm.mu.Lock()
	jm.watches[key] = cfg
	jm.mu.Unlock()
}

func watchLiveAndAcceptingDelivery(jm *jobManager, key watchKey, cfg *watchConfig) (live, rejecting bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.watches[key] == cfg, cfg.rejectingDelivery
}

func failWatchClearedAppends(jm *jobManager, fail error) (restore func()) {
	real := jm.appendEvents
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchCleared {
				return fail
			}
		}
		return real(events)
	}
	return func() { jm.appendEvents = real }
}

func TestClearWatchPersistFailureLeavesWatchLiveAndRetryable(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	key, cfg := teardownFailureConfig("watch-clear", "job_clear", "dlg_clear")
	installTeardownFailureConfig(jm, key, cfg)

	appendErr := errors.New("teardown persist failed")
	restore := failWatchClearedAppends(jm, appendErr)
	if _, err := jm.clearWatch(key); !errors.Is(err, appendErr) {
		t.Fatalf("clear error = %v, want the persist failure", err)
	}
	if live, rejecting := watchLiveAndAcceptingDelivery(jm, key, cfg); !live || rejecting {
		t.Fatalf("after failed clear: live=%t rejecting=%t, want live and accepting delivery", live, rejecting)
	}

	restore()
	if _, err := jm.clearWatch(key); err != nil {
		t.Fatalf("retry clear: %v", err)
	}
	if live, _ := watchLiveAndAcceptingDelivery(jm, key, cfg); live {
		t.Fatal("watch is still live after a successful retry")
	}
}

func TestBudgetAutoClearPersistFailureReArmsTheBudgetLatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	key, cfg := teardownFailureConfig("watch-budget", "job_budget", "dlg_budget")
	cfg.budgetTripped = true
	installTeardownFailureConfig(jm, key, cfg)

	restore := failWatchClearedAppends(jm, errors.New("budget persist failed"))
	if _, ok := jm.autoClearWatchOverBudgetNotification(cfg); ok {
		t.Fatal("auto-clear reported success although its teardown did not persist")
	}
	if live, rejecting := watchLiveAndAcceptingDelivery(jm, key, cfg); !live || rejecting {
		t.Fatalf("after failed auto-clear: live=%t rejecting=%t, want live and accepting delivery", live, rejecting)
	}
	jm.mu.Lock()
	tripped := cfg.budgetTripped
	jm.mu.Unlock()
	if tripped {
		t.Fatal("budget latch stayed set, so no later fire could retry the auto-clear")
	}

	restore()
	if _, ok := jm.autoClearWatchOverBudgetNotification(cfg); !ok {
		t.Fatal("retry auto-clear did not complete")
	}
	if live, _ := watchLiveAndAcceptingDelivery(jm, key, cfg); live {
		t.Fatal("watch is still live after a successful retry")
	}
}

func TestAbandonRunningJobsPersistFailureLeavesWatchLive(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	const jobID = "job_abandoned"
	key, cfg := teardownFailureConfig("watch-abandon", jobID, "dlg_abandon")
	first := jobstore.WatchSendKey{WatchID: cfg.watchID, WatchTarget: jobID}
	second := jobstore.WatchSendKey{WatchID: cfg.watchID, WatchTarget: jobID, ResolvedSendTo: "second"}
	addTeardownFailurePending(cfg, first, 1)
	addTeardownFailurePending(cfg, second, 2)
	installTeardownFailureConfig(jm, key, cfg)
	jm.mu.Lock()
	jm.running[jobID] = &runningJob{rec: &jobstore.JobRecord{JobID: jobID}, done: make(chan struct{})}
	jm.mu.Unlock()

	// The first dropped frame lands durably and the second does not: the batch
	// is partly applied.
	appended := 0
	jm.appendEvent = func(event jobstore.Event) error {
		appended++
		if appended > 1 {
			return errors.New("abandon persist failed")
		}
		return nil
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	jm.abandonRunningJobs()

	if live, rejecting := watchLiveAndAcceptingDelivery(jm, key, cfg); !live || rejecting {
		t.Fatalf("after failed abandon: live=%t rejecting=%t, want live and accepting delivery", live, rejecting)
	}
	jm.mu.Lock()
	_, appliedStillPending := cfg.pending[first]
	_, unappliedStillPending := cfg.pending[second]
	jm.mu.Unlock()
	if appliedStillPending {
		t.Fatal("the frame whose drop landed durably is still pending")
	}
	if !unappliedStillPending {
		t.Fatal("the frame whose drop did not land was removed")
	}
	if len(notified) != 1 {
		t.Fatalf("notifications = %d, want one cleanup-failure notice", len(notified))
	}
}

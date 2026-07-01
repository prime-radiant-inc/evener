package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// w2conc_recordedWatchSend builds a job-manager with one output-match watch
// that sends to a delegate target, fires it, and records the pending send —
// returning the current cfg+state the delivery primitive operates on.
func w2conc_recordedWatchSend(t *testing.T) (*jobManager, *watchConfig, jobstore.WatchSendState) {
	t.Helper()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready")
	state, cfg, ok, err := jm.recordWatchSend(delivery)
	if err != nil {
		t.Fatalf("recordWatchSend: %v", err)
	}
	if !ok {
		t.Fatal("recordWatchSend returned ok=false")
	}
	return jm, cfg, state
}

// TestW2Conc_DeliverPendingWatchSendStaleStateNoOps pins the not-current arm:
// a state whose DeliveryID no longer matches the recorded pending is dropped as
// a no-op (a superseded frame that raced with a newer one).
func TestW2Conc_DeliverPendingWatchSendStaleStateNoOps(t *testing.T) {
	t.Parallel()
	jm, cfg, state := w2conc_recordedWatchSend(t)
	state.DeliveryID = "superseded"

	sent := 0
	delivered, err := jm.deliverPendingWatchSend(context.Background(), cfg, state, false,
		func(context.Context, sendMessageArgs) sendMessageResult { sent++; return sendMessageResult{} })
	if err != nil || delivered {
		t.Fatalf("deliver = (%v, %v), want (false, nil) for a superseded state", delivered, err)
	}
	if sent != 0 {
		t.Fatalf("superseded state still invoked the sender %d times", sent)
	}
}

// TestW2Conc_DeliverPendingWatchSendNilSenderDrops pins the nil-sender arm: a
// current pending with no available sender is permanently dropped.
func TestW2Conc_DeliverPendingWatchSendNilSenderDrops(t *testing.T) {
	t.Parallel()
	jm, cfg, state := w2conc_recordedWatchSend(t)

	delivered, err := jm.deliverPendingWatchSend(context.Background(), cfg, state, false, nil)
	if err != nil {
		t.Fatalf("deliver with nil sender: %v", err)
	}
	if delivered {
		t.Fatal("nil sender reported delivered=true, want false (dropped)")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after nil-sender drop = %+v, want dropped", pending)
	}
}

// TestW2Conc_DeliverPendingWatchSendEnsurePendingAppendFails pins the
// ensure-pending durable-append failure arm: when re-persisting the pending
// state fails, the primitive surfaces the error and does not deliver.
func TestW2Conc_DeliverPendingWatchSendEnsurePendingAppendFails(t *testing.T) {
	t.Parallel()
	jm, cfg, state := w2conc_recordedWatchSend(t)
	failAppendN(jm, jobstore.EventWatchSendPending, 1)

	sent := 0
	delivered, err := jm.deliverPendingWatchSend(context.Background(), cfg, state, true,
		func(context.Context, sendMessageArgs) sendMessageResult { sent++; return sendMessageResult{} })
	if err == nil {
		t.Fatal("deliver with failing pending append returned nil error, want the append failure")
	}
	if delivered || sent != 0 {
		t.Fatalf("deliver = (%v), sent=%d; want no delivery when the pending append fails", delivered, sent)
	}
}

// TestW2Conc_DeliverPendingWatchSendRacedAwayAfterDeliver pins the
// post-delivery not-current arm: the send succeeds, but the pending was
// concurrently settled/superseded before the delivered-state commit, so the
// primitive returns without re-settling.
func TestW2Conc_DeliverPendingWatchSendRacedAwayAfterDeliver(t *testing.T) {
	t.Parallel()
	jm, cfg, state := w2conc_recordedWatchSend(t)

	// The sender simulates a concurrent settle: it removes the pending entry so
	// the post-delivery isCurrentPendingWatchSend re-check fails.
	delivered, err := jm.deliverPendingWatchSend(context.Background(), cfg, state, false,
		func(context.Context, sendMessageArgs) sendMessageResult {
			jm.mu.Lock()
			delete(cfg.pending, state.Key)
			jm.mu.Unlock()
			return sendMessageResult{}
		})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if delivered {
		t.Fatal("delivered=true though the pending raced away before the delivered-state commit")
	}
}

// TestW2Conc_DeliverPendingWatchSendUnknownClassNoOps pins the defensive
// default arm of the delivery-class switch: an unrecognized delivery class is
// treated as a no-op (neither settled nor dropped).
func TestW2Conc_DeliverPendingWatchSendUnknownClassNoOps(t *testing.T) {
	t.Parallel()
	jm, cfg, state := w2conc_recordedWatchSend(t)

	delivered, err := jm.deliverPendingWatchSend(context.Background(), cfg, state, false,
		func(context.Context, sendMessageArgs) sendMessageResult {
			return sendMessageResult{
				WatchSendDeliveryClassSet: true,
				WatchSendDeliveryClass:    watchSendDeliveryClass(99),
			}
		})
	if err != nil || delivered {
		t.Fatalf("deliver = (%v, %v), want (false, nil) for an unknown delivery class", delivered, err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after unknown-class no-op = %+v, want the frame left pending", pending)
	}
}

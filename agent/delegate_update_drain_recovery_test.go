package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// drainingDelegateWithUpdate builds a delegate-owned session whose transcript
// holds one unresolved update from a child, the state a coordinator is in when
// its drain starts with that update pending.
func drainingDelegateWithUpdate(t *testing.T, configure func(*SessionConfig)) (sess *Session, updateID string) {
	t.Helper()
	stateDir := t.TempDir()
	cfg := SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}
	if configure != nil {
		configure(&cfg)
	}
	sess = newSession(t, withDir(stateDir), withConfig(cfg))
	sess.owningDelegateID = "dlg_coordinator"
	updateID = delegateAttentionID(delegatestore.UpdateDeliveryID("dlg_worker", 1))
	if _, err := sess.appendDelegateNotificationDurably(updateID, `<delegate-notification delegate_id="dlg_worker" kind="update">which table holds the drain cursor?</delegate-notification>`); err != nil {
		t.Fatalf("append update attention: %v", err)
	}
	return sess, updateID
}

// creditUpdate leaves the coverage a successful leased turn leaves when its
// settled full-history request presented the update.
func creditUpdate(sess *Session, updateID string) {
	sess.attentionMu.Lock()
	defer sess.attentionMu.Unlock()
	sess.attentionCoveredIDs = map[string]struct{}{updateID: {}}
}

func pendingUpdateCount(t *testing.T, sess *Session) int {
	t.Helper()
	pending, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	return len(pending)
}

// A coordinator's last running turn showed the model an update but could not
// mark it consumed. Its drain turn is a responses-continuation delta, which
// credits no attention and starts by clearing the previous turn's coverage, so
// the drain retries the marker before any turn and runs no model turn for an
// update the model already saw.
func TestDelegateUpdateShownByThePrecedingTurnIsNotRerunByTheDrain(t *testing.T) {
	t.Parallel()
	sess, updateID := drainingDelegateWithUpdate(t, nil)
	installResolutionSyncFailuresWriter(t, sess, 1)
	creditUpdate(sess, updateID)
	if err := sess.finishAttentionTurn(nil, nil); err == nil {
		t.Fatal("test setup: the preceding turn's marker write did not fail")
	}

	turns := 0
	deltaTurn := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns++
		sess.resetAttentionCoverage()
		if turns > 2 {
			return "", errors.New("the drain keeps running turns for an update the model saw")
		}
		return "", nil
	}
	if _, err := sess.drainJobTreeWith(context.Background(), make(chan time.Time), func(context.Context) error { return nil }, deltaTurn); err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}
	if turns != 0 {
		t.Fatalf("the drain ran %d model turns for an update the preceding turn showed, want 0", turns)
	}
	if got := pendingUpdateCount(t, sess); got != 0 {
		t.Fatalf("%d updates still pending after storage recovered, want the drain's retry to have marked it consumed", got)
	}
}

// When both marker writes fail and the next read of the transcript fails too,
// the turn's coverage alone says the update was shown: the drain runs no
// second turn for it once storage recovers.
func TestDelegateUpdateIsNotRerunWhenMarkersAndTheFollowingReadFail(t *testing.T) {
	t.Parallel()
	var failing atomic.Pointer[rootAttentionResolutionFailFS]
	var readFailed atomic.Bool
	sess, updateID := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) {
		cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			if writes := failing.Load(); writes != nil && writes.injectedFailures() == 2 && readFailed.CompareAndSwap(false, true) {
				return delegateAttentionFold{}, errors.New("transient transcript read failure")
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	failing.Store(installResolutionSyncFailuresWriter(t, sess, 2))

	turns := 0
	fullHistoryTurn := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns++
		sess.resetAttentionCoverage()
		creditUpdate(sess, updateID)
		// The turn's own marker write; its failure is the drain's to retry.
		_ = sess.finishAttentionTurn(nil, nil)
		return "", nil
	}
	if _, err := sess.drainJobTreeWith(context.Background(), make(chan time.Time), func(context.Context) error { return nil }, fullHistoryTurn); err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}
	if !readFailed.Load() {
		t.Fatal("test setup: the transcript read never failed")
	}
	if turns != 1 {
		t.Fatalf("the drain ran %d model turns, want 1: the update was shown by the first", turns)
	}
}

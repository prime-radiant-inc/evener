package agent

import (
	"errors"
	"sort"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func (jm *jobManager) observeWatchReceiptBoundary() {
	if jm.watchReceiptBoundary != nil {
		jm.watchReceiptBoundary()
	}
}

func (jm *jobManager) rememberStableWatchReceipt(receipt *delegateWatchReceipt) {
	if receipt == nil {
		return
	}
	jm.mu.Lock()
	jm.stableWatchReceipts[receipt.deliveryID] = receipt
	jm.mu.Unlock()
}

func (jm *jobManager) stableWatchReceipt(deliveryID string) *delegateWatchReceipt {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.stableWatchReceipts[deliveryID]
}

func (jm *jobManager) releaseStableWatchReceipt(deliveryID string) {
	jm.mu.Lock()
	receipt := jm.stableWatchReceipts[deliveryID]
	delete(jm.stableWatchReceipts, deliveryID)
	// The frame is settled, dropped or retired: its failed acknowledgement,
	// if one was noted, no longer needs a retry.
	delete(jm.unsettledWatchSends, deliveryID)
	jm.mu.Unlock()
	if receipt != nil {
		jm.observeWatchReceiptBoundary()
		_ = receipt.controller.CompleteWatchDelivery(receipt)
	}
}

func (jm *jobManager) rememberStableWatchSettlementRetry(cfg *watchConfig, state jobstore.WatchSendState) {
	if state.DeliveryID == "" {
		return
	}
	jm.mu.Lock()
	jm.rememberStableWatchSettlementRetryLocked(pendingWatchSendDelivery{cfg: cfg, state: state})
	jm.mu.Unlock()
	jm.kick()
}

// rememberStableWatchSettlementRetryLocked queues retry for the settlement
// retry pass. Caller holds jm.mu and wakes the owner once it lets go.
func (jm *jobManager) rememberStableWatchSettlementRetryLocked(retry pendingWatchSendDelivery) {
	if jm.stableWatchSettlementRetries == nil {
		jm.stableWatchSettlementRetries = make(map[string]pendingWatchSendDelivery)
	}
	jm.stableWatchSettlementRetries[retry.state.DeliveryID] = retry
}

func (jm *jobManager) forgetStableWatchSettlementRetry(state jobstore.WatchSendState) {
	jm.mu.Lock()
	retry, ok := jm.stableWatchSettlementRetries[state.DeliveryID]
	if ok && retry.state.UpdateSeq == state.UpdateSeq {
		delete(jm.stableWatchSettlementRetries, state.DeliveryID)
	}
	jm.mu.Unlock()
}

func (jm *jobManager) claimStableWatchSettlementRetries() ([]pendingWatchSendDelivery, bool) {
	jm.mu.Lock()
	if jm.stableWatchSettlementRetrying {
		jm.mu.Unlock()
		return nil, false
	}
	retries := jm.stableWatchSettlementRetryBatchLocked()
	if len(retries) != 0 {
		jm.stableWatchSettlementRetrying = true
	}
	jm.mu.Unlock()
	return retries, true
}

func (jm *jobManager) stableWatchSettlementRetryBatchLocked() []pendingWatchSendDelivery {
	retries := make([]pendingWatchSendDelivery, 0, len(jm.stableWatchSettlementRetries))
	for _, retry := range jm.stableWatchSettlementRetries {
		retries = append(retries, retry)
	}
	sort.Slice(retries, func(i, j int) bool {
		return watchSendStateLess(&retries[i].state, &retries[j].state)
	})
	return retries
}

func (jm *jobManager) finishStableWatchSettlementRetry() {
	jm.mu.Lock()
	jm.stableWatchSettlementRetrying = false
	jm.mu.Unlock()
}

func (jm *jobManager) nextStableWatchSettlementRetryBatch() ([]pendingWatchSendDelivery, bool) {
	jm.mu.Lock()
	if len(jm.stableWatchSettlementRetries) == 0 {
		jm.stableWatchSettlementRetrying = false
		jm.mu.Unlock()
		return nil, false
	}
	retries := jm.stableWatchSettlementRetryBatchLocked()
	jm.mu.Unlock()
	return retries, true
}

func (jm *jobManager) hasPendingStableWatchSettlementRetry() bool {
	if jm == nil {
		return false
	}
	jm.mu.Lock()
	pending := len(jm.stableWatchSettlementRetries) != 0 || jm.stableWatchSettlementRetrying
	jm.mu.Unlock()
	return pending
}

func (jm *jobManager) retryStableWatchSettlements() (bool, error) {
	retries, claimed := jm.claimStableWatchSettlementRetries()
	if !claimed {
		return false, nil
	}
	if len(retries) == 0 {
		return true, nil
	}
	releaseClaim := true
	defer func() {
		if releaseClaim {
			jm.finishStableWatchSettlementRetry()
		}
	}()
	for {
		for _, retry := range retries {
			controller := jm.delegateController
			if controller == nil {
				return true, errors.New("stable watch controller is unavailable")
			}
			receiver, err := controller.stableWatchReceiver(retry.state.ReceiverSessionID, retry.state.ReceiverDelegateID)
			if err != nil {
				return true, err
			}
			attentionID := stableWatchAttentionID(retry.state)
			// A frame whose arm failed is armed before it is acknowledged:
			// acknowledging releases its receipt and its retry, after which
			// nothing would arm it.
			if retry.unarmed {
				if err := jm.armStableWatchFrame(controller, receiver, retry.state.ReceiverDelegateID, attentionID); err != nil {
					return true, err
				}
			}
			if err := jm.settleWatchSendDelivered(retry.cfg, retry.state); err != nil {
				return true, err
			}
			jm.forgetStableWatchSettlementRetry(retry.state)
			if retry.unarmed {
				continue
			}
			if err := jm.armStableWatchFrame(controller, receiver, retry.state.ReceiverDelegateID, attentionID); err != nil {
				return true, err
			}
		}
		var more bool
		retries, more = jm.nextStableWatchSettlementRetryBatch()
		if !more {
			releaseClaim = false
			return true, nil
		}
	}
}

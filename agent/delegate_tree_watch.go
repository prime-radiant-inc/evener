package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/transcript"
)

type delegateWatchSourceBinding struct {
	lease   *delegateLease
	runtime *Session
}

type delegateWatchReceipt struct {
	controller         *delegateTreeController
	token              uint64
	sourceDelegateID   string
	sourceGeneration   uint64
	receiverDelegateID string
	deliveryID         string
	updateSeq          uint64
}

func (c *delegateTreeController) ResolveStableWatchSource(actor delegateActor, sourceID string) (delegateWatchSourceBinding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return delegateWatchSourceBinding{}, errDelegateTargetBusy
	}
	if err := c.authorizeMutationLocked(actor, sourceID); err != nil {
		return delegateWatchSourceBinding{}, err
	}
	aggregate := c.durable[sourceID]
	live := c.live[sourceID]
	if aggregate == nil || !aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseRunning || live == nil || live.binding == nil || !live.binding.ready || live.binding.runtime == nil {
		return delegateWatchSourceBinding{}, fmt.Errorf("source_not_watchable: stable delegate %q is not running", sourceID)
	}
	if c.stopCoversLocked(sourceID) {
		return delegateWatchSourceBinding{}, errDelegateTargetBusy
	}
	lease := live.binding.lease
	return delegateWatchSourceBinding{lease: &lease, runtime: live.binding.runtime}, nil
}

func (c *delegateTreeController) ResolveParentWatchSource(actor delegateActor) (delegateWatchSourceBinding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || actor.lease == nil {
		return delegateWatchSourceBinding{}, errDelegateNotControllable
	}
	child, _, err := c.exactLeaseLocked(*actor.lease)
	if err != nil {
		return delegateWatchSourceBinding{}, err
	}
	if !child.Descriptor.ParentWatchGranted {
		return delegateWatchSourceBinding{}, errors.New("source_not_watchable: source parent requires delegate(watch_parent=true)")
	}
	parentID := child.Descriptor.ParentDelegateID
	if parentID == "" {
		if c.rootRuntime == nil {
			return delegateWatchSourceBinding{}, errDelegateTargetBusy
		}
		return delegateWatchSourceBinding{runtime: c.rootRuntime}, nil
	}
	parent := c.durable[parentID]
	live := c.live[parentID]
	if parent == nil || !parent.CurrentRunOpen || parent.Phase != delegatestore.PhaseRunning || live == nil || live.binding == nil || !live.binding.ready || live.binding.runtime == nil {
		return delegateWatchSourceBinding{}, fmt.Errorf("source_not_watchable: parent delegate %q is not running", parentID)
	}
	lease := live.binding.lease
	return delegateWatchSourceBinding{lease: &lease, runtime: live.binding.runtime}, nil
}
func (c *delegateTreeController) BeginWatchEnqueue(sourceID string, sourceGeneration uint64, receiverID, deliveryID string, updateSeq uint64, terminalSource bool) (*delegateWatchReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateWatchReceiptLocked(sourceID, sourceGeneration, receiverID, terminalSource); err != nil {
		return nil, err
	}
	if c.stopIntersectsWatchLocked(sourceID, receiverID) {
		return nil, errDelegateTargetBusy
	}
	c.nextToken++
	receipt := &delegateWatchReceipt{
		controller: c, token: c.nextToken,
		sourceDelegateID: sourceID, sourceGeneration: sourceGeneration,
		receiverDelegateID: receiverID, deliveryID: deliveryID, updateSeq: updateSeq,
	}
	c.watchEnqueues[receipt.token] = receipt
	c.evidenceVersion++
	return receipt, nil
}

func (c *delegateTreeController) CompleteWatchEnqueue(receipt *delegateWatchReceipt) (*delegateWatchReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt == nil || receipt.controller != c || c.watchEnqueues[receipt.token] != receipt {
		return nil, errDelegateStaleLease
	}
	delete(c.watchEnqueues, receipt.token)
	if c.stop != nil {
		delete(c.stop.watchEnqueues, receipt.token)
	}
	c.nextToken++
	delivery := *receipt
	delivery.token = c.nextToken
	c.watchDeliveries[delivery.token] = &delivery
	if c.stopIntersectsWatchLocked(delivery.sourceDelegateID, delivery.receiverDelegateID) {
		c.stop.watchDeliveries[delivery.token] = struct{}{}
	}
	c.evidenceVersion++
	c.signalStopProgressLocked()
	return &delivery, nil
}

func (c *delegateTreeController) AbortWatchEnqueue(receipt *delegateWatchReceipt) {
	if receipt == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt.controller != c || c.watchEnqueues[receipt.token] != receipt {
		return
	}
	delete(c.watchEnqueues, receipt.token)
	if c.stop != nil {
		delete(c.stop.watchEnqueues, receipt.token)
	}
	c.evidenceVersion++
	c.signalStopProgressLocked()
}

func (c *delegateTreeController) AcquireWatchDelivery(sourceID string, sourceGeneration uint64, receiverID, deliveryID string, updateSeq uint64, terminalSource bool) (*delegateWatchReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateWatchReceiptLocked(sourceID, sourceGeneration, receiverID, terminalSource); err != nil {
		return nil, err
	}
	for _, receipt := range c.watchDeliveries {
		if receipt != nil && receipt.deliveryID == deliveryID && receipt.updateSeq == updateSeq {
			if receipt.sourceDelegateID != sourceID || receipt.sourceGeneration != sourceGeneration || receipt.receiverDelegateID != receiverID {
				return nil, errDelegateStaleLease
			}
			return receipt, nil
		}
	}
	if c.stopIntersectsWatchLocked(sourceID, receiverID) {
		return nil, errDelegateTargetBusy
	}
	c.nextToken++
	receipt := &delegateWatchReceipt{
		controller: c, token: c.nextToken,
		sourceDelegateID: sourceID, sourceGeneration: sourceGeneration,
		receiverDelegateID: receiverID, deliveryID: deliveryID, updateSeq: updateSeq,
	}
	c.watchDeliveries[receipt.token] = receipt
	c.evidenceVersion++
	return receipt, nil
}

func (c *delegateTreeController) CompleteWatchDelivery(receipt *delegateWatchReceipt) error {
	if receipt == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt.controller != c || c.watchDeliveries[receipt.token] != receipt {
		return errDelegateStaleLease
	}
	delete(c.watchDeliveries, receipt.token)
	if c.stop != nil {
		delete(c.stop.watchDeliveries, receipt.token)
	}
	c.evidenceVersion++
	c.signalStopProgressLocked()
	return nil
}

func (c *delegateTreeController) stableWatchReceiver(receiverSessionID, receiverDelegateID string) (delegateDeliveryReceiver, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if receiverDelegateID == "" {
		if receiverSessionID != c.rootSessionID || c.rootRuntime == nil {
			return nil, errDelegateDeliveryReceiverUnavailable
		}
		return c.rootRuntime, nil
	}
	aggregate := c.durable[receiverDelegateID]
	if aggregate == nil || aggregate.Descriptor.ChildSessionID != receiverSessionID {
		return nil, errDelegateDeliveryReceiverUnavailable
	}
	receiver := c.deliveryReceiverLocked(receiverDelegateID)
	if receiver == nil {
		return nil, errDelegateDeliveryReceiverUnavailable
	}
	return receiver, nil
}

func (c *delegateTreeController) watchSourceSessions() []*Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[*Session]struct{})
	var sessions []*Session
	add := func(session *Session) {
		if session == nil {
			return
		}
		if _, ok := seen[session]; ok {
			return
		}
		seen[session] = struct{}{}
		sessions = append(sessions, session)
	}
	add(c.rootRuntime)
	for _, live := range c.live {
		if live != nil {
			add(live.runtime)
		}
	}
	return sessions
}

func (c *delegateTreeController) validateWatchReceiptLocked(sourceID string, sourceGeneration uint64, receiverID string, terminalSource bool) error {
	if c.closing {
		return errDelegateTargetBusy
	}
	if sourceID != "" {
		aggregate := c.durable[sourceID]
		if aggregate == nil || aggregate.Generation != sourceGeneration || (!terminalSource && !aggregate.CurrentRunOpen) {
			return errDelegateStaleLease
		}
	}
	if receiverID != "" && c.durable[receiverID] == nil {
		return errDelegateNotControllable
	}
	return nil
}

func (c *delegateTreeController) stopIntersectsWatchLocked(sourceID, receiverID string) bool {
	if c.stop == nil {
		return false
	}
	_, sourceCovered := c.stop.members[sourceID]
	_, receiverCovered := c.stop.members[receiverID]
	return sourceCovered || receiverCovered
}

type stableWatchBootstrapSnapshot struct {
	stateDir      string
	storePaths    []string
	receiverByID  map[string]stableWatchReceiverSnapshot
	attentionOpen delegateAttentionWriterOpener
	now           func() time.Time
}

type stableWatchReceiverSnapshot struct {
	sessionID     string
	transcriptRef string
}

// repairStableWatchDeliveriesForBootstrap completes source-journal handoffs
// before the controller is published. Process receipts do not survive a crash,
// so startup refolds each stable pending cursor, durably appends its exact
// receiver attention, and only then acknowledges the source journal.
func repairStableWatchDeliveriesForBootstrap(c *delegateTreeController) error {
	if c == nil {
		return errors.New("stable watch bootstrap controller is nil")
	}
	snapshot := c.stableWatchBootstrapSnapshot()
	for _, storePath := range snapshot.storePaths {
		info, err := os.Stat(storePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat stable watch source journal: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("stable watch source journal %s is not a regular file", storePath)
		}
		if err := repairStableWatchStoreForBootstrap(snapshot, storePath); err != nil {
			return err
		}
	}
	return nil
}

func (c *delegateTreeController) stableWatchBootstrapSnapshot() stableWatchBootstrapSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := map[string]struct{}{
		filepath.Join(jobsDir(c.stateDir, c.rootSessionID), "jobs.jsonl"): {},
	}
	receivers := map[string]stableWatchReceiverSnapshot{
		"": {sessionID: c.rootSessionID, transcriptRef: "local:" + c.rootSessionID},
	}
	for id, aggregate := range c.durable {
		if aggregate == nil {
			continue
		}
		descriptor := aggregate.Descriptor
		paths[filepath.Join(jobsDir(c.stateDir, descriptor.ChildSessionID), "jobs.jsonl")] = struct{}{}
		receivers[id] = stableWatchReceiverSnapshot{
			sessionID:     descriptor.ChildSessionID,
			transcriptRef: descriptor.TranscriptRef,
		}
	}
	storePaths := make([]string, 0, len(paths))
	for path := range paths {
		storePaths = append(storePaths, path)
	}
	sort.Strings(storePaths)
	return stableWatchBootstrapSnapshot{
		stateDir:      c.stateDir,
		storePaths:    storePaths,
		receiverByID:  receivers,
		attentionOpen: c.attentionOpen,
		now:           c.now,
	}
}

func repairStableWatchStoreForBootstrap(snapshot stableWatchBootstrapSnapshot, storePath string) (err error) {
	store, err := jobstore.Open(storePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	folded, err := store.LoadWatchSends()
	if err != nil {
		return err
	}
	pending := make([]jobstore.WatchSendState, 0, len(folded.Pending))
	for _, state := range folded.Pending {
		if state != nil && state.StableReceiver {
			pending = append(pending, *state)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].DeliveryID != pending[j].DeliveryID {
			return pending[i].DeliveryID < pending[j].DeliveryID
		}
		return pending[i].UpdateSeq < pending[j].UpdateSeq
	})
	for _, state := range pending {
		current, err := store.LoadWatchSends()
		if err != nil {
			return err
		}
		exact := current.Pending[state.Key]
		if exact == nil || exact.DeliveryID != state.DeliveryID || exact.UpdateSeq != state.UpdateSeq {
			continue
		}
		receiver, ok := snapshot.receiverByID[state.ReceiverDelegateID]
		if !ok || receiver.sessionID == "" || receiver.transcriptRef == "" || receiver.sessionID != state.ReceiverSessionID {
			return fmt.Errorf("stable watch receiver %q is unavailable", state.ReceiverDelegateID)
		}
		path, sessionID, err := delegateTranscriptPathFromRef(snapshot.stateDir, receiver.transcriptRef)
		if err != nil {
			return err
		}
		if sessionID != receiver.sessionID {
			return fmt.Errorf("stable watch receiver transcript session %q does not match %q", sessionID, receiver.sessionID)
		}
		now := time.Now().UTC()
		if snapshot.now != nil {
			now = snapshot.now()
		}
		open := snapshot.attentionOpen
		if open == nil {
			open = transcript.OpenWriterForSession
		}
		if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, stableWatchAttentionID(state), stableWatchNotificationContent(state), now, open); err != nil {
			return err
		}
		if err := store.Append(jobstore.Event{
			Kind:      jobstore.EventWatchSendDelivered,
			TS:        now,
			WatchSend: exact,
		}); err != nil {
			return err
		}
	}
	return nil
}

package agent

import (
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

// jobManagerForToken resolves which jobManager owns a token's pending state.
func (s *Session) jobManagerForToken(tok *watchSendToken) *jobManager {
	if tok == nil {
		return nil
	}
	if tok.ChildSessionID == "" {
		return s.jobManager
	}
	if s.subagents == nil {
		return nil
	}
	if sub := s.subagents.get(tok.ChildSessionID); sub != nil && sub.sess != nil {
		return sub.sess.jobManager
	}
	return nil
}

// resolveWatchSendToken returns the CURRENT frame for a token, or ok=false if
// the token is stale (latest-frame-wins; also covers delivery-after-drop).
func (s *Session) resolveWatchSendToken(tok *watchSendToken) (jm *jobManager, cfg *watchConfig, state jobstore.WatchSendState, ok bool) {
	jm = s.jobManagerForToken(tok)
	if jm == nil {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg = jm.watchConfigForKeyLocked(tok.Key)
	if cfg == nil {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	cur := cfg.pending[tok.Key]
	if cur == nil || cur.UpdateSeq != tok.UpdateSeq {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	return jm, cfg, *cur, true
}

// watchConfigForKeyLocked returns the live or terminal-flush watch config that
// currently holds a pending entry for key, or nil. Caller holds jm.mu.
func (jm *jobManager) watchConfigForKeyLocked(key jobstore.WatchSendKey) *watchConfig {
	for _, cfg := range jm.watches {
		if cfg.pending[key] != nil {
			return cfg
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg.pending[key] != nil {
			return cfg
		}
	}
	return nil
}

// settleWatchSendDelivered durably records delivery for a watch-send state and
// removes its exact pending entry when it is still the current cursor. A newer
// coalesced cursor is preserved while this delivered source is acknowledged.
// The caller is responsible for any error-notification behavior. This is the
// model-facing completion for both watch-send rails (delegate sidecar sends and
// caller frames), so it is where the send half of the delivery budget is counted;
// observation/coalescing does not count (spec §4 F1). It is not, however, the
// only place the condition-fire breaker can trip — the match site latches it
// too, so frames that never settle stay bounded — and the latch keeps the two
// to one teardown.
func (jm *jobManager) settleWatchSendDelivered(cfg *watchConfig, state jobstore.WatchSendState) error {
	defer jm.publishWatchActivity()
	delivered := state
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendDelivered,
		TS:        jm.now(),
		WatchSend: &delivered,
	}}); err != nil {
		return err
	}
	jm.removePendingWatchSend(cfg, delivered.Key, delivered.UpdateSeq)
	jm.releaseStableWatchReceipt(delivered.DeliveryID)
	jm.mu.Lock()
	if delivered.DeliveryID != "" {
		jm.deliveredWatchSendIDs[delivered.DeliveryID] = struct{}{}
	}
	crossedBudget := jm.recordWatchDeliveryLocked(cfg)
	jm.mu.Unlock()
	if crossedBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return nil
}

// watchSendDeliveredLocked reports whether a watch-send with deliveryID has
// settled delivered. Caller holds jm.mu.
func (jm *jobManager) watchSendDeliveredLocked(deliveryID string) bool {
	_, ok := jm.deliveredWatchSendIDs[deliveryID]
	return ok
}

// watchSendDelivered reports whether a watch-send with deliveryID has settled
// delivered, taking jm.mu for callers outside a locked section.
func (jm *jobManager) watchSendDelivered(deliveryID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.watchSendDeliveredLocked(deliveryID)
}

// claimWatchSendDelivery takes process-local ownership of one watch-send
// delivery id. It returns false when another executor already owns it, so the
// second caller stands down instead of racing the same store write. Mirrors
// claimStableWatchSettlementRetries. Guarded by jm.mu.
func (jm *jobManager) claimWatchSendDelivery(deliveryID string) bool {
	if deliveryID == "" {
		return true
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if _, held := jm.deliveringWatchSends[deliveryID]; held {
		return false
	}
	if jm.deliveringWatchSends == nil {
		jm.deliveringWatchSends = make(map[string]struct{})
	}
	jm.deliveringWatchSends[deliveryID] = struct{}{}
	return true
}

// releaseWatchSendDeliveryClaim releases a claim taken by
// claimWatchSendDelivery. It is safe to call on a delivery id that was never
// claimed.
func (jm *jobManager) releaseWatchSendDeliveryClaim(deliveryID string) {
	if deliveryID == "" {
		return
	}
	jm.mu.Lock()
	delete(jm.deliveringWatchSends, deliveryID)
	_, superseded := jm.supersededWatchSends[deliveryID]
	delete(jm.supersededWatchSends, deliveryID)
	handOff := superseded && jm.retryUnsettledWatchSendLocked(deliveryID)
	_, retrying := jm.stableWatchSettlementRetries[deliveryID]
	jm.mu.Unlock()
	if handOff {
		jm.kick()
	}
	if superseded && !retrying {
		jm.releaseStableWatchReceipt(deliveryID)
	}
}

// retryUnsettledWatchSendLocked hands a replaced frame the receiver took but
// never acknowledged to the settlement retry, which keeps its receipt until it
// acknowledges. It reports false when the frame has no failed acknowledgement
// noted. Caller holds jm.mu.
func (jm *jobManager) retryUnsettledWatchSendLocked(deliveryID string) bool {
	unsettled, accepted := jm.unsettledWatchSends[deliveryID]
	if !accepted {
		return false
	}
	delete(jm.unsettledWatchSends, deliveryID)
	jm.rememberStableWatchSettlementRetryLocked(unsettled)
	return true
}

// watchSendSuperseded reports whether a coalesced frame replaced the claimed
// delivery deliveryID while it was being delivered.
func (jm *jobManager) watchSendSuperseded(deliveryID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, superseded := jm.supersededWatchSends[deliveryID]
	return superseded
}

func (jm *jobManager) watchSendSnapshot(cfg *watchConfig, jobID, trigger string, root events.SessionEvent) watchSendDelivery {
	sendTo := ""
	if cfg.send != nil {
		sendTo = cfg.send.To
	}
	cfg.nextUpdateSeq++
	deliveryID := jobstore.NewWatchSendDeliveryID()
	return watchSendDelivery{
		cfg:               cfg,
		key:               watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: sendTo, ReceiverSessionID: cfg.receiverSessionID, ReceiverDelegateID: cfg.receiverDelegateID},
		generation:        cfg.generation,
		updateSeq:         cfg.nextUpdateSeq,
		send:              cloneWatchSendArgs(cfg.send),
		deliveryID:        deliveryID,
		visibleSessionID:  jm.sessionID,
		watchTarget:       cfg.target,
		watchedIdentity:   jobID,
		trigger:           trigger,
		triggerProvenance: provenance.Clone(root.Provenance),
		provenance:        provenance.WithWatch(root.Provenance, cfg.watchID, cfg.generation, deliveryID, root.SessionID, jobID),
		eventKind:         root.Kind,
		eventData:         root.Data,
	}
}

// jobProvenanceForWatch returns a clone of the watched job's recorded causal
// provenance, the base for a synthetic delivery root on the non-session-event
// fire paths (output_match, attach scan, terminal catch-up, terminal flush,
// progress tick) where there is no triggering SessionEvent to carry it. It is
// self-locking and reads the store on a fallback, so callers MUST NOT hold
// jm.mu. A session-target jobID (or an unknown job) has no job provenance and
// returns nil.
func jobProvenanceForWatch(jm *jobManager, jobID string) *provenance.Causal {
	if jm == nil || jobID == "" || isWatchSessionTarget(jobID) {
		return nil
	}
	jm.mu.Lock()
	if run := jm.running[jobID]; run != nil && run.rec != nil {
		p := provenance.Clone(run.rec.Provenance)
		jm.mu.Unlock()
		return p
	}
	jm.mu.Unlock()
	recs, err := jm.store.Load()
	if err != nil {
		return nil
	}
	if rec := recs[jobID]; rec != nil {
		return provenance.Clone(rec.Provenance)
	}
	return nil
}

type watchSendLoader func() (jobstore.WatchSendRecord, error)

func (jm *jobManager) restoreWatchSendPending() error {
	return jm.restoreWatchSendPendingFrom(jm.store.LoadWatchSends)
}

func (jm *jobManager) restoreWatchSendPendingFrom(load watchSendLoader) error {
	rec, err := load()
	if err != nil {
		return err
	}
	if len(rec.Pending) == 0 {
		return nil
	}
	retired, err := jm.retiredDelegateJobIDs()
	if err != nil {
		return err
	}
	states := make([]*jobstore.WatchSendState, 0, len(rec.Pending))
	for _, state := range rec.Pending {
		if state == nil || addressedThroughRetiredDelegate(state.Key, retired) {
			continue
		}
		copied := *state
		states = append(states, &copied)
	}
	sort.SliceStable(states, func(i, j int) bool {
		return watchSendStateLess(states[i], states[j])
	})

	cfgs := make(map[restoredWatchConfigKey]*watchConfig)
	for _, state := range states {
		key := state.Key
		cfgKey := restoredWatchConfigKey{
			visibleSessionID:   key.VisibleSessionID,
			watchID:            key.WatchID,
			target:             key.WatchTarget,
			sendTo:             key.ResolvedSendTo,
			receiverSessionID:  strings.TrimSpace(state.ReceiverSessionID),
			receiverDelegateID: strings.TrimSpace(state.ReceiverDelegateID),
			generation:         key.WatchGeneration,
			sourceDelegateID:   strings.TrimSpace(state.SourceDelegateID),
			sourceGeneration:   state.SourceDelegateGeneration,
			stableReceiver:     state.StableReceiver,
		}
		cfg := cfgs[cfgKey]
		if cfg == nil {
			sourcePublic := watchPublicSource(state.SourceDelegateID, key.WatchTarget)
			if cfgKey.sourceDelegateID == "" && cfgKey.receiverSessionID != "" && key.WatchTarget == runtimeMessageAliasCaller {
				sourcePublic = "parent"
			}
			cfg = &watchConfig{
				watchID:            key.WatchID,
				sourcePublic:       sourcePublic,
				receiverSessionID:  cfgKey.receiverSessionID,
				receiverDelegateID: cfgKey.receiverDelegateID,
				target:             key.WatchTarget,
				send:               &watchSendArgs{To: key.ResolvedSendTo},
				generation:         key.WatchGeneration,
				pending:            make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
				sourceDelegateID:   cfgKey.sourceDelegateID,
				sourceGeneration:   cfgKey.sourceGeneration,
				stableReceiver:     cfgKey.stableReceiver,
			}
			cfgs[cfgKey] = cfg
		}
		pending := *state
		cfg.pending[key] = &pending
		cfg.pendingOrder = append(cfg.pendingOrder, key)
		if pending.UpdateSeq > cfg.nextUpdateSeq {
			cfg.nextUpdateSeq = pending.UpdateSeq
		}
	}
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	for _, cfg := range cfgs {
		if len(cfg.pending) != 0 {
			jm.terminalFlush[cfg] = true
		}
	}
	return nil
}

func watchSendStateLess(a, b *jobstore.WatchSendState) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.Before(b.UpdatedAt)
	}
	if a.UpdateSeq != b.UpdateSeq {
		return a.UpdateSeq < b.UpdateSeq
	}
	return watchSendKeyLess(a.Key, b.Key)
}

func watchSendKeyLess(a, b jobstore.WatchSendKey) bool {
	if a.VisibleSessionID != b.VisibleSessionID {
		return a.VisibleSessionID < b.VisibleSessionID
	}
	if a.WatchTarget != b.WatchTarget {
		return a.WatchTarget < b.WatchTarget
	}
	if a.ResolvedWatchedIdentity != b.ResolvedWatchedIdentity {
		return a.ResolvedWatchedIdentity < b.ResolvedWatchedIdentity
	}
	if a.ResolvedSendTo != b.ResolvedSendTo {
		return a.ResolvedSendTo < b.ResolvedSendTo
	}
	if a.WatchID != b.WatchID {
		return a.WatchID < b.WatchID
	}
	return a.WatchGeneration < b.WatchGeneration
}

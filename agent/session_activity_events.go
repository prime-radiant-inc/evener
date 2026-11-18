package agent

import (
	"context"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// Activity invalidations travel through the existing bridge without becoming
// model-facing watch observations. Publication happens outside domain locks.
func (s *Session) emitSessionActivityChanged(owner string, resource appwire.SessionActivityResource) {
	s.emitSessionActivityChangedBounded(nil, owner, resource)
}

// emitSessionActivityChangedBounded is emitSessionActivityChanged on the bound
// of the caller whose event caused the change; see publishWatchActivityContext.
func (s *Session) emitSessionActivityChangedBounded(ctx context.Context, owner string, resource appwire.SessionActivityResource) {
	if s == nil || owner == "" {
		return
	}
	targets := []string{owner}
	if controller := s.delegateController; controller != nil {
		controller.mu.Lock()
		targets = controller.sessionAncestryLocked(owner)
		controller.mu.Unlock()
	}
	if ctx == nil {
		s.publishSessionActivityChanged(owner, targets, resource)
		return
	}
	s.publishSessionActivityChangedContext(ctx, owner, targets, resource)
}

// publishSessionActivityChanged invalidates owner's activity on each target
// thread; targets is owner's sessionAncestryLocked chain, which a caller that
// already holds it passes in rather than walking the tree again.
func (s *Session) publishSessionActivityChanged(owner string, targets []string, resource appwire.SessionActivityResource) {
	s.publishSessionActivityChangedContext(context.Background(), owner, targets, resource)
}

// publishSessionActivityChangedContext is publishSessionActivityChanged on a
// caller's bound. The invalidation is part of the announcement a close drain's
// pass publishes, so it must release on that pass's bound rather than the full
// close budget: a nested pass that parks here still holds the pass open, and the
// budget it spends is the one that removes the remaining collectible lanes.
//
// Only a caller that has a bound passes one. Every other caller reaches this
// through publishSessionActivityChanged, which supplies context.Background and
// so keeps today's behavior exactly: the session's published close budget, when
// there is one, and no caller deadline otherwise.
func (s *Session) publishSessionActivityChangedContext(ctx context.Context, owner string, targets []string, resource appwire.SessionActivityResource) {
	if s == nil || owner == "" {
		return
	}
	rootID := s.ID()
	if s.delegateController != nil {
		rootID = s.delegateController.rootSessionID
	}
	sessionActivityIndexes.Lock()
	index := sessionActivityIndexes.entries[s.stateDir+"\x00"+rootID]
	if index != nil {
		index.revision.Add(1)
	}
	sessionActivityIndexes.Unlock()
	for _, target := range targets {
		s.sendEventOnContext(ctx, events.SessionActivityChangedData{ThreadID: target, Ref: encodeRef("", target), SessionID: owner, Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceSummary, resource}}, nil)
	}
}

// sessionAncestryLocked is sessionID followed by each session above it in the
// tree's logical ownership, nearest first, ending at the root: the sessions
// whose subtree holds sessionID.
func (c *delegateTreeController) sessionAncestryLocked(sessionID string) []string {
	chain := []string{sessionID}
	seen := map[string]bool{sessionID: true}
	for current := sessionID; current != c.rootSessionID; {
		row := c.durable[c.delegateOwnerOfSessionLocked(current)]
		if row == nil {
			break
		}
		current = sessionActivityDelegateOwner(c.durable, row)
		if current == "" || seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, current)
	}
	return chain
}

func (jm *jobManager) noteWatchActivity(receiver string) {
	if receiver == "" {
		receiver = jm.sessionID
	}
	jm.watchActivityMu.Lock()
	if jm.watchActivityReceivers == nil {
		jm.watchActivityReceivers = make(map[string]struct{})
	}
	jm.watchActivityReceivers[receiver] = struct{}{}
	jm.watchActivityMu.Unlock()
}
func (jm *jobManager) publishWatchActivity() {
	jm.publishWatchActivityContext(nil)
}

// publishWatchActivityContext flushes the receivers whose watch activity moved,
// on the bound of the caller that caused the move. A nil bound is the ordinary
// path and keeps today's behavior exactly: the session's published close budget
// when there is one, and no caller deadline otherwise.
//
// It exists because the flush is SYNCHRONOUS with the send that triggered it.
// A close drain publishing a lost-result warning can take the last slot in a
// full authoritative stream; the flush that follows then runs on the drain's
// goroutine, and on the ordinary path it waits out the whole close cascade --
// spending the budget the worktree removals need, after the warning itself was
// already correctly bounded. Undelivered invalidations are simply not published;
// the receiver's next ordinary event publishes them, exactly as before.
func (jm *jobManager) publishWatchActivityContext(ctx context.Context) {
	if jm == nil || jm.retirementOwner == nil {
		return
	}
	jm.watchActivityMu.Lock()
	receivers := jm.watchActivityReceivers
	jm.watchActivityReceivers = nil
	jm.watchActivityMu.Unlock()
	for receiver := range receivers {
		jm.retirementOwner.emitSessionActivityChangedBounded(ctx, receiver, appwire.SessionActivityResourceWatches)
	}
}
func (jm *jobManager) noteAcceptedWatchEvents(batch []jobstore.Event) {
	for _, event := range batch {
		if event.Kind == jobstore.EventWatchRegistered && event.Watch != nil {
			receiver := ""
			if config := event.Watch.Config; config != nil {
				receiver = config.ReceiverSessionID
			}
			jm.noteWatchActivity(receiver)
		}
		if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend != nil {
			jm.noteWatchActivity(event.WatchSend.ReceiverSessionID)
		}
	}
}

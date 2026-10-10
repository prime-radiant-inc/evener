package agent

import (
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// Activity invalidations travel through the existing bridge without becoming
// model-facing watch observations. Publication happens outside domain locks.
func (s *Session) emitSessionActivityChanged(owner string, resource appwire.SessionActivityResource) {
	if s == nil || owner == "" {
		return
	}
	targets := []string{owner}
	if controller := s.delegateController; controller != nil {
		controller.mu.Lock()
		targets = controller.sessionAncestryLocked(owner)
		controller.mu.Unlock()
	}
	s.publishSessionActivityChanged(owner, targets, resource)
}

// publishSessionActivityChanged invalidates owner's activity on each target
// thread; targets is owner's sessionAncestryLocked chain, which a caller that
// already holds it passes in rather than walking the tree again.
func (s *Session) publishSessionActivityChanged(owner string, targets []string, resource appwire.SessionActivityResource) {
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
		s.sendEvent(events.EventSessionActivityChanged, events.SessionActivityChangedData{ThreadID: target, Ref: encodeRef("", target), SessionID: owner, Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceSummary, resource}}, nil)
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
	if jm == nil || jm.retirementOwner == nil {
		return
	}
	jm.watchActivityMu.Lock()
	receivers := jm.watchActivityReceivers
	jm.watchActivityReceivers = nil
	jm.watchActivityMu.Unlock()
	for receiver := range receivers {
		jm.retirementOwner.emitSessionActivityChanged(receiver, appwire.SessionActivityResourceWatches)
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

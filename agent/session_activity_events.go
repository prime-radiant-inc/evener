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
	targets := []string{owner}
	if controller := s.delegateController; controller != nil {
		controller.mu.Lock()
		current := owner
		seen := map[string]bool{owner: true}
		for current != controller.rootSessionID {
			id := controller.delegateOwnerOfSessionLocked(current)
			row := controller.durable[id]
			if row == nil {
				break
			}
			current = sessionActivityDelegateOwner(controller.durable, row)
			if current == "" {
				break
			}
			if seen[current] {
				break
			}
			seen[current] = true
			targets = append(targets, current)
		}
		controller.mu.Unlock()
	}
	for _, target := range targets {
		s.sendEvent(events.EventSessionActivityChanged, events.SessionActivityChangedData{ThreadID: target, Ref: encodeRef("", target), SessionID: owner, Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceSummary, resource}}, nil)
	}
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

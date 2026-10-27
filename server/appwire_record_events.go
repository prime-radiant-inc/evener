package server

import (
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/appserver"
)

func (s *Server) AppNotificationsAfter(cursor uint64, threadID string) []appserver.SequencedNotification {
	return s.appNotifier.ReplayAfter(cursor, s.appNotificationTarget(threadID))
}

func (s *Server) AppSubscriberCount(threadID string) int {
	return s.appServer.SubscriberCount(s.appNotificationTarget(threadID))
}

func (s *Server) appNotificationTarget(threadID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if threadID == s.appThreadID && s.appRef != "" {
		return s.appRef
	}
	return threadID
}

// insideAppProjectionCommitHook, when non-nil, runs inside RecordAppEvent's
// commit callback, i.e. while the projection gate is held. It is a test seam:
// production never assigns it, so the commit path pays one nil check on a
// package-level word instead of the s.mu.RLock a per-server field would need
// on every projected event.
var insideAppProjectionCommitHook func()

func (s *Server) RecordAppEvent(event events.SessionEvent) {
	s.mu.RLock()
	beforeCommit := s.beforeAppProjectionCommit
	s.mu.RUnlock()
	if beforeCommit != nil {
		beforeCommit()
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		// A test-only park INSIDE the commit, where projectionMu is actually
		// held. beforeAppProjectionCommit above cannot serve that purpose: it
		// runs before CommitProjection takes the gate, so a goroutine parked
		// there holds nothing and any ordering it appears to establish is a
		// coin toss. Deliberately consulted without s.mu, so a parked commit
		// blocks a concurrent one on the projection gate rather than on s.mu.
		if insideAppProjectionCommitHook != nil {
			insideAppProjectionCommitHook()
		}
		s.mu.Lock()
		if !s.acceptsSessionEventLocked(event.SessionID) {
			s.mu.Unlock()
			return nil
		}
		if sessionEventClosesSession(event) {
			s.endProcessingLocked()
			s.status.State = string(agent.SessionClosed)
			s.appDeferredTerminalNotifications = nil
		}
		s.ensureAppProjectorLocked(event.SessionID)
		supersededSessionEnd := event.Kind == events.EventSessionEnd && s.appPendingStableTurnID != "" && !sessionEventClosesSession(event)
		supersededSettledStatus := event.Kind == events.EventStatusSettled && s.settledStatusSupersededLocked()
		if event.Kind == events.EventStatusSettled {
			s.settledStatusLocked(event, supersededSettledStatus)
		}
		if started, ok := event.Data.(events.ExecutionStartedData); ok && started.TurnID != "" && started.TurnID == s.appPendingStableTurnID {
			// The execution SetProcessingTurn published has started, so a
			// terminal status from the input before it must not be replayed
			// after this boundary.
			s.appPendingStableTurnID = ""
			s.appDeferredTerminalNotifications = nil
		}
		if event.Kind == events.EventSessionEnd {
			// Every SESSION_END ends the input that held a rest, including an
			// interrupted one, whose status effect is nil and so can't drop it.
			s.appHeldSettledEffect = nil
		}
		s.appActivity.noteIntent(event)
		s.appActivity.observe(event.Kind)
		projected := s.appProjector.Project(event)
		threadID, ref := s.appRootIdentityLocked()
		if threadID == "" {
			threadID = event.SessionID
			ref = appwire.Ref{SourceID: sourceIDForProjection(s.appSourceID), ThreadID: threadID}.String()
		}
		start, _ := event.Data.(events.SessionStartData)
		pending := make([]pendingAppNotification, 0, len(projected))
		pending = append(pending, s.activityChangeNotificationLocked(event)...)
		history := s.appHistories.get(threadID)
		if changed, ok := event.Data.(events.EnvironmentChangedData); ok && event.Kind == events.EventEnvironmentChanged {
			s.appEnvelope.WorkingDir = changed.WorkingDir
			s.appEnvelope.cwdCarrierGeneration++
			resync := appwire.ThreadResyncParams{
				ThreadID:       threadID,
				Ref:            ref,
				BootGeneration: s.appBootGeneration,
			}
			if history != nil {
				resync.Epoch = history.Epoch()
			}
			pending = append(pending, pendingAppNotification{
				threadID: threadID,
				ref:      ref,
				method:   appwire.NotifyEvenerThreadResync,
				params:   resync,
			})
		}
		for _, item := range projected {
			// The root's running execution is published by SetProcessingTurn
			// and its end by finishProcessing (or the input's SESSION_END),
			// synchronously with the session loop. The projector's execution
			// statuses reach the bridge later, and one from an execution that
			// ended would land over the next one's active status.
			if item.Method == appwire.NotifyThreadStatusChanged && (isExecutionBoundary(event) || supersededSettledStatus) {
				continue
			}
			// A queued terminal event can end the input before an execution
			// SetProcessingTurn already published. Do not publish its thread
			// status or close frame over that newer active identity.
			if supersededSessionEnd && (item.Method == appwire.NotifyThreadStatusChanged || item.Method == appwire.NotifyThreadClosed) {
				s.appDeferredTerminalNotifications = append(s.appDeferredTerminalNotifications, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: item.Params})
				continue
			}
			switch params := item.Params.(type) {
			case appwire.ThreadStartedParams:
				startSeed := start.CurrentWork
				if !s.acceptTaskStartPublicationLocked(item.TaskStoreOwnerSessionID, item.TaskPublicationEpoch, item.TaskPublicationRevision) {
					startSeed = currentWorkSeedWithoutTasks(startSeed)
				}
				mergeStartCurrentWork(&params.Thread.Evener, s.appEnvelope.Tasks, s.appEnvelope.Goal, startSeed)
				s.appEnvelope.Tasks = appwire.CloneTaskAggregate(params.Thread.Evener.Tasks)
				s.appEnvelope.Goal = cloneGoalState(params.Thread.Evener.Goal)
				if startSeed != nil && startSeed.Tasks != nil {
					s.appEnvelope.taskCarrierGeneration++
				}
				if start.CurrentWork != nil {
					s.appEnvelope.goalCarrierGeneration++
				}
				if item.TaskStoreOwnerSessionID != "" {
					s.appEnvelope.TaskStoreOwnerSessionID = item.TaskStoreOwnerSessionID
				}
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: params})
			case appwire.TaskUpdatedParams:
				if !s.acceptTaskUpdatePublicationLocked(item.TaskStoreOwnerSessionID, item.TaskPublicationEpoch, item.TaskPublicationRevision) {
					continue
				}
				if item.TaskStoreOwnerSessionID != "" {
					s.appEnvelope.TaskStoreOwnerSessionID = item.TaskStoreOwnerSessionID
				}
				routeOwner := item.TaskStoreOwnerSessionID
				if item.TaskPublicationEpoch == 0 || item.TaskPublicationRevision == 0 {
					routeOwner = ""
				}
				for _, target := range s.taskCarrierTargetsLocked(threadID, routeOwner) {
					targetParams := params
					targetParams.ThreadID = target.threadID
					targetParams.Ref = target.ref
					s.applyTaskCarrierLocked(target.threadID, targetParams)
					pending = append(pending, pendingAppNotification{threadID: target.threadID, ref: target.ref, method: item.Method, params: targetParams})
				}
			case appwire.GoalUpdatedParams:
				s.appEnvelope.Goal = goalPatch(params)
				s.appEnvelope.goalCarrierGeneration++
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: params})
			case appwire.NotesUpdatedParams:
				// Direct typed carrier like GoalUpdatedParams above: the
				// generation bump fences a sampled facetGoal assign taken
				// before this carrier against overwriting it (threadEnvelope's
				// notesCarrierGeneration, mirroring goalCarrierGeneration).
				// It fences the note fields only: the URL list has its own
				// generation, so this commit cannot drop a newer sampled list.
				s.appEnvelope.HumanNote = params.HumanNote
				s.appEnvelope.AgentNote = params.AgentNote
				s.appEnvelope.notesCarrierGeneration++
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: params})
			case appwire.UrlsUpdatedParams:
				// Fresh copy, nil-on-empty: the envelope owns its slices
				// once written (threadEnvelope's rule), so the push's
				// backing array must not alias the installed state.
				s.appEnvelope.SessionURLs = append([]appwire.SessionURL(nil), params.URLs...)
				// This carrier fences SessionURLs alone (threadEnvelope's
				// urlsCarrierGeneration, the URL list's own counter): a URLs
				// commit must not drop newer sampled notes.
				s.appEnvelope.urlsCarrierGeneration++
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: params})
			default:
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: item.Params})
			}
		}
		if s.failureCountMovedLocked() && !pendingRootStatus(pending, threadID) {
			pending = append(pending, pendingAppNotification{
				threadID: threadID,
				ref:      ref,
				method:   appwire.NotifyThreadStatusChanged,
				params:   appwire.ThreadStatusChangedParams{ThreadID: threadID, Ref: ref, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
			})
		}
		s.mu.Unlock()
		// The overlay's changes commit with the projector's, in the same
		// order the session emitted the event.
		if history != nil {
			for _, change := range history.overlayEvent(event) {
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: change.Method, params: change.Params})
			}
		}
		return s.recordAppNotifications(threadID, pending)
	})
}

// appRootIdentityLocked is the served root thread and its ref. Callers hold
// s.mu.
func (s *Server) appRootIdentityLocked() (threadID, ref string) {
	threadID = s.appThreadID
	if threadID == "" {
		threadID = s.status.SessionID
	}
	ref = s.appRef
	if ref == "" && threadID != "" {
		ref = appwire.Ref{SourceID: sourceIDForProjection(s.appSourceID), ThreadID: threadID}.String()
	}
	return threadID, ref
}

// recordAppNotifications records pending, in order, from inside the
// projection commit it runs in. Root notifications (threadID rootThreadID)
// are addressed to the root's notification target and their status changes
// carry the root envelope's figures; every other notification is addressed to
// its own thread. Callers hold no lock.
func (s *Server) recordAppNotifications(rootThreadID string, pending []pendingAppNotification) []appserver.SequencedNotification {
	committed := make([]appserver.SequencedNotification, 0, len(pending))
	rootNotificationTarget := s.appNotificationTarget(rootThreadID)
	for _, item := range pending {
		params := item.params
		notificationTarget := item.threadID
		if item.threadID == rootThreadID {
			params = s.stampFailureCountOnStatusChange(item.method, params)
			params = s.stampCapabilitiesOnStatusChange(item.method, params)
			params = s.stampAskPendingOnStatusChange(item.method, params)
			params = s.stampActiveTurnOnStatusChange(item.method, params)
			notificationTarget = rootNotificationTarget
		}
		params = stampAppNotificationTarget(params, item.threadID, item.ref)
		committed = append(committed, s.appNotifier.Record(notificationTarget, item.method, params))
	}
	return committed
}

// stampActiveTurnOnStatusChange makes the root's thread/status/changed carry
// the running execution SetProcessingTurn published when the thread is
// active, and no turn otherwise.
func (s *Server) stampActiveTurnOnStatusChange(method string, params any) any {
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if method != appwire.NotifyThreadStatusChanged || !ok {
		return params
	}
	status.ActiveTurnID = ""
	if status.Status.Type == appwire.ThreadStatusActive {
		s.mu.RLock()
		status.ActiveTurnID = s.appActiveTurnID
		s.mu.RUnlock()
	}
	return status
}

// finishProcessing ends the running input and publishes the thread settled
// (the state the session's events stored, or idle standing in for a
// published turn's end that hasn't arrived yet), or the terminal status its
// SESSION_END deferred, in one projection commit. A thread whose
// processing already ended publishes nothing: the bridge's status effect for
// a SESSION_END (applySessionEventStatus) clears processing, except for an
// interrupted one, so after any other its status was the last word. After an
// interrupted SESSION_END processing is still set, and this publishes the
// settled status.
func (s *Server) finishProcessing() {
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		wasProcessing := s.processing
		// A published turn whose end the bridge hasn't reached yet: its
		// SESSION_END will state the resting state, and until then idle
		// stands in for it rather than the rest from before the turn.
		awaitingTurnEnd := s.appActiveTurnID != "" && !s.appTurnEndStated
		s.endProcessingLocked()
		threadID, ref := s.appRootIdentityLocked()
		var pending []pendingAppNotification
		for _, item := range s.appDeferredTerminalNotifications {
			if item.threadID != threadID || item.ref != ref {
				continue
			}
			if params, ok := item.params.(appwire.ThreadStatusChangedParams); ok && item.method == appwire.NotifyThreadStatusChanged {
				s.status.State = params.Status.Type
			}
			pending = append(pending, item)
		}
		s.appDeferredTerminalNotifications = nil
		held := s.appHeldSettledEffect
		s.appHeldSettledEffect = nil
		// A deferred terminal status comes from a published turn, and
		// publishing a turn drops the held rest, so the two never meet.
		if held != nil {
			held(s)
		}
		if len(pending) == 0 && wasProcessing && threadID != "" {
			// The stored state is what the session's own events stated: a
			// pass that ran no turn left it as it was, and a held rest or an
			// interrupted turn's end restated it. Nothing runs once processing
			// ends, so a stored active (a forecast that work would follow)
			// reads idle, stored and published alike; work that does follow
			// publishes its own turn.
			status := appStatus(s.status.State, false)
			if awaitingTurnEnd || status == appwire.ThreadStatusActive {
				s.status.State = appwire.ThreadStatusIdle
				status = appwire.ThreadStatusIdle
			}
			pending = append(pending, pendingAppNotification{
				threadID: threadID,
				ref:      ref,
				method:   appwire.NotifyThreadStatusChanged,
				params:   appwire.ThreadStatusChangedParams{ThreadID: threadID, Ref: ref, Status: appwire.ThreadStatus{Type: status}},
			})
		}
		s.mu.Unlock()
		return s.recordAppNotifications(threadID, pending)
	})
}

// settledStatusLocked applies a resting status that settled outside any turn
// (EventStatusSettled) in the same lock hold that decides whether it is
// published: applied when it can be published now; held for the end of
// processing when an input is being taken and no turn is published
// yet, since that input may end without a turn; dropped otherwise.
// The caller holds s.mu.
func (s *Server) settledStatusLocked(event events.SessionEvent, superseded bool) {
	effect := sessionEventStatusEffect(event)
	if effect == nil {
		return
	}
	switch {
	case !superseded:
		effect(s)
	case s.processing && s.appActiveTurnID == "" && s.status.State != string(agent.SessionClosed):
		s.appHeldSettledEffect = effect
	}
}

// pendingRootStatus reports whether pending already holds a status change for
// the root, which will carry the failure count anyway.
func pendingRootStatus(pending []pendingAppNotification, rootThreadID string) bool {
	for _, item := range pending {
		if item.threadID == rootThreadID && item.method == appwire.NotifyThreadStatusChanged {
			return true
		}
	}
	return false
}

// isExecutionBoundary reports an event announcing an execution's start or end.
func isExecutionBoundary(event events.SessionEvent) bool {
	return event.Kind == events.EventExecutionStarted || event.Kind == events.EventExecutionEnded
}

// RecordDescendantAppEvent projects an in-process descendant onto its root
// daemon's AppWire transport. ownerThreadID fences late events from a child of
// a replaced root identity: those events must not bleed into the new tree.
// This deliberately does not sample or stamp the root session envelope;
// descendant mutations remain owned by the agent tree, while this path supplies
// the independently addressed read/notification view.
// Its notes/URL carriers therefore bump no generation: the fields land on the
// descendant's own appwire.Thread, and refreshFacets samples and installs only
// the root envelope, so no sampled install can race these carriers.
func (s *Server) RecordDescendantAppEvent(ownerThreadID string, event events.SessionEvent) {
	threadID := strings.TrimSpace(event.SessionID)
	ownerThreadID = strings.TrimSpace(ownerThreadID)
	if threadID == "" || ownerThreadID == "" || threadID == ownerThreadID {
		return
	}
	var released *threadHistory
	defer func() {
		if released != nil {
			closeHistories([]*threadHistory{released})
		}
	}()
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		if s.appThreadID != ownerThreadID {
			s.mu.Unlock()
			return nil
		}
		parentRef := s.appRef
		if parentRef == "" {
			parentRef = appwire.Ref{SourceID: sourceIDForProjection(s.appSourceID), ThreadID: ownerThreadID}.String()
		}
		projection := s.appDescendants[threadID]
		if projection == nil {
			sourceID := s.appSourceID
			if sourceID == "" {
				sourceID = "local"
			}
			ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
			projection = &appDescendantProjection{
				projector: appprojector.NewAppEventProjector(threadID, ref),
				thread: appwire.Thread{
					ID:        threadID,
					SessionID: threadID,
					Source:    sourceID,
					Evener:    appwire.EvenerThread{Ref: ref, Kind: "subagent", ParentRef: parentRef},
				},
			}
			s.appDescendants[threadID] = projection
		}
		// The history exists before the thread becomes readable (it is readable
		// once appDescendants holds it, from this commit on), so every entry a
		// subscriber's read does not cover is announced to it.
		history := s.ensureDescendantHistory(ownerThreadID, threadID)
		// A descendant's motion is its root's too: the meter shows the whole tree.
		s.appActivity.observe(event.Kind)
		projected := projection.projector.Project(event)
		start, _ := event.Data.(events.SessionStartData)
		pending := make([]pendingAppNotification, 0, len(projected))
		pending = append(pending, s.activityChangeNotificationLocked(event)...)
		for _, item := range projected {
			switch params := item.Params.(type) {
			case appwire.ThreadStartedParams:
				startSeed := start.CurrentWork
				cachedTasks := projection.thread.Evener.Tasks
				if !s.acceptTaskStartPublicationLocked(item.TaskStoreOwnerSessionID, item.TaskPublicationEpoch, item.TaskPublicationRevision) {
					if sharedTasks := s.taskAggregateForOwnerLocked(item.TaskStoreOwnerSessionID); sharedTasks != nil {
						cachedTasks = sharedTasks
					}
					startSeed = currentWorkSeedWithoutTasks(startSeed)
				}
				mergeStartCurrentWork(&params.Thread.Evener, cachedTasks, projection.thread.Evener.Goal, startSeed)
				params.Thread.Evener.ParentRef = parentRef
				projection.thread = params.Thread
				projection.thread.Evener.Kind = "subagent"
				projection.thread.Evener.Access = appwire.SessionAccess(start.Sandbox, start.SandboxNet)
				projection.thread.Evener.Tasks = appwire.CloneTaskAggregate(params.Thread.Evener.Tasks)
				projection.thread.Evener.Goal = cloneGoalState(params.Thread.Evener.Goal)
				params.Thread = projection.thread
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params})
			case appwire.ThreadStatusChangedParams:
				projection.thread.Status = params.Status
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params})
			case appwire.TaskUpdatedParams:
				if !s.acceptTaskUpdatePublicationLocked(item.TaskStoreOwnerSessionID, item.TaskPublicationEpoch, item.TaskPublicationRevision) {
					continue
				}
				routeOwner := item.TaskStoreOwnerSessionID
				if item.TaskPublicationEpoch == 0 || item.TaskPublicationRevision == 0 {
					routeOwner = ""
				}
				for _, target := range s.taskCarrierTargetsLocked(threadID, routeOwner) {
					targetParams := params
					targetParams.ThreadID = target.threadID
					targetParams.Ref = target.ref
					s.applyTaskCarrierLocked(target.threadID, targetParams)
					pending = append(pending, pendingAppNotification{threadID: target.threadID, ref: target.ref, method: item.Method, params: targetParams})
				}
			case appwire.GoalUpdatedParams:
				projection.thread.Evener.Goal = goalPatch(params)
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params})
			case appwire.NotesUpdatedParams:
				projection.thread.Evener.HumanNote = params.HumanNote
				projection.thread.Evener.AgentNote = params.AgentNote
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params})
			case appwire.UrlsUpdatedParams:
				projection.thread.Evener.SessionURLs = append([]appwire.SessionURL(nil), params.URLs...)
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params})
			default:
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: item.Params})
			}
		}
		ref := projection.thread.Evener.Ref
		if ref == "" {
			ref = appwire.Ref{SourceID: projection.thread.Source, ThreadID: threadID}.String()
			projection.thread.Evener.Ref = ref
		}
		for i := range pending {
			if pending[i].ref == "" {
				pending[i].ref = appwire.Ref{SourceID: sourceIDForProjection(s.appSourceID), ThreadID: pending[i].threadID}.String()
			}
		}
		s.mu.Unlock()
		if history != nil {
			for _, change := range history.overlayEvent(event) {
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: change.Method, params: change.Params})
			}
			// A delegate whose session closed releases its history: the
			// projection goroutine and the overlay's notices go with its
			// runtime. It is detached here and closed after the commit, which
			// its goroutine may be waiting on; a later read projects the
			// transcript again (appHistoryForID).
			if sessionEventClosesSession(event) {
				released = s.appHistories.detach(threadID)
			}
		}
		return s.recordAppNotifications(ownerThreadID, pending)
	})
}

type pendingAppNotification struct {
	threadID string
	ref      string
	method   string
	params   any
}

func (s *Server) acceptsSessionEvent(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.acceptsSessionEventLocked(sessionID)
}

func (s *Server) acceptsSessionEventLocked(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return true
	}
	if s.appThreadID != "" {
		return sessionID == s.appThreadID || s.status.SessionID == ""
	}
	if s.status.SessionID != "" {
		return sessionID == s.status.SessionID
	}
	return true
}

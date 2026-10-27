package server

import (
	"sort"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

type taskCarrierTarget struct {
	threadID string
	ref      string
}

func (p *appDescendantProjection) taskStoreOwnerSessionID() string {
	if p == nil || p.projector == nil {
		return ""
	}
	return p.projector.TaskStoreOwnerSessionID()
}

func sourceIDForProjection(sourceID string) string {
	if sourceID == "" {
		return "local"
	}
	return sourceID
}

func legacyTaskPublication(ownerSessionID string, epoch, revision uint64) bool {
	return ownerSessionID == "" || epoch == 0 || revision == 0
}

// acceptTaskStartPublicationLocked establishes an owner's active TaskStore
// incarnation. A larger process-local epoch replaces the old incarnation even
// when its revision restarts at one; a delayed start from a retired epoch cannot
// replace it back. A start from the same shared store retains ordinary revision
// ordering rather than resetting the fence.
func (s *Server) acceptTaskStartPublicationLocked(ownerSessionID string, epoch, revision uint64) bool {
	if legacyTaskPublication(ownerSessionID, epoch, revision) {
		return true
	}
	if s.appTaskPublications == nil {
		s.appTaskPublications = make(map[string]taskPublicationCursor)
	}
	current, exists := s.appTaskPublications[ownerSessionID]
	if !exists || epoch > current.epoch {
		s.appTaskPublications[ownerSessionID] = taskPublicationCursor{epoch: epoch, revision: revision}
		return true
	}
	if epoch < current.epoch || revision < current.revision {
		return false
	}
	if revision > current.revision {
		s.appTaskPublications[ownerSessionID] = taskPublicationCursor{epoch: epoch, revision: revision}
	}
	return true
}

// acceptTaskUpdatePublicationLocked accepts revisioned updates only from the
// active incarnation established by SessionStart. Zero metadata remains the
// old-producer compatibility path and is routed source-only by the caller.
func (s *Server) acceptTaskUpdatePublicationLocked(ownerSessionID string, epoch, revision uint64) bool {
	if legacyTaskPublication(ownerSessionID, epoch, revision) {
		return true
	}
	current, exists := s.appTaskPublications[ownerSessionID]
	if !exists || epoch != current.epoch || revision < current.revision {
		return false
	}
	if revision > current.revision {
		s.appTaskPublications[ownerSessionID] = taskPublicationCursor{epoch: epoch, revision: revision}
	}
	return true
}

// taskAggregateForOwnerLocked returns an already-materialized snapshot for a
// shared store, preferring the root. It is used only when a delayed descendant
// start seed is older than the owner's accepted revision.
func (s *Server) taskAggregateForOwnerLocked(ownerSessionID string) *appwire.TaskAggregate {
	if ownerSessionID == "" {
		return nil
	}
	if s.appEnvelope.TaskStoreOwnerSessionID == ownerSessionID {
		return appwire.CloneTaskAggregate(s.appEnvelope.Tasks)
	}
	ids := make([]string, 0, len(s.appDescendants))
	for id, projection := range s.appDescendants {
		if projection.taskStoreOwnerSessionID() == ownerSessionID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if tasks := s.appDescendants[id].thread.Evener.Tasks; tasks != nil {
			return appwire.CloneTaskAggregate(tasks)
		}
	}
	return nil
}

// taskCarrierTargetsLocked computes one deterministic task-store fanout while
// s.mu is held. The root, when selected, precedes lexically sorted descendants;
// the source is always present exactly once, including for old ownerless events.
func (s *Server) taskCarrierTargetsLocked(sourceThreadID, ownerSessionID string) []taskCarrierTarget {
	rootID := s.appThreadID
	sourceID := sourceIDForProjection(s.appSourceID)
	includeRoot := sourceThreadID == rootID || (ownerSessionID != "" && s.appEnvelope.TaskStoreOwnerSessionID == ownerSessionID)
	targets := make([]taskCarrierTarget, 0, 1+len(s.appDescendants))
	if includeRoot && rootID != "" {
		targets = append(targets, taskCarrierTarget{threadID: rootID, ref: appwire.Ref{SourceID: sourceID, ThreadID: rootID}.String()})
	}
	descendantIDs := make([]string, 0, len(s.appDescendants))
	for id, projection := range s.appDescendants {
		if id == sourceThreadID || (ownerSessionID != "" && projection.taskStoreOwnerSessionID() == ownerSessionID) {
			descendantIDs = append(descendantIDs, id)
		}
	}
	sort.Strings(descendantIDs)
	for _, id := range descendantIDs {
		targets = append(targets, taskCarrierTarget{threadID: id, ref: appwire.Ref{SourceID: sourceID, ThreadID: id}.String()})
	}
	return targets
}

func mergeStartCurrentWork(target *appwire.EvenerThread, cachedTasks *appwire.TaskAggregate, cachedGoal *appwire.GoalState, seed *events.CurrentWorkSeedData) {
	if seed == nil {
		target.Tasks = appwire.CloneTaskAggregate(cachedTasks)
		target.Goal = cloneGoalState(cachedGoal)
		return
	}
	if seed.Tasks == nil {
		target.Tasks = appwire.CloneTaskAggregate(cachedTasks)
	}
	// A present seed's Goal is authoritative, including nil clear. The projector
	// already converted it into target.Goal.
	target.Goal = cloneGoalState(target.Goal)
}

func currentWorkSeedWithoutTasks(seed *events.CurrentWorkSeedData) *events.CurrentWorkSeedData {
	if seed == nil {
		return nil
	}
	clone := *seed
	clone.Tasks = nil
	return &clone
}

func taskPatch(params appwire.TaskUpdatedParams) *appwire.TaskAggregate {
	return appwire.CloneTaskAggregate(&appwire.TaskAggregate{
		Total:     params.Total,
		Done:      params.Done,
		Cancelled: params.Cancelled,
		Remaining: params.Remaining,
		Current:   params.Current,
	})
}

func goalPatch(params appwire.GoalUpdatedParams) *appwire.GoalState {
	return cloneGoalState(params.Goal)
}

func cloneGoalState(value *appwire.GoalState) *appwire.GoalState {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// cloneThreadAccess copies a session's access value so a snapshot owns it
// rather than aliasing the cached envelope's or a descendant projection's
// pointer (S15). Access holds only value fields, so one level is enough.
func cloneThreadAccess(value *appwire.ThreadAccess) *appwire.ThreadAccess {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func (s *Server) applyTaskCarrierLocked(threadID string, params appwire.TaskUpdatedParams) {
	if threadID == s.appThreadID {
		s.appEnvelope.Tasks = taskPatch(params)
		s.appEnvelope.taskCarrierGeneration++
		return
	}
	if projection := s.appDescendants[threadID]; projection != nil {
		projection.thread.Evener.Tasks = taskPatch(params)
	}
}

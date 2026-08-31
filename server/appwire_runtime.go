package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func (s *Server) AppServer() *appserver.Server {
	return s.appServer
}

// PreparedAppIdentity is a validated AppWire identity whose transcript has
// already been projected, but which nothing has published yet. Every fallible
// step -- header validation and transcript projection -- happens while building
// one, so installing it cannot fail and cannot leave the server half-switched.
type PreparedAppIdentity struct {
	sourceID  string
	threadID  string
	ref       string
	projector *appprojector.AppEventProjector
	turns     *appTurnSnapshot
}

// PrepareAppIdentity projects transcriptPath into a seeded turn snapshot for
// threadID and builds the matching event projector. It touches no Server state,
// so a caller can abandon the result on any later failure.
//
// An empty transcript path seeds empty state. A transcript that does not exist
// yet is the same answer: the session simply has no persisted history to seed
// from. A transcript whose header names a DIFFERENT session is an error --
// seeding one thread from another thread's history would publish a
// conversation that never happened.
func PrepareAppIdentity(sourceID, threadID, transcriptPath string) (PreparedAppIdentity, error) {
	if sourceID == "" {
		sourceID = "local"
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	return PrepareAppIdentityForRef(sourceID, threadID, ref, transcriptPath)
}

// prepareAppIdentitySource is the already-projected half every
// PrepareAppIdentity* entry point installs.
type prepareAppIdentitySource struct {
	turns            []appwire.Turn
	persistedEntries int
}

// fromTranscriptFile is the file-reading source. A missing transcript is not
// an error: the session simply has no persisted history to seed from. A
// transcript whose header names a DIFFERENT session is an error -- seeding
// one thread from another thread's history would publish a conversation
// that never happened.
func fromTranscriptFile(threadID, transcriptPath string) (prepareAppIdentitySource, error) {
	var out prepareAppIdentitySource
	if path := strings.TrimSpace(transcriptPath); path != "" {
		header := transcriptHeader(path, appTranscriptMaxLineBytes)
		if header.SessionID != "" && header.SessionID != threadID {
			return out, fmt.Errorf("transcript %s belongs to session %s, not %s", path, header.SessionID, threadID)
		}
		projected, entries, err := appTurnsFromTranscriptFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return out, err
		}
		out.turns = projected
		out.persistedEntries = entries
	}
	return out, nil
}

// PrepareAppIdentityForRef is PrepareAppIdentity for a replacement that keeps
// the same stable workspace ref while advancing the live session instance.
// The projector must use that ref too: otherwise the first event after clear
// would be published to the replacement session's derived ref and existing
// subscribers would miss it.
func PrepareAppIdentityForRef(sourceID, threadID, ref, transcriptPath string) (PreparedAppIdentity, error) {
	if sourceID == "" {
		sourceID = "local"
	}
	if strings.TrimSpace(threadID) == "" {
		return PreparedAppIdentity{}, errors.New("thread id is required")
	}
	parsedRef, err := appwire.ParseRef(strings.TrimSpace(ref))
	if err != nil {
		return PreparedAppIdentity{}, fmt.Errorf("invalid app identity ref: %w", err)
	}
	if parsedRef.SourceID != sourceID {
		return PreparedAppIdentity{}, fmt.Errorf("app identity ref belongs to source %s, not %s", parsedRef.SourceID, sourceID)
	}
	source, err := fromTranscriptFile(threadID, transcriptPath)
	if err != nil {
		return PreparedAppIdentity{}, err
	}
	return finishPreparedAppIdentity(sourceID, threadID, parsedRef, source)
}

// PrepareAppIdentityFromEntries is PrepareAppIdentityForRef for the resume
// path: it projects the SAME transcript the session restore just
// strict-decoded, instead of re-reading and re-decoding the file. header and
// entries must come from that restore pass (OpenWriterForSession's resume
// reader), which already validated the header's SessionID against threadID --
// so a transcript naming a different session cannot reach this point, and
// the error contract of the file form (a mismatch is an error) is preserved
// by construction rather than by re-reading. Callers that cannot prove
// that validation must use the file form.
//
// Everything else -- ref parsing, turn seeding, projector construction, and
// the fence of live turn ids above the seeded ones -- is identical to
// PrepareAppIdentityForRef.
func PrepareAppIdentityFromEntries(sourceID, threadID, ref string, header transcript.Header, entries []transcript.Entry) (PreparedAppIdentity, error) {
	if sourceID == "" {
		sourceID = "local"
	}
	if strings.TrimSpace(threadID) == "" {
		return PreparedAppIdentity{}, errors.New("thread id is required")
	}
	parsedRef, err := appwire.ParseRef(strings.TrimSpace(ref))
	if err != nil {
		return PreparedAppIdentity{}, fmt.Errorf("invalid app identity ref: %w", err)
	}
	if parsedRef.SourceID != sourceID {
		return PreparedAppIdentity{}, fmt.Errorf("app identity ref belongs to source %s, not %s", parsedRef.SourceID, sourceID)
	}
	if header.SessionID != "" && header.SessionID != threadID {
		return PreparedAppIdentity{}, fmt.Errorf("transcript header belongs to session %s, not %s", header.SessionID, threadID)
	}
	turns, persisted, err := appTurnsFromEntries(header, entries)
	if err != nil {
		return PreparedAppIdentity{}, err
	}
	return finishPreparedAppIdentity(sourceID, threadID, parsedRef, prepareAppIdentitySource{turns: turns, persistedEntries: persisted})
}

// finishPreparedAppIdentity validates the identity triple and installs the
// projected source into a PreparedAppIdentity. It is the shared tail of
// PrepareAppIdentityForRef and PrepareAppIdentityFromEntries.
func finishPreparedAppIdentity(sourceID, threadID string, parsedRef appwire.Ref, source prepareAppIdentitySource) (PreparedAppIdentity, error) {
	snapshot := &appTurnSnapshot{threadID: threadID}
	snapshot.Seed(source.turns)
	// Fence the live turn ids above the seeded ones HERE, where the seed count
	// is known, rather than waiting for the session's own SessionStart to carry
	// it: nothing orders that event ahead of the first turn-starting request,
	// and since this snapshot became the only turn authority a collision
	// overwrites the seeded turn permanently.
	projector := appprojector.NewAppEventProjector(threadID, parsedRef.String())
	projector.SeedPersistedTurns(source.persistedEntries)
	return PreparedAppIdentity{
		sourceID:  sourceID,
		threadID:  threadID,
		ref:       parsedRef.String(),
		projector: projector,
		turns:     snapshot,
	}, nil
}

// ReplaceAppIdentity publishes a prepared identity. It runs inside one
// projection commit so no notification can cross between the old authority and
// the new one, and it cannot fail: preparation already did everything that
// could.
//
// activate, when non-nil, runs first and must itself be infallible -- it is
// where a caller swaps whatever it owns alongside the daemon's identity (the
// live session on clear).
//
// Replacing a different, non-empty identity closes the old thread's stream with
// one thread/closed record targeted at the OLD thread and ref. That record is
// recorded through the notifier so it takes the same ordered delivery path as
// every other notification, and it is deliberately not applied to the new
// snapshot: it describes the thread that just ended.
func (s *Server) ReplaceAppIdentity(prepared PreparedAppIdentity, activate func()) {
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		if activate != nil {
			activate()
		}
		s.mu.Lock()
		oldSourceID, oldThreadID := s.appSourceID, s.appThreadID
		oldRef := s.appRef
		if oldRef == "" && oldThreadID != "" {
			oldSource := oldSourceID
			if oldSource == "" {
				oldSource = "local"
			}
			oldRef = appwire.Ref{SourceID: oldSource, ThreadID: oldThreadID}.String()
		}
		newRef := prepared.ref
		if newRef == "" {
			newSource := prepared.sourceID
			if newSource == "" {
				newSource = "local"
			}
			newRef = appwire.Ref{SourceID: newSource, ThreadID: prepared.threadID}.String()
		}
		s.appSourceID = prepared.sourceID
		s.appThreadID = prepared.threadID
		s.appRef = newRef
		s.appProjector = prepared.projector
		s.installCostLookup(s.appProjector)
		s.appTurns = prepared.turns
		oldDescendantIDs := make([]string, 0, len(s.appDescendants))
		for threadID := range s.appDescendants {
			oldDescendantIDs = append(oldDescendantIDs, threadID)
		}
		s.appDescendants = make(map[string]*appDescendantProjection)
		s.appTaskPublications = make(map[string]taskPublicationCursor)
		s.appActiveTurnID = ""
		s.appReservedTurnID = ""
		s.appLastStampedFailedToolCalls = nil
		// The envelope describes the session that just stopped being this
		// daemon's session, so it is replaced in the SAME commit as the identity
		// it belongs to. Zeroing the whole struct rather than clearing named
		// fields is what makes that structural: a field added to threadEnvelope
		// later is reset here for free, and cannot survive an identity change by
		// being forgotten. The caller re-seeds with RefreshThreadEnvelope once
		// the replacement session is the live one.
		s.appEnvelope = threadEnvelope{}
		s.status.SessionID = prepared.threadID
		s.mu.Unlock()

		if oldThreadID == "" || oldThreadID == prepared.threadID {
			return nil
		}
		if oldSourceID == "" {
			oldSourceID = "local"
		}
		var notifications []appserver.SequencedNotification
		if oldRef == newRef {
			notifications = []appserver.SequencedNotification{s.appNotifier.Record(newRef, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
				ThreadID: prepared.threadID,
				Ref:      newRef,
			})}
		} else {
			notifications = []appserver.SequencedNotification{s.appNotifier.Record(oldRef, appwire.NotifyThreadClosed, appwire.ThreadClosedParams{
				ThreadID: oldThreadID,
				Ref:      oldRef,
				Reason:   "replaced",
			})}
		}
		sort.Strings(oldDescendantIDs)
		for _, threadID := range oldDescendantIDs {
			notifications = append(notifications, s.appNotifier.Record(threadID, appwire.NotifyThreadClosed, appwire.ThreadClosedParams{
				ThreadID: threadID,
				Ref:      appwire.Ref{SourceID: oldSourceID, ThreadID: threadID}.String(),
				Reason:   "replaced",
			}))
		}
		return notifications
	})
}

// SetAppIdentity installs an identity with no seeded history. Production serve
// prepares from the session's transcript instead; this is the shorthand for a
// thread that has none. It runs the same validation, so an identity
// PrepareAppIdentity would reject is not installed through this door either --
// it is rejected here and nothing changes.
func (s *Server) SetAppIdentity(sourceID, threadID string) {
	prepared, err := PrepareAppIdentity(sourceID, threadID, "")
	if err != nil {
		return
	}
	s.ReplaceAppIdentity(prepared, nil)
}

// SetDescendantTranscriptPathFunc installs the resolver RecordDescendantAppEvent
// consults on a descendant's first observation to seed its turn snapshot from
// persisted history (ledger #110/#111). fn may return "" for a thread with no
// backing transcript; nil disables seeding entirely (the historical behavior).
func (s *Server) SetDescendantTranscriptPathFunc(fn func(threadID string) string) {
	s.mu.Lock()
	s.appDescendantTranscriptPathFunc = fn
	s.mu.Unlock()
}

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
		s.ensureAppProjectorLocked(event.SessionID)
		projected := s.appProjector.Project(event)
		s.appActiveTurnID = s.appProjector.ActiveTurnID()
		threadID := s.appThreadID
		if threadID == "" {
			threadID = event.SessionID
		}
		if threadID == "" {
			threadID = s.status.SessionID
		}
		sourceID := s.appSourceID
		if sourceID == "" {
			sourceID = "local"
		}
		ref := s.appRef
		if ref == "" {
			ref = appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
		}
		start, _ := event.Data.(events.SessionStartData)
		pending := make([]pendingAppNotification, 0, len(projected))
		for _, item := range projected {
			switch params := item.Params.(type) {
			case appwire.ThreadStartedParams:
				startSeed := start.CurrentWork
				if !s.acceptTaskStartPublicationLocked(item.TaskStoreOwnerSessionID, item.TaskPublicationEpoch, item.TaskPublicationRevision) {
					startSeed = currentWorkSeedWithoutTasks(startSeed)
				}
				mergeStartCurrentWork(&params.Thread.Evener, s.appEnvelope.Tasks, s.appEnvelope.Goal, startSeed)
				s.appEnvelope.Tasks = cloneTaskAggregate(params.Thread.Evener.Tasks)
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
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: params, snapshot: s.appTurns})
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
					pending = append(pending, pendingAppNotification{threadID: target.threadID, ref: target.ref, method: item.Method, params: targetParams, snapshot: target.snapshot})
				}
			case appwire.GoalUpdatedParams:
				s.appEnvelope.Goal = goalPatch(params)
				s.appEnvelope.goalCarrierGeneration++
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: params, snapshot: s.appTurns})
			default:
				pending = append(pending, pendingAppNotification{threadID: threadID, ref: ref, method: item.Method, params: item.Params, snapshot: s.appTurns})
			}
		}
		s.mu.Unlock()

		committed := make([]appserver.SequencedNotification, 0, len(pending))
		rootNotificationTarget := s.appNotificationTarget(threadID)
		for _, item := range pending {
			params := s.stampFailureCountOnStatusChange(item.method, item.params)
			params = s.stampCapabilitiesOnStatusChange(item.method, params)
			params = s.stampFailureCountOnItemCompleted(item.method, params)
			params = stampAppNotificationTarget(params, item.threadID, item.ref)
			notificationTarget := item.threadID
			if item.threadID == threadID {
				notificationTarget = rootNotificationTarget
			}
			record := s.appNotifier.Record(notificationTarget, item.method, params)
			item.snapshot.Apply([]appserver.SequencedNotification{record})
			committed = append(committed, record)
		}
		return committed
	})
}

// RecordDescendantAppEvent projects an in-process descendant onto its root
// daemon's AppWire transport. ownerThreadID fences late events from a child of
// a replaced root identity: those events must not bleed into the new tree.
// This deliberately does not sample or stamp the root session envelope;
// descendant mutations remain owned by the agent tree, while this path supplies
// the independently addressed read/notification view.
func (s *Server) RecordDescendantAppEvent(ownerThreadID string, event events.SessionEvent) {
	threadID := strings.TrimSpace(event.SessionID)
	ownerThreadID = strings.TrimSpace(ownerThreadID)
	if threadID == "" || ownerThreadID == "" || threadID == ownerThreadID {
		return
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		if s.appThreadID != ownerThreadID {
			s.mu.Unlock()
			return nil
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
				turns:     &appTurnSnapshot{threadID: threadID},
				thread: appwire.Thread{
					ID:        threadID,
					SessionID: threadID,
					Source:    sourceID,
					Evener:    appwire.EvenerThread{Ref: ref, Kind: "subagent"},
				},
			}
			s.installCostLookup(projection.projector)
			// Seed from the descendant's own transcript BEFORE this first event is
			// applied, the same order PrepareAppIdentity uses for the ROOT thread:
			// a resumed descendant's persisted history must already be in the
			// snapshot when its first live event lands, or thread/read only ever
			// answers from the restore point forward (ledger #110/#111).
			if s.appDescendantTranscriptPathFunc != nil {
				if path := strings.TrimSpace(s.appDescendantTranscriptPathFunc(threadID)); path != "" {
					if turns, entries, err := appTurnsFromTranscriptFile(path); err == nil {
						projection.turns.Seed(turns)
						projection.projector.SeedPersistedTurns(entries)
					}
				}
			}
			s.appDescendants[threadID] = projection
		}
		projected := projection.projector.Project(event)
		projection.activeTurnID = projection.projector.ActiveTurnID()
		start, _ := event.Data.(events.SessionStartData)
		pending := make([]pendingAppNotification, 0, len(projected))
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
				projection.thread = params.Thread
				projection.thread.Evener.Kind = "subagent"
				projection.thread.Evener.Tasks = cloneTaskAggregate(params.Thread.Evener.Tasks)
				projection.thread.Evener.Goal = cloneGoalState(params.Thread.Evener.Goal)
				params.Thread = projection.thread
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params, snapshot: projection.turns})
			case appwire.ThreadStatusChangedParams:
				projection.thread.Status = params.Status
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params, snapshot: projection.turns})
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
					pending = append(pending, pendingAppNotification{threadID: target.threadID, ref: target.ref, method: item.Method, params: targetParams, snapshot: target.snapshot})
				}
			case appwire.GoalUpdatedParams:
				projection.thread.Evener.Goal = goalPatch(params)
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: params, snapshot: projection.turns})
			default:
				pending = append(pending, pendingAppNotification{threadID: threadID, method: item.Method, params: item.Params, snapshot: projection.turns})
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

		committed := make([]appserver.SequencedNotification, 0, len(pending))
		rootNotificationTarget := s.appNotificationTarget(ownerThreadID)
		for _, item := range pending {
			params := stampAppNotificationTarget(item.params, item.threadID, item.ref)
			notificationTarget := item.threadID
			if item.threadID == ownerThreadID {
				notificationTarget = rootNotificationTarget
			}
			record := s.appNotifier.Record(notificationTarget, item.method, params)
			item.snapshot.Apply([]appserver.SequencedNotification{record})
			committed = append(committed, record)
		}
		return committed
	})
}

type pendingAppNotification struct {
	threadID string
	ref      string
	method   string
	params   any
	snapshot *appTurnSnapshot
}

type taskCarrierTarget struct {
	threadID string
	ref      string
	snapshot *appTurnSnapshot
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
		return cloneTaskAggregate(s.appEnvelope.Tasks)
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
			return cloneTaskAggregate(tasks)
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
		targets = append(targets, taskCarrierTarget{threadID: rootID, ref: appwire.Ref{SourceID: sourceID, ThreadID: rootID}.String(), snapshot: s.appTurns})
	}
	descendantIDs := make([]string, 0, len(s.appDescendants))
	for id, projection := range s.appDescendants {
		if id == sourceThreadID || (ownerSessionID != "" && projection.taskStoreOwnerSessionID() == ownerSessionID) {
			descendantIDs = append(descendantIDs, id)
		}
	}
	sort.Strings(descendantIDs)
	for _, id := range descendantIDs {
		projection := s.appDescendants[id]
		targets = append(targets, taskCarrierTarget{threadID: id, ref: appwire.Ref{SourceID: sourceID, ThreadID: id}.String(), snapshot: projection.turns})
	}
	return targets
}

func mergeStartCurrentWork(target *appwire.EvenerThread, cachedTasks *appwire.TaskAggregate, cachedGoal *appwire.GoalState, seed *events.CurrentWorkSeedData) {
	if seed == nil {
		target.Tasks = cloneTaskAggregate(cachedTasks)
		target.Goal = cloneGoalState(cachedGoal)
		return
	}
	if seed.Tasks == nil {
		target.Tasks = cloneTaskAggregate(cachedTasks)
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
	return cloneTaskAggregate(&appwire.TaskAggregate{Total: params.Total, Done: params.Done, Current: params.Current})
}

func goalPatch(params appwire.GoalUpdatedParams) *appwire.GoalState {
	return cloneGoalState(params.Goal)
}

func cloneTaskAggregate(value *appwire.TaskAggregate) *appwire.TaskAggregate {
	if value == nil {
		return nil
	}
	clone := *value
	if value.Current != nil {
		current := *value.Current
		clone.Current = &current
	}
	return &clone
}

func cloneGoalState(value *appwire.GoalState) *appwire.GoalState {
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

// stampAppNotificationTarget re-addresses params to the fanout target the
// server is committing them under. The projector already stamps its own view
// of threadId/ref; the server overwrites both with the authoritative target
// (ref remapping, descendant fanout). Params are either structs implementing
// appwire.NotificationTargeted or map[string]any carrying threadId/ref keys;
// anything else passes through untouched.
func stampAppNotificationTarget(params any, threadID, ref string) any {
	switch p := params.(type) {
	case appwire.NotificationTargeted:
		return p.WithNotificationTarget(threadID, ref)
	case map[string]any:
		// Copy rather than mutate: the caller's map must stay untouched so a
		// future multi-target fanout of one params value cannot make every
		// notification share the last target stamped.
		out := make(map[string]any, len(p))
		maps.Copy(out, p)
		out["threadId"] = threadID
		out["ref"] = ref
		return out
	default:
		return params
	}
}

// stampFailureCountOnStatusChange rides the session's running failure count
// along on every thread/status/changed (kata 12rq).
//
// The count is otherwise snapshot-only, refreshed by thread/read — so a client
// that attached while the session was clean would keep showing nothing however
// many failures followed, which is exactly the watcher the figure exists for.
// A status transition is a turn boundary, the only moment the count can have
// moved, so this refreshes it precisely when there is something to say and
// never polls.
//
// It happens HERE, at the server's single notification egress, rather than in
// the projector: the projector maps events to notifications and holds no
// session handle, while the pull callback is the server's.
//
// An unmeasured count is left off entirely. Absence on a notification means
// "no update" — a zero here would let a client that never measured anything
// start claiming a clean run.
func (s *Server) stampFailureCountOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	count, measured := s.envelopeFailedToolCalls()
	if !measured {
		return params
	}
	status.FailedToolCalls = &count
	return status
}

// stampCapabilitiesOnStatusChange rides the action set that goes with the
// announced status along on every thread/status/changed (kata 06t8).
//
// The set is otherwise snapshot-only, refreshed by thread/read — and Send,
// Steer and Queue are all defined by whether a turn is in flight
// (appCapabilities). So a client that hydrated while the session was idle, or
// while it was a cold exited session the hub answered from the past index,
// holds steer=false/queue=false for the whole turn its own send starts: the
// composer knows the turn is live (it has the status change and turn/started)
// and still renders no Steer, no Stop and a disabled Send until a reload.
// Every capability change is a status transition, so this refreshes them
// exactly when they can have moved and never polls — the same shape
// stampFailureCountOnStatusChange already uses for the failure count.
//
// It happens HERE, at the server's single notification egress, rather than in
// the projector, for the same reason that one does: the projector maps events
// to notifications and holds no session handle.
//
// The set is computed FROM THE ANNOUNCED STATUS, not from the server's ambient
// processing flag. The two are written by different goroutines — the projector
// emits this notification from the event bridge while SetProcessing is the
// session loop's — so reading the ambient flag here would be a race that
// silently publishes the old status's capabilities. Deriving them from the
// status in hand makes the frame self-consistent by construction: a client
// applying both fields of one notification can never hold a status and a
// capability set that disagree.
func (s *Server) stampCapabilitiesOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	// A daemon announcing its own close is describing a thread it is about to
	// stop running, and what that thread can still be asked to do is the HUB's
	// to say: it answers an exited session's read from the past index and
	// resumes it on the next send, advertising Send there
	// (cmd/evener-hub/app_threadread.go's pastThreadCapabilities). This daemon's
	// own all-false set would take the follow-up composer away from a session
	// the hub would happily wake — the same wedge in the other direction. So
	// the close frame leaves the field empty and the hub fills it in with its
	// own answer as it relays the frame (app_relay.go's
	// stampClosedThreadCapabilities): a client is never left reading a set the
	// departing daemon cut for a turn that is over (kata pk2d).
	if status.Status.Type == appwire.ThreadStatusClosed {
		return params
	}
	capabilities := s.appCapabilities(status.Status.Type, status.Status.Type == appwire.ThreadStatusActive)
	status.Capabilities = &capabilities
	return status
}

// envelopeFailedToolCalls reads the running failure count out of the
// materialized envelope. The bridge refreshes the failures facet BEFORE the
// commit that projects the event being stamped, so the figure a notification
// carries is at least as new as the event it rides on.
func (s *Server) envelopeFailedToolCalls() (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appEnvelope.FailedToolCalls == nil {
		return 0, false
	}
	return *s.appEnvelope.FailedToolCalls, true
}

// stampFailureCountOnItemCompleted rides the running failure count on an
// item/completed notification, but ONLY on the item whose completion just
// moved it (kata 895d) — every other item/completed passes through
// untouched.
//
// thread/status/changed already carries the count unconditionally, but only
// at a turn boundary; a live watcher on a long turn sees nothing move however
// many tool calls fail inside it, the same shape of harm the count exists to
// fix at session scale (kata 12rq). item/completed is the natural finer-grain
// carrier — a failure IS an item completing — but it fires once per tool
// call, so stamping it unconditionally would resend an unchanged figure on
// every success to change it on a few. Gating on "did the count move since
// the last stamp" adds the field only where it is news, which is the same
// rule the client's own render gate applies (StatusRow.tsx's FailureCount:
// absent or unchanged says nothing).
//
// The params arrive as either a typed ItemLifecycleParams or a map[string]any,
// so this handles both. That is not future-proofing: this function was written
// when internal/appprojector built these params as a map, kcb5 then converted
// that producer to the catalog type, and the map-only type assertion here
// started failing - silently, returning the params unstamped, because a failed
// assertion is not an error. The count simply stopped moving mid-turn and only
// two tests noticed.
func (s *Server) stampFailureCountOnItemCompleted(method string, params any) any {
	if method != appwire.NotifyItemCompleted {
		return params
	}
	typed, isTyped := params.(appwire.ItemLifecycleParams)
	fields, isMap := params.(map[string]any)
	if !isTyped && !isMap {
		return params
	}
	count, measured := s.envelopeFailedToolCalls()
	if !measured {
		return params
	}
	s.mu.Lock()
	unchanged := s.appLastStampedFailedToolCalls != nil && *s.appLastStampedFailedToolCalls == count
	if !unchanged {
		stamped := count
		s.appLastStampedFailedToolCalls = &stamped
	}
	s.mu.Unlock()
	if unchanged {
		return params
	}
	if isTyped {
		stamped := count
		typed.FailedToolCalls = &stamped
		return typed
	}
	fields["failedToolCalls"] = count
	return fields
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

func (s *Server) registerAppWireHandlers() {
	router := s.appServer.Router()
	appserver.HandleTyped(router, appwire.MethodThreadList, s.handleAppThreadList)
	appserver.HandleTyped(router, appwire.MethodThreadRead, s.handleAppThreadRead)
	appserver.HandleTyped(router, appwire.MethodThreadUnsubscribe, s.handleAppThreadUnsubscribe)
	appserver.HandleTyped(router, appwire.MethodTurnStart, s.handleAppTurnStart)
	appserver.HandleTyped(router, appwire.MethodTurnSteer, s.handleAppTurnSteer)
	appserver.HandleTyped(router, appwire.MethodEvenerSandboxEscalationResolve, s.handleAppSandboxEscalationResolve)
	appserver.HandleTyped(router, appwire.MethodTurnInterrupt, s.handleAppTurnInterrupt)
	appserver.HandleTyped(router, appwire.MethodTurnQueue, s.handleAppTurnQueue)
	appserver.HandleTyped(router, appwire.MethodTurnDrainAsSteer, s.handleAppTurnDrainAsSteer)
	appserver.HandleTyped(router, appwire.MethodTurnPromoteQueuedAsSteer, s.handleAppTurnPromoteQueuedAsSteer)
	appserver.HandleTyped(router, appwire.MethodTurnCancelQueued, s.handleAppTurnCancelQueued)
	appserver.HandleTyped(router, appwire.MethodGoalSet, s.handleAppGoalSet)
	appserver.HandleTyped(router, appwire.MethodThreadCompactStart, s.handleAppThreadCompactStart)
	appserver.HandleTyped(router, appwire.MethodThreadShutdown, s.handleAppThreadShutdown)
	appserver.HandleTyped(router, appwire.MethodThreadClear, s.handleAppThreadClear)
	appserver.HandleTyped(router, appwire.MethodThreadModelSet, s.handleAppThreadModelSet)
	appserver.HandleTyped(router, appwire.MethodThreadVisionModelSet, s.handleAppThreadVisionModelSet)
	appserver.HandleTyped(router, appwire.MethodEvenerThreadNameSet, s.handleAppThreadNameSet)
	appserver.HandleTyped(router, appwire.MethodThreadReasoningEffortSet, s.handleAppThreadReasoningEffortSet)
	appserver.HandleTyped(router, appwire.MethodEvenerTasksList, s.handleAppTasksList)
	appserver.HandleTyped(router, appwire.MethodEvenerJobsList, s.handleAppJobsList)
	appserver.HandleTyped(router, appwire.MethodEvenerJobsOutput, s.handleAppJobsOutput)
	appserver.HandleTyped(router, appwire.MethodModelList, s.handleAppModelList)
	appserver.HandleTyped(router, appwire.MethodThreadTurnsList, s.handleAppThreadTurnsList)
}

func (s *Server) handleAppThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.mu.RLock()
	data := []appwire.Thread{s.appThreadLocked()}
	ids := make([]string, 0, len(s.appDescendants))
	for id := range s.appDescendants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		projection := s.appDescendants[id]
		thread := projection.thread
		thread.Evener.ActiveTurnID = projection.activeTurnID
		data = append(data, thread)
	}
	s.mu.RUnlock()
	return appwire.ThreadListResponse{Data: data}, nil
}

func (s *Server) handleAppThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if !params.Subscribe {
		return s.appThreadReadSnapshot(params), nil
	}
	threadID := s.appThreadIDForRead(params)
	if threadID == "" {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread is unavailable")
	}
	var response appwire.ThreadReadResponse
	captured := appserver.CaptureSubscription(
		ctx,
		params.ReplaceSubscription,
		func() string { return s.appNotificationTarget(threadID) },
		s.appNotifier.CurrentSequence,
		func() bool {
			response = s.appThreadReadSnapshot(params)
			return true
		},
	)
	if !captured {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
	}
	return response, nil
}

// appThreadReadSnapshot answers entirely from memory. It runs under the
// subscription cut, so it must not open a file: transcript persistence can lead
// live event delivery, and a read that parsed the file there would return an
// output whose matching notification is still on the other side of the cut --
// the same answer twice, under two identities.
//
// It stays under the cut for a second reason, which is why sampling the session
// envelope before CaptureSubscription would be wrong however tempting: queue,
// status, active turn, escalations and the failure count are each announced by
// a notification as well as carried here. A session writes its state before
// emitting the event that announces it, so a sample taken inside the gate can
// only lead its notifications. A sample taken outside can lag one -- an event
// emitted after the sample and committed before the cut is discarded on release
// as already-reflected, and the state it announced then never reaches the
// client at all.
//
// Anything called from here must therefore be cheap AND must not block on a
// lock another component holds across disk I/O. That is now structural rather
// than a rule to remember: the envelope is materialized (server/thread_envelope.go),
// this is a struct copy, and the Server holds no callback that could reach a
// session from a read path.
func (s *Server) appThreadReadSnapshot(params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	threadID := s.appThreadIDForRead(params)
	thread, ok := s.appThreadForID(threadID)
	if !ok {
		return appwire.ThreadReadResponse{}
	}
	var olderCursor string
	if params.IncludeTurns {
		thread.Turns, olderCursor = s.appLatestTurns(thread.ID, params.TurnLimit)
	}
	return appwire.ThreadReadResponse{Thread: thread, OlderCursor: olderCursor}
}

// handleAppThreadUnsubscribe drops the calling connection's subscription to
// this daemon's thread. It resolves the thread identity exactly as
// handleAppThreadRead does, so an unsubscribe names the same registry key the
// subscribe created — including through a replace/clear identity swap, where
// appNotificationTarget's stable ref is the key subscribers were registered
// under. A ref that no longer resolves (an identity swap moved the stable ref
// on, an old pre-swap ref) still tries the raw identities the client named:
// the registry keys a subscribe could have used are exactly the stable-ref
// form and the bare thread ID, and Unsubscribe is idempotent, so trying both
// costs nothing and leaks nothing.
func (s *Server) handleAppThreadUnsubscribe(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
	threadID := s.appThreadIDForRead(appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
	if threadID == "" {
		// Teardown finding nothing is a success; still try the raw identities
		// so a pre-swap key does not linger until connection close.
		rawRef := strings.TrimSpace(params.Ref)
		if rawRef != "" {
			appserver.Unsubscribe(ctx, rawRef)
		}
		if bare := strings.TrimSpace(params.ThreadID); bare != "" {
			appserver.Unsubscribe(ctx, s.appNotificationTarget(bare))
		}
		return appwire.EmptyResponse{}, nil
	}
	appserver.Unsubscribe(ctx, s.appNotificationTarget(threadID))
	return appwire.EmptyResponse{}, nil
}

func (s *Server) appThreadIDForRead(params appwire.ThreadReadParams) string {
	threadID := strings.TrimSpace(params.ThreadID)
	rawRef := strings.TrimSpace(params.Ref)
	if threadID == "" && rawRef != "" {
		// A Ref was supplied but is either unparseable or names a source this
		// daemon does not serve. Either way it is a caller error, not "no ref
		// given" -- falling through to appProjectionThreadID here would answer
		// with the ROOT thread's content for a request that named something
		// else entirely (ledger #110/#111).
		ref, err := appwire.ParseRef(rawRef)
		if err != nil || ref.SourceID != "local" {
			return ""
		}
		s.mu.RLock()
		stableRef := s.appRef
		currentThreadID := s.appThreadID
		s.mu.RUnlock()
		if rawRef == stableRef {
			threadID = currentThreadID
		} else {
			threadID = ref.ThreadID
		}
	}
	if threadID == "" {
		threadID = s.appProjectionThreadID()
	}
	if _, ok := s.appThreadForID(threadID); !ok {
		return ""
	}
	return threadID
}

func (s *Server) appThreadForID(threadID string) (appwire.Thread, bool) {
	if threadID == s.appProjectionThreadID() {
		return s.appThread(), true
	}
	s.mu.RLock()
	projection := s.appDescendants[threadID]
	if projection == nil {
		s.mu.RUnlock()
		return appwire.Thread{}, false
	}
	thread := projection.thread
	thread.Evener.ActiveTurnID = projection.activeTurnID
	s.mu.RUnlock()
	return thread, true
}

func (s *Server) appProjectionThreadID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appThreadID != "" {
		return s.appThreadID
	}
	return s.status.SessionID
}

// appAllTurns returns a defensive copy of the whole installed snapshot for
// threadID, oldest-first.
func (s *Server) appAllTurns(threadID string) []appwire.Turn {
	snapshot := s.appTurnSnapshotForID(threadID)
	if snapshot == nil {
		return nil
	}
	return snapshot.Snapshot()
}

// appTurnSnapshotForID selects the installed snapshot shared by full reads,
// latest windows, and older pages. Callers use the snapshot's locking accessors
// so the three views cannot disagree with each other or with subscriber state.
func (s *Server) appTurnSnapshotForID(threadID string) *appTurnSnapshot {
	s.mu.RLock()
	snapshot := s.appTurns
	installed := s.appThreadID == threadID && snapshot != nil && snapshot.threadID == threadID
	if !installed {
		if projection := s.appDescendants[threadID]; projection != nil {
			snapshot = projection.turns
			installed = snapshot != nil && snapshot.threadID == threadID
		}
	}
	s.mu.RUnlock()
	if !installed {
		return nil
	}
	return snapshot
}

func transcriptHeader(path string, maxLineBytes int) transcript.Header {
	file, err := os.Open(path)
	if err != nil {
		return transcript.Header{}
	}
	defer file.Close() //nolint:errcheck // read-only file; close error is not actionable
	return transcriptHeaderFromReader(file, maxLineBytes)
}

func transcriptHeaderFromReader(source io.Reader, maxLineBytes int) transcript.Header {
	reader := bufio.NewReaderSize(source, transcriptHeaderReadBufferBytes)
	for {
		lineBytes, complete, _, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil || !complete {
			return transcript.Header{}
		}
		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			continue
		}
		header, err := transcript.DecodeHeader([]byte(line))
		if err != nil {
			return transcript.Header{}
		}
		return header
	}
}

// appLatestTurns windows the newest limit turns out of the installed snapshot
// and returns the cursor for the page before them. A limit of zero or less
// returns the whole thread with no cursor.
func (s *Server) appLatestTurns(threadID string, limit int) ([]appwire.Turn, string) {
	snapshot := s.appTurnSnapshotForID(threadID)
	if snapshot == nil {
		return nil, ""
	}
	return snapshot.Latest(limit)
}

// appPageTurns pages backward through the installed snapshot from cursor.
func (s *Server) appPageTurns(threadID, cursor string, limit int) appwire.ThreadTurnsListResponse {
	snapshot := s.appTurnSnapshotForID(threadID)
	if snapshot == nil {
		return appwire.ThreadTurnsListResponse{}
	}
	return snapshot.Page(cursor, limit)
}

// handleAppThreadTurnsList pages turns backward (older) for lazy transcript
// loading. The web client seeds the latest window via thread/read(TurnLimit)
// and walks back with this as the user scrolls up.
func (s *Server) handleAppThreadTurnsList(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	threadID := s.appThreadIDForRead(appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
	if threadID == "" {
		return appwire.ThreadTurnsListResponse{}, appwire.SessionUnavailable("thread is unavailable")
	}
	return s.appPageTurns(threadID, params.Cursor, params.Limit), nil
}

func (s *Server) handleAppTurnStart(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, params.ThreadID, params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	defer unlock()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if !input.HasContent() {
		return appwire.TurnStartResponse{}, appwire.InvalidParams("input is required")
	}
	params.Input = input.Items
	s.mu.RLock()
	fn := s.retrySafeTurns.Start
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnStartResponse{}, appwire.Unavailable("turn start not available")
	}
	response, err := fn(params)
	// turn/start accepts a durable mutation intent and emits NOTHING: unlike
	// steer/queue/drain/promote/cancel, it does not reflect the durable queue, so
	// no QUEUE_CHANGED announces the pending execution it just recorded. The
	// serve loop's later claim does emit one, but a client reading between accept
	// and claim would not see its own in-flight mutation -- which is the single
	// thing the retry-safe projection exists to show a reconnecting client.
	//
	// Every mutation handler refreshes, not just this one. They all commit to the
	// same store, and a uniform refresh means nobody has to re-derive which of
	// the seven happens to emit. The sample is cheap: the store publishes its
	// committed generation under a narrow mutex it never holds across a write.
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, params.ThreadID, params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	defer unlock()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	if !input.HasContent() {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams("input is required")
	}
	params.Input = input.Items
	s.mu.RLock()
	fn := s.retrySafeTurns.Steer
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnSteerResponse{}, appwire.Unavailable("steer not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppSandboxEscalationResolve(_ context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.EmptyResponse{}, err
	}
	escalationID := strings.TrimSpace(params.EscalationID)
	if escalationID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("escalationId is required")
	}
	s.mu.RLock()
	fn := s.sandboxEscalationResolveFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("sandbox escalation resolve not available")
	}
	if err := fn(escalationID, params.Approve); err != nil {
		// The escalation is unknown or already resolved (a stale card, a double
		// click, or a race with turn-interrupt/close). Surface it as a conflict so
		// the client can drop the card rather than retry.
		return appwire.EmptyResponse{}, appwire.Conflict(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppTurnInterrupt(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, params.ThreadID, params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	defer unlock()
	s.mu.RLock()
	fn := s.retrySafeTurns.Interrupt
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnInterruptResponse{}, appwire.Unavailable("interrupt not available")
	}
	response, err := fn(ctx, params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, "", params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	defer unlock()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
	}
	if !input.HasContent() {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams("input required")
	}
	params.Input = input.Items
	s.mu.RLock()
	fn := s.retrySafeTurns.Queue
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnQueueResponse{}, appwire.Unavailable("queue not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnDrainAsSteer(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, "", params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	defer unlock()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Input = input.Items
	s.mu.RLock()
	fn := s.retrySafeTurns.Drain
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("drain-as-steer not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppTurnPromoteQueuedAsSteer validates static request shape and leaves
// active-turn and queue compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnPromoteQueuedAsSteer(_ context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, "", params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	defer unlock()
	if params.Index < 0 {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(params.ExpectedEntryID) == "" {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedEntryId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Promote
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Unavailable("promote-queued-as-steer not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppTurnCancelQueued validates static request shape and leaves queue
// compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnCancelQueued(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	unlock, err := s.lockRetrySafeMutation(params.Ref, "", params.ExpectedInstanceID, params.ClientMutationID)
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	defer unlock()
	if params.Index < 0 {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(params.ExpectedEntryID) == "" {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("expectedEntryId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Cancel
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnCancelQueuedResponse{}, appwire.Unavailable("cancel-queued not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppGoalSet handles goal/set. An empty objective clears the goal; both
// set and clear route through the single goalFunc callback (the callback maps an
// empty objective to ClearGoal). Started reports whether the goal loop began
// immediately (idle session) versus after the current turn (a turn is running,
// whose gate is the backstop).
func (s *Server) handleAppGoalSet(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.GoalSetResponse{}, err
	}
	s.mu.RLock()
	fn := s.goalFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.GoalSetResponse{}, appwire.Unavailable("goal not available")
	}
	started, err := fn(params.Objective)
	if err != nil {
		return appwire.GoalSetResponse{}, err
	}
	// The callback emits EventGoalUpdated after its successful store mutation.
	// That typed carrier is committed into the cached snapshot with its
	// notification; pulling here would race it with a second, stale authority.
	return appwire.GoalSetResponse{Started: started}, nil
}

func (s *Server) handleAppThreadCompactStart(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.compactFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("compact not available")
	}
	return appwire.EmptyResponse{}, fn(ctx)
}

func (s *Server) handleAppThreadShutdown(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.shutdownFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("shutdown not available")
	}
	go fn()
	return appwire.EmptyResponse{}, nil
}

// handleAppThreadClear owns the retry-safe identity transition for clear. The
// callback is deliberately outside s.mu: it provisions and closes sessions and
// may run hooks, while appMutationGate prevents another clear or retry-safe
// turn mutation from competing with it.
func (s *Server) handleAppThreadClear(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	params.Ref = strings.TrimSpace(params.Ref)
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	if params.Ref == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("ref is required")
	}
	if params.ClientMutationID == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	if params.ExpectedInstanceID == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("expectedInstanceId is required")
	}
	parsedRef, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams(err.Error())
	}
	if parsedRef.SourceID != "local" {
		return appwire.ThreadClearResponse{}, appwire.SessionUnavailable("thread is unavailable")
	}
	requestHash, err := threadClearRequestHash(params)
	if err != nil {
		return appwire.ThreadClearResponse{}, appwire.InternalError(err.Error())
	}
	if !s.appMutationGate.TryLock() {
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "a retry-safe mutation is already in progress")
	}
	defer s.appMutationGate.Unlock()

	s.mu.Lock()
	if s.clearJournalErr != nil {
		err := appwire.MutationUnknown(params.ClientMutationID, "thread clear journal is unavailable")
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, err
	}
	if s.appRef != "" && params.Ref != s.appRef {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread identity is stale")
	}
	if record, ok := s.clearRecords[params.ClientMutationID]; ok {
		if record.RequestHash != requestHash || record.Ref != params.Ref || record.ExpectedInstanceID != params.ExpectedInstanceID {
			s.mu.Unlock()
			return appwire.ThreadClearResponse{}, appwire.InvalidRequest("client mutation ID reused with a different clear request")
		}
		if record.State == threadClearApplied {
			if record.Response == nil {
				s.mu.Unlock()
				return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt is incomplete")
			}
			response := *record.Response
			response.Receipt.Disposition = appwire.MutationDispositionReplayed
			s.mu.Unlock()
			return response, nil
		}
		// A reserved record with a different current instance means the process
		// reached the replacement before it crashed or lost its response. The
		// current stable ref is authoritative, so persist the reconstructed
		// receipt and replay it instead of running a second replacement.
		currentRef := s.appRef
		currentInstanceID := s.appThreadID
		if currentRef == params.Ref && currentInstanceID != params.ExpectedInstanceID {
			s.mu.Unlock()
			response := s.threadClearResponse(params.ClientMutationID, appwire.MutationDispositionApplied)
			s.mu.Lock()
			currentRecord, stillReserved := s.clearRecords[params.ClientMutationID]
			if !stillReserved || currentRecord.State != threadClearReserved || s.appRef != params.Ref || s.appThreadID == params.ExpectedInstanceID {
				s.mu.Unlock()
				return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread clear state changed while recovering")
			}
			record = currentRecord
			record.State = threadClearApplied
			record.Response = &response
			s.clearRecords[params.ClientMutationID] = record
			if err := persistThreadClearJournal(s.clearJournalPath, s.clearRecords); err != nil {
				s.mu.Unlock()
				return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt could not be persisted")
			}
			response.Receipt.Disposition = appwire.MutationDispositionReplayed
			s.mu.Unlock()
			return response, nil
		}
	}

	if params.ExpectedInstanceID != s.appThreadID {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread instance is stale")
	}
	if reason := s.clearBlockedReasonLocked(); reason != "" {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, reason)
	}
	fn := s.clearFunc
	if fn == nil {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.Unavailable("thread/clear is unavailable")
	}
	record := threadClearRecord{
		ClientMutationID:   params.ClientMutationID,
		RequestHash:        requestHash,
		Ref:                params.Ref,
		ExpectedInstanceID: params.ExpectedInstanceID,
		State:              threadClearReserved,
	}
	// A new reservation supersedes every older record for the same stable ref:
	// the instance those records expected (or installed) is being replaced, so
	// none of them can be a live client's retry anymore. This bounds the
	// journal at one record per ref instead of one per clear, forever. The two
	// snapshots make each rollback below a wholesale restore; only this
	// handler mutates clearRecords, and appMutationGate serializes it.
	beforeReservation := maps.Clone(s.clearRecords)
	for id, existing := range s.clearRecords {
		if existing.Ref == params.Ref {
			delete(s.clearRecords, id)
		}
	}
	s.clearRecords[params.ClientMutationID] = record
	if err := persistThreadClearJournal(s.clearJournalPath, s.clearRecords); err != nil {
		s.clearRecords = beforeReservation
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear reservation could not be persisted")
	}
	reserved := maps.Clone(s.clearRecords)
	s.mu.Unlock()

	if err := fn(ctx, params); err != nil {
		s.mu.Lock()
		// The failed clear installed nothing, so the records its reservation
		// superseded describe the still-current instance: restoring the
		// pre-reservation journal keeps an older receipt's replay alive after
		// a failed newer clear.
		s.clearRecords = beforeReservation
		persistErr := persistThreadClearJournal(s.clearJournalPath, s.clearRecords)
		if persistErr != nil {
			// Roll memory back to the journal the reservation persisted. Disk
			// may already hold the rollback if only the post-rename directory
			// sync failed, but either state is safe: every persist rewrites
			// the whole file from memory, so the next successful one resyncs
			// disk, and a retry that re-invokes the callback re-runs work that
			// failed without installing anything.
			s.clearRecords = reserved
		}
		s.mu.Unlock()
		if persistErr != nil {
			return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear outcome could not be persisted")
		}
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, err.Error())
	}

	response := s.threadClearResponse(params.ClientMutationID, appwire.MutationDispositionApplied)
	s.mu.Lock()
	record = s.clearRecords[params.ClientMutationID]
	record.State = threadClearApplied
	record.Response = &response
	s.clearRecords[params.ClientMutationID] = record
	if err := persistThreadClearJournal(s.clearJournalPath, s.clearRecords); err != nil {
		// Keep the reserved record and the new identity. A retry will take the
		// crash-recovery branch above and make the receipt durable before it
		// reports success.
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt could not be persisted")
	}
	s.mu.Unlock()
	return response, nil
}

func (s *Server) clearBlockedReasonLocked() string {
	if s.processing || strings.TrimSpace(s.appReservedTurnID) != "" || strings.TrimSpace(s.appActiveTurnID) != "" {
		return "thread has unresolved turn work"
	}
	if s.appEnvelope.Queue.Depth > 0 || len(s.appEnvelope.PendingMutations) > 0 {
		return "thread has unresolved queued work"
	}
	if s.appEnvelope.AskPending || len(s.appEnvelope.PendingEscalations) > 0 {
		return "thread has unresolved approval work"
	}
	return ""
}

func (s *Server) threadClearResponse(clientMutationID string, disposition appwire.MutationDisposition) appwire.ThreadClearResponse {
	thread := s.appThread()
	return appwire.ThreadClearResponse{
		Thread: thread,
		Ref:    thread.Evener.Ref,
		Receipt: appwire.MutationReceipt{
			ClientMutationID: clientMutationID,
			Disposition:      disposition,
			ThreadID:         thread.ID,
			InstanceID:       thread.Evener.InstanceID,
			ProjectionState:  appwire.MutationProjectionReflected,
		},
	}
}

func (s *Server) handleAppThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	model := strings.TrimSpace(params.Model)
	if model == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("model is required")
	}
	if provider := strings.TrimSpace(params.ModelProvider); provider != "" {
		model = provider + "/" + model
	}
	s.mu.RLock()
	processing := s.processing
	reservedTurnID := strings.TrimSpace(s.appReservedTurnID)
	fn := s.modelFunc
	s.mu.RUnlock()
	if processing || reservedTurnID != "" {
		msg := "session is processing"
		if reservedTurnID != "" {
			msg = "turn " + reservedTurnID + " is active"
		}
		return appwire.EmptyResponse{}, appwire.Conflict(msg)
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("model change not available")
	}
	if err := fn(model); err != nil {
		// Every SetModel failure is mapped to InvalidParams deliberately: the
		// hook's error modes today are all validation-shaped (unknown model,
		// not a member of the instance's catalog, unresolvable ref). Revisit
		// this blanket mapping if SetModel grows I/O failure modes (e.g. a live
		// ListModels fetch failing), which would deserve a distinct code.
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadVisionModelSet(_ context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	processing := s.processing
	reservedTurnID := strings.TrimSpace(s.appReservedTurnID)
	fn := s.visionModelFunc
	s.mu.RUnlock()
	if processing || reservedTurnID != "" {
		msg := "session is processing"
		if reservedTurnID != "" {
			msg = "turn " + reservedTurnID + " is active"
		}
		return appwire.EmptyResponse{}, appwire.Conflict(msg)
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("vision model change not available")
	}
	// "" and "off" are legitimate setting values (session-model and disabled),
	// so unlike model/set there is no empty-value rejection here; ref shape is
	// the session's job to validate (Session.SetVisionModel).
	if err := fn(params.VisionModel); err != nil {
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadNameSet(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("name is required")
	}
	s.mu.RLock()
	fn := s.nameFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("rename not available")
	}
	fn(name)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadReasoningEffortSet(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.reasoningEffortFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("reasoning effort change not available")
	}
	// Normalize disable-aliases to "none" and reject unknown vocabulary, so a
	// typo or direct API call can't persist a provider-rejected effort that
	// breaks later requests.
	effort := llm.NormalizeReasoningEffort(params.ReasoningEffort)
	if err := llm.ValidateReasoningEffort(effort); err != nil {
		return appwire.EmptyResponse{}, appwire.InvalidParams("invalid reasoning effort: " + params.ReasoningEffort)
	}
	fn(effort)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) requireRootMutationTarget(rawRef, threadID string) error {
	target := strings.TrimSpace(threadID)
	if strings.TrimSpace(rawRef) != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err != nil {
			return appwire.InvalidParams(err.Error())
		}
		if ref.SourceID != "local" {
			return appwire.SessionUnavailable("thread is unavailable")
		}
		s.mu.RLock()
		root := s.appThreadID
		stableRef := s.appRef
		s.mu.RUnlock()
		if stableRef != "" && strings.TrimSpace(rawRef) == stableRef {
			target = root
		} else {
			target = ref.ThreadID
		}
	}
	root := s.appProjectionThreadID()
	if target != "" && target != root {
		return appwire.SessionUnavailable("thread is not served by this daemon: " + target)
	}
	return nil
}

// requireRootMutationIdentity checks both halves of a retry-safe mutation's
// target. The ref identifies the stable workspace; ExpectedInstanceID fences
// the live session generation behind it. A clear intentionally keeps the ref
// while replacing that instance, so accepting only the ref would let a delayed
// old-generation outbox entry run against the replacement.
func (s *Server) requireRootMutationIdentity(rawRef, threadID, expectedInstanceID, clientMutationID string) error {
	if clientMutationID == "" {
		return appwire.InvalidParams("clientMutationId is required")
	}
	if expectedInstanceID == "" {
		return appwire.InvalidParams("expectedInstanceId is required")
	}
	if err := s.requireRootMutationTarget(rawRef, threadID); err != nil {
		return err
	}
	s.mu.RLock()
	currentInstanceID := s.appThreadID
	s.mu.RUnlock()
	if expectedInstanceID != currentInstanceID {
		return appwire.MutationNotAccepted(clientMutationID, "thread instance is stale")
	}
	return nil
}

// lockRetrySafeMutation admits one retry-safe turn callback only while no
// thread/clear owns the identity gate. It validates before and after taking
// the read lock so a clear that wins the race cannot leave an old-generation
// callback admitted behind its replacement.
func (s *Server) lockRetrySafeMutation(rawRef, threadID, expectedInstanceID, clientMutationID string) (func(), error) {
	if err := s.requireRootMutationIdentity(rawRef, threadID, expectedInstanceID, clientMutationID); err != nil {
		return nil, err
	}
	if !s.appMutationGate.TryRLock() {
		return nil, appwire.MutationNotAccepted(clientMutationID, "thread clear is already in progress")
	}
	if err := s.requireRootMutationIdentity(rawRef, threadID, expectedInstanceID, clientMutationID); err != nil {
		s.appMutationGate.RUnlock()
		return nil, err
	}
	return s.appMutationGate.RUnlock, nil
}

func (s *Server) handleAppTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	s.mu.RLock()
	fn := s.tasksFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TaskListResponse{}, nil
	}
	return appwire.TaskListResponse{Data: fn()}, nil
}

func (s *Server) handleAppJobsList(_ context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	s.mu.RLock()
	fn := s.jobsFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsListResponse{}, nil
	}
	data, err := fn(params)
	if err != nil {
		return appwire.JobsListResponse{}, err
	}
	return appwire.JobsListResponse{Data: data}, nil
}

func (s *Server) handleAppJobsOutput(_ context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	s.mu.RLock()
	fn := s.jobOutputFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsOutputResponse{}, appwire.Unavailable("job output not available")
	}
	data, found, err := fn(params.JobID, params.BeforeBytes, params.MaxBytes)
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	if !found {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsOutputResponse{Data: data}, nil
}

func (s *Server) handleAppModelList(ctx context.Context, _ appwire.ModelListParams) (appwire.ModelListResponse, error) {
	s.mu.RLock()
	fn := s.listModelsFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{}}, nil
	}
	models, err := fn(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	if models == nil {
		models = []appwire.ModelDescriptor{}
	}
	return appwire.ModelListResponse{Data: models}, nil
}

// appThread assembles the thread snapshot. It is a struct copy plus identity:
// every live-session value comes from the materialized envelope, which the
// bridge maintains at the moments those values change.
//
// It must stay that way. This runs under the response cut, which holds
// projectionMu AND deliveryMu, so anything reached from here blocks every other
// connection's capture, every projection commit, and the whole event bridge for
// as long as it takes. It used to pull sixteen live session callbacks from
// here; four of them could block behind a transcript fsync, a synchronous
// jobs.jsonl read, or the session's own mutex.
func (s *Server) appThread() appwire.Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appThreadLocked()
}

func (s *Server) appThreadLocked() appwire.Thread {
	status := s.status
	sourceID := s.appSourceID
	threadID := s.appThreadID
	ref := s.appRef
	processing := s.processing
	turnReserved := strings.TrimSpace(s.appReservedTurnID) != ""
	envelope := s.appEnvelope
	activeTurnID := s.appActiveTurnID

	if sourceID == "" {
		sourceID = "local"
	}
	if threadID == "" {
		threadID = status.SessionID
	}
	if ref == "" {
		ref = appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	}
	pressure := envelope.ContextPressure
	metrics := envelope.ContextMetrics
	var diagnostics *appwire.EvenerDiagnostics
	if envelope.Detailed != nil {
		diagnostics = appDiagnosticsFromDetailedStatus(*envelope.Detailed)
	}
	queue := envelope.Queue
	pendingMutations := envelope.PendingMutations
	goalState := envelope.Goal
	taskAggregate := envelope.Tasks
	workMillis := envelope.WorkMillis
	usage := envelope.Usage
	activeTurnStartedAt := envelope.ActiveTurnStartedAt
	failedToolCalls := envelope.FailedToolCalls
	askPending := envelope.AskPending
	pendingEscalations := envelope.PendingEscalations
	reasoningEffort := envelope.ReasoningEffort
	reasoningEffortLevels := envelope.ReasoningEffortLevels
	supportsReasoning := envelope.SupportsReasoning
	visionModel := envelope.VisionModel
	threadName := envelope.Name
	threadPreview := envelope.Preview
	if threadPreview == "" {
		threadPreview = status.SessionID
	}
	return appwire.Thread{
		ID:            threadID,
		SessionID:     status.SessionID,
		Name:          threadName,
		Preview:       threadPreview,
		ModelProvider: status.Model,
		Status:        appwire.ThreadStatus{Type: appStatus(status.State, processing, turnReserved)},
		CWD:           status.WorkingDir,
		Path:          filepath.Base(status.WorkingDir),
		Source:        sourceID,
		Evener: appwire.EvenerThread{
			Ref:                   ref,
			InstanceID:            threadID,
			Profile:               status.Profile,
			TurnCount:             status.Turns,
			ActiveTurnID:          activeTurnID,
			ContextPressure:       pressure,
			ContextUsed:           metrics.Used,
			ContextWindow:         metrics.Window,
			ContextRemaining:      metrics.Remaining,
			Capabilities:          s.appCapabilitiesLocked(status.State, processing),
			Diagnostics:           diagnostics,
			Queue:                 queue,
			PendingMutations:      pendingMutations,
			Tasks:                 taskAggregate,
			Goal:                  goalState,
			Usage:                 usage,
			Cost:                  appwire.EstimateCost(s.costFor(status.Profile+"/"+status.Model), usage),
			WorkMillis:            workMillis,
			ActiveTurnStartedAt:   activeTurnStartedAt,
			FailedToolCalls:       failedToolCalls,
			AskPending:            askPending,
			PendingEscalations:    pendingEscalations,
			ReasoningEffort:       reasoningEffort,
			ReasoningEffortLevels: reasoningEffortLevels,
			SupportsReasoning:     supportsReasoning,
			VisionModel:           visionModel,
		},
	}
}

func appDiagnosticsFromDetailedStatus(ds DetailedStatus) *appwire.EvenerDiagnostics {
	out := &appwire.EvenerDiagnostics{}
	if ds.Plugins != nil {
		out.Plugins = make([]appwire.EvenerPluginInfo, 0, len(ds.Plugins))
	}
	for _, tool := range ds.Tools {
		out.Tools = append(out.Tools, appwire.EvenerToolInfo{Name: tool.Name, Source: tool.Source})
	}
	for _, srv := range ds.MCP {
		out.MCP = append(out.MCP, appwire.EvenerMCPServerInfo{Name: srv.Name, Tools: append([]string(nil), srv.Tools...), Status: srv.Status, Error: srv.Error})
	}
	for _, skill := range ds.Skills {
		out.Skills = append(out.Skills, appwire.EvenerSkillInfo{Name: skill.Name, Description: skill.Description})
	}
	for _, plugin := range ds.Plugins {
		out.Plugins = append(out.Plugins, appwire.EvenerPluginInfo{
			Name:       plugin.Name,
			Version:    plugin.Version,
			SkillCount: plugin.SkillCount,
			AgentCount: plugin.AgentCount,
			HookCount:  plugin.HookCount,
			MCPCount:   plugin.MCPCount,
		})
	}
	for _, he := range ds.HookEvents {
		if out.HookEvents == nil {
			out.HookEvents = make([]appwire.EvenerHookEventStatus, 0, len(ds.HookEvents))
		}
		out.HookEvents = append(out.HookEvents, appwire.EvenerHookEventStatus{
			Event:     he.Event,
			Count:     he.Count,
			Tier:      he.Tier,
			Supported: he.Supported,
		})
	}
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, appwire.EvenerJobInfo{
			JobID:            job.JobID,
			JobType:          job.JobType,
			Status:           job.Status,
			Reason:           job.Reason,
			ExhaustionBudget: job.ExhaustionBudget,
			ExhaustionLimit:  job.ExhaustionLimit,
			Resumable:        job.Resumable,
			ExitCode:         job.ExitCode,
			OutputBytes:      job.OutputBytes,
			TranscriptRef:    job.TranscriptRef,
			Command:          job.Command,
			Intent:           job.Intent,
			Task:             job.Task,
		})
	}
	for _, delegate := range ds.Delegates {
		out.Delegates = append(out.Delegates, appDelegateFromDetailedStatus(delegate))
	}
	if ds.TurnSlots != nil {
		out.TurnSlots = &appwire.EvenerTurnSlots{
			InUse: ds.TurnSlots.InUse, Cap: ds.TurnSlots.Cap, Jobs: ds.TurnSlots.Jobs, Drives: ds.TurnSlots.Drives,
		}
	}
	out.Agents = append(out.Agents, ds.Agents...)
	return out
}

func appDelegateFromDetailedStatus(delegate DelegateStatusInfo) appwire.EvenerDelegateInfo {
	out := appwire.EvenerDelegateInfo{
		DelegateID: delegate.DelegateID, OwnerSessionID: delegate.OwnerSessionID, RootSessionID: delegate.RootSessionID,
		ChildSessionID: delegate.ChildSessionID, TranscriptRef: delegate.TranscriptRef, ParentDelegateID: delegate.ParentDelegateID,
		Type: delegate.Type, Lifecycle: delegate.Lifecycle, Phase: delegate.Phase, Status: delegate.Status,
		Outcome: delegate.Outcome, Reason: delegate.Reason, Terminal: delegate.Terminal, Resumable: delegate.Resumable, NeedsAttention: delegate.NeedsAttention,
		NotResumableReason: delegate.NotResumableReason, ProjectionRevision: delegate.ProjectionRevision,
		Task: delegate.Task, Description: delegate.Description, AgentType: delegate.AgentType, RequestedModel: delegate.RequestedModel,
		ResolvedProfileID: delegate.ResolvedProfileID, ResolvedModel: delegate.ResolvedModel, Model: delegate.Model,
		ReasoningEffort: delegate.ReasoningEffort, OriginTurnID: delegate.OriginTurnID, OriginToolCallID: delegate.OriginToolCallID,
		OriginItemID: delegate.OriginItemID, RunStartedAt: delegate.RunStartedAt, RunEndedAt: delegate.RunEndedAt,
		LatestActivityAt: delegate.LatestActivityAt, RunningForMS: cloneServerInt64(delegate.RunningForMS),
		QuietForMS: cloneServerInt64(delegate.QuietForMS), DurationMS: cloneServerInt64(delegate.DurationMS),
		PacketKind: delegate.PacketKind, Message: append(json.RawMessage(nil), delegate.Message...),
		StructuredResult: append(json.RawMessage(nil), delegate.StructuredResult...), StructuredValid: cloneServerBool(delegate.StructuredValid),
		StructuredReason: delegate.StructuredReason, Warnings: append([]string(nil), delegate.Warnings...),
		Diagnostics: append([]string(nil), delegate.Diagnostics...), ExhaustionBudget: delegate.ExhaustionBudget,
		ExhaustionLimit: delegate.ExhaustionLimit, ExhaustionResumable: cloneServerBool(delegate.ExhaustionResumable),
		DelegationAllowance: delegate.DelegationAllowance, ParentWatchGranted: delegate.ParentWatchGranted,
	}
	if delegate.Usage != nil {
		usage := *delegate.Usage
		out.Usage = &usage
	}
	if delegate.Worktree != nil {
		worktree := *delegate.Worktree
		out.Worktree = &worktree
	}
	return out
}

func cloneServerBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneServerInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func (s *Server) appCapabilities(state string, processing bool) appwire.ThreadCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appCapabilitiesLocked(state, processing)
}

func (s *Server) appCapabilitiesLocked(state string, processing bool) appwire.ThreadCapabilities {
	// Status-dependent capabilities derive from the status this thread PUBLISHES,
	// not assembled again from the fields behind it. Clear additionally requires
	// its handler and state gate to report that the action is currently safe.
	// A client reading a status and the capabilities beside it is reading one
	// decision, so no window can open where the two describe different threads.
	status := appStatus(state, processing, strings.TrimSpace(s.appReservedTurnID) != "")
	active := status == appwire.ThreadStatusActive
	closed := status == appwire.ThreadStatusClosed
	steerAvailable := s.steerFunc != nil || s.steerWithImagesFunc != nil
	clearAvailable := s.clearFunc != nil && !active && !closed && s.clearBlockedReasonLocked() == ""
	return appwire.ThreadCapabilities{
		Send:  !active && !closed,
		Steer: steerAvailable && active && !closed,
		// Interrupt answers "is there work to stop", which is the same `active`
		// Steer is derived from -- deliberately NOT the ambient cancelFunc.
		//
		// That field is armed and cleared once per turn by the session loop, on
		// a different goroutine and a different clock from the reservation the
		// status reads, and cmd/evener/serve.go's drain path published processing
		// before arming it. Reading it here let this set say steer=true
		// interrupt=false: a turn is running and cannot be stopped. The set is
		// PUSHED on thread/status/changed and a client keeps it until the status
		// changes again, so a frame stamped inside that window takes Stop away
		// for the whole turn that follows (kata 5gdv).
		//
		// interruptWired keeps the honesty the cancelFunc read also carried: a
		// harness that never arms a cancel does not advertise Stop. It is
		// sticky where cancelFunc is per-turn, which is the whole difference.
		// Whether a cancel is armed at the instant the request arrives stays the
		// business of the typed interrupt handler. InterruptClientMutation has
		// its own
		// quiescence precondition (kata vewa).
		Interrupt:         s.interruptWired && active && !closed,
		Compact:           s.compactFunc != nil && !closed,
		Clear:             clearAvailable,
		ForkFromTurn:      false,
		Shutdown:          s.shutdownFunc != nil,
		ChangeModel:       s.modelFunc != nil && !closed,
		ChangeVisionModel: s.visionModelFunc != nil && !closed,
		Rename:            s.nameFunc != nil && !closed,
		// Queue mirrors Steer's "active turn" gate: only meaningful while
		// a turn is in flight or reserved by turn/start (kata 111a).
		Queue: s.queueFunc != nil && active && !closed,
		// Goal is available whenever the engine is wired and the session is
		// open. It is intentionally NOT gated on !active: a goal may be set
		// mid-turn (it arms for the next continuation), unlike Send.
		Goal: s.goalFunc != nil && !closed,
	}
}

func (s *Server) ensureAppProjectorLocked(threadID string) {
	if s.appProjector != nil {
		return
	}
	if threadID == "" {
		threadID = s.appThreadID
	}
	ref := s.appRef
	if ref == "" {
		ref = appwire.Ref{SourceID: s.appSourceID, ThreadID: threadID}.String()
	}
	s.appProjector = appprojector.NewAppEventProjector(threadID, ref)
	s.installCostLookup(s.appProjector)
}

// installCostLookup points a projector at this daemon's cost source, so the
// turns it stamps are priced from the live session's registry (spec §7.5).
// The closure reads the lookup at call time, so a projector installed before
// the daemon was wired still prices correctly once it is.
func (s *Server) installCostLookup(p *appprojector.AppEventProjector) {
	if p == nil {
		return
	}
	p.SetCostLookup(func(provider, model string) *registry.Cost {
		return s.costFor(provider + "/" + model)
	})
}

func (s *Server) reserveAppTurnIDForStart() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	processing := s.processing
	reserved := strings.TrimSpace(s.appReservedTurnID) != ""
	closed := appStatus(s.status.State, processing, reserved) == appwire.ThreadStatusClosed
	if closed {
		return "", appwire.Conflict("session is closed")
	}
	if processing || reserved {
		return "", appwire.Conflict("session is processing")
	}
	s.ensureAppProjectorLocked("")
	turnID := s.appProjector.ReserveTurnID()
	s.appActiveTurnID = turnID
	s.appReservedTurnID = turnID
	return turnID, nil
}

func (s *Server) releaseAppTurnID(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appProjector != nil {
		s.appProjector.ReleaseReservedTurnID(turnID)
		s.appActiveTurnID = s.appProjector.ActiveTurnID()
	}
	if s.appReservedTurnID == turnID {
		s.appReservedTurnID = ""
	}
}

// appStatus is the daemon's ONE answer to "what is this thread doing". The
// status the wire publishes and the capability set published beside it are both
// derived from it, so the two cannot disagree about whether a turn is running.
//
// They used to be separate expressions over overlapping state -- this one read
// `processing` and `state`, appCapabilities read `processing` and the turn
// reservation -- and each had a window the other did not see. The reachable
// half of that: a session state of "active" with the daemon's processing flag
// already cleared published status=active beside interrupt=false and
// steer=false, which is a busy composer with nothing on it (katas vewa, 5gdv,
// and 06t8 before them).
//
// turnReserved folds appCapabilities' third input in so the two expressions
// become one. It is NOT what opens the window this fix is about, and an earlier
// version of this comment said it was. Measured in the pre-turn window on a
// live stack: 12 of 12 samples reported state="active" processing=true
// turnReserved=false. The window comes from the daemon holding `processing`
// across the whole of an input -- pre-turn work included, where a slash
// command's inline shell span can run for seconds -- while the SESSION has not
// entered SessionProcessing yet and so reports itself settled.
//
// turnReserved is in fact unreachable in the shipped daemon today:
// reserveAppTurnIDForStart is the only writer of appReservedTurnID and has no
// non-test callers, because turn/start is wired to
// agent.AcceptClientMutationStart, which reserves nothing here. It is kept
// because this function must stay correct for the reservation if it is ever
// wired up, and dropping the input would let the two expressions diverge again
// -- but no reader should go looking for it in a live trace.
func appStatus(state string, processing, turnReserved bool) string {
	// Closed wins over everything. A thread whose session has gone is not
	// working, whatever flag was left set behind it -- and with a reservation
	// now able to report active, a lingering one must not mask a close.
	if strings.TrimSpace(state) == appwire.ThreadStatusClosed {
		return appwire.ThreadStatusClosed
	}
	if processing || turnReserved {
		return appwire.ThreadStatusActive
	}
	switch strings.TrimSpace(state) {
	case appwire.ThreadStatusIdle:
		return appwire.ThreadStatusIdle
	case appwire.ThreadStatusActive:
		return appwire.ThreadStatusActive
	case appwire.ThreadStatusAwaiting:
		return appwire.ThreadStatusAwaiting
	case appwire.ThreadStatusWarning:
		return appwire.ThreadStatusWarning
	case appwire.ThreadStatusSystemError:
		return appwire.ThreadStatusSystemError
	case appwire.ThreadStatusClosed:
		return appwire.ThreadStatusClosed
	case appwire.ThreadStatusNotLoaded:
		return appwire.ThreadStatusNotLoaded
	default:
		return appwire.ThreadStatusIdle
	}
}

func inputFromItems(prompt string, items []appwire.InputItem) (string, []ImageAttachment) {
	parts := []string{}
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, prompt)
	}
	images := make([]ImageAttachment, 0)
	for _, item := range items {
		switch item.Type {
		case "text", "input_text", "":
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case "image", "input_image":
			images = append(images, ImageAttachment{
				MediaType: item.MediaType,
				Data:      item.Data,
				Name:      item.Name,
			})
		}
	}
	return strings.Join(parts, "\n"), images
}

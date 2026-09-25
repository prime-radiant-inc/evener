package server

import (
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm/registry"
)

// Thread histories: each thread this daemon serves (the root and every
// in-process descendant) projects its history from its own transcript's
// recorded entries (server/thread_history.go) and publishes it as
// history/updated; its live unrecorded state is the history's overlay.

// descendantHistorySource is what the recorded-entry hook needs to ensure a
// descendant's history without taking s.mu: the hook runs under the child's
// transcript append lock, where only leaf locks may be taken. It is replaced
// whole, under s.mu, whenever one of its fields changes.
type descendantHistorySource struct {
	ownerThreadID  string
	sourceID       string
	bootGeneration string
	pathFunc       func(threadID string) string
}

func newAppHistories() *threadHistories {
	return newThreadHistories(transcriptindex.DefaultCacheCapacity, appoverlay.DefaultBudgetBytes)
}

// Close stops every thread history's projection. The server publishes no
// history afterwards.
func (s *Server) Close() {
	s.appHistories.close()
}

// WireTranscriptHistory installs sess's recorded-entry hooks into this
// server's thread histories, as serve does for every session it makes
// current: the recorded entries of sess and of each descendant it spawns
// reach their thread's history. Install them before the session's identity is
// installed, so the history's recorded length
// (PreparedAppIdentity.WithRecordedLength, read after this call) leaves no
// entry unhooked. A session whose identity is replaced keeps its hooks; they
// find no history for it and publish nothing.
func (s *Server) WireTranscriptHistory(sess *agent.Session) {
	threadID := sess.ID()
	sess.SetTranscriptRecordedFunc(func(rec transcript.Record) { s.recordTranscriptEntry(threadID, rec) })
	sess.SetDescendantRecordedFunc(func(childID string, rec transcript.Record) {
		s.recordDescendantTranscriptEntry(threadID, childID, rec)
	})
}

// recordTranscriptEntry is the root session's recorded-entry hook: it hands
// rec to the history of threadID, the session that recorded it. A session
// whose identity is no longer served has no history, and its records are
// dropped. It runs under the transcript append lock, so it takes only the
// history's queue mutex: the registry lookup is lock-free.
func (s *Server) recordTranscriptEntry(threadID string, rec transcript.Record) {
	if h := s.appHistories.get(threadID); h != nil {
		h.recorded(rec)
	}
}

// recordDescendantTranscriptEntry is the descendants' recorded-entry hook
// installed on the owning root's session: it hands rec to threadID's
// history. It runs under the child's transcript append lock, so it takes only
// the history's queue mutex: the lookup is lock-free, and a record that finds
// no history is dropped. The history is created on the event path
// (RecordDescendantAppEvent), and covers what was recorded before it through
// its recorded-length source. A detached history (its tree replaced, its
// delegate released) is no longer found, so a replaced tree's records go
// nowhere.
func (s *Server) recordDescendantTranscriptEntry(_, threadID string, rec transcript.Record) {
	if h := s.appHistories.get(threadID); h != nil {
		h.recorded(rec)
	}
}

// ensureDescendantHistory returns threadID's history, creating it over the
// descendant's transcript if ownerThreadID is the served root and the path
// resolver names one. It takes no lock but the registry's and does no I/O, so
// it is safe from a recorded-entry hook and inside a projection commit.
func (s *Server) ensureDescendantHistory(ownerThreadID, threadID string) *threadHistory {
	if h := s.appHistories.get(threadID); h != nil {
		return h
	}
	source := s.appDescendantHistorySource.Load()
	if source == nil || source.ownerThreadID != ownerThreadID || source.pathFunc == nil {
		return nil
	}
	path := strings.TrimSpace(source.pathFunc(threadID))
	if path == "" {
		return nil
	}
	ref := appwire.Ref{SourceID: sourceIDForProjection(source.sourceID), ThreadID: threadID}.String()
	publish, resync := s.historyPublishers(threadID, ref, source.bootGeneration)
	return s.appHistories.ensureDescendant(ownerThreadID, threadID, ref, path, source.bootGeneration, publish, resync, s.historyCost)
}

// storeDescendantHistorySourceLocked republishes the descendant hook's view of
// the served root. Callers hold s.mu.
func (s *Server) storeDescendantHistorySourceLocked() {
	s.appDescendantHistorySource.Store(&descendantHistorySource{
		ownerThreadID:  s.appThreadID,
		sourceID:       s.appSourceID,
		bootGeneration: s.appBootGeneration,
		pathFunc:       s.appDescendantTranscriptPathFunc,
	})
}

// ensureHistory returns threadID's history, creating it over path at epoch
// and bootGeneration with recordedLength already covered.
func (s *Server) ensureHistory(threadID, ref, path string, recordedLength int64, epoch uint64, bootGeneration string) *threadHistory {
	publish, resync := s.historyPublishers(threadID, ref, bootGeneration)
	return s.appHistories.ensure(threadID, ref, path, recordedLength, epoch, bootGeneration, publish, resync, s.historyCost)
}

// historyPublishers are the publish and resync a history of threadID commits
// through, at bootGeneration.
func (s *Server) historyPublishers(threadID, ref, bootGeneration string) (func(appwire.HistoryUpdatedParams) error, func(uint64)) {
	publish := func(params appwire.HistoryUpdatedParams) error {
		s.commitHistoryNotification(threadID, appwire.NotifyHistoryUpdated, params)
		return nil
	}
	resync := func(epoch uint64) {
		s.commitHistoryNotification(threadID, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{ThreadID: threadID, Ref: ref, BootGeneration: bootGeneration, Epoch: epoch})
	}
	return publish, resync
}

// currentBootGeneration is the served identity's boot generation.
func (s *Server) currentBootGeneration() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appBootGeneration
}

// commitHistoryNotification commits one notification from threadID's history
// projection, if the thread is still served: a history detached by an
// identity replacement may still be finishing a projection, and what it
// publishes describes a thread nobody reads any more.
func (s *Server) commitHistoryNotification(threadID, method string, params any) {
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.RLock()
		target, served := s.historyTargetLocked(threadID)
		s.mu.RUnlock()
		if !served {
			return nil
		}
		return []appserver.SequencedNotification{s.appNotifier.Record(target, method, params)}
	})
}

// historyTargetLocked is threadID's notification target, and whether the
// thread is served. Callers hold s.mu.
func (s *Server) historyTargetLocked(threadID string) (string, bool) {
	if threadID == s.appThreadID {
		if s.appRef != "" {
			return s.appRef, true
		}
		return threadID, true
	}
	return threadID, s.appDescendants[threadID] != nil
}

// historyCost prices a turn's usage at the model its entries recorded,
// through the session's current provider instance.
func (s *Server) historyCost(model string) *registry.Cost {
	s.mu.RLock()
	profile := s.status.Profile
	s.mu.RUnlock()
	return s.costFor(profile + "/" + model)
}

// appHistoryForID is threadID's history, or nil when the thread has none (no
// transcript). A served descendant whose history was released when its
// session closed gets a fresh one over its transcript. It does no file I/O,
// so it is safe inside a read's cut.
func (s *Server) appHistoryForID(threadID string) *threadHistory {
	if h := s.appHistories.get(threadID); h != nil {
		return h
	}
	s.mu.RLock()
	owner := s.appThreadID
	descendant := threadID != owner && s.appDescendants[threadID] != nil
	s.mu.RUnlock()
	if !descendant {
		return nil
	}
	return s.ensureDescendantHistory(owner, threadID)
}

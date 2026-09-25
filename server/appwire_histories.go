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
	ownerThreadID string
	sourceID      string
	pathFunc      func(threadID string) string
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
// dropped. It runs under the transcript append lock and takes only the
// registry's leaf lock.
func (s *Server) recordTranscriptEntry(threadID string, rec transcript.Record) {
	if h := s.appHistories.get(threadID); h != nil {
		h.recorded(rec)
	}
}

// recordDescendantTranscriptEntry is the descendants' recorded-entry hook
// installed on ownerThreadID's session: it hands rec to threadID's history,
// ensuring it first. Records from a tree whose root this server no longer
// serves are dropped. It runs under the child's transcript append lock and
// takes only leaf locks.
func (s *Server) recordDescendantTranscriptEntry(ownerThreadID, threadID string, rec transcript.Record) {
	if h := s.ensureDescendantHistory(ownerThreadID, threadID); h != nil {
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
	return s.ensureHistory(threadID, ref, path, 0, 0)
}

// storeDescendantHistorySourceLocked republishes the descendant hook's view of
// the served root. Callers hold s.mu.
func (s *Server) storeDescendantHistorySourceLocked() {
	s.appDescendantHistorySource.Store(&descendantHistorySource{
		ownerThreadID: s.appThreadID,
		sourceID:      s.appSourceID,
		pathFunc:      s.appDescendantTranscriptPathFunc,
	})
}

// ensureHistory returns threadID's history, creating it over path at epoch
// with recordedLength already covered.
func (s *Server) ensureHistory(threadID, ref, path string, recordedLength int64, epoch uint64) *threadHistory {
	return s.appHistories.ensure(threadID, ref, path, recordedLength, epoch,
		func(params appwire.HistoryUpdatedParams) error {
			s.commitHistoryNotification(threadID, appwire.NotifyHistoryUpdated, params)
			return nil
		},
		func(epoch uint64) {
			s.commitHistoryNotification(threadID, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{ThreadID: threadID, Ref: ref, Epoch: epoch})
		},
		s.historyCost,
	)
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
// transcript).
func (s *Server) appHistoryForID(threadID string) *threadHistory {
	return s.appHistories.get(threadID)
}

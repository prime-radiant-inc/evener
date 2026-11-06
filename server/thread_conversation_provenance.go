package server

import (
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// conversationEnvelopeSource is optional for old/source-backed producers. Its
// absence means unknown, never an authoritative empty admission frontier.
type conversationProvenanceSource interface {
	ConversationProvenance() *appwire.ConversationProvenanceState
}

func (s *Server) commitConversationProvenance(threadID, sourceID, ref string, next *appwire.ConversationProvenanceState) {
	if next == nil {
		return
	}
	next = appwire.CloneConversationProvenance(next)
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.appThreadID != threadID || s.appSourceID != sourceID {
			return nil
		}
		if h := s.appHistories.get(threadID); h != nil {
			h.mu.Lock()
			h.adoptLocked(next)
			next = h.provenance
			h.mu.Unlock()
		}
		old := s.appEnvelope.ConversationProvenance
		if old != nil && old.InstanceID == next.InstanceID && old.Revision >= next.Revision {
			return nil
		}
		s.appEnvelope.ConversationProvenance = next
		return []appserver.SequencedNotification{s.appNotifier.Record(ref, appwire.NotifyConversationProvenanceChanged, appwire.ConversationProvenanceChangedParams{ThreadID: threadID, Ref: ref, Provenance: next})}
	})
}

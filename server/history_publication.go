package server

import (
	"sync"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// historyPublication is seeded before identity installation and then owned by
// exactly one history. Both producer handoffs and admission-only commits use
// this leaf lock. No callback or I/O runs while it is held.
type historyPublication struct {
	mu             sync.Mutex
	paired         bool
	completeLength int64
	stagedEnd      int64
	provenance     *appwire.ConversationProvenanceState
	history        *threadHistory
}

// stageLocked fences output demand until the producer publishes its snapshot.
// Input-only side writes need no graph to resolve, but cannot cross a pending
// output prefix. The boundary is independent of the droppable overlay queue.
func (p *historyPublication) stageLocked(rec transcript.Record) {
	end := rec.Offset + rec.Length
	if rec.Turn.ConversationFrontier != nil || rec.Turn.ConversationProvenance != nil {
		p.stagedEnd = max(p.stagedEnd, end)
	} else if p.stagedEnd <= p.completeLength {
		p.completeLength = max(p.completeLength, end)
	}
}

func (p *historyPublication) adoptLocked(next *appwire.ConversationProvenanceState) {
	if next == nil {
		return
	}
	if old := p.provenance; old != nil && (old.InstanceID != next.InstanceID || old.Revision >= next.Revision) {
		return
	}
	p.provenance = next
}

func (p *historyPublication) complete(length int64, next *appwire.ConversationProvenanceState) {
	p.mu.Lock()
	h := p.history
	if h != nil && h.closed {
		p.mu.Unlock()
		return
	}
	p.adoptLocked(next)
	p.completeLength = max(p.completeLength, length)
	if h != nil && p.stagedEnd <= p.completeLength {
		p.completeLength = max(p.completeLength, h.recordedLength)
	}
	p.mu.Unlock()
	if h != nil {
		h.wakeUp()
	}
}

func (h *threadHistory) readableLengthLocked() int64 {
	if h.paired {
		return h.completeLength
	}
	return h.recordedLength
}

func (h *threadHistory) conversationProvenance() *appwire.ConversationProvenanceState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.provenance
}

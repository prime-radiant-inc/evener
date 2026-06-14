package agent

import (
	"context"
	"errors"

	"primeradiant.com/serf/agent/schema"
)

func (s *Session) setPinnedNote(note string) {
	s.mu.Lock()
	s.pinnedNote = note
	s.mu.Unlock()
}

// PinnedNote returns the current agent-authored note (empty if none).
func (s *Session) PinnedNote() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinnedNote
}

// requestForceCompact records that the compact tool asked for a compaction at the
// round tail. One per round: a second request before takeForceRequest consumes the
// first is an error so distinct intents are never silently clobbered.
func (s *Session) requestForceCompact(instructions string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forceRequested {
		return errors.New("a compaction is already pending this round")
	}
	s.forceRequested = true
	s.pendingInstructions = instructions
	return nil
}

// applyPendingForceCompact runs an agent-requested compaction at the tool-round
// tail. It mirrors Session.Compact but threads the agent's instructions and runs
// only when a request is pending. The pinned note is re-stamped inside the
// compaction via runPreCompactHook, so no post-call append is needed.
func (s *Session) applyPendingForceCompact(ctx context.Context) {
	instructions, ok := s.takeForceRequest()
	if !ok || s.contextMgr == nil {
		return
	}
	s.contextMgr.Meta = s.buildCompactionMeta()

	s.mu.Lock()
	histCopy := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	emitFn, flush := s.compactionEmitFunc(ctx, &histCopy)
	s.contextMgr.ForceCompact(ctx, &histCopy, instructions, emitFn)
	flush()

	s.mu.Lock()
	s.history = histCopy
	s.nudgedSinceCompact = false // reset nudge latch on any compaction
	s.mu.Unlock()

	s.maybeAutoSave()
}

// takeForceRequest consumes a pending force request (called once at the round tail).
func (s *Session) takeForceRequest() (instructions string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.forceRequested {
		return "", false
	}
	instructions = s.pendingInstructions
	s.forceRequested = false
	s.pendingInstructions = ""
	return instructions, true
}

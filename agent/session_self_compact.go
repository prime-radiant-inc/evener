package agent

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
)

// maybeElicitNoteBeforeCompaction implements Variant B of the forced-note
// mechanism: when a compaction is imminent (pressure ≥ CheckpointThreshold) and
// no note is already set, ask the model for the must-keep-verbatim details and pin
// them, so the compaction hands them forward. Runs before ManageContext folds the
// history. Best-effort: a failed elicitation just warns and lets the normal
// compaction proceed.
//
// It skips when a note is already set — the agent's own compact-tool note (or a
// note elicited earlier this cycle) wins and is never overwritten; the slot reopens
// when the compaction consumes the note (clearPinnedNote), so the next cycle
// re-elicits fresh facts. That skip also serves as the per-compaction latch, so a
// stuck-high pressure turn does not re-fire the side LLM call every round.
func (s *Session) maybeElicitNoteBeforeCompaction(ctx context.Context, history []schema.Turn, sysPromptChars int) {
	if s.contextMgr == nil {
		return
	}
	if s.PinnedNote() != "" {
		return // a note is already set — don't overwrite the agent's (or this cycle's) note
	}
	if s.contextMgr.Pressure(history, sysPromptChars) < s.contextMgr.CheckpointThreshold {
		return // no compaction imminent — nothing to capture yet
	}
	// Elicit only over the prefix the compaction will fold into a lossy summary;
	// the most-recent PreserveRecentTurns survive verbatim and need no rescuing.
	preserve := s.contextMgr.PreserveRecentTurns
	if len(history) <= preserve {
		return // nothing will be folded yet — nothing to capture
	}
	foldable := history[:len(history)-preserve]

	fn := s.elicitNoteFn
	if fn == nil {
		if !s.contextMgr.HasClient() {
			return // no elicitor available (no client) — skip silently
		}
		fn = s.contextMgr.ElicitNote
	}
	note, err := fn(ctx, foldable)
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: "note elicitation failed: " + err.Error()})
		return
	}
	if strings.TrimSpace(note) != "" {
		s.setPinnedNote(note)
	}
}

func (s *Session) setPinnedNote(note string) {
	s.mu.Lock()
	s.pinnedNote = note
	s.mu.Unlock()
}

// clearPinnedNote drops the pending note after a compaction has handed it forward,
// so it is a one-shot handoff (not re-injected at future compactions) and the slot
// reopens for the next cycle's elicitation.
func (s *Session) clearPinnedNote() {
	s.mu.Lock()
	s.pinnedNote = ""
	s.mu.Unlock()
}

// PinnedNote returns the current agent-authored note (empty if none).
func (s *Session) PinnedNote() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinnedNote
}

const selfCompactNudge = "You are running low on context-window headroom. If you are " +
	"at or near a clean stopping point, call the `compact_context` tool now to fold " +
	"older history into a summary checkpoint and free headroom — include a note_to_self " +
	"with the exact details that must survive (and optional compaction_instructions). " +
	"If you don't, an automatic compaction will run without your steering."

// maybeNudgeSelfCompact injects a one-time steering nudge when pressure crosses
// WarnThreshold. Best-effort: a single large tool result can jump past the
// checkpoint threshold before this fires; the checkpoint/summary fallback is the
// guarantee. The nudge is queued via Steer; the round loop drains it into history
// at the next tool-round seam (injectPostToolSteering), so the agent acts on it at
// its next seam — if the nudging round is the turn's last, it carries to the next
// turn. The latch resets on any compaction. Returns true if it nudged.
//
// Assumes the single per-session turn goroutine: the production caller and the
// latch resets all run on that goroutine, so the unlocked pressure read between
// the latch check and set cannot double-fire.
func (s *Session) maybeNudgeSelfCompact(sysPromptChars int) bool {
	if s.contextMgr == nil {
		return false
	}
	s.mu.Lock()
	if s.nudgedSinceCompact {
		s.mu.Unlock()
		return false
	}
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	if s.contextMgr.Pressure(hist, sysPromptChars) < s.contextMgr.WarnThreshold {
		return false
	}
	s.mu.Lock()
	s.nudgedSinceCompact = true
	s.mu.Unlock()
	s.Steer(selfCompactNudge)
	return true
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

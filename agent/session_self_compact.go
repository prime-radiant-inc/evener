package agent

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
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
// when a winning compaction claims the note at publication
// (claimPinnedNoteLocked), so the next cycle re-elicits fresh facts. That skip
// also serves as the per-compaction latch, so a stuck-high pressure turn does
// not re-fire the side LLM call every round.
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
	cutoff, foldableExists := attentionTransparentRecentCutoff(history, preserve)
	if !foldableExists {
		return // nothing will be folded yet — nothing to capture
	}
	foldable := history[:cutoff]

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
	s.pinnedNoteGen++
	s.mu.Unlock()
}

// PinnedNote returns the current agent-authored note (empty if none).
func (s *Session) PinnedNote() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinnedNote
}

// pinnedNoteSnapshot returns the current note together with its generation,
// so a fold capturing the note for handoff can later claim exactly what it
// captured (claimPinnedNoteLocked) instead of blindly clearing whatever is
// pinned by then.
func (s *Session) pinnedNoteSnapshot() (note string, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinnedNote, s.pinnedNoteGen
}

// claimPinnedNoteLocked consumes the pinned note a fold captured at
// generation gen — but only if that is still the live generation: a note set
// (or cleared and re-set) since the capture belongs to the NEXT compaction
// cycle and must survive this fold's claim. Runs inside the publication
// transaction; callers hold s.mu.
func (s *Session) claimPinnedNoteLocked(gen uint64) {
	if s.pinnedNoteGen != gen {
		return
	}
	s.pinnedNote = ""
	s.pinnedNoteGen++
}

// selfCompactNudge is the low-headroom warning. The pressure is real either
// way; only the remedy is tool-dependent, so a session without compact_context
// gets the same warning worded as something it can actually do.
func selfCompactNudge(canCompact bool) string {
	if !canCompact {
		return "You are running low on context-window headroom. Summarize and drop stale " +
			"context in your next messages — restate the exact details that must survive " +
			"(ids, paths, numbers, decisions, next steps) and stop carrying the rest " +
			"forward. If you don't, an automatic compaction will run without your steering."
	}
	return "You are running low on context-window headroom. If you are " +
		"at or near a clean stopping point, call the `compact_context` tool now to fold " +
		"older history into a summary checkpoint and free headroom — include a note_to_self " +
		"with the exact details that must survive (and optional compaction_instructions). " +
		"If you don't, an automatic compaction will run without your steering."
}

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
	s.SteerKind(selfCompactNudge(s.canInstructTool("compact_context")), events.SteeringKindCompactNudge)
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

	// compact_context runs mid-turn, at every round tail, so this can race
	// another ForceCompact/ManageContext publisher (Compact(), the
	// content-filter retry, or the round loop's own ManageContext).
	// foldWithForceCompact retries once against the current history on
	// conflict; on total failure this is a
	// best-effort self-compaction, so skip silently rather than retrying
	// indefinitely or failing the round — a competing fold already relieved
	// whatever pressure prompted this one.
	if !s.foldWithForceCompact(ctx, instructions) {
		return
	}

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

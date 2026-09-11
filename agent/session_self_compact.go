package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// (claimPinnedNoteLocked), so the next cycle re-elicits fresh facts. It also
// skips when any pending or published-but-undelivered compaction operation
// owns the cycle: the accepted response is recorded once per cycle, so that
// latch is also the first-wins rule for the reload selection — a later
// elicitation can never overwrite the concrete selection the cycle already
// owns, not even with an invalid one.
//
// The note generation is captured BEFORE the elicitor call; the acceptance
// (acceptAutomaticSkillCompaction) is rejected when the note changed or an
// operation appeared mid-elicitation, and persists the elicited response as a
// generation-owned automatic operation before any compaction runs.
func (s *Session) maybeElicitNoteBeforeCompaction(ctx context.Context, history []schema.Turn, sysPromptChars int) {
	if s.contextMgr == nil {
		return
	}
	if s.PinnedNote() != "" {
		return // a note is already set — don't overwrite the agent's (or this cycle's) note
	}
	if s.pendingSkillCompactionSnapshot() != nil {
		return // an operation already owns this compaction cycle — its response is recorded
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

	// Capture the note generation before the actual elicitor call, so the
	// acceptance can reject a response that raced a note change.
	_, capturedNoteGen := s.pinnedNoteSnapshot()

	inventory := s.skillInventorySnapshot()
	var raw string
	var err error
	if fn := s.elicitNoteFn; fn != nil {
		raw, err = fn(ctx, foldable)
	} else {
		if !s.contextMgr.HasClient() {
			return // no elicitor available (no client) — skip silently
		}
		raw, err = s.contextMgr.ElicitNote(ctx, foldable, skillInventorySummaries(inventory))
	}
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: "note elicitation failed: " + err.Error()})
		return
	}
	// Split the selection block from the free-text note and accept both as one
	// generation-owned automatic operation. An invalid selection preserves the
	// note verbatim; a rejected acceptance (stale generation, or an operation
	// that appeared mid-elicitation) records nothing, so a losing attempt
	// writes no metadata. A whitespace-only elicited note pins nothing — the
	// same visible note behavior the pre-persistence elicitor had.
	note, selection := parseSkillReloadElicitation(raw, inventory)
	if strings.TrimSpace(note) == "" {
		note = ""
	}
	if _, err := s.acceptAutomaticSkillCompaction(ctx, capturedNoteGen, note, selection); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: "persisting the elicited compaction intent failed: " + err.Error()})
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
// gets the same warning worded as something it can actually do. loaded lists
// the session's successfully loaded skills: the compact-tool remedy asks which
// to reload via reload_skills; the no-tool remedy lists them for awareness and
// never requests a structured selection the model cannot submit.
func selfCompactNudge(canCompact bool, loaded []schema.SkillInventorySummary) string {
	list := ""
	if len(loaded) > 0 {
		var b strings.Builder
		b.WriteString(" Skills loaded in this session:\n")
		for _, sk := range loaded {
			fmt.Fprintf(&b, "- %s — %s\n", sk.Name, sk.Description)
		}
		list = b.String()
	}
	if !canCompact {
		advice := "You are running low on context-window headroom. Summarize and drop stale " +
			"context in your next messages — restate the exact details that must survive " +
			"(ids, paths, numbers, decisions, next steps) and stop carrying the rest " +
			"forward. If you don't, an automatic compaction will run without your steering."
		if list == "" {
			return advice
		}
		return advice + list + "An automatic compaction may drop their instruction bodies; " +
			"re-invoke the ones you still need afterward."
	}
	advice := "You are running low on context-window headroom. If you are " +
		"at or near a clean stopping point, call the `compact_context` tool now to fold " +
		"older history into a summary checkpoint and free headroom — include a note_to_self " +
		"with the exact details that must survive (and optional compaction_instructions). " +
		"If you don't, an automatic compaction will run without your steering."
	if list == "" {
		return advice
	}
	return advice + list + "Compaction drops their instruction bodies; pass the exact names " +
		"you want restored as compact_context's reload_skills array ([] reloads none)."
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
	s.SteerKind(selfCompactNudge(s.canInstructTool("compact_context"), skillInventorySummaries(s.skillInventorySnapshot())), events.SteeringKindCompactNudge)
	return true
}

// requestForceCompact records that a compaction was requested for the round
// tail. One per round: a second request before the transient is consumed is an
// error so distinct per-round intents are never silently clobbered. This is
// the transient trigger primitive; the compact tool's durable intent is the
// generation-owned operation requestSkillCompaction persists, and a restored
// forced operation re-arms this same transient at resume.
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
// tail. The transient round request is only the per-round trigger: it is cleared
// here, but the instructions come from the pending forced operation's owner
// snapshot (retained until publication or terminal cancellation) when one
// exists — never consumed from the transient — so a crash between the tool call
// and this tail cannot lose the steering. A bare transient without an
// operation (the requestForceCompact primitive) keeps its own instructions.
//
// The pending FORCED operation is captured here — this dispatch is its
// REQUESTING caller — and handed to the fold, whose winning publication claims
// exactly that generation. An automatic operation is never captured by this
// path: only the per-request fold that elicited it can claim it. On total
// publication loss, foldWithForceCompact retires the captured forced operation
// (forced_not_published); on conflict the caller's compaction_instructions are
// still intent, not pressure, and losing them without a trace hides real
// steering loss.
func (s *Session) applyPendingForceCompact(ctx context.Context) {
	s.mu.Lock()
	requested := s.forceRequested
	instructions := s.pendingInstructions
	s.forceRequested = false
	s.pendingInstructions = ""
	var captured *schema.SkillCompactionOperation
	if op := s.skillLifecycle.PendingCompaction; op != nil && op.Origin == skillCompactionOriginForced && op.Phase == skillCompactionPhasePending {
		// The persisted owner snapshot wins over the transient copy.
		instructions = op.Instructions
		captured = &schema.SkillCompactionOperation{
			Generation:     op.Generation,
			Origin:         op.Origin,
			Instructions:   op.Instructions,
			NoteGeneration: op.NoteGeneration,
			Selection:      schema.SkillReloadSelection{State: op.Selection.State, Names: slices.Clone(op.Selection.Names), ErrorCode: op.Selection.ErrorCode},
			Phase:          op.Phase,
		}
	}
	s.mu.Unlock()
	if !requested || s.contextMgr == nil {
		return
	}

	// compact_context runs mid-turn, at every round tail, so this can race
	// another ForceCompact/ManageContext publisher (Compact(), the
	// content-filter retry, or the round loop's own ManageContext).
	// foldWithForceCompact retries once against the current history on
	// conflict; on total failure this is a best-effort self-compaction, so
	// the fold's own loss stays silent
	// rather than retrying indefinitely or failing the round — a competing
	// fold already relieved whatever pressure prompted this one. The forced
	// operation's terminal retirement notice (when one was captured) is
	// emitted by the fold driver itself. The
	// caller's compaction_instructions are different: they are intent, not
	// pressure, and the competitor did not honor them, so losing them
	// without a trace hides real steering loss.
	if ok, refusal := s.foldWithForceCompact(ctx, instructions, captured); !ok {
		if strings.TrimSpace(instructions) != "" {
			reason := "a concurrent compaction published first"
			if refusal != nil {
				// The same distinction Compact draws: naming a competitor that
				// does not exist sends the model's next attempt at the same
				// steering into a fold the transcript will refuse again.
				reason = refusal.Error()
			}
			s.emit(events.EventWarning, events.WarningData{Message: "compact_context instructions were not applied — " + reason + ": " + instructions})
		}
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

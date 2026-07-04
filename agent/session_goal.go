package agent

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/llm"
)

// SetKickFunc registers the callback an idle SetGoal uses to start the goal loop
// immediately by feeding the first continuation prompt back into the serve loop's
// input channel. The agent module must not import server, so serve.go wires this.
func (s *Session) SetKickFunc(f func(prompt string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kickFunc = f
}

// SetGoal sets the session's objective and starts (or arms) the goal loop. It
// rejects an empty objective. The new goal is stored active, then under s.mu the
// in-turn flag decides the start path: if a turn is already running (goalInTurn),
// its drain-loop gate will pick the goal up after the current turn, so SetGoal
// returns started=false; if the session is idle and a kick callback is wired,
// SetGoal renders the first continuation prompt and kicks (outside the lock),
// returning started=true. Holding s.mu across the goalInTurn read makes this
// mutually exclusive with the gate's "clear flag + go idle" step (spec §7), so a
// goal set as a turn ends can neither be dropped nor double-started.
//
// Arm, don't kick, while awaiting (spec §5.3): a /goal issued while the session
// rests on a pending question has no in-flight turn for a drain-loop gate to
// back it, so without this check the idle-kick branch below would drive a turn
// straight past the unanswered ask. The goal is still stored active; it resumes
// at the first non-awaiting settle once the reply resolves the ask.
func (s *Session) SetGoal(ctx context.Context, objective string) (started bool, err error) {
	_ = ctx
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return false, errors.New("goal objective must not be empty")
	}
	store := s.getOrCreateGoalStore()

	// Set the goal and read the in-turn flag and awaiting state under s.mu so the
	// write is mutually exclusive with the gate's "clear flag + settle" step
	// (settleGoalOnIdle): a first goal set as a turn ends is then either kicked
	// here (idle) or picked up by the settle re-check (turn-tail window) — never
	// stranded (spec §7).
	s.mu.Lock()
	store.Set(objective, s.sclock().Now())
	inTurn := s.goalInTurn
	kick := s.kickFunc
	awaiting := s.state == SessionAwaiting
	s.mu.Unlock()

	if inTurn || kick == nil || awaiting {
		// A turn is running (its gate backs the goal), there is no way to kick an
		// idle session, or the session is resting awaiting a reply; either way the
		// caller cannot rely on an immediate start.
		return false, nil
	}
	kick(goal.Render(objective))
	return true, nil
}

// ClearGoal removes the session's goal. It takes the same s.mu coordination as
// SetGoal so a clear landing exactly as the drain-loop gate arms cannot leak one
// extra unwanted continuation: the gate's terminal "clear flag + go idle" step
// and this clear are mutually exclusive on s.mu (spec §7).
func (s *Session) ClearGoal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getOrCreateGoalStore().Clear()
}

// GoalStatus reports the session's current /goal state for status surfaces (the
// appwire SerfThread.Goal field, `/goal status`). ok is false when no goal is
// set. It returns primitives rather than the internal goal.Snapshot so callers
// outside the agent module (which cannot import agent/internal/goal) can consume
// it.
func (s *Session) GoalStatus() (status string, iterations int, ok bool) {
	snap, ok := s.getOrCreateGoalStore().Snapshot()
	if !ok {
		return "", 0, false
	}
	return string(snap.Status), snap.Iterations, true
}

// goalCompactionSteering returns the active goal's rendered objective as a
// single steering message, or nil when no goal is set or the goal is not
// active. It is appended after any plugin PreCompact output on every compaction
// path so the objective survives mid-turn compaction (spec §2b): re-injecting it
// as the trailing TurnSteering turn restores it at the strongest recency
// position, which safeCutoff then protects from the same compaction.
func (s *Session) goalCompactionSteering() []string {
	snap, ok := s.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive {
		return nil
	}
	return []string{goal.Render(snap.Objective)}
}

// currentGoalContinuation returns a fresh render of the CURRENT active objective,
// or ("", false) when no goal is set or the goal is not active. It is the read-only
// re-validation used at the drain loop's inline-continuation site: a continuation
// decided at the gate is deferred across an interleaved notification turn, during
// which the user may clear (/goal clear) or retarget (/goal <new>) the goal, making
// the gate-time render stale. Re-reading here drops a continuation for a goal that is
// no longer active and runs the new objective after a retarget. It does NOT fold the
// turn (no RecordContinuation) — the fold already happened at the gate — so it never
// advances iteration/no-progress accounting; it only re-reads and re-renders.
func (s *Session) currentGoalContinuation() (string, bool) {
	snap, ok := s.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive {
		return "", false
	}
	return goal.Render(snap.Objective), true
}

// goalRoundCap selects the per-input tool-round cap. User-input turns use the
// configured cap verbatim. Continuation turns (the goal engine) clamp an
// unbounded (cfg<0) or larger-than-GoalTurnMaxRounds config down to
// GoalTurnMaxRounds, bounding per-goal-turn spend and reducing the likelihood of
// intra-turn compaction eroding the re-injected objective (spec §2b/C13). A bare
// min(cfg, cap) is wrong because cfg<0 means "unbounded", not "smallest".
func goalRoundCap(cfg int, kind EntryKind) int {
	if kind == EntryContinuation && (cfg < 0 || cfg > goal.GoalTurnMaxRounds) {
		return goal.GoalTurnMaxRounds
	}
	return cfg
}

// callsMadeProgress reports whether any tool call in a round is a real mutating
// action for the goal no-progress signal: a call that is !ReadOnly AND is neither
// the result/communicate tool nor task_list. Read-only tools, the result tool,
// and plan updates all register ReadOnly==false or are otherwise excluded, so the
// ReadOnly flag alone is insufficient (spec §2). An unknown tool name (Get==nil)
// is treated as a non-read-only mutation, matching the executor's default.
func (s *Session) callsMadeProgress(calls []llm.ToolCallData) bool {
	resultName := s.resultToolName()
	for i := range calls {
		name := calls[i].Name
		if name == resultName || name == "task_list" {
			continue
		}
		t := s.reg.Get(name)
		if t == nil || !t.ReadOnly {
			return true
		}
	}
	return false
}

// armGoalContinuation runs in the drain-loop gate (on the turn goroutine) after a
// goal continuation turn completes. progressed reports whether the just-finished
// turn made a mutating tool call. It folds that signal into the goal under the
// goal lock and decides whether to issue another continuation.
//
// It returns (renderedPrompt, true) to continue, or ("", false) to stop. The gate
// owns two stop paths — a model-declared terminal status and the no-progress
// breaker — and emits exactly one EventGoalEnded on each so the user is told why
// the loop stopped. There is no iteration cap: a goal that keeps making progress
// runs until it is completed or the no-progress breaker fires. With no goal set
// it is a no-op returning ("", false).
func (s *Session) armGoalContinuation(progressed, wasContinuation bool) (string, bool) {
	store := s.getOrCreateGoalStore()
	snap, ok := store.Snapshot()
	if !ok {
		return "", false // no goal
	}
	if snap.Status != goal.StatusActive {
		// Already terminal: update_goal complete/blocked set it this turn, or it
		// finished on an earlier turn (a terminated goal lingers in the store until
		// /goal clear, and the gate runs at every turn tail). reportGoalEnded emits
		// exactly once via the store's once-gate, so the terminal report does not
		// repeat on every subsequent turn.
		s.reportGoalEnded()
		return "", false
	}
	if !wasContinuation {
		// A user (or other non-continuation) turn completed while a goal is active:
		// resume the goal, but do NOT fold the user's own turn into the no-progress
		// streak or the iteration count — only the goal's own continuation turns
		// count toward those (/par #4).
		return goal.Render(snap.Objective), true
	}
	snap, stillActive := store.RecordContinuation(progressed, s.sclock().Now())
	if !stillActive {
		// The no-progress breaker fired this turn. Persist the terminal transition:
		// it happens after processOneInput's defer-save, so without this a blocked
		// goal would be saved as still-active and resume on restart (/par A4).
		s.reportGoalEnded()
		s.maybeAutoSave()
		return "", false
	}
	return goal.Render(snap.Objective), true
}

// reportGoalEnded emits the terminal EventGoalEnded report exactly once, via the
// store's once-gate (TakeTerminalReport). It is safe to call on every gate stop
// path and on repeated turns after the goal has already finished.
func (s *Session) reportGoalEnded() {
	if snap, ok := s.getOrCreateGoalStore().TakeTerminalReport(); ok {
		s.emitGoalEnded(snap)
	}
}

// settleGoalOnIdle runs at the drain loop's idle transition — including the
// asking turn's own tail, where the "idle" transition actually rests
// SessionAwaiting (this func runs regardless of which state the turn just
// left behind). Under s.mu it clears the in-turn flag and, if an active goal
// was set in the turn-tail window (after the gate's store read but before the
// flag clear), captures the first continuation prompt so the goal is kicked
// rather than stranded active-but-idle until the next user message (spec §7).
// The kick is issued outside the lock. Mutually exclusive on s.mu with
// SetGoal's "set goal + read flag", so the goal is kicked exactly once.
//
// Arm, don't kick, while awaiting (spec §5.3): goalInTurn still clears — the
// turn genuinely finished — but the prompt is computed only when the session
// is NOT resting on a pending question, so an active goal is left armed in
// the store instead of being kicked past the user's unanswered ask. The
// normal resume fold (armGoalContinuation's non-continuation branch, folded
// at the reply turn's own drain tail) picks it up once the reply resolves it.
func (s *Session) settleGoalOnIdle() {
	s.mu.Lock()
	s.goalInTurn = false
	kick := s.kickFunc
	awaiting := s.state == SessionAwaiting
	var prompt string
	if kick != nil && !awaiting {
		if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok && snap.Status == goal.StatusActive {
			prompt = goal.Render(snap.Objective)
		}
	}
	s.mu.Unlock()
	if prompt != "" {
		kick(prompt)
	}
}

// terminateGoalOnError transitions an active goal to blocked and emits its
// terminal report when a turn ends in a system cancellation or error. It is a
// no-op when there is no active goal, and — critically — when err is a genuine
// user /interrupt: the goal stays active and resumes after the next completed
// turn (spec §6). The discriminator is the queuedInputDrainContext bool, not the
// WithQueuedInputDrainOnInterrupt marker (which is installed on every turn ctx and
// so discriminates nothing); a DeadlineExceeded or non-retryable provider error
// routes to blocked.
func (s *Session) terminateGoalOnError(ctx context.Context, err error) {
	store := s.getOrCreateGoalStore()
	if snap, ok := store.Snapshot(); !ok || snap.Status != goal.StatusActive {
		return
	}
	if _, isUserInterrupt := queuedInputDrainContext(ctx, err); isUserInterrupt {
		return // genuine user interrupt: leave the goal active
	}
	if goalRootShutdown(ctx, err) {
		// The root/daemon context is shutting down (e.g. a restart or deploy), not a
		// genuine turn failure: leave the goal active so it resumes on the next load
		// rather than being permanently blocked. This only matters now that A4
		// persists terminal transitions (/par B3, surfaced once blocks are saved).
		return
	}
	if store.SetTerminal(goal.StatusBlocked, err.Error(), s.sclock().Now()) {
		s.reportGoalEnded()
		// Persist the block: terminateGoalOnError runs after processOneInput's
		// defer-save, so without this the goal is saved as still-active and would
		// resume on restart (/par A4).
		s.maybeAutoSave()
	}
}

// goalRootShutdown reports whether err is a cancellation that came from the
// root/daemon context being torn down (vs a genuine per-turn failure). On shutdown
// an active goal must be left active so it survives the restart; only a real error
// blocks it. The discriminator is the same queuedInputDrainConfig the queue uses:
// its rootCtx being Done while err is a context.Canceled is the shutdown signature.
func goalRootShutdown(ctx context.Context, err error) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	cfg, ok := ctx.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok || cfg.rootCtx == nil {
		return false
	}
	return cfg.rootCtx.Err() != nil
}

// emitGoalEnded emits the terminal goal report from a snapshot. Every goal stop
// path routes through here so the "told why it stopped" promise holds on each one.
func (s *Session) emitGoalEnded(snap goal.Snapshot) {
	s.emit(events.EventGoalEnded, events.GoalEndedData{
		Status:     string(snap.Status),
		Reason:     snap.StopReason,
		Iterations: snap.Iterations,
	})
}

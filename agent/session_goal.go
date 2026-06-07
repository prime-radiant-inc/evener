package agent

import (
	"context"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/llm"
)

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
// It returns (renderedPrompt, true) to continue, or ("", false) to stop. On every
// stop path that the gate owns — model-declared terminal status, the no-progress
// breaker, and the iteration cap — it emits exactly one EventGoalEnded so the user
// is told why the loop stopped. With no goal set it is a no-op returning ("", false).
func (s *Session) armGoalContinuation(progressed bool) (string, bool) {
	store := s.getOrCreateGoalStore()
	if _, ok := store.Snapshot(); !ok {
		return "", false // no goal
	}
	snap, stillActive := store.RecordContinuation(progressed, time.Now())
	if !stillActive {
		// Terminal: update_goal complete/blocked, or the no-progress breaker fired.
		s.emitGoalEnded(snap)
		return "", false
	}
	if !goal.ShouldContinue(snap) {
		// Still "active" but the iteration cap is reached: the gate stops the loop.
		store.SetTerminal(goal.StatusBlocked, "hit iteration limit", time.Now())
		s2, _ := store.Snapshot()
		s.emitGoalEnded(s2)
		return "", false
	}
	return goal.Render(snap.Objective), true
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
	if store.SetTerminal(goal.StatusBlocked, err.Error(), time.Now()) {
		s2, _ := store.Snapshot()
		s.emitGoalEnded(s2)
	}
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

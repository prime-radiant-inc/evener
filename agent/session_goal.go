package agent

import (
	"context"
	"errors"
	"strings"
	"time"

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
func (s *Session) SetGoal(ctx context.Context, objective string) (started bool, err error) {
	_ = ctx
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return false, errors.New("goal objective must not be empty")
	}
	store := s.getOrCreateGoalStore()
	store.Set(objective, time.Now())

	s.mu.Lock()
	inTurn := s.goalInTurn
	kick := s.kickFunc
	s.mu.Unlock()

	if inTurn || kick == nil {
		// A turn is running (its gate backs the goal) or there is no way to kick
		// an idle session; either way the caller cannot rely on an immediate start.
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
// set. max is the iteration cap (goal.DefaultMaxIterations). It returns
// primitives rather than the internal goal.Snapshot so callers outside the agent
// module (which cannot import agent/internal/goal) can consume it.
func (s *Session) GoalStatus() (status string, iterations, maxIter int, ok bool) {
	snap, ok := s.getOrCreateGoalStore().Snapshot()
	if !ok {
		return "", 0, 0, false
	}
	return string(snap.Status), snap.Iterations, goal.DefaultMaxIterations, true
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

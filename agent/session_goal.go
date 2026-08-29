package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
// Arm, don't kick, while a question is pending (spec §5.3): a /goal issued
// while the session has a genuinely unanswered ask_user question has no
// in-flight turn for a drain-loop gate to back it, so without this check the
// idle-kick branch below would drive a turn straight past the unanswered ask.
// The goal is still stored active; it resumes at the first settle once the
// reply resolves the ask. The hold is keyed on the pending-ask set
// (len(s.askPending) > 0), NOT on SessionAwaiting: under attention-status-model
// v5, SessionAwaiting also covers a plain output-producing rest with nothing
// pending, where an idle /goal must kick normally, exactly as it would on
// SessionIdle.
func (s *Session) SetGoal(ctx context.Context, objective string) (started bool, err error) {
	_ = ctx
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return false, errors.New("goal objective must not be empty")
	}
	store := s.getOrCreateGoalStore()
	s.goalUpdateMu.Lock()

	// Set the goal and read the in-turn flag and pending-ask set under s.mu so
	// the write is mutually exclusive with the gate's "clear flag + settle" step
	// (settleGoalOnIdle): a first goal set as a turn ends is then either kicked
	// here (nothing pending) or picked up by the settle re-check (turn-tail
	// window) — never stranded (spec §7). askPending is read directly under the
	// held lock (not via askPendingCount()/HasPendingAsk(), which self-lock s.mu
	// and would deadlock here).
	s.mu.Lock()
	store.Set(objective, s.sclock().Now())
	inTurn := s.goalInTurn
	kick := s.kickFunc
	pendingAsk := len(s.askPending) > 0
	// A fresh objective never waited on the old goal's dependents: void any
	// pending hold so the settle cannot suppress this goal's kick with it.
	s.goalDependentsHeld = false
	s.mu.Unlock()
	s.emitCurrentGoalState()
	s.goalUpdateMu.Unlock()

	if inTurn || kick == nil || pendingAsk {
		// A turn is running (its gate backs the goal), there is no way to kick an
		// idle session, or a question is genuinely pending a reply; either way
		// the caller cannot rely on an immediate start.
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
	s.goalUpdateMu.Lock()
	s.mu.Lock()
	s.getOrCreateGoalStore().Clear()
	s.goalDependentsHeld = false
	s.mu.Unlock()
	s.emitCurrentGoalState()
	s.goalUpdateMu.Unlock()
}

// GoalStatus reports the session's current /goal lifecycle state. The objective
// is persisted and projected through Meta().Goal; this positional API remains
// only for callers that need status and iteration count. ok is false when no
// goal is set.
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
// unbounded (cfg<0) or at-least-GoalTurnMaxRounds config down to
// GoalTurnMaxRounds, bounding per-goal-turn spend and reducing the likelihood of
// intra-turn compaction eroding the re-injected objective (spec §2b/C13). A bare
// min(cfg, cap) is wrong because cfg<0 means "unbounded", not "smallest".
func goalRoundCap(cfg int, kind EntryKind) int {
	if goalControlsRoundCap(cfg, kind) {
		return goal.GoalTurnMaxRounds
	}
	return cfg
}

func goalControlsRoundCap(cfg int, kind EntryKind) bool {
	return kind == EntryContinuation && (cfg < 0 || cfg >= goal.GoalTurnMaxRounds)
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

// hasWakePendingDependents reports whether the session owns work that is
// guaranteed to deliver a future wake: a delegate in a non-terminal phase
// (running/settling/stopping — a report or terminal notification is coming,
// turn/drive budgets bound the run, and the quiet watchdog covers a silent
// runner) or a supervised running job (progress-interval watch: periodic
// ticks even if the job never exits). Dependents that can never wake the
// session on their own do NOT count: idle delegates awaiting delegate_send,
// closed delegates, detached processes (kept out of the job manager), and
// unwatched running jobs (no job watchdog — holding on one would park the
// goal forever with the breaker unreachable). The goal gate's no-progress
// hold keys on this: waiting on a guaranteed wake is not stalling, but
// holding on a dependent that will never deliver would strand the goal the
// other way.
//
// Known residual windows, accepted and documented rather than closed: a
// dependent that terminates between this read and the gate's fold (or the
// settle's re-check) can cost one no-progress fold — with a pre-loaded streak
// that can fire the breaker one turn before the already-armed report lands
// (the report still arrives and the block is now transcript-visible); and a
// wedged delegate yields at most one quiet-watchdog wake per quiet stretch,
// after which the hold parks the goal on a supervised-but-silent dependent
// until the delegate's own turn budget (or a hung-tool timeout) forces its
// terminal notification — the wedge is surfaced to the user and model on
// that one wake, and repeat watchdog cadence is a delegate-supervision
// follow-up, not this gate's. The hold also requires both serve-loop
// callbacks to be wired, which only root daemon sessions are: delegate-child
// sessions are out of scope (see the wiring note in armGoalContinuation).
//
// It takes delegate-controller and job-manager locks and must never be called
// with goalUpdateMu or s.mu held (see the askPending lock-discipline comment
// in SetGoal): callers compute it before taking either. Callers should also
// keep it lazy — the delegate side is an O(len(c.durable)) early-exit scan
// under the controller lock — computing it only on paths that can use the
// answer.
func (s *Session) hasWakePendingDependents() bool {
	if s == nil {
		return false
	}
	if s.jobManager.hasSupervisedRunningJobs() {
		return true
	}
	return s.delegateController.hasWakePendingDelegateFor(s)
}

// armGoalContinuation runs in the drain-loop gate (on the turn goroutine) after a
// goal continuation turn completes. progressed reports whether the just-finished
// turn made a mutating tool call. It folds that signal into the goal under the
// goal lock and decides whether to issue another continuation.
//
// It returns (renderedPrompt, true) to continue, or ("", false) when there is
// nothing to drive right now: no goal is set, the goal is terminal (the gate
// owns two stop paths — a model-declared terminal status and the no-progress
// breaker — and emits exactly one EventGoalEnded on each so the user is told
// why the loop stopped), or the wake-pending hold parked the goal until a
// dependent's notification lands. There is no iteration cap: a goal that keeps
// making progress runs until it is completed or the no-progress breaker fires.
func (s *Session) armGoalContinuation(progressed, wasContinuation bool) (string, bool) {
	// Lazy, and computed before goalUpdateMu: the query takes delegate-controller
	// and job-manager locks, which must never be held under the goal serializer.
	// Two short-circuits keep hot paths free of it: only a non-progressed
	// continuation can hold at all, and the hold can only fire when both
	// serve-loop callbacks are wired (bridgeSession installs them together).
	// Unwired sessions never pay for the dependent scan they cannot use: a
	// one-shot `evener run` (the drain's defer chain is the only driver) and —
	// see the wiring note on the hold branch below — delegate-child sessions,
	// whose goals stay covered by the documented child-session gap follow-up.
	var wakePending bool
	if wasContinuation && !progressed {
		s.mu.Lock()
		wired := s.kickFunc != nil && s.notifyFunc != nil
		s.mu.Unlock()
		wakePending = wired && s.hasWakePendingDependents()
	}
	s.goalUpdateMu.Lock()
	store := s.getOrCreateGoalStore()
	snap, ok := store.Snapshot()
	if !ok {
		s.goalUpdateMu.Unlock()
		return "", false // no goal
	}
	if snap.Status != goal.StatusActive {
		s.goalUpdateMu.Unlock()
		// Already terminal: update_goal complete/blocked set it this turn, or it
		// finished on an earlier turn (a terminated goal lingers in the store until
		// /goal clear, and the gate runs at every turn tail). reportGoalEnded emits
		// exactly once via the store's once-gate, so the terminal report does not
		// repeat on every subsequent turn.
		s.reportGoalEnded()
		return "", false
	}
	if !wasContinuation {
		s.goalUpdateMu.Unlock()
		// A user (or other non-continuation) turn completed while a goal is active:
		// resume the goal, but do NOT fold the user's own turn into the no-progress
		// streak or the iteration count — only the goal's own continuation turns
		// count toward those (/par #4).
		return goal.Render(snap.Objective), true
	}
	if wakePending {
		// Wake-pending hold: the turn made no mutating call (wakePending is
		// computed only for non-progressed continuations), but owned work is
		// guaranteed to wake the session (a running delegate's report/terminal
		// notification, a supervised background job's progress tick or terminal
		// notification). Waiting on a guaranteed wake is not stalling, so the
		// no-progress fold is skipped — three polling turns must not block a
		// goal whose next phase starts when the last dependent reports — and no
		// further continuation is armed: the notification machinery drives the
		// session, and that turn's settle re-arms the goal. The settle flag
		// makes the held decision visible to settleGoalOnIdle so it does not
		// immediately re-kick past the same wait. wakePending implies the
		// kick+notify pair is wired (the gate's short-circuit), so the held
		// decision always has a live resume path. Wiring note: only root daemon
		// sessions have both callbacks (serve.go's bridgeSession). Delegate
		// children get SetNotifyFunc but never SetKickFunc — their restored
		// active goals self-drive through the drain's inline continuation path,
		// where the hold cannot apply; that gap is the documented child-session
		// follow-up, not this gate's.
		s.mu.Lock()
		s.goalDependentsHeld = true
		s.mu.Unlock()
		s.goalUpdateMu.Unlock()
		return "", false
	}
	snap, stillActive := store.RecordContinuation(progressed, s.sclock().Now())
	s.emitGoalUpdated(snap)
	s.goalUpdateMu.Unlock()
	if !stillActive {
		// The no-progress breaker fired this turn. Record it as a steering turn
		// (user-role, the channel the goal engine already speaks on): durable in
		// the transcript and projected on reload, without becoming a mid-history
		// system-role message provider adapters would fold into persistent
		// instructions. Then persist the terminal transition: it happens after
		// processOneInput's defer-save, so without the save a blocked goal would
		// be saved as still-active and resume on restart (/par A4).
		s.appendTurn(schema.TurnSteering, llm.User(fmt.Sprintf(
			"[goal-no-progress-breaker] Goal blocked: no mutating progress in %d consecutive goal-continuation turns. The goal engine has stopped driving the objective; it resumes only via /goal clear or a new /goal.",
			snap.NoProgressStreak)))
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
// Arm, don't kick, while a question is pending (spec §5.3): goalInTurn still
// clears — the turn genuinely finished — but the prompt is computed only when
// the pending-ask set is empty (len(s.askPending) == 0, read directly under
// the held lock — not via askPendingCount()/HasPendingAsk(), which self-lock
// s.mu and would deadlock here), so an active goal is left armed in the store
// instead of being kicked past the user's unanswered ask. The normal resume
// fold (armGoalContinuation's non-continuation branch, folded at the reply
// turn's own drain tail) picks it up once the reply resolves it. The hold is
// keyed on the pending-ask set, NOT on SessionAwaiting: under
// attention-status-model v5, SessionAwaiting also covers a plain
// output-producing rest with nothing pending, where the goal must kick
// normally.
//
// It reports whether it kicked, so the settle-state upgrade knows autonomy is
// in flight (attention-status-model v5: a kicked goal suppresses awaiting —
// suppressor condition 3 of the idle→awaiting upgrade).
func (s *Session) settleGoalOnIdle() bool {
	// Probe the hold flag first so the delegate-tree snapshot is paid only when
	// a hold actually stands. The flag can only transition true→false between
	// the probe and the main section (the gate that sets it runs on this same
	// goroutine; SetGoal/ClearGoal only clear), so a false probe never misses a
	// hold — and a true probe is re-read authoritatively below.
	s.mu.Lock()
	probe := s.goalDependentsHeld
	s.mu.Unlock()
	// Computed outside s.mu: the query takes delegate-controller and
	// job-manager locks, which must never be acquired under s.mu.
	wakePending := probe && s.hasWakePendingDependents()
	s.mu.Lock()
	s.goalInTurn = false
	kick := s.kickFunc
	pendingAsk := len(s.askPending) > 0
	// Consume a pending dependents hold: suppress the kick only while the
	// dependents the gate waited on still pend. Recomputed now (not trusted
	// from the gate's read) so a stale hold — the last delegate terminated and
	// its notification is already queued — cannot strand the goal.
	held := s.goalDependentsHeld
	s.goalDependentsHeld = false
	var prompt string
	if kick != nil && !pendingAsk && (!held || !wakePending) {
		if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok && snap.Status == goal.StatusActive {
			prompt = goal.Render(snap.Objective)
		}
	}
	s.mu.Unlock()
	if prompt != "" {
		kick(prompt)
		return true
	}
	return false
}

// terminateGoalOnError transitions an active goal to blocked and emits its
// terminal report when a turn ends in a system cancellation or error. It is a
// no-op when there is no active goal, and — critically — when err is a genuine
// user /interrupt: the goal stays active and resumes after the next completed
// turn (spec §6). The discriminator is the interruptDrainConfig bool, not the
// WithQueuedInputDrainOnInterrupt marker (which is installed on every turn ctx and
// so discriminates nothing); a DeadlineExceeded or provider error routes the goal
// to blocked while the session remains available for later input.
//
// Classifying is all this wants, so it asks the classifier. Building the drain
// context is what announces a new turn to the host, and no turn is starting
// here — a goal decision must not tell the daemon a turn began.
func (s *Session) terminateGoalOnError(ctx context.Context, err error) {
	store := s.getOrCreateGoalStore()
	if snap, ok := store.Snapshot(); !ok || snap.Status != goal.StatusActive {
		return
	}
	if _, isUserInterrupt := interruptDrainConfig(ctx, err); isUserInterrupt {
		return // genuine user interrupt: leave the goal active
	}
	if goalRootShutdown(ctx, err) {
		// The root/daemon context is shutting down (e.g. a restart or deploy), not a
		// genuine turn failure: leave the goal active so it resumes on the next load
		// rather than being permanently blocked. This only matters now that A4
		// persists terminal transitions (/par B3, surfaced once blocks are saved).
		return
	}
	if _, changed := s.setGoalTerminal(goal.StatusBlocked, err.Error()); changed {
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

// goalStateData converts the internal goal snapshot into the public event
// payload shared by every goal mutation boundary.
func goalStateData(snap goal.Snapshot) events.GoalStateData {
	return events.GoalStateData{
		Objective:  snap.Objective,
		Status:     string(snap.Status),
		Iterations: snap.Iterations,
	}
}

// emitGoalUpdated publishes one committed non-clear goal transition. Callers
// invoke it only after the store mutation has released its own mutex.
func (s *Session) emitGoalUpdated(snap goal.Snapshot) {
	state := goalStateData(snap)
	s.emit(events.EventGoalUpdated, events.GoalUpdatedData{Goal: &state})
}

// emitCurrentGoalState snapshots and publishes the current store state. A
// missing snapshot deliberately carries a nil Goal so JSON encodes goal:null.
// This helper must never be called while Session.mu is held because emit reads
// session provenance through the same mutex.
func (s *Session) emitCurrentGoalState() {
	if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok {
		s.emitGoalUpdated(snap)
		return
	}
	s.emit(events.EventGoalUpdated, events.GoalUpdatedData{Goal: nil})
}

// setGoalTerminal commits and announces a terminal transition as one ordered
// unit. The goal store releases its own mutex before emission, and this helper
// never takes Session.mu.
func (s *Session) setGoalTerminal(status goal.Status, reason string) (goal.Snapshot, bool) {
	s.goalUpdateMu.Lock()
	defer s.goalUpdateMu.Unlock()
	store := s.getOrCreateGoalStore()
	if !store.SetTerminal(status, reason, s.sclock().Now()) {
		return goal.Snapshot{}, false
	}
	snap, _ := store.Snapshot()
	s.emitGoalUpdated(snap)
	return snap, true
}

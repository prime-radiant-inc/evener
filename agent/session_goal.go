package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	// Retarget voids waits (the store just cleared them) and disarms their
	// timer (spec section 3): no expired wait may re-trigger after the
	// retarget. The terminal latch and delivered set belong to the old
	// objective — a superseded claimed wake drives a single no-op evaluation
	// on the current objective, never the old one.
	s.stopGoalWaitTimerLocked()
	s.goalTerminalPending = false
	s.goalWakeDelivered = nil
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
	// No goal, no notice, wake dropped (spec section 3): disarm the wait
	// timer so a stale fire cannot claim after the clear, and drop the latch
	// and delivered set with the goal they belonged to.
	s.stopGoalWaitTimerLocked()
	s.goalTerminalPending = false
	s.goalWakeDelivered = nil
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

// goalWaitNoticePrefix is the honest-loss notice frame (spec section 2):
// every lost wait produces an exactly-once notice naming the cause, delivered
// at the next turn tail - never a silent strand, never a terminal lie.
// Non-rule-5 losses pair the notice with a re-drive; rule-5 losses carry it
// on the terminal report.
const goalWaitNoticePrefix = "[goal-wait-lost]"

// goalWaitWakeTrailerPrefix frames the wait-attributable wake turn (spec
// section 2): one resume turn per claim batch carrying (wait_id, trigger
// excerpt, fired_at), coalesced when several waits fire on one tick. The
// trailer rides the continuation prompt so the model sees which wait fired
// and why it was woken; the turn's first duty is a re-validation read (event
// is a hint, state is re-read).
const goalWaitWakeTrailerPrefix = "[goal-wait-wake]"

// boundsBreached reports whether any spec section-1 rule-2/3 spend bound is
// exceeded for full at now: maxContinuations consumed, maxParkedTotal
// exhausted, or the wall-clock deadline passed. The terminal-pending latch
// consults this so the wake turn still runs while a budget is also exceeded,
// with enforcement deferred to the next gate (no starvation by flapping
// predicates).
func boundsBreached(full goal.GoalSnapshot, now time.Time) bool {
	if full.Budgets.MaxContinuations > 0 && full.Budgets.UsedContinuations >= full.Budgets.MaxContinuations {
		return true
	}
	if full.Budgets.MaxParkedTotal > 0 && full.Budgets.ParkedTotal >= full.Budgets.MaxParkedTotal {
		return true
	}
	return !full.Budgets.Deadline.IsZero() && !now.Before(full.Budgets.Deadline)
}

// armGoalContinuation runs in the drain-loop gate (on the turn goroutine) after a
// goal continuation turn completes. progressed reports whether the just-finished
// turn made a mutating tool call. It folds that signal into the goal under the
// goal lock and decides whether to issue another continuation.
//
// Slice-1 gate (spec sections 1, 3): pre-reads (dependent wake predicate,
// sclock instant) run before goalUpdateMu/s.mu; the atomic claim step converts
// expired until_time leases into pendingWake before the pure DecideGoalStep
// table; the mutator commits under goalUpdateMu; kicks fire outside all locks.
// Waiting parks (skips every fold, arms nothing - only the coalesced wait
// timer); undelivered pendingWake drives exactly one wake turn; budget,
// deadline, and lost-wait rules block with their distinct verdicts. The
// interim v1 judge (wake-pending hold + mutation breaker) stays armed for
// non-parked loops per spec section 9; wait-attributable turns bypass
// RecordContinuation entirely (zero stall accrual while parked).
//
// It returns (renderedPrompt, true) to continue, or ("", false) when there is
// nothing to drive right now: no goal is set, the goal is terminal (the gate
// owns the model-declared terminal stop path plus the spec section-1 rule-2/3/5
// budget/deadline/loss blocks and the interim no-progress breaker - and emits
// exactly one EventGoalEnded on each so the user is told why the loop
// stopped), or the goal parked until a wait's kick lands. There is no
// iteration cap: a goal that keeps making progress runs until it is completed
// or a stop rule fires.
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
	// sclock stamp for every wait/claim/budget read below (spec section 3:
	// time comes from s.sclock() only). Read once, before any lock, so the
	// gate's expiry/budget reads share one instant.
	now := s.sclock().Now()
	s.goalUpdateMu.Lock()
	store := s.getOrCreateGoalStore()
	snap, ok := store.Snapshot()
	if !ok {
		s.goalUpdateMu.Unlock()
		return "", false // no goal
	}
	if snap.Status != goal.StatusActive && snap.Status != goal.StatusWaiting {
		s.goalUpdateMu.Unlock()
		// Already terminal: update_goal complete/blocked set it this turn, or it
		// finished on an earlier turn (a terminated goal lingers in the store until
		// /goal clear, and the gate runs at every turn tail). reportGoalEnded emits
		// exactly once via the store's once-gate, so the terminal report does not
		// repeat on every subsequent turn.
		s.reportGoalEnded()
		return "", false
	}
	// Full-shape read for the pure table (spec section 1: snapshot x
	// turnOutcome x predicateTruth x pendingWake x markers x now).
	// Claim-before-decide: the expiry claims below run under this same
	// goalUpdateMu hold, so rule 1 always observes claimable fires (no
	// gate-wins-before-claim interleave).
	full, ok := store.GoalSnapshot()
	if !ok {
		s.goalUpdateMu.Unlock()
		return "", false // cleared between the reads
	}
	// Wake-turn tail fold (spec section 1: pendingWake clears in the same
	// commit as the wake turn's tail fold, never at delivery): when the
	// just-finished continuation was a wake turn - every backlog entry was
	// marked delivered when its kick went out - consume the backlog now so a
	// crash between kick and fold is the only path that re-drives. Fresh
	// entries claimed mid-turn (never marked) are NOT consumed: they drive
	// their own wake below. Wait-attributable turns bypass RecordContinuation
	// entirely (spec section 9 slice-1 interim).
	wakeTail := false
	if wasContinuation && len(full.PendingWake) > 0 {
		s.mu.Lock()
		var consumed []string
		for _, p := range full.PendingWake {
			if s.goalWakeDelivered[p.WaitID] {
				consumed = append(consumed, p.WaitID)
			}
		}
		s.mu.Unlock()
		if len(consumed) > 0 {
			store.DrainPendingWakeIDs(consumed, now)
			s.mu.Lock()
			for _, id := range consumed {
				delete(s.goalWakeDelivered, id)
			}
			s.mu.Unlock()
			full, ok = store.GoalSnapshot()
			if !ok {
				s.goalUpdateMu.Unlock()
				return "", false
			}
			wakeTail = true
		}
	}
	// Atomic fire step (spec section 3): convert expired until_time leases
	// into persisted pendingWake claims before deciding. Each claim is the
	// claimWaitFireLocked analogue - ClaimFire removes the lease from waits[]
	// into pendingWake atomically, so a repeat claim finds no live lease and
	// returns false (fired_epoch dedupe: exactly one kick per fire). Slice 1
	// claims timer expiry only; notification/attach-scan claims arrive with
	// later tasks. Already-delivered wait_ids (a timer re-fire after a
	// delivered claim) are skipped - the second claim would duplicate one
	// fire's wake.
	s.mu.Lock()
	delivered := s.goalWakeDelivered
	latched := s.goalTerminalPending
	s.mu.Unlock()
	var claimed []goal.PendingWake
	for _, w := range full.Waits {
		if w.Lease.Kind != goal.WaitUntilTime || !w.Live() {
			continue
		}
		if !now.Before(w.Lease.Deadline) {
			if delivered[w.Lease.WaitID] {
				continue
			}
			if entry, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: "+w.Lease.Label, now); ok {
				claimed = append(claimed, entry)
			}
		}
	}
	if len(claimed) > 0 {
		full, ok = store.GoalSnapshot()
		if !ok {
			s.goalUpdateMu.Unlock()
			return "", false
		}
	}
	// Predicate truth is the section-3-top pre-read seam: evaluated outside
	// the pure function (slice 1: timer-expiry truth only - live until_time
	// leases read false, expired ones were just claimed above; richer
	// substrate reads wire in with the notification path). Positional in
	// Waits order.
	truth := make([]bool, len(full.Waits))
	markers := goal.AdvancementMarkers{AdvancedSinceLoss: full.AdvancementSinceLoss}
	outcome := goal.TurnOutcome{Mutated: progressed}
	// Terminal-pending latch (spec section 1 R7 M-I1): while latched, rules
	// 2/3 (plus the section-5 re-park graduation - a later task) evaluate
	// before rule 1. The latch stays hidden from the pure table: a
	// bounds-only pre-decide with pending AND the persisted backlog withheld
	// decides; on breach the latched turn blocks and the fresh wakes drop
	// with the honest loss notice, otherwise the latch clears and the real
	// decide sees the wakes.
	decidePending := claimed
	if latched && len(full.PendingWake) == 0 && len(claimed) == 0 && !boundsBreached(full, now) {
		// Latched but nothing fresh to gate and bounds clean - clear and
		// decide normally. (Bounds breached with no fresh wakes still
		// pre-decides below so the block path - not a plain drive - runs.)
		s.mu.Lock()
		s.goalTerminalPending = false
		s.mu.Unlock()
		latched = false
	}
	if latched {
		gated := full
		gated.PendingWake = nil
		if step, verdict := goal.DecideGoalStep(gated, outcome, truth, nil, markers, now); step == goal.StepBlock {
			s.goalUpdateMu.Unlock()
			s.dropLatchedWakes(full, verdict, now)
			return s.blockGoalFromGate(verdict)
		}
		s.mu.Lock()
		s.goalTerminalPending = false
		s.mu.Unlock()
	}
	step, verdict := goal.DecideGoalStep(full, outcome, truth, decidePending, markers, now)
	switch step {
	case goal.StepPark:
		// Waiting branch: skip every fold, arm nothing (spec section 1 -
		// only the coalesced wait timer). Preserve the interim wake-pending
		// hold's settle flag so the settle does not re-kick past the same
		// wait, and keep the legacy hold working when no registry wait
		// exists (its dependents still guarantee a future wake).
		if wakePending || len(full.Waits) > 0 {
			s.mu.Lock()
			s.goalDependentsHeld = true
			s.mu.Unlock()
		}
		s.goalUpdateMu.Unlock()
		s.armGoalWaitTimer()
		return "", false
	case goal.StepDrive:
		if len(full.PendingWake) > 0 || len(claimed) > 0 {
			// Fired/expiry path: an undelivered wake batch drives exactly
			// one resume turn carrying all coalesced triggers (spec section
			// 2: one combined wake turn; the continuation charge accrues at
			// the wake turn's own tail fold via the interim judge).
			// Terminal-flagged when a budget is also exceeded: latch so the
			// NEXT gate enforces bounds before rule 1 (no starvation by
			// flapping predicates).
			if boundsBreached(full, now) {
				s.mu.Lock()
				s.goalTerminalPending = true
				s.mu.Unlock()
			}
			prompt := s.renderGoalWakePrompt(full)
			var ids []string
			for _, p := range full.PendingWake {
				ids = append(ids, p.WaitID)
			}
			s.markGoalWakesDelivered(ids)
			s.goalUpdateMu.Unlock()
			// A non-continuation turn completed while wakes stood
			// undelivered (e.g. a notification turn landing between claim
			// and kick): the wake turn is due - drive it rather than the
			// plain objective.
			return prompt, true
		}
		// Plain drive: fall through to the interim fold below (active goal,
		// no wakes). A waiting status with no live waits and no backlog is
		// unreachable (the store returns to active on the last claim/cancel),
		// but drive it rather than strand it.
	case goal.StepBlock:
		s.goalUpdateMu.Unlock()
		return s.blockGoalFromGate(verdict)
	default:
		// StepNudge arrives with the ledger task; slice 1 never returns it
		// (the pure table documents it as unreturned until then). Treat as a
		// plain drive - never park, never block on an unknown step.
	}
	if !wasContinuation {
		s.goalUpdateMu.Unlock()
		// A user (or other non-continuation) turn completed while a goal is active:
		// resume the goal, but do NOT fold the user's own turn into the no-progress
		// streak or the iteration count — only the goal's own continuation turns
		// count toward those (/par #4).
		return goal.Render(snap.Objective), true
	}
	if wakeTail {
		// The just-finished turn was the wake turn itself: its backlog is
		// consumed above and the turn was wait-attributable, so bypass the
		// stall fold and re-arm the plain objective. The follow-up turn's
		// own tail folds normally.
		s.goalUpdateMu.Unlock()
		return goal.Render(full.Objective), true
	}
	if wakePending && len(full.Waits) == 0 && len(full.PendingWake) == 0 {
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

// renderGoalWakePrompt renders the wait-attributable wake turn prompt (spec
// section 2): the current objective plus one trailer frame carrying every
// coalesced (wait_id, trigger excerpt, fired_at) triple. Pure: no locks.
func (s *Session) renderGoalWakePrompt(full goal.GoalSnapshot) string {
	base := goal.Render(full.Objective)
	if len(full.PendingWake) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n" + goalWaitWakeTrailerPrefix + " The goal was parked waiting and the following wait(s) fired; evaluate the trigger state first (re-read it - the event is a hint, not proof), then continue the objective:")
	for _, p := range full.PendingWake {
		fmt.Fprintf(&b, "\n- %s: %s (fired %s)", p.WaitID, p.Trigger, p.FiredAt.UTC().Format(time.RFC3339))
	}
	return b.String()
}

// markGoalWakesDelivered records a kicked claim batch in the delivered set
// (spec section 2 fired_epoch dedupe in session form): a timer re-fire or a
// racing notification for the same wait_ids collapses instead of re-kicking.
// Self-locking; call with no session locks held.
func (s *Session) markGoalWakesDelivered(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goalWakeDelivered == nil {
		s.goalWakeDelivered = make(map[string]bool)
	}
	for _, id := range ids {
		s.goalWakeDelivered[id] = true
	}
}

// dropLatchedWakes consumes a terminal-latched turn's fresh wakes (spec
// section 1 R7 M-I1): the bound was breached while latched, so the wakes
// drop with the honest loss notice instead of driving. Call with no locks
// held; the block itself runs in blockGoalFromGate.
func (s *Session) dropLatchedWakes(full goal.GoalSnapshot, verdict string, now time.Time) {
	var ids []string
	for _, p := range full.PendingWake {
		ids = append(ids, p.WaitID)
	}
	s.markGoalWakesDelivered(ids)
	s.goalUpdateMu.Lock()
	drained := s.getOrCreateGoalStore().DrainPendingWake(now)
	s.goalUpdateMu.Unlock()
	s.mu.Lock()
	s.goalTerminalPending = false
	s.mu.Unlock()
	if len(drained) > 0 {
		s.appendTurn(schema.TurnSteering, llm.User(fmt.Sprintf(
			"%s Dropped %d coalesced wake(s) (%s): the goal already spent its bound (%s). Re-arm the wait or proceed without it.",
			goalWaitNoticePrefix, len(drained), strings.Join(ids, ", "), verdict)))
	}
}

// blockGoalFromGate commits a spec section-1 rule-2/3/5 stop (budget
// exhausted, deadline exceeded, waiting lost) as one ordered unit: terminal
// transition with the distinct verdict, timer disarm, latch/delivered reset,
// one steering note, exactly-once terminal report, persist. Call with no
// locks held; kicks never apply to a stop path.
func (s *Session) blockGoalFromGate(verdict string) (string, bool) {
	if _, changed := s.setGoalTerminal(goal.StatusBlocked, verdict); changed {
		s.emitCurrentGoalState()
	}
	s.stopGoalWaitTimer()
	s.mu.Lock()
	s.goalTerminalPending = false
	s.mu.Unlock()
	var note string
	switch verdict {
	case goal.VerdictBudgetExhausted:
		note = "[goal-budget] Goal blocked: budget exhausted (continuation or parked-time bound spent). Renew with /goal resume --extend or retarget with /goal."
	case goal.VerdictDeadlineExceeded:
		note = "[goal-deadline] Goal blocked: deadline exceeded (wall-clock budget spent, including parked time). Renew with /goal resume --extend or retarget with /goal."
	default:
		note = fmt.Sprintf("[goal-wait] Goal blocked: %s. Re-arm the wait or proceed without it; /goal resume re-drives.", verdict)
	}
	s.appendTurn(schema.TurnSteering, llm.User(note))
	s.reportGoalEnded()
	s.maybeAutoSave()
	return "", false
}

// claimGoalWaitExpiredWaits converts expired until_time leases into persisted
// pendingWake claims (the claimWaitFireLocked analogue at session scope) and
// reports the claims with the owning objective. It sequences s.mu and
// goalUpdateMu sections without nesting either (the established SetGoal order
// is goalUpdateMu-then-s.mu; this helper never holds one while taking the
// other). ClaimFire's atomicity is the exactly-once guarantee: concurrent
// claimants race on the lease, exactly one wins. Timer-expiry claims only in
// slice 1; notification/attach-scan claims arrive with later tasks.
func (s *Session) claimGoalWaitExpiredWaits(now time.Time) ([]goal.PendingWake, string) {
	s.mu.Lock()
	delivered := make(map[string]bool, len(s.goalWakeDelivered))
	for id := range s.goalWakeDelivered {
		delivered[id] = true
	}
	s.mu.Unlock()
	s.goalUpdateMu.Lock()
	defer s.goalUpdateMu.Unlock()
	store := s.getOrCreateGoalStore()
	full, ok := store.GoalSnapshot()
	if !ok {
		return nil, ""
	}
	var claimed []goal.PendingWake
	for _, w := range full.Waits {
		if w.Lease.Kind != goal.WaitUntilTime || !w.Live() {
			continue
		}
		if now.Before(w.Lease.Deadline) {
			continue
		}
		if delivered[w.Lease.WaitID] {
			continue
		}
		if entry, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: "+w.Lease.Label, now); ok {
			claimed = append(claimed, entry)
		}
	}
	return claimed, full.Objective
}

// armGoalWaitTimer arms the single coalesced wait timer (spec section 2) to
// the earliest live wait deadline - the slice-1 subset of the four-way min
// (poll/parked-total/deadline projection arrives with persistence). No live
// waits, or an already-expired earliest deadline (the claim path owns it),
// leaves the timer disarmed. Re-arming strands the previous callback via the
// generation counter. Lock order: goalUpdateMu, then s.mu (the SetGoal order).
func (s *Session) armGoalWaitTimer() {
	s.goalUpdateMu.Lock()
	full, ok := s.getOrCreateGoalStore().GoalSnapshot()
	s.goalUpdateMu.Unlock()
	var earliest time.Time
	if ok && full.Status == goal.StatusWaiting {
		for _, w := range full.Waits {
			if !w.Live() {
				continue
			}
			if earliest.IsZero() || w.Lease.Deadline.Before(earliest) {
				earliest = w.Lease.Deadline
			}
		}
	}
	now := s.sclock().Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.goalWaitTimer; t != nil {
		t.Stop()
		s.goalWaitTimer = nil
	}
	if earliest.IsZero() || !now.Before(earliest) {
		// Nothing to wait on, or the earliest deadline already passed (the
		// claim path converts it on the next gate/settle/timer pass) -
		// disarm, stranding any in-flight callback via the generation bump.
		s.goalWaitTimerGen++
		return
	}
	s.goalWaitTimerGen++
	gen := s.goalWaitTimerGen
	s.goalWaitTimer = s.sclock().AfterFunc(earliest.Sub(now), func() { s.fireGoalWaitTimer(gen) })
}

// stopGoalWaitTimerLocked disarms the coalesced wait timer. Caller must hold
// s.mu. The generation bump strands an in-flight callback, which drops on its
// generation check without claiming (spec section 2 disarm race).
func (s *Session) stopGoalWaitTimerLocked() {
	if t := s.goalWaitTimer; t != nil {
		t.Stop()
		s.goalWaitTimer = nil
	}
	s.goalWaitTimerGen++
}

// stopGoalWaitTimer disarms the coalesced wait timer. Self-locking; call with
// no session locks held (close paths, block path).
func (s *Session) stopGoalWaitTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopGoalWaitTimerLocked()
}

// fireGoalWaitTimer is the coalesced wait timer's callback (spec sections
// 2-3): claim-then-wake across the session lock order, with the kick outside
// all locks. A superseded callback (generation mismatch from a re-arm/disarm
// race) drops without claiming. Retarget/clear detection rides the objective:
// a clear drops the wake silently (spec section 3 - no goal, no notice); a
// retarget between claim and kick routes the honest loss notice (not the
// stale trigger) to the current objective while the stale excerpts drop. With
// no kick wired the claim persists and the next gate drives the wake inline
// (one-shot `evener run`).
func (s *Session) fireGoalWaitTimer(gen uint64) {
	s.mu.Lock()
	if s.closing || gen != s.goalWaitTimerGen {
		s.mu.Unlock()
		return
	}
	s.goalWaitTimer = nil
	s.mu.Unlock()
	now := s.sclock().Now()
	claimed, objective := s.claimGoalWaitExpiredWaits(now)
	if len(claimed) == 0 {
		s.armGoalWaitTimer()
		return
	}
	// Read kick/ask state AFTER the claim (never across it): the callback
	// runs on the clock's goroutine, and the claim path plus a concurrent
	// gate/settle may interleave - holding s.mu across the claim would
	// deadlock against a gate holding goalUpdateMu and wanting s.mu.
	s.mu.Lock()
	kick := s.kickFunc
	pendingAsk := len(s.askPending) > 0
	s.mu.Unlock()
	if pendingAsk {
		// Arm, don't kick past an unanswered ask: the claim persists and
		// the reply turn's gate drives the wake.
		s.armGoalWaitTimer()
		return
	}
	s.goalUpdateMu.Lock()
	full, ok := s.getOrCreateGoalStore().GoalSnapshot()
	s.goalUpdateMu.Unlock()
	if !ok {
		// Cleared between claim and kick: drop silently (spec section 3 -
		// no goal, no notice).
		s.armGoalWaitTimer()
		return
	}
	if full.Objective != objective {
		// Retarget won between claim and kick (spec section 3
		// superseded/drop): the store already wiped the stale leases and
		// the retarget cleared the backlog path - drop the stale trigger
		// excerpts (never drive the old objective) and leave the honest
		// loss notice on the current objective.
		s.appendTurn(schema.TurnSteering, llm.User(fmt.Sprintf(
			"%s The waited event fired but the goal was retargeted before the wake ran; the stale trigger was dropped. Re-arm the wait or proceed without it.",
			goalWaitNoticePrefix)))
		s.armGoalWaitTimer()
		return
	}
	var ids []string
	for _, p := range full.PendingWake {
		ids = append(ids, p.WaitID)
	}
	s.markGoalWakesDelivered(ids)
	prompt := s.renderGoalWakePrompt(full)
	s.armGoalWaitTimer()
	if kick == nil {
		return
	}
	kick(prompt)
}

// CancelGoalWait removes one live lease by wait_id (spec section 7): the live
// lease leaves the registry; an already-claimed pendingWake entry still
// drives once with the cancellation noted - cancel never swallows a consumed
// fire. The coalesced timer re-arms to the next deadline (or disarms when no
// live leases remain). Reports whether a live lease was removed.
func (s *Session) CancelGoalWait(waitID string) bool {
	now := s.sclock().Now()
	s.goalUpdateMu.Lock()
	removed := s.getOrCreateGoalStore().CancelWait(waitID, now)
	s.goalUpdateMu.Unlock()
	if removed {
		s.armGoalWaitTimer()
	}
	return removed
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
	// Slice-1 wait re-check (spec section 3: settle re-checks leases live, the
	// stale-hold philosophy): claim expired until_time leases now — before the
	// s.mu section — so a stale park whose deadline passed between the gate
	// and the settle kicks exactly once via the same claim path. With no kick
	// wired the claim persists and the next gate drives the wake inline.
	now := s.sclock().Now()
	claimed, _ := s.claimGoalWaitExpiredWaits(now)
	// Snapshot BEFORE the s.mu section below: getOrCreateGoalStore's Once is
	// lock-free, but Snapshot takes the store mutex — and the section already
	// holds s.mu while the gate path may hold goalUpdateMu and want s.mu
	// (the SetGoal goalUpdateMu-then-s.mu order). Reading here keeps the
	// section to s.mu alone. The full-shape re-read for the wake prompt uses
	// the same split: goalUpdateMu section first, s.mu section after.
	preSnap, preHasGoal := s.getOrCreateGoalStore().Snapshot()
	preParked := preHasGoal && preSnap.Status == goal.StatusWaiting
	var wakeFull goal.GoalSnapshot
	var wakeOK bool
	var wakeIDs []string
	var wakePrompt string
	if len(claimed) > 0 {
		s.goalUpdateMu.Lock()
		wakeFull, wakeOK = s.getOrCreateGoalStore().GoalSnapshot()
		s.goalUpdateMu.Unlock()
		if wakeOK {
			for _, p := range wakeFull.PendingWake {
				wakeIDs = append(wakeIDs, p.WaitID)
			}
			wakePrompt = s.renderGoalWakePrompt(wakeFull)
		}
	}
	// Undelivered backlog suppresses a repeat settle kick (spec section 2
	// exactly-once): the first settle's kick already scheduled the wake turn;
	// kicking again would double-drive one fire. Read before the s.mu section
	// (store mutex only, never nested under s.mu).
	// Note the status subtlety: claiming the last live lease returns the goal
	// to active, so the check keys on the backlog itself - not on waiting -
	// or a post-claim active-with-backlog settle would re-kick.
	backlogPending := false
	if len(claimed) == 0 && preHasGoal && (preParked || preSnap.Status == goal.StatusActive) {
		s.goalUpdateMu.Lock()
		if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok && len(full.PendingWake) > 0 {
			backlogPending = true
		}
		s.goalUpdateMu.Unlock()
	}
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
	suppressHold := held && wakePending
	// A parked goal holds its settle kick unless this settle claimed a fire:
	// the wait's own kick path (timer callback, or the gate's wake drive)
	// owns the resume, so the settle must not double-kick past the same wait.
	// The exception is exactly-one kick for a stale park: a claim landed here
	// means the wait already fired, so the wake turn is due now. A non-parked
	// active goal kicks normally when no interim hold suppresses it.
	// A delivered-but-unconsumed backlog (kick went out, wake turn not yet
	// folded) also suppresses: the wake is already scheduled.
	if kick != nil && !pendingAsk && !suppressHold && !backlogPending && (!preParked || len(claimed) > 0) {
		if preHasGoal && (preSnap.Status == goal.StatusActive || (preSnap.Status == goal.StatusWaiting && len(claimed) > 0)) {
			if len(claimed) > 0 {
				prompt = wakePrompt
			} else {
				prompt = goal.Render(preSnap.Objective)
			}
		}
	}
	s.mu.Unlock()
	if prompt != "" {
		// Delivered-set mark after the kick decision, outside s.mu: a timer
		// re-fire for these wait_ids collapses instead of re-kicking. (The
		// mark is idempotent, so a racing gate marking the same batch is
		// harmless - exactly-once still holds via ClaimFire's lease consume.)
		s.markGoalWakesDelivered(wakeIDs)
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

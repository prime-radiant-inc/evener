package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// SetKickFunc registers the callback an idle SetGoal uses to start the goal loop
// immediately by feeding the first continuation prompt back into the serve loop's
// input channel. The agent module must not import server, so serve.go wires this.
// Wiring the callback is also the moment a crash-restored wake becomes
// deliverable (mirroring SetNotifyFunc's pending-work flush): a restored
// pendingWake with no live waits arms no timer, so without this flush the
// wake would strand until unrelated input starts a turn whose tail settles
// it. The flush kicks at most once — the delivered set marks the batch, so
// the turn tail the kick starts suppresses any repeat.
func (s *Session) SetKickFunc(f func(prompt string)) {
	s.mu.Lock()
	s.kickFunc = f
	s.mu.Unlock()
	if f == nil {
		return
	}
	s.goalUpdateMu.Lock()
	store := s.getOrCreateGoalStore()
	full, ok := store.GoalSnapshot()
	s.goalUpdateMu.Unlock()
	if !ok || len(full.PendingWake) == 0 {
		return
	}
	// Delivered-set copy under s.mu (same shape as the backlogPending split
	// in settleGoalOnIdle): markGoalWakesDelivered mutates the live map under
	// s.mu, so iterating the live map after Unlock races the writer
	// (concurrent-map panic). The copy freezes the flush decision.
	s.mu.Lock()
	delivered := make(map[string]bool, len(s.goalWakeDelivered))
	for id := range s.goalWakeDelivered {
		delivered[id] = true
	}
	s.mu.Unlock()
	for _, p := range full.PendingWake {
		if !delivered[p.WaitID] {
			s.settleGoalOnIdle()
			return
		}
	}
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
	// retarget. The terminal latch belongs to the old objective and resets;
	// the delivered set is kept: the carried superseded batch was marked at
	// drive time (or is about to drive), and wiping it would let a racing
	// timer re-claim the same wait_ids. Claimed entries survive in the store
	// marked Superseded and drive a single no-op evaluation on the current
	// objective, never the old one. The session-level synthetic deadline id
	// is kept with them: a carried synthetic entry still drives via the
	// superseded no-op, while the new objective's own final turn claims
	// fresh against the reset store marker (the kept mark suppresses only
	// same-id lease re-claims, never the synthetic path).
	s.stopGoalWaitTimerLocked()
	s.getOrCreateGoalStore().SetTerminalPending(false, s.sclock().Now())
	s.goalTerminalPending = false
	s.goalSupersededArmed = false
	s.mu.Unlock()
	// Bump before the emit: this commit is newer than any unlock-then-emit
	// snapshot captured before it (registerGoalWait/registerGoalExpect/
	// CancelGoalWait), suppressing their stale events. The emit itself stays
	// under the held serializer (pinned by
	// TestConcurrentGoalMutationsEmitInCommittedOrder).
	s.bumpGoalEventGen()
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
	s.getOrCreateGoalStore().SetTerminalPending(false, s.sclock().Now())
	s.goalTerminalPending = false
	s.goalWakeDelivered = nil
	s.goalSupersededArmed = false
	s.mu.Unlock()
	// Bump before the emit (see SetGoal): the emit stays under the held
	// serializer per TestConcurrentGoalMutationsEmitInCommittedOrder.
	s.bumpGoalEventGen()
	s.emitCurrentGoalState()
	s.goalUpdateMu.Unlock()
}

// goalResume re-drives a terminal-blocked goal (spec §5): the store Resume
// commits ledger-reset/waits-cleared/budgets-kept/autoReparks-reset plus the
// renewal check (reject naming the exhausted budget without --extend; drive
// with --extend). From "waiting lost" the same path applies with the cause
// cleared and re-arm-or-proceed guidance appended. Resume never re-blocks
// without first consuming an explanatory turn or an explicit renewal: the
// resumed goal is active, so the next gate drives an evaluation turn.
//
// Like SetGoal it coordinates on s.mu (goalInTurn read) so a resume racing
// the gate's "clear flag + go idle" step is kicked exactly once: in-turn (or
// pending-ask) resumes return started=false for the drain-loop gate to pick
// up; idle resumes kick the first continuation prompt immediately (outside
// the lock) and return started=true. A resume error (no blocked goal, or a
// renewal rejection) drives nothing and returns started=false.
//
// Unexported: the request names the internal goal.ResumeRequest type, which
// must not leak through the agent library surface (lint-internal). External
// callers resume via GoalResumeFromWire (plain wire types); tests call this
// directly same-package.
func (s *Session) goalResume(req goal.ResumeRequest, now time.Time) (started bool, err error) {
	return s.goalResumeWithRetarget(req, "", now)
}

// goalResumeWithRetarget re-drives a terminal-blocked goal and, when
// replacement is non-empty, retargets to it atomically under the same
// goalUpdateMu hold (spec §7 "/goal resume <text>"): the resumed-then-
// retargeted objective is recovered (budgets/ledger per Resume) and
// replaced (budgets-kept per Set) before any kick renders — the serve loop
// can never consume a stale old-objective continuation first. The kick (if
// any) renders the final objective.
func (s *Session) goalResumeWithRetarget(req goal.ResumeRequest, replacement string, now time.Time) (started bool, err error) {
	store := s.getOrCreateGoalStore()
	s.goalUpdateMu.Lock()
	// Capture the pre-resume verdict before Resume clears it: a "waiting
	// lost" resume appends re-arm-or-proceed guidance (spec §5) below.
	preStop := ""
	if full, ok := store.GoalSnapshot(); ok {
		preStop = full.StopReason
	}
	_, rerr := store.Resume(req, now)
	if rerr != nil {
		s.goalUpdateMu.Unlock()
		return false, rerr
	}
	if strings.TrimSpace(replacement) != "" {
		store.Set(strings.TrimSpace(replacement), now)
	}
	// The resumed goal drives fresh: void any stale dependents hold, disarm
	// any stale wait timer (waits were cleared), drop the delivered set and
	// the terminal latch with the block they belonged to.
	s.mu.Lock()
	inTurn := s.goalInTurn
	kick := s.kickFunc
	pendingAsk := len(s.askPending) > 0
	s.goalDependentsHeld = false
	s.stopGoalWaitTimerLocked()
	s.goalTerminalPending = false
	s.goalWakeDelivered = nil
	s.goalSupersededArmed = false
	s.mu.Unlock()
	// Bump before the emit (see SetGoal): the emit stays under the held
	// serializer per TestConcurrentGoalMutationsEmitInCommittedOrder.
	s.bumpGoalEventGen()
	s.emitCurrentGoalState()
	s.goalUpdateMu.Unlock()

	if strings.HasPrefix(preStop, "waiting lost:") {
		// Waiting-lost resume guidance (spec §5): the wait is gone and will
		// not refire — re-arm it with goal_wait or proceed without it.
		s.appendTurn(schema.TurnSteering, llm.User(
			"[goal-wait] Goal resumed after "+preStop+". The lost wait will not refire: re-arm it with goal_wait or proceed without the wait."))
		s.maybeAutoSave()
	}
	if inTurn || kick == nil || pendingAsk {
		return false, nil
	}
	kick(goal.Render(s.resumedObjective()))
	return true, nil
}

// resumedObjective re-reads the current (just-resumed, active) objective for
// the idle-kick render. Call with no locks held (store methods self-lock).
func (s *Session) resumedObjective() string {
	if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok {
		return full.Objective
	}
	return ""
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

// goalWaitLossNotice frames the honest-loss notice (spec section 2): every
// lost wait produces an exactly-once notice naming the cause.
func goalWaitLossNotice(losses []string) string {
	return goalWaitNoticePrefix + " " + strings.Join(losses, "; ") + "; re-arm or proceed without the wait."
}

// goalKickState reads the idle-kick state (kick callback + pending ask) under
// s.mu. Call with no locks held.
func (s *Session) goalKickState() (kick func(prompt string), pendingAsk bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kickFunc, len(s.askPending) > 0
}

// boundsBreachedAt reports whether any spec section-1 rule-2/3 spend bound
// is exceeded for full at now: maxContinuations consumed, maxParkedTotal
// exhausted, or the wall-clock deadline passed. parkedTotal is the LIVE
// total (persisted plus the open entry→now stretch): the persisted field
// alone accrues only at segment boundaries, so a boundary read would let a
// goal sit parked past its cap between claims — callers with a live parked
// stretch pass store.ParkedTotalAt(now). The terminal-pending latch consults
// this so the wake turn still runs while a budget is also exceeded, with
// enforcement deferred to the next gate (no starvation by flapping
// predicates).
func boundsBreachedAt(full goal.GoalSnapshot, parkedTotal time.Duration, now time.Time) bool {
	if full.Budgets.MaxContinuations > 0 && full.Budgets.UsedContinuations >= full.Budgets.MaxContinuations {
		return true
	}
	if full.Budgets.MaxParkedTotal > 0 && parkedTotal >= full.Budgets.MaxParkedTotal {
		return true
	}
	return !full.Budgets.Deadline.IsZero() && !now.Before(full.Budgets.Deadline)
}

// armGoalContinuation runs in the drain-loop gate (on the turn goroutine) after a
// goal continuation turn completes. progressed reports whether the just-finished
// turn made a mutating tool call. It folds that signal into the goal under the
// goal lock and decides whether to issue another continuation.
//
// Slice-2 gate (spec sections 1, 3, 4): pre-reads (dependent wake predicate,
// sclock instant, pre-scoped state digest) run before goalUpdateMu/s.mu; the
// atomic claim step converts expired until_time leases plus terminal-child
// until_child matches into pendingWake before the pure DecideGoalStep table;
// the plain-drive path folds the turn's TurnOutcome into the ledger
// (FoldLedger via RecordContinuation — the slice-1 v1 mutation breaker is
// retired, not bypassed); the mutator commits under goalUpdateMu; kicks fire
// outside all locks. Waiting parks (skips every fold, arms nothing - only
// the coalesced wait timer); undelivered pendingWake drives exactly one wake
// turn; budget, deadline, lost-wait, and stall rules block with their
// distinct verdicts. The wake-pending hold (dependents guaranteeing a future
// wake) stays armed: a held turn skips the fold like a park. The wake turn's
// own drive folds like any other turn (spec §5: wake turns count) — only the
// wake tail (delivered batch consumed) and the superseded no-op evaluation
// bypass the fold. The notifying turn for a waited target IS the wake turn:
// no separate notification turn folds, so there is no double-turn accounting.
//
// It returns (renderedPrompt, true) to continue, or ("", false) when there is
// nothing to drive right now: no goal is set, the goal is terminal (the gate
// owns the model-declared terminal stop path plus the spec section-1 rule-2/3/5
// budget/deadline/loss/stall blocks - and emits
// exactly one EventGoalEnded on each so the user is told why the loop
// stopped), or the goal parked until a wait's kick lands. There is no
// iteration cap: a goal that keeps making progress runs until it is completed
// or a stop rule fires.
func (s *Session) armGoalContinuation(progressed, wasContinuation bool) (string, bool) {
	// Production fold input: the turn's recorded evidence plus a fresh
	// pre-scoped digest, built BEFORE goalUpdateMu (the digest's
	// controller/job-manager reads are §3-top pre-reads). Tests and scripted
	// gates that need exact outcomes call armGoalContinuationWithOutcome.
	outcome := buildGoalTurnOutcome(s.takeGoalTurnEvidence(), s.goalStateDigest())
	outcome.Mutated = outcome.Mutated || progressed
	return s.armGoalContinuationInner(progressed, wasContinuation, &outcome)
}

// armGoalContinuationInner is the gate shared by the production entry point
// (which builds the outcome from recorded evidence) and
// armGoalContinuationWithOutcome (explicit outcomes for deterministic tests).
// A nil outcome folds a bare {Mutated: progressed} turn — the pre-ledger
// equivalent the old signature carried.
func (s *Session) armGoalContinuationInner(progressed, wasContinuation bool, outcome *goal.TurnOutcome) (string, bool) {
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
	// commit as the wake turn's tail drain, never at delivery): when the
	// just-finished continuation was a wake turn - every backlog entry was
	// marked delivered when its kick went out - consume the backlog now so a
	// crash between kick and fold is the only path that re-drives. Fresh
	// entries claimed mid-turn (never marked) are NOT consumed: they drive
	// their own wake below. The wake turn already folded at drive time (spec
	// §5: wake turns count) — the tail drains and re-arms without re-folding.
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
	// Atomic fire step (spec section 3): classify every live lease and convert
	// fires into persisted pendingWake claims before deciding (spec §§1-2:
	// job/delegate retained-terminal catch-up, approval liveness, child
	// terminality, file baseline delta, HTTP match at the poll leg, plus
	// expiry for EVERY kind when its lease deadline passes). Each claim is
	// the claimWaitFireLocked analogue - ClaimFire removes the lease from
	// waits[] into pendingWake atomically, so a repeat claim finds no live
	// lease and returns false (fired_epoch dedupe: exactly one kick per
	// fire). Already-delivered wait_ids (a timer re-fire after a delivered
	// claim) are skipped - the second claim would duplicate one fire's wake.
	// Substrate reads run before goalUpdateMu/s.mu (the §3-top pre-read
	// discipline); the claim loop below holds goalUpdateMu only.
	s.mu.Lock()
	delivered := make(map[string]bool, len(s.goalWakeDelivered))
	for id := range s.goalWakeDelivered {
		delivered[id] = true
	}
	latched := s.goalTerminalPending || full.TerminalPending
	s.mu.Unlock()
	var liveForClassify []goal.Wait
	for _, w := range full.Waits {
		if w.Live() && !delivered[w.Lease.WaitID] {
			liveForClassify = append(liveForClassify, w)
		}
	}
	batch := store.ClassifyWaits(liveForClassify, now, s.childTerminalTrigger)
	claimed, gateLosses := store.ClaimClassified(batch, now)
	if len(claimed) > 0 || len(gateLosses) > 0 {
		full, ok = store.GoalSnapshot()
		if !ok {
			s.goalUpdateMu.Unlock()
			return "", false
		}
	}
	// Non-rule-5 losses (spec §2): the substrate disappeared but live waits
	// remain or advancement followed — honest notice + re-drive, never a
	// silent strand and never a terminal lie. Rule-5 losses (no live waits,
	// no advancement) flow to the pure table via markers below. A persisted
	// cause from an out-of-gate scan (restore attach-scan) with no fresh
	// same-gate loss joins the batch exactly once via TakeLossCause, so a
	// loss that landed between scans still reaches the rule-5 read — never
	// a silent strand.
	markers := goal.AdvancementMarkers{AdvancedSinceLoss: full.AdvancementSinceLoss}
	if len(gateLosses) == 0 {
		if cause, ok := store.TakeLossCause(); ok {
			gateLosses = []string{cause}
		}
	}
	if len(gateLosses) > 0 {
		if goal.HasLiveWait(full.Waits) || full.AdvancementSinceLoss {
			// Non-rule-5 loss: notice + re-drive, then consume the cause
			// the drop persisted at claim time — otherwise every later gate
			// re-consumes it into a duplicate notice. A later terminal loss
			// arrives with its own fresh cause via its own drop.
			store.TakeLossCause()
			full, ok = store.GoalSnapshot()
			if !ok {
				s.goalUpdateMu.Unlock()
				return "", false
			}
			markers = goal.AdvancementMarkers{AdvancedSinceLoss: full.AdvancementSinceLoss}
			s.goalUpdateMu.Unlock()
			s.appendTurn(schema.TurnSteering, llm.User(
				goalWaitLossNotice(gateLosses)))
			s.maybeAutoSave()
			// Re-drive below via the fresh read: fall through to a plain
			// drive of the current objective (the loss notice is delivered;
			// the evaluation turn itself runs next).
			s.goalUpdateMu.Lock()
			full, ok = store.GoalSnapshot()
			if !ok {
				s.goalUpdateMu.Unlock()
				return "", false
			}
		} else {
			markers = goal.AdvancementMarkers{LossThisTurn: true, LossCause: gateLosses[0], AdvancedSinceLoss: full.AdvancementSinceLoss}
		}
	}
	// Predicate truth is the section-3-top pre-read seam: evaluated outside
	// the pure function (claims above consumed every fireable lease, so the
	// survivors read false). Positional in Waits order.
	truth := make([]bool, len(full.Waits))
	// Deadline-expiry synthetic claim (spec §1 rule 3): past the wall-clock
	// deadline with the one-shot unspent, claim the synthetic wake through
	// the same exactly-once backlog BEFORE the pure table — rule 1 drives it
	// as the final evaluation turn carrying "deadline exceeded" + the wait
	// labels (a same-tick deadline never swallows a fired result), and the
	// marker set at claim time keeps rule 3 from looping final turns. The
	// following gate lands on rule 3 and blocks with the distinct verdict.
	// Delivered-set dedupe shares the path: a re-drive after delivery never
	// re-claims. Terminal-pending latch reads below stay ordered after it.
	s.mu.Lock()
	deadlineDelivered := delivered[goal.DeadlineWakeID]
	s.mu.Unlock()
	if !deadlineDelivered && !full.DeadlineFinalDelivered &&
		!full.Budgets.Deadline.IsZero() && !now.Before(full.Budgets.Deadline) {
		if _, ok := store.ClaimDeadlineExpiry(now); ok {
			full, ok = store.GoalSnapshot()
			if !ok {
				s.goalUpdateMu.Unlock()
				return "", false
			}
			// Refresh the claim batch the latch pre-decide withholds: the
			// synthetic entry must ride decidePending like any lease fire.
			claimed = append([]goal.PendingWake(nil), full.PendingWake...)
		}
	}
	// Fold-before-decide (spec §§1, 4): the ledger folds the just-finished
	// turn BEFORE the pure table reads the stall signals, so rules 6-7 act
	// on the post-fold summary. The caller's outcome is authoritative (tests
	// pass explicit outcomes; production passes the recorded-evidence
	// outcome); a nil outcome folds a bare {Mutated: progressed} turn. The
	// fold commits only on the paths that reach the plain drive below — park,
	// hold, wake-drive, superseded, tails, and non-continuation resumes all
	// return before it. waitAdvanced is true when this gate claimed any
	// waits-predicate fire (waits' predicate flips are the only subgoal
	// evidence, spec §4 — expiry claims accrue as ordinary non-advancing
	// turns so the re-park counter cannot be laundered through refires).
	foldOutcome := goal.TurnOutcome{Mutated: progressed}
	if outcome != nil {
		foldOutcome = *outcome
		foldOutcome.Mutated = foldOutcome.Mutated || progressed
	}
	waitAdvanced := claimedPredicateFire(claimed)
	if waitAdvanced {
		// Waits-predicate flips are the only subgoal evidence (spec §4):
		// persist the advancement window the next loss's rule-5 read checks
		// (spec §1 rule 5 check-before-reset). Expiry-only batches skip it —
		// timer refires must not soften a later genuine loss into a re-drive.
		store.MarkAdvanced(now)
	}
	// Terminal-pending latch (spec section 1 R7 M-I1): while latched, rules
	// 2/3 (plus the section-5 re-park graduation - a later task) evaluate
	// before rule 1. The latch stays hidden from the pure table: a
	// bounds-only pre-decide with pending AND the persisted backlog withheld
	// decides; on breach the latched turn blocks and the fresh wakes drop
	// with the honest loss notice, otherwise the latch clears and the real
	// decide sees the wakes.
	decidePending := claimed
	if latched && len(full.PendingWake) == 0 && len(claimed) == 0 && !boundsBreachedAt(full, store.ParkedTotalAt(now), now) {
		// Latched but nothing fresh to gate and bounds clean - clear and
		// decide normally. (Bounds breached with no fresh wakes still
		// pre-decides below so the block path - not a plain drive - runs.)
		store.SetTerminalPending(false, now)
		s.mu.Lock()
		s.goalTerminalPending = false
		s.mu.Unlock()
		latched = false
	}
	if latched {
		gated := full
		gated.PendingWake = nil
		if step, verdict := goal.DecideGoalStep(gated, foldOutcome, truth, nil, markers, now); step == goal.StepBlock {
			s.goalUpdateMu.Unlock()
			s.dropLatchedWakes(full, verdict, now)
			return s.blockGoalFromGate(verdict)
		}
		store.SetTerminalPending(false, now)
		s.mu.Lock()
		s.goalTerminalPending = false
		s.mu.Unlock()
	}
	// The decide reads the POST-fold ledger (spec §1 rules 6-7 key off the
	// folded summary): fold the outcome onto a copy first and decide on the
	// folded snapshot. The commit below persists the same fold — one fold
	// per gate, never two.
	folded := full
	folded.LedgerSummary = goal.FoldLedger(full.LedgerSummary, foldOutcome, waitAdvanced)
	// Auto-parked re-drive (spec §6 stage 2): the auto-wait lease is a
	// bounded re-check timer, not a parked predicate. A continuation gate
	// landing while auto-parked with no fresh claim consumes the lease and
	// drives the bounded evaluation turn (the fold commits below through the
	// plain-drive path): consecutive stall evaluations accrue AutoReparks
	// toward the bound, and the wake-tail bypass never applies (no backlog
	// was ever marked delivered).
	if full.Status == goal.StatusWaiting && full.LedgerSummary.Stage == goal.StageAutoPark && len(claimed) == 0 && len(full.PendingWake) == 0 && wasContinuation {
		consumedAuto := false
		for _, w := range full.Waits {
			if w.Live() && w.Lease.Label == goal.AutoWaitLabel {
				store.ClaimFire(w.Lease.WaitID, "auto re-check due", now)
				consumedAuto = true
			}
		}
		if consumedAuto {
			drained := store.DrainPendingWake(now)
			_ = drained
			full, ok = store.GoalSnapshot()
			if !ok {
				s.goalUpdateMu.Unlock()
				return "", false
			}
			// Fall through to the plain-drive fold below with the lease
			// consumed: this evaluation turn counts (spec §5: no free
			// turns), and the next stall trip re-parks or blocks on the
			// committed AutoReparks.
			snap, stillActive := s.foldGoalLedgerTurn(store, foldOutcome, waitAdvanced, now)
			s.emitGoalUpdated(snap)
			autoFull, _ := store.GoalSnapshot()
			s.goalUpdateMu.Unlock()
			if !stillActive {
				return s.finishStallBlock()
			}
			if boundsBreachedAt(autoFull, store.ParkedTotalAt(now), now) {
				// The auto-park-consumed fold just spent the last
				// continuation: same committed-exhaustion rule as the
				// plain-drive site — block instead of re-parking or
				// rendering another prompt.
				return s.blockGoalFromGate(goal.VerdictBudgetExhausted)
			}
			// Still stalled but bound remains: re-park the auto-wait for the
			// next evaluation. The fold above already released goalUpdateMu
			// (the commit path unlocks before the stillActive check, like
			// the plain-drive site) — re-acquire for the park mutation.
			// The pure table below would drive (no live waits, below-K on
			// the committed summary is impossible here — the committed fold
			// just tripped) — re-park explicitly.
			if goal.LedgerStalled(snapLedger(store)) {
				s.goalUpdateMu.Lock()
				auto := s.parkGoalOnAutoWait(store, now)
				s.emitGoalUpdated(autoSnapshot(store))
				s.emitGoalWaitingSilent(auto)
				s.goalUpdateMu.Unlock()
				s.armGoalWaitTimer()
				s.maybeAutoSave()
				return "", false
			}
			return goal.Render(snap.Objective), true
		}
	}
	step, verdict := goal.DecideGoalStep(folded, foldOutcome, truth, decidePending, markers, now)
	// The pure table's stage-2 park verdict carries VerdictNoProgress; the
	// live-wait rule-4 park carries none. Split them here: the former routes
	// to the bounded auto-park below (fold commits with the AutoReparks
	// increment), the latter keeps the zero-fold waiting branch.
	autoPark := step == goal.StepPark && verdict == goal.VerdictNoProgress
	switch step {
	case goal.StepPark:
		if autoPark {
			break
		}
		// Waiting branch: skip every fold, arm nothing (spec section 1 -
		// only the coalesced wait timer). Preserve the wake-pending
		// hold's settle flag so the settle does not re-kick past the same
		// wait, and keep the hold working when no registry wait exists (its
		// dependents still guarantee a future wake). The pre-decide fold
		// above is discarded — parked turns accrue zero stall signal.
		if wakePending || len(full.Waits) > 0 {
			s.mu.Lock()
			s.goalDependentsHeld = true
			s.mu.Unlock()
		}
		s.goalUpdateMu.Unlock()
		s.armGoalWaitTimer()
		return "", false
	case goal.StepNudge:
		// Stall-nudge (spec §§1, 6 stage 1): commit the fold (which trips the
		// stage none → nudged transition), accrue the continuation, emit
		// exactly one steering note naming the repetition evidence, and drive
		// the objective — never block on the first trip.
		snap := s.commitGoalLedgerFold(store, full, foldOutcome, waitAdvanced, now)
		s.emitGoalUpdated(snap)
		s.goalUpdateMu.Unlock()
		s.appendTurn(schema.TurnSteering, llm.User("[goal-stall-nudge] "+stallNudgeText(full, folded)))
		s.maybeAutoSave()
		return goal.Render(snap.Objective), true
	case goal.StepDrive:
		if hasSupersededWake(full) {
			// Superseded path (spec section 3): the goal was retargeted
			// between claim and kick. Drive a single no-op evaluation on
			// the CURRENT objective with the stale excerpt marked
			// superseded - never the old objective's wake. The turn is
			// wait-attributable: the pre-decide fold above is discarded,
			// consume the marked batch at this tail, and re-arm the current
			// objective.
			prompt := s.renderGoalSupersededPrompt(full)
			var ids []string
			for _, p := range full.PendingWake {
				if p.Superseded {
					ids = append(ids, p.WaitID)
				}
			}
			s.markGoalWakesDelivered(ids)
			// The no-op turn runs next; its own tail consumes the marked
			// batch via the wake-tail fold (entries are marked delivered
			// here). Flag it so the tail below re-arms without folding even
			// though the backlog still stands at this instant.
			s.mu.Lock()
			s.goalSupersededArmed = true
			s.mu.Unlock()
			s.goalUpdateMu.Unlock()
			return prompt, true
		}
		if len(full.PendingWake) > 0 || len(claimed) > 0 {
			// Fired/expiry path: an undelivered wake batch drives exactly
			// one resume turn carrying all coalesced triggers (spec section
			// 2: one combined wake turn). The wake turn's own drive folds
			// via the ledger (spec §5: wake turns count — the drive
			// commits; the tail only drains the delivered batch, never
			// re-folds).
			// Terminal-flagged when a budget is also exceeded: latch so the
			// NEXT gate enforces bounds before rule 1 (no starvation by
			// flapping predicates). Reads the live parked total: the wake
			// turn being driven extends the open stretch to now.
			if boundsBreachedAt(full, store.ParkedTotalAt(now), now) {
				store.SetTerminalPending(true, now)
				s.mu.Lock()
				s.goalTerminalPending = true
				s.mu.Unlock()
			}
			// The wake fold attributes through RecordWakeContinuation: when
			// ClaimFire leaves sibling live waits (goal stays waiting),
			// the drive still ran a real turn and must accrue (spec §5).
			snap := s.commitGoalWakeFold(store, full, foldOutcome, waitAdvanced, now)
			s.emitGoalUpdated(snap)
			wakeFull, _ := store.GoalSnapshot()
			s.goalUpdateMu.Unlock()
			// The committed fold accrues exactly one continuation, so only
			// an accrual FROM below the cap TO the cap belongs to this
			// drive: a pre-spent cap predates the fold and stays latched
			// for next-gate enforcement with its own verdict (the
			// terminal-flagged wake must still drive: spec §1 rule 1
			// drives first, the block happens on the next gate). Count
			// semantics unchanged: the fold already accrued exactly once.
			// goalUpdateMu is already released above; blockGoalFromGate
			// takes no session locks itself (it takes goalUpdateMu
			// internally via setGoalTerminal).
			if wakeFull.Budgets.MaxContinuations > 0 && wakeFull.Budgets.UsedContinuations >= wakeFull.Budgets.MaxContinuations && full.Budgets.UsedContinuations < full.Budgets.MaxContinuations {
				// The committed wake fold just spent the last continuation:
				// rule 2 would block on the FOLLOWING gate, but rendering
				// the wake prompt first hands the model a free turn past
				// the cap — the claimed wake never drives. Enforce on the
				// committed snapshot instead — same terminal path as the
				// plain-drive committed-exhaustion site (blockGoalFromGate
				// with VerdictBudgetExhausted, which clears waits per the
				// every-terminal rule while the delivered-set mark below
				// never lands, so the stranded backlog drains on the
				// block's terminal read). A pre-spent cap predates the fold
				// and stays latched for next-gate enforcement (the
				// terminal-flagged wake still drives first). Scope is the
				// continuation cap only: the fold moves neither the parked
				// anchor nor the clock, so parked and deadline read
				// identically pre/post fold and keep their own latched
				// verdict paths (the deadline final turn must still drive:
				// TestFixWaveI2DeadlineFinalTurnThenBlock). Count semantics
				// unchanged: the fold already accrued exactly once.
				// goalUpdateMu is already released above; blockGoalFromGate
				// takes no session locks itself (it takes goalUpdateMu
				// internally via setGoalTerminal).
				return s.blockGoalFromGate(goal.VerdictBudgetExhausted)
			}
			prompt := s.renderGoalWakePrompt(full)
			var ids []string
			for _, p := range full.PendingWake {
				ids = append(ids, p.WaitID)
			}
			s.markGoalWakesDelivered(ids)
			// The notifying turn for a waited target IS the wake turn
			// (spec §7: no double-turn accounting): announce the resume on
			// the kick itself, after the store locks are released.
			s.emitGoalResumed(ids)
			// Wake delivery is watchdog activity (spec §6 signal 2): the
			// wake itself resets the stretch.
			s.noteGoalWatchdogActivity(now)
			// A non-continuation turn completed while wakes stood
			// undelivered (e.g. a notification turn landing between claim
			// and kick): the wake turn is due - drive it rather than the
			// plain objective.
			return prompt, true
		}
		// Plain drive: fall through to the ledger fold below (active goal,
		// no wakes). A waiting status with no live waits and no backlog is
		// unreachable (the store returns to active on the last claim/cancel),
		// but drive it rather than strand it.
	case goal.StepBlock:
		s.goalUpdateMu.Unlock()
		return s.blockGoalFromGate(verdict)
	}
	if autoPark {
		// Stage-2 bounded auto-park (spec §§1, 5, 6): the post-nudge stall
		// looks like waiting, so park until the bounded auto-wait instead of
		// blocking — up to MaxConsecutiveAutoReparks consecutive parks, then
		// the commit graduates to block. The commit keys on the COMMITTED
		// summary (foldGoalLedgerTurn: RecordContinuation owns the stage trip
		// + AutoReparks++), not the pre-fold read: a re-park-exhausted stall
		// blocks here with "no progress" and the single terminal note, while
		// a bounded one parks on the persisted auto lease (timer re-arms to
		// it) with the silent EventGoalWaiting (AnnounceSilently — audit
		// trail preserved for replay consumers, coalesced into the stall
		// episode's single notice per §6).
		snap, stillActive := s.foldGoalLedgerTurn(store, foldOutcome, waitAdvanced, now)
		s.emitGoalUpdated(snap)
		s.goalUpdateMu.Unlock()
		if !stillActive {
			return s.finishStallBlock()
		}
		s.goalUpdateMu.Lock()
		auto := s.parkGoalOnAutoWait(store, now)
		s.emitGoalUpdated(autoSnapshot(store))
		s.emitGoalWaitingSilent(auto)
		s.goalUpdateMu.Unlock()
		s.armGoalWaitTimer()
		s.maybeAutoSave()
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
	// Always consume the superseded flag - even when the wake-tail fold above
	// already ran (a continuation-tail superseded no-op leaves the flag set
	// while consuming its batch via the per-ID drain): a stale set flag must
	// never strand a later empty-backlog gate on the superseded branch below.
	supersededArmed := s.takeGoalSupersededArmed()
	if wakeTail || supersededArmed {
		// The just-finished turn was a wake turn (or the superseded no-op
		// evaluation): its backlog is consumed above (wake-tail per-ID drain
		// of delivered ids) or now (superseded: drained by Superseded flag
		// below) and the turn was wait-attributable, so bypass the stall
		// fold and re-arm the plain objective. The follow-up turn's own
		// tail folds normally. No extra goalUpdateMu lock here: the gate
		// already holds the serializer (store methods self-lock; cf. the
		// wake-tail drain above), and goalUpdateMu is non-reentrant.
		if !wakeTail {
			// Drain ONLY the superseded IDs (spec section 3): fresh claims
			// that landed before the no-op ran were never Superseded-marked
			// and keep their own backlog and their own wake - never
			// silently dropped with the stale batch.
			var supersededIDs []string
			for _, p := range full.PendingWake {
				if p.Superseded {
					supersededIDs = append(supersededIDs, p.WaitID)
				}
			}
			store.DrainPendingWakeIDs(supersededIDs, now)
			full, ok = store.GoalSnapshot()
			if !ok {
				s.goalUpdateMu.Unlock()
				return "", false
			}
		}
		if boundsBreachedAt(full, store.ParkedTotalAt(now), now) {
			// A spent continuation cap at the wake tail means the drive-time
			// fold already consumed the last turn (spec §5), so this re-arm
			// would hand the model a free turn past the cap — block instead
			// of rendering another prompt. Scope is the continuation cap
			// only: the general boundsBreachedAt read (parked, deadline)
			// owns its own verdict path through the latch/decide below, and
			// the deadline final turn must still drive
			// (TestFixWaveI2DeadlineFinalTurnThenBlock). Same terminal path
			// as the plain-drive site (blockGoalFromGate with
			// VerdictBudgetExhausted). The gate holds goalUpdateMu here and
			// it is non-reentrant, so release before the block:
			// blockGoalFromGate takes no session locks itself (it takes
			// goalUpdateMu internally via setGoalTerminal).
			capSpent := full.Budgets.MaxContinuations > 0 && full.Budgets.UsedContinuations >= full.Budgets.MaxContinuations
			s.goalUpdateMu.Unlock()
			if capSpent {
				return s.blockGoalFromGate(goal.VerdictBudgetExhausted)
			}
			return goal.Render(full.Objective), true
		}
		s.goalUpdateMu.Unlock()
		return goal.Render(full.Objective), true
	}
	if wakePending && len(full.Waits) == 0 && len(full.PendingWake) == 0 {
		// Wake-pending hold: the turn made no mutating call (wakePending is
		// computed only for non-progressed continuations), but owned work is
		// guaranteed to wake the session (a running delegate's report/terminal
		// notification, a supervised background job's progress tick or terminal
		// notification). Waiting on a guaranteed wake is not stalling, so the
		// ledger fold is skipped — polling turns must not accrue stall signal on
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
	// Plain-drive fold (spec §§1, 4): commit the same ledger fold the decide
	// read, accrue the continuation, and apply the two-tier stall bound. The
	// fold trips stage none → nudged on its first breach (the StepNudge arm
	// above already returned); a further breach after the nudge blocks here
	// with "no progress" and the single terminal note. Repetition/backstop
	// verdicts below key off the COMMITTED summary (not the pre-fold read),
	// so the block and the persisted stage agree on the same trip.
	snap, stillActive := s.foldGoalLedgerTurn(store, foldOutcome, waitAdvanced, now)
	s.emitGoalUpdated(snap)
	driveFull, _ := store.GoalSnapshot()
	s.goalUpdateMu.Unlock()
	if !stillActive {
		// The ledger stall bound fired this turn: the single transcript
		// note (user-role, durable in the transcript and projected on
		// reload) plus persist-after (a blocked goal saved as still-active
		// would resume on restart). finishStallBlock also disarms the
		// coalesced timer (Task-7 Minor-6 parity).
		return s.finishStallBlock()
	}
	if boundsBreachedAt(driveFull, store.ParkedTotalAt(now), now) {
		// The committed fold just spent the last continuation (or the parked
		// total crossed mid-turn): rule 2 would block on the FOLLOWING gate,
		// but rendering another prompt first hands the model a free turn past
		// the cap. Enforce on the committed snapshot instead — same predicate
		// as DecideGoalStep rule 2 (boundsBreachedAt), same terminal path as
		// the StepBlock arm (blockGoalFromGate with VerdictBudgetExhausted).
		// Count semantics unchanged: the fold already accrued exactly once.
		// goalUpdateMu is already released above; blockGoalFromGate takes no
		// locks itself.
		return s.blockGoalFromGate(goal.VerdictBudgetExhausted)
	}
	// Turn-tail watchdog eval + activity signals (spec §6 Task-8 residual):
	// the committed fold is ledger activity (signal 1 — the fold's own
	// advancement resets the quiet stretch inside noteGoalWatchdogFold), and
	// the active-goal tail evaluates the watchdog (active goals arm no
	// timer — this tail is their only evaluation). Parked goals skip the
	// eval (their stretch evaluates through the timer piggyback).
	s.noteGoalWatchdogFold(driveFull, now)
	s.evalGoalWatchdogAtTurnTail(driveFull, now)
	return s.goalContinuationWithDelta(snap.Objective, driveFull.Conditions), true
}

// finishStallBlock completes a ledger stall block (spec §§1, 6): the commit
// (RecordContinuation) already transitioned the goal to blocked with "no
// progress" and cleared the waits per the every-terminal rule. This finishes
// the stop discipline shared with every other gate stop path: disarm the
// coalesced timer (no post-block stale fire — the Task-7 Minor-6 parity),
// the single transcript note, the exactly-once terminal report, persist.
// Call with no locks held; kicks never apply to a stop path.
func (s *Session) finishStallBlock() (string, bool) {
	s.stopGoalWaitTimer()
	s.appendTurn(schema.TurnSteering, llm.User(fmt.Sprintf(
		"[goal-no-progress] Goal blocked: %s. The goal engine has stopped driving the objective; it resumes only via /goal clear or a new /goal.",
		stallBlockText(s.getOrCreateGoalStore()))))
	s.reportGoalEnded()
	s.maybeAutoSave()
	return "", false
}

// commitGoalLedgerFold persists the pre-decide ledger fold for the nudge arm:
// same FoldLedger + continuation accrual as the plain-drive fold, plus the
// stage none → nudged trip the nudge verdict implies. Call with goalUpdateMu
// held; store methods self-lock.
func (s *Session) commitGoalLedgerFold(store *goal.Store, full goal.GoalSnapshot, outcome goal.TurnOutcome, waitAdvanced bool, now time.Time) goal.Snapshot {
	_ = full
	snap, _ := store.RecordContinuation(outcome, waitAdvanced, now)
	return snap
}

// commitGoalWakeFold persists the wake turn's own drive fold (spec §5: wake
// turns count, no free turns): the identical ledger fold as
// commitGoalLedgerFold, but attributed through RecordWakeContinuation so a
// wake driving while sibling live waits keep the goal waiting still accrues —
// the drive ran a real model turn. The wake tail still bypasses (it only
// drains the delivered batch), so one wake turn folds exactly once. Call with
// goalUpdateMu held; store methods self-lock.
func (s *Session) commitGoalWakeFold(store *goal.Store, full goal.GoalSnapshot, outcome goal.TurnOutcome, waitAdvanced bool, now time.Time) goal.Snapshot {
	_ = full
	snap, _ := store.RecordWakeContinuation(outcome, waitAdvanced, now)
	return snap
}

// foldGoalLedgerTurn persists one plain-drive ledger fold and applies the
// two-tier stall bound (spec §§1, 4 rules 6-7). Call with goalUpdateMu held;
// store methods self-lock.
func (s *Session) foldGoalLedgerTurn(store *goal.Store, outcome goal.TurnOutcome, waitAdvanced bool, now time.Time) (goal.Snapshot, bool) {
	return store.RecordContinuation(outcome, waitAdvanced, now)
}

// goalContinuationWithDelta renders the drive prompt with the spec §6
// direction-4 delta frame: condition flips since the last evaluation (not
// full state). The wake trailer already carries fired triggers; the delta
// covers condition truth changes. Updates the last-driven snapshot. Pure
// except the substrate read (EvaluateExpectations) and the s.mu section.
func (s *Session) goalContinuationWithDelta(objective string, conds []goal.Condition) string {
	if len(conds) == 0 {
		return goal.Render(objective)
	}
	checks := s.getOrCreateGoalStore().EvaluateExpectations(conds)
	s.mu.Lock()
	last := s.goalDeltaLastConds
	s.goalDeltaLastConds = append([]goal.ConditionCheck(nil), checks...)
	s.mu.Unlock()
	// Positional: last[i] answers conds[i] (same order as checks) — never
	// joined by Desc, so duplicate descriptions cannot collapse two
	// conditions into one truth and lose a flip.
	var flips []goal.ConditionFlip
	for i, c := range checks {
		if i < len(last) && last[i].Satisfied != c.Satisfied {
			flips = append(flips, goal.ConditionFlip{Desc: c.Desc, From: last[i].Satisfied, To: c.Satisfied})
		}
	}
	return goal.RenderWithDelta(objective, flips, nil)
}

// stallNudgeText names the repetition evidence for the stage-1 nudge note
// (spec §6: the nudge names the evidence): the trailing action fingerprint
// plus the run length and tier. Pure: no locks.
func stallNudgeText(pre, folded goal.GoalSnapshot) string {
	fp := ""
	class := ""
	if n := len(folded.LedgerSummary.Entries); n > 0 {
		fp = folded.LedgerSummary.Entries[n-1].Fingerprint
		class = folded.LedgerSummary.Entries[n-1].Class
	}
	k := folded.LedgerSummary.Tier
	if k != goal.RepetitionThresholdAdvanced && k != goal.RepetitionThresholdFresh {
		k = goal.RepetitionThresholdFresh
	}
	backstop := goal.BackstopStalled(folded.LedgerSummary)
	if backstop {
		return fmt.Sprintf("%d consecutive non-advancing turns (total non-advancement backstop); state your unblock condition or register the wait", goal.BackstopThreshold)
	}
	_ = pre
	return fmt.Sprintf("%d turns of `%s` (%s) with no state change; state your unblock condition or register the wait", folded.LedgerSummary.Repetition, fp, class) +
		fmt.Sprintf(" (tier K=%d)", k)
}

// stallBlockText names the committed stall evidence for the terminal note.
// Call with no locks held (store methods self-lock).
func stallBlockText(store *goal.Store) string {
	full, ok := store.GoalSnapshot()
	if !ok {
		return "no progress"
	}
	if goal.BackstopStalled(full.LedgerSummary) {
		return fmt.Sprintf("no state advancement in the last %d continuation turns", goal.BackstopThreshold)
	}
	if n := len(full.LedgerSummary.Entries); n > 0 {
		last := full.LedgerSummary.Entries[n-1]
		return fmt.Sprintf("%d turns of `%s` (%s) with no state change", full.LedgerSummary.Repetition, last.Fingerprint, last.Class)
	}
	return "no progress"
}

// childTerminalTrigger reports the terminal trigger for a direct child target
// (the §8 gate attach-scan): terminal-only — a non-terminal or unknown child
// reads false and never claims. Controller reads are §3-top pre-reads
// (acquire, read, release — never held across the claim). Restored children
// whose runtime is untracked still resolve through their durable delegate
// record (Descriptor.ChildSessionID, PhaseClosed — mirroring the
// LookupDelegate PhaseClosed branch).
func (s *Session) childTerminalTrigger(childID string) (string, bool) {
	if s == nil || s.subagents == nil || childID == "" {
		return "", false
	}
	for _, sub := range s.subagents.directSubagents() {
		if sub == nil || sub.sess == nil || sub.id != childID {
			continue
		}
		sub.mu.Lock()
		terminal := terminalStatus(sub.status)
		result := sub.result
		sub.mu.Unlock()
		if !terminal {
			return "", false
		}
		trigger := "child " + childID + " terminal"
		if strings.TrimSpace(result) != "" {
			trigger += ": " + strings.TrimSpace(result)
		}
		return trigger, true
	}
	if c := s.delegateController; c != nil {
		c.mu.Lock()
		var outcome string
		var reason string
		terminal := false
		for _, agg := range c.durable {
			if agg == nil || agg.Descriptor.ChildSessionID != childID {
				continue
			}
			if agg.Phase != delegatestore.PhaseClosed {
				continue
			}
			terminal = true
			if agg.LatestOutcome != nil {
				outcome = string(agg.LatestOutcome.Status)
				reason = agg.LatestOutcome.Reason
			}
			break
		}
		c.mu.Unlock()
		if terminal {
			trigger := "child " + childID + " terminal"
			if detail := strings.TrimSpace(strings.TrimSpace(outcome + " " + reason)); detail != "" {
				trigger += ": " + detail
			}
			return trigger, true
		}
	}
	return "", false
}

// claimedPredicateFire reports whether any claim in the batch carries
// waits-predicate evidence for the ledger fold (spec §4: waits' predicate
// flips are the only subgoal evidence — any non-expiry fire across all
// kinds, not just until_child). Expiry claims accrue as ordinary
// non-advancing turns, so the re-park counter cannot be laundered through
// timer refires. Structural: the Expiry mark rides the PendingWake entry
// from classification (a deadline-only wake — lease expiry or the synthetic
// deadline wake — never advances, whatever its trigger text). Pure.
func claimedPredicateFire(claimed []goal.PendingWake) bool {
	for _, c := range claimed {
		if !c.Expiry && c.WaitID != goal.DeadlineWakeID {
			return true
		}
	}
	return false
}

// batchForIDs narrows a snapshot to the named backlog entries for one kick's
// prompt frame (spec section 2: one combined wake turn per claim batch).
// Pure: no locks.
func batchForIDs(full goal.GoalSnapshot, ids []string) goal.GoalSnapshot {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var kept []goal.PendingWake
	for _, p := range full.PendingWake {
		if want[p.WaitID] {
			kept = append(kept, p)
		}
	}
	full.PendingWake = kept
	return full
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

// hasSupersededWake reports whether the backlog carries entries marked
// Superseded by a retarget-after-claim (spec section 3). Pure: no locks.
func hasSupersededWake(full goal.GoalSnapshot) bool {
	for _, p := range full.PendingWake {
		if p.Superseded {
			return true
		}
	}
	return false
}

// renderGoalSupersededPrompt renders the single no-op evaluation turn for a
// superseded claim batch (spec section 3): the CURRENT objective plus a frame
// carrying each stale trigger marked superseded, so the model evaluates once
// against current state and never pursues the old objective. Pure: no locks.
func (s *Session) renderGoalSupersededPrompt(full goal.GoalSnapshot) string {
	base := goal.Render(full.Objective)
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n" + goalWaitWakeTrailerPrefix + " A waited event fired for a previous objective (superseded by retarget); treat the stale trigger(s) below as dropped context, evaluate the CURRENT objective once against current state, then continue:")
	for _, p := range full.PendingWake {
		if !p.Superseded {
			continue
		}
		fmt.Fprintf(&b, "\n- %s (superseded): %s (fired %s)", p.WaitID, p.Trigger, p.FiredAt.UTC().Format(time.RFC3339))
	}
	return b.String()
}

// takeGoalSupersededArmed consumes the superseded no-op flag set when the
// gate drives the marked evaluation turn. Self-locking.
func (s *Session) takeGoalSupersededArmed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	armed := s.goalSupersededArmed
	s.goalSupersededArmed = false
	return armed
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
	s.getOrCreateGoalStore().SetTerminalPending(false, now)
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
	s.getOrCreateGoalStore().SetTerminalPending(false, s.sclock().Now())
	s.mu.Lock()
	s.goalTerminalPending = false
	s.goalSupersededArmed = false
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

// claimGoalWaitExpiredWaits classifies every live lease and converts fires
// into persisted pendingWake claims (the claimWaitFireLocked analogue at
// session scope) and reports the claims with the owning objective, plus the
// loss causes dropped this pass (persisted via DropLostWait + RecordLoss for
// the gate's TakeLossCause/rule-5 read). It sequences s.mu and goalUpdateMu
// sections without nesting either (the established SetGoal order is
// goalUpdateMu-then-s.mu; this helper never holds one while taking the
// other). ClaimFire's atomicity is the
// exactly-once guarantee: concurrent claimants race on the lease, exactly
// one wins. Covers ALL kinds (spec §§1-2: predicate truth plus expiry for
// every kind), not timer expiry alone: the coalesced timer's poll leg is the
// evaluation tick for file/HTTP predicates.
func (s *Session) claimGoalWaitExpiredWaits(now time.Time) ([]goal.PendingWake, string, []string) {
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
		return nil, "", nil
	}
	var live []goal.Wait
	for _, w := range full.Waits {
		if w.Live() && !delivered[w.Lease.WaitID] {
			live = append(live, w)
		}
	}
	batch := store.ClassifyWaits(live, now, s.childTerminalTrigger)
	claimed, losses := store.ClaimClassified(batch, now)
	for _, cause := range losses {
		store.RecordLoss(cause, now)
	}
	// Accrue the open parked stretch at the crossing (spec §5): the timer
	// fires exactly at the projected maxParkedTotal instant, so fold
	// entry→now into the persisted total now — otherwise the persisted field
	// still reads pre-crossing and the bound never binds on this pass.
	// ClaimClassified's per-claim settles already folded the stretch when
	// leases fired; this covers the no-claim crossing (pure budget fire).
	// The status read is post-claim: a batch that fired the last lease
	// transitioned Waiting→Active, so its stretch already settled — only a
	// still-waiting goal re-stamps the anchor for the next segment (never
	// re-arm it while active, or later active time would charge the cap).
	full, ok = store.GoalSnapshot()
	if !ok {
		return nil, "", nil
	}
	if full.Status == goal.StatusWaiting {
		store.AccrueParked(now)
		store.NoteParkEnter(now)
	}
	// Synthetic deadline claim (spec §1 rule 3 — the timer leg mirrors the
	// gate): past the wall-clock deadline with the one-shot unspent and no
	// standing synthetic entry, claim the final evaluation turn through the
	// same exactly-once backlog. Without this a deadline earlier than every
	// wait deadline fires the timer with nothing to claim, and the goal
	// parks past its deadline with no final turn. The delivered-set + store
	// marker dedupe (shared with the gate) keeps it one-shot: a re-drive
	// after delivery never re-claims, and a carried (retarget-Superseded)
	// entry suppresses a duplicate append via ClaimDeadlineExpiry's
	// standing-entry report.
	if !delivered[goal.DeadlineWakeID] && !full.DeadlineFinalDelivered &&
		!full.Budgets.Deadline.IsZero() && !now.Before(full.Budgets.Deadline) {
		if entry, ok := store.ClaimDeadlineExpiry(now); ok {
			claimed = append(claimed, entry)
		}
	}
	return claimed, full.Objective, losses
}

// armGoalWaitTimer arms the single coalesced wait timer (spec section 2) to
// the four-way min computed by goalWaitNextFire — min(earliest live wait
// deadline, goal deadline, projected maxParkedTotal-crossing, next poll due)
// — the single source stated once there and referenced from every arming
// site (never the two- or three-way subsets). A fire instant at or before
// now (the claim path owns already-expired deadlines) or no live waits
// leaves the timer disarmed. Re-arming strands the previous callback via the
// generation counter. Lock order: goalUpdateMu, then s.mu (the SetGoal order).
func (s *Session) armGoalWaitTimer() {
	s.goalUpdateMu.Lock()
	boundObjective, bound := s.armGoalWaitTimerLocked()
	s.goalUpdateMu.Unlock()
	// Bound-at-arm kick outside all locks (kick path sequences the locks
	// itself): the parked cap already bound, so the budget evaluation turn
	// is due now instead of a silent disarm.
	if bound {
		s.kickGoalWaitBudgetBound(boundObjective)
	}
}

// armGoalWaitTimerLocked arms the single coalesced wait timer under the
// goal serializer: snapshot, four-way-min computation, and timer
// replacement happen atomically, so a newer arm can never be overwritten by
// an older arm's stale later deadline (the reported stale-snapshot race).
// Reports the bound-at-arm objective ("" when the parked cap did not bind):
// waiting + live total at/over maxParkedTotal with fire==now means silent
// disarm would strand the goal parked past its cap with no kick scheduled
// (the settle suppresses parked kicks with no claim). Caller must hold
// goalUpdateMu; takes s.mu for the timer swap (the SetGoal
// goalUpdateMu-then-s.mu order).
func (s *Session) armGoalWaitTimerLocked() (string, bool) {
	store := s.getOrCreateGoalStore()
	full, ok := store.GoalSnapshot()
	now := s.sclock().Now()
	var fire time.Time
	var armed bool
	if ok {
		// Live parked total (persisted + open stretch): projecting the
		// maxParkedTotal crossing from the persisted field alone re-bases
		// every poll and the crossing never arrives.
		fire, armed = goalWaitNextFireAt(full, store.ParkedTotalAt(now), now)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.goalWaitTimer; t != nil {
		t.Stop()
		s.goalWaitTimer = nil
	}
	if !armed || !now.Before(fire) {
		// Nothing to wait on, or the computed fire instant already passed
		// (the claim path converts it on the next gate/settle/timer pass)
		// - disarm, stranding any in-flight callback via the generation
		// bump.
		s.goalWaitTimerGen++
		if ok && full.Status == goal.StatusWaiting &&
			full.Budgets.MaxParkedTotal > 0 &&
			store.ParkedTotalAt(now) >= full.Budgets.MaxParkedTotal {
			return full.Objective, true
		}
		return "", false
	}
	s.goalWaitTimerGen++
	gen := s.goalWaitTimerGen
	s.goalWaitTimer = s.sclock().AfterFunc(fire.Sub(now), func() { s.fireGoalWaitTimer(gen) })
	return "", false
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
	claimed, objective, losses := s.claimGoalWaitExpiredWaits(now)
	// Watchdog piggyback (spec §6 Task-8 residual): the parked stretch
	// evaluates on the same coalesced fire — never a second timer. On a
	// no-claim fire the watchdog may itself re-arm the timer: a stretch that
	// crossed the quiet threshold between fires must still notify, and the
	// next fire instant (four-way min) keeps the evaluation live until the
	// stretch ends. kickClaimedGoalWake re-arms on the claim path below.
	s.evalGoalWatchdogForTimer(now)
	// Losses-only fire (spec §2: never a silent strand): the drop already
	// persisted the cause for the gate's TakeLossCause/rule-5 read — deliver
	// the honest notice now and kick the current objective so the re-drive
	// (or the rule-5 terminal verdict) runs promptly.
	if len(claimed) == 0 && len(losses) > 0 {
		s.kickGoalWaitLosses(losses, objective)
		return
	}
	if len(claimed) == 0 {
		// Parked-cap crossing (spec §5): the accrual above bound the cap
		// with nothing claimed — kick the budget evaluation turn so rule 2
		// enforces it now, instead of re-arming into a disarm (fire==now)
		// that strands the goal parked past its cap.
		s.goalUpdateMu.Lock()
		bound := false
		if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok {
			bound = full.Budgets.MaxParkedTotal > 0 && full.Budgets.ParkedTotal >= full.Budgets.MaxParkedTotal
		}
		s.goalUpdateMu.Unlock()
		if bound {
			s.kickGoalWaitBudgetBound(objective)
			return
		}
		s.armGoalWaitTimer()
		return
	}
	s.kickClaimedGoalWake(claimed, objective)
}

// kickClaimedGoalWake delivers the kick for a claim batch the caller already
// consumed (spec section 3 superseded/drop routing). Extracted from
// fireGoalWaitTimer so tests can drive the retarget-between-claim-and-kick
// branch deterministically: pass the claim-time objective alongside a store
// that has since been retargeted (carried Superseded batch) or cleared.
// Call with no locks held; kicks fire outside all locks.
func (s *Session) kickClaimedGoalWake(claimed []goal.PendingWake, objective string) {
	// Read kick/ask state AFTER the claim (never across it): the callback
	// runs on the clock's goroutine, and the claim path plus a concurrent
	// gate/settle may interleave - holding s.mu across the claim would
	// deadlock against a gate holding goalUpdateMu and wanting s.mu.
	kick, pendingAsk := s.goalKickState()
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
		// Retarget won between claim and kick (spec section 3 superseded):
		// Set carried the claim marked Superseded, so re-read the current
		// goal (which now owns the marked batch) and drive the single no-op
		// evaluation on it - never the old objective's wake, never a drop.
		// The no-op turn's own gate drives via the superseded branch (the
		// batch is already marked here).
		s.goalUpdateMu.Lock()
		current, ok := s.getOrCreateGoalStore().GoalSnapshot()
		s.goalUpdateMu.Unlock()
		if !ok {
			s.armGoalWaitTimer()
			return
		}
		var ids []string
		for _, p := range current.PendingWake {
			if p.Superseded {
				ids = append(ids, p.WaitID)
			}
		}
		s.markGoalWakesDelivered(ids)
		s.mu.Lock()
		s.goalSupersededArmed = true
		s.mu.Unlock()
		prompt := s.renderGoalSupersededPrompt(current)
		s.armGoalWaitTimer()
		if kick == nil {
			return
		}
		// No GoalResumed announcement here (fix round 1/4): the stale
		// trigger is dropped context, not a resumed wait — the prompt
		// frames it as superseded, so a "Goal resumed: <wait_id>"
		// announcement identical to a real wake would mislead. The no-op
		// evaluation still drives and still bypasses the fold.
		kick(prompt)
		return
	}
	// Normal path: kick exactly the claimed batch (spec section 2: one
	// combined wake turn per claim batch). Other backlog entries that landed
	// concurrently (a deadline synthetic, a second forward) keep their own
	// kick path — kicking them here would double-deliver one fire.
	want := make(map[string]bool, len(claimed))
	for _, c := range claimed {
		want[c.WaitID] = true
	}
	var ids []string
	for _, p := range full.PendingWake {
		if want[p.WaitID] {
			ids = append(ids, p.WaitID)
		}
	}
	if len(ids) == 0 {
		// Consumed between claim and kick (wake-tail drain won the race):
		// nothing left to deliver.
		s.armGoalWaitTimer()
		return
	}
	s.markGoalWakesDelivered(ids)
	prompt := s.renderGoalWakePrompt(batchForIDs(full, ids))
	s.armGoalWaitTimer()
	if kick == nil {
		return
	}
	// The notifying turn for a waited target IS the wake turn (spec §7: no
	// double-turn accounting): announce the resume on the kick itself.
	s.emitGoalResumed(ids)
	// Wake delivery is watchdog activity (spec §6 signal 2): the wake
	// itself resets the stretch, even when the wake's own fold carries no
	// advancement.
	s.noteGoalWatchdogActivity(s.sclock().Now())
	kick(prompt)
}

// kickGoalWaitBudgetBound kicks the budget evaluation turn when the parked
// cap binds with no lease fire (spec §5): the timer accrued the crossing
// but nothing claimed, so no wake is scheduled — without this kick the goal
// would sit parked past its cap (the re-arm disarms at fire==now). No loss
// notice (nothing was lost); the next gate's rule-2 read owns the
// budget-exhausted verdict. Resets the watchdog stretch like any wake (spec
// §6 signal 2). Call with no locks held; kicks fire outside all locks.
func (s *Session) kickGoalWaitBudgetBound(objective string) {
	kick, pendingAsk := s.goalKickState()
	if pendingAsk {
		// Arm, don't kick past an unanswered ask: the bound persists and
		// the reply turn's gate enforces it.
		s.armGoalWaitTimer()
		return
	}
	s.goalUpdateMu.Lock()
	full, ok := s.getOrCreateGoalStore().GoalSnapshot()
	s.goalUpdateMu.Unlock()
	if !ok || full.Objective != objective {
		// Cleared or retargeted between accrue and kick: the new goal owns
		// its own bounds — §3 clear drops silently, retarget routes via
		// the gate's superseded path.
		s.armGoalWaitTimer()
		return
	}
	prompt := goal.Render(full.Objective)
	s.noteGoalWatchdogActivity(s.sclock().Now())
	// No re-arm here: the cap is already bound, so re-arming re-reports
	// bound-at-arm and recurses (arm→kick→arm stack overflow). The kicked
	// evaluation turn's own gate/tail owns the next arming decision — after
	// the rule-2 block there is nothing to arm.
	if kick == nil {
		return
	}
	kick(prompt)
}

// kickGoalWaitLosses notices + kicks a losses-only fire (spec §2: never a
// silent strand). The claim already persisted the cause, so the next gate's
// TakeLossCause/rule-5 read owns the verdict — this only schedules that
// evaluation promptly, and resets the watchdog stretch like any wake (spec
// §6 signal 2). Call with no locks held; kicks fire outside all locks.
func (s *Session) kickGoalWaitLosses(losses []string, objective string) {
	kick, pendingAsk := s.goalKickState()
	if pendingAsk {
		// Arm, don't kick past an unanswered ask: the cause persists and
		// the reply turn's gate delivers the notice.
		s.armGoalWaitTimer()
		return
	}
	s.goalUpdateMu.Lock()
	full, ok := s.getOrCreateGoalStore().GoalSnapshot()
	s.goalUpdateMu.Unlock()
	if !ok || full.Objective != objective {
		// Cleared or retargeted between drop and kick: the persisted cause
		// (or the superseded routing) already owns the notice — §3 clear
		// drops silently, retarget routes via the gate's superseded path.
		s.armGoalWaitTimer()
		return
	}
	prompt := goal.Render(full.Objective)
	s.appendTurn(schema.TurnSteering, llm.User(
		goalWaitLossNotice(losses)))
	s.maybeAutoSave()
	s.noteGoalWatchdogActivity(s.sclock().Now())
	s.armGoalWaitTimer()
	if kick == nil {
		return
	}
	kick(prompt)
}

// registerGoalWait validates and installs one wait lease (spec section 2),
// keeping the mutation and its GOAL_UPDATED event ordered under goalUpdateMu
// like setGoalTerminal. It reports the registered lease and whether the
// registration succeeded; on failure the store's LastRejectReason names the
// failed check. On success the coalesced timer re-arms to the new lease (the
// arm runs after the unlock: armGoalWaitTimer takes goalUpdateMu itself).
// An until_child lease on an already-terminal child catches up immediately
// (spec §8 terminal-only matching): the registration claims the terminal
// trigger into pendingWake instead of parking with no guaranteed future
// notification.
func (s *Session) registerGoalWait(req goal.WaitKind, now time.Time) (goal.Wait, bool) {
	// Terminal catch-up pre-read (§3-top discipline): controller/subagent
	// reads never run under goalUpdateMu. An until_child lease on an
	// already-terminal child claims the terminal trigger below instead of
	// parking with no guaranteed future notification (spec §8
	// terminal-only matching).
	var catchTrigger string
	var catchUp bool
	if req.Kind == goal.WaitUntilChild {
		catchTrigger, catchUp = s.childTerminalTrigger(req.Target)
	}
	s.goalUpdateMu.Lock()
	w, ok := s.getOrCreateGoalStore().RegisterWait(req, now)
	if !ok {
		s.goalUpdateMu.Unlock()
		return goal.Wait{}, false
	}
	if catchUp {
		if entry, ok := s.getOrCreateGoalStore().ClaimFire(w.Lease.WaitID, catchTrigger, now); ok {
			_ = entry
		}
	}
	snap, _ := s.getOrCreateGoalStore().Snapshot()
	full, _ := s.getOrCreateGoalStore().GoalSnapshot()
	gen := s.bumpGoalEventGen()
	s.goalUpdateMu.Unlock()
	s.emitGoalUpdatedAtGen(snap, gen)
	s.emitGoalWaitingAtGen(full, gen)
	s.armGoalWaitTimer()
	return w, true
}

// registerGoalExpect validates and installs one stop-claim condition (spec
// section 6), keeping the mutation and its GOAL_UPDATED event ordered under
// goalUpdateMu like registerGoalWait. Registration never feeds the ledger
// (check-on-claim only). On success the condition list grows; the verifier
// at update_goal("complete") evaluates it.
func (s *Session) registerGoalExpect(req goal.ExpectRequest, now time.Time) (goal.Condition, bool) {
	s.goalUpdateMu.Lock()
	cond, ok := s.getOrCreateGoalStore().RegisterExpect(req, now)
	if !ok {
		s.goalUpdateMu.Unlock()
		return goal.Condition{}, false
	}
	snap, _ := s.getOrCreateGoalStore().Snapshot()
	gen := s.bumpGoalEventGen()
	s.goalUpdateMu.Unlock()
	s.emitGoalUpdatedAtGen(snap, gen)
	return cond, true
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
	if !removed {
		// Live-lease miss: a claimed pendingWake entry may still stand. It
		// drives once with the cancellation noted (spec §7) - annotate it
		// under the same serializer hold. The miss itself still reports
		// false; the wake (not the cancel) carries the note.
		s.getOrCreateGoalStore().AnnotateCancelledWake(waitID, now)
		s.goalUpdateMu.Unlock()
		return false
	}
	snap, _ := s.getOrCreateGoalStore().Snapshot()
	gen := s.bumpGoalEventGen()
	s.goalUpdateMu.Unlock()
	// Emission parity with registerGoalWait/setGoalTerminal: cancelling the
	// last live lease flips waiting->active, which observers must see. No
	// emit on a miss (nothing changed).
	s.emitGoalUpdatedAtGen(snap, gen)
	s.armGoalWaitTimer()
	return true
}

// reportGoalEnded emits the terminal EventGoalEnded report exactly once, via the
// store's once-gate (TakeTerminalReport). It is safe to call on every gate stop
// path and on repeated turns after the goal has already finished.
func (s *Session) reportGoalEnded() {
	if snap, ok := s.getOrCreateGoalStore().TakeTerminalReport(); ok {
		s.emitGoalEnded(snap)
	}
}

// emitGoalWaiting publishes one EventGoalWaiting announcement for a freshly
// parked goal (spec §7: the announcement channel for the park). Callers pass
// the full-shape read taken at the parking commit; an empty live-wait set
// emits nothing (a catch-up registration never parks). Must be called without
// session locks held (emit reads provenance through s.mu).
func (s *Session) emitGoalWaiting(full goal.GoalSnapshot) {
	state := goalStateDataFromFull(full)
	if len(state.WaitingOn) == 0 {
		return
	}
	s.emit(events.EventGoalWaiting, events.GoalWaitingData{
		Count:                    len(state.WaitingOn),
		NearestLabel:             state.NearestLabel,
		NearestDeadlineUnixMilli: state.NearestDeadlineUnixMilli,
	})
}

// parkGoalOnAutoWait installs the bounded stage-2 auto-wait lease (spec §6):
// an until_time lease whose expiry re-drives exactly one evaluation turn
// (never auto-blocks). The lease persists so the park survives restart and
// the coalesced timer re-arms to it; the wake turn's own gate decides the
// next step (re-park while the bound holds, block on exhaustion). Call with
// goalUpdateMu held; store methods self-lock. Returns the post-park
// full-shape read for the silent emit.
func (s *Session) parkGoalOnAutoWait(store *goal.Store, now time.Time) goal.GoalSnapshot {
	store.ParkAutoWait(goal.DefaultWaitTimeout, now)
	full, _ := store.GoalSnapshot()
	return full
}

// autoSnapshot reads the narrow snapshot after a park commit for the
// updated emit. Call with goalUpdateMu held; store methods self-lock.
func autoSnapshot(store *goal.Store) goal.Snapshot {
	snap, _ := store.Snapshot()
	return snap
}

// snapLedger reads the committed ledger summary. Call with goalUpdateMu held;
// store methods self-lock.
func snapLedger(store *goal.Store) goal.LedgerSummary {
	full, ok := store.GoalSnapshot()
	if !ok {
		return goal.LedgerSummary{}
	}
	return full.LedgerSummary
}

// emitGoalWaitingSilent publishes the stage-2 auto-park EventGoalWaiting with
// AnnounceSilently set (spec §7 emit-vs-project): replay consumers keep the
// audit trail while the projector suppresses the announcement (coalesced
// into the stall episode's single notice). Must be called without session
// locks held.
func (s *Session) emitGoalWaitingSilent(full goal.GoalSnapshot) {
	state := goalStateDataFromFull(full)
	if len(state.WaitingOn) == 0 {
		return
	}
	s.emit(events.EventGoalWaiting, events.GoalWaitingData{
		Count:                    len(state.WaitingOn),
		NearestLabel:             state.NearestLabel,
		NearestDeadlineUnixMilli: state.NearestDeadlineUnixMilli,
		AnnounceSilently:         true,
	})
}

// emitGoalResumed publishes one EventGoalResumed announcement for a wake turn
// kick (spec §7): the notifying turn for a waited target IS the wake turn, so
// the kick site — not a separate notification — owns the announcement. ids
// names the claim batch the kick delivers. Empty batches emit nothing. Must be
// called without session locks held.
func (s *Session) emitGoalResumed(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.emit(events.EventGoalResumed, events.GoalResumedData{WaitIDs: append([]string(nil), ids...)})
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
	claimed, _, settleLosses := s.claimGoalWaitExpiredWaits(now)
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
	// A restored-but-never-kicked backlog is the exception (spec §7
	// kick-immediately): restore claims into pendingWake with no kick wired,
	// so no wake turn is scheduled yet — the first settle after wiring must
	// kick it. The delivered set distinguishes them: a kicked batch is
	// marked at kick time, a restored one is not.
	backlogPending := false
	restoredBacklog := false
	if len(claimed) == 0 && preHasGoal && (preParked || preSnap.Status == goal.StatusActive) {
		// Delivered-set snapshot BEFORE the goalUpdateMu section below
		// (s.mu only, never nested): the established order is
		// goalUpdateMu-then-s.mu (SetGoal), so nesting s.mu inside
		// goalUpdateMu here would deadlock against a concurrent retarget.
		s.mu.Lock()
		deliveredHere := make(map[string]bool, len(s.goalWakeDelivered))
		for id := range s.goalWakeDelivered {
			deliveredHere[id] = true
		}
		s.mu.Unlock()
		s.goalUpdateMu.Lock()
		var restoredFull goal.GoalSnapshot
		restoredOK := false
		if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok && len(full.PendingWake) > 0 {
			backlogPending = true
			for _, p := range full.PendingWake {
				if !deliveredHere[p.WaitID] {
					restoredBacklog = true
					break
				}
			}
			if restoredBacklog {
				backlogPending = false
				restoredFull, restoredOK = full, true
			}
		}
		s.goalUpdateMu.Unlock()
		// Wake re-read for the restored backlog, still BEFORE the s.mu
		// section below (never nested inside it): the established order is
		// goalUpdateMu-then-s.mu (SetGoal), so the s.mu section must only
		// consume these locals. Rendered outside the lock — pure over full.
		if restoredOK {
			wakeFull = restoredFull
			for _, p := range restoredFull.PendingWake {
				wakeIDs = append(wakeIDs, p.WaitID)
			}
			wakePrompt = s.renderGoalWakePrompt(restoredFull)
		}
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
	// active goal kicks normally when no hold suppresses it.
	// A delivered-but-unconsumed backlog (kick went out, wake turn not yet
	// folded) also suppresses: the wake is already scheduled. A
	// restored-but-never-kicked backlog kicks below with the wake prompt.
	if kick != nil && !pendingAsk && !suppressHold && !backlogPending && (!preParked || len(claimed) > 0 || restoredBacklog) {
		if preHasGoal && (preSnap.Status == goal.StatusActive || (preSnap.Status == goal.StatusWaiting && (len(claimed) > 0 || restoredBacklog))) {
			if len(claimed) > 0 || restoredBacklog {
				// Both wake branches (claimed above, restored backlog in the
				// pre-s.mu split) feed the same wakePrompt/wakeIDs locals:
				// this section holds s.mu alone and never takes goalUpdateMu.
				prompt = wakePrompt
			} else {
				prompt = goal.Render(preSnap.Objective)
			}
		}
	}
	s.mu.Unlock()
	// Losses-only settle (spec §2: never a silent strand): the claim dropped
	// leases but claimed none — notice + kick through the shared loss path
	// (which re-checks clear/retarget/ask under its own locks).
	if prompt == "" && len(claimed) == 0 && len(settleLosses) > 0 {
		s.goalUpdateMu.Lock()
		full, ok := s.getOrCreateGoalStore().GoalSnapshot()
		s.goalUpdateMu.Unlock()
		if ok {
			s.kickGoalWaitLosses(settleLosses, full.Objective)
			return kick != nil && !pendingAsk
		}
		return false
	}
	if prompt != "" {
		// Delivered-set mark after the kick decision, outside s.mu: a timer
		// re-fire for these wait_ids collapses instead of re-kicking. (The
		// mark is idempotent, so a racing gate marking the same batch is
		// harmless - exactly-once still holds via ClaimFire's lease consume.)
		s.markGoalWakesDelivered(wakeIDs)
		// A stale-park claim kick is the wait's wake turn (spec §7): announce
		// the resume on the kick. A plain active-objective kick is not a
		// wake and announces nothing.
		if len(wakeIDs) > 0 {
			s.emitGoalResumed(wakeIDs)
			// Wake delivery is watchdog activity (spec §6 signal 2): the
			// wake itself resets the stretch.
			s.noteGoalWatchdogActivity(s.sclock().Now())
		}
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
// payload shared by every goal mutation boundary. The wait list carries
// labels + deadlines only (spec §7 wire gap, M2); full predicate payloads
// stay in the store/schema snapshot and never ride the wire. Nearest =
// earliest deadline, tie → smallest wait_id (spec §6 chip rule).
func goalStateData(snap goal.Snapshot) events.GoalStateData {
	return goalStateDataFromFull(goal.GoalSnapshot{
		Objective:  snap.Objective,
		Status:     snap.Status,
		Iterations: snap.Iterations,
	})
}

// goalStateDataFromFull converts the full persisted-shape goal read into the
// public event payload, including the live wait list, the nearest-deadline
// summary, and the spend progress. Callers hold no store lock (GoalSnapshot
// is a value copy).
func goalStateDataFromFull(full goal.GoalSnapshot) events.GoalStateData {
	out := events.GoalStateData{
		Objective:         full.Objective,
		Status:            string(full.Status),
		Iterations:        full.Iterations,
		UsedContinuations: full.Budgets.UsedContinuations,
		MaxContinuations:  full.Budgets.MaxContinuations,
		Stage:             string(full.LedgerSummary.Stage),
	}
	var nearestDeadline time.Time
	var haveNearest bool
	var best goal.Wait
	for _, w := range full.Waits {
		if !w.Live() {
			continue
		}
		out.WaitingOn = append(out.WaitingOn, events.GoalWaitData{
			WaitID:            w.Lease.WaitID,
			Label:             w.Lease.Label,
			DeadlineUnixMilli: w.Lease.Deadline.UnixMilli(),
		})
		// Nearest = earliest deadline, tie → registration order
		// (goal.WaitOrderLess, mirroring schema.NearestWait; spec §6).
		if !haveNearest || w.Lease.Deadline.Before(nearestDeadline) ||
			(w.Lease.Deadline.Equal(nearestDeadline) && goal.WaitOrderLess(w, best)) {
			haveNearest = true
			best = w
			nearestDeadline = w.Lease.Deadline
			out.NearestDeadlineUnixMilli = w.Lease.Deadline.UnixMilli()
			out.NearestLabel = w.Lease.Label
		}
	}
	return out
}

// emitGoalUpdated publishes one committed non-clear goal transition. Callers
// invoke it only after the store mutation has released its own mutex.
func (s *Session) emitGoalUpdated(snap goal.Snapshot) {
	// Re-read the full shape so the payload carries the live wait list,
	// nearest summary, and spend progress (spec §7): the narrow Snapshot has
	// none of those. A goal cleared between the mutation and this read emits
	// the narrow form (no waits) rather than dropping the update.
	if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok && full.Objective == snap.Objective && full.Status == snap.Status {
		state := goalStateDataFromFull(full)
		s.emit(events.EventGoalUpdated, events.GoalUpdatedData{Goal: &state})
		return
	}
	state := goalStateData(snap)
	s.emit(events.EventGoalUpdated, events.GoalUpdatedData{Goal: &state})
}

// bumpGoalEventGen records one goal mutation's publication generation. Call
// with goalUpdateMu held, alongside the mutation's snapshot read. The
// returned generation travels with the snapshot to an unlock-then-emit site,
// where the gated emit drops the event when a newer mutation has since
// published; held-lock emit sites (SetGoal/ClearGoal/resume/setGoalTerminal)
// bump for the same suppression effect and emit normally.
func (s *Session) bumpGoalEventGen() uint64 {
	return s.goalEventGen.Add(1)
}

// emitGoalUpdatedAtGen publishes one committed non-clear goal transition
// unless a newer goal mutation has since published (gen < live generation).
// The check runs just before the emit with no locks held; a race that slips
// past it is benign — emitGoalUpdated re-reads the live full shape, so the
// payload still carries current state. Call after goalUpdateMu is released.
func (s *Session) emitGoalUpdatedAtGen(snap goal.Snapshot, gen uint64) {
	if s.goalEventGen.Load() != gen {
		return
	}
	s.emitGoalUpdated(snap)
}

// emitGoalWaitingAtGen publishes one EventGoalWaiting announcement for a
// freshly parked goal unless a newer goal mutation has since published. Same
// best-effort ordering as emitGoalUpdatedAtGen; an empty live-wait set still
// emits nothing. Call after goalUpdateMu is released.
func (s *Session) emitGoalWaitingAtGen(full goal.GoalSnapshot, gen uint64) {
	if s.goalEventGen.Load() != gen {
		return
	}
	s.emitGoalWaiting(full)
}

// emitCurrentGoalState snapshots and publishes the current store state. A
// missing snapshot deliberately carries a nil Goal so JSON encodes goal:null.
// This helper must never be called while Session.mu is held because emit reads
// session provenance through the same mutex.
func (s *Session) emitCurrentGoalState() {
	if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok {
		state := goalStateDataFromFull(full)
		s.emit(events.EventGoalUpdated, events.GoalUpdatedData{Goal: &state})
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
	// The emit runs under the held serializer (deferred Unlock), so it is
	// inherently the newest at emit time — but the bump still matters: it
	// marks this commit newer than any unlock-then-emit snapshot captured
	// before it, suppressing their stale events.
	s.bumpGoalEventGen()
	s.emitGoalUpdated(snap)
	return snap, true
}

// completeGoalIfConditionsSatisfied verifies the goal's registered stop-claim
// conditions check-on-claim and completes only when every condition is
// satisfied (spec §6), as one ordered unit under goalUpdateMu. It returns the
// terminal snapshot, the failing condition desc ("" when satisfied or
// condition-free), and whether the goal transitioned to complete.
//
// Locking: EvaluateExpectations is safe under goalUpdateMu — it copies the
// substrate pointer under a short store hold and performs every substrate
// lookup outside the store lock (see Store.EvaluateExpectations), so holding
// the registration serializer across verify+commit matches the
// registerGoalExpect precedent (registerGoalExpect holds goalUpdateMu across
// RegisterExpect, whose own pre-pass uses the same outside-the-store-lock
// substrate discipline). The substrate reads themselves (jobManager,
// controller, s.mu-backed ask/env reads via StatFile/LookupApproval) are
// point-in-time like every other goalUpdateMu-held registration path — never
// held across a timer/gate claim — so no new lock order is introduced.
// Holding the serializer closes the registration race: a concurrent
// RegisterExpect or retargeting Set cannot interleave between the verify and
// the commit, so a complete can never land on stale conditions.
func (s *Session) completeGoalIfConditionsSatisfied(now time.Time) (goal.Snapshot, string, bool) {
	s.goalUpdateMu.Lock()
	defer s.goalUpdateMu.Unlock()
	store := s.getOrCreateGoalStore()
	full, ok := store.GoalSnapshot()
	if !ok {
		return goal.Snapshot{}, "", false
	}
	if len(full.Conditions) > 0 {
		checks := store.EvaluateExpectations(full.Conditions)
		if _, failing := goal.VerifyConditions(full.Conditions, checks); failing != "" {
			return goal.Snapshot{}, failing, false
		}
	}
	if !store.SetTerminal(goal.StatusComplete, "", now) {
		return goal.Snapshot{}, "", false
	}
	snap, _ := store.Snapshot()
	// Like setGoalTerminal: the emit runs under the held serializer, so it is
	// inherently newest — the bump marks the commit for older
	// unlock-then-emit snapshots still in flight.
	s.bumpGoalEventGen()
	s.emitGoalUpdated(snap)
	return snap, "", true
}

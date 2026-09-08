// Package goal implements the per-session objective engine state for /goal.
package goal

import (
	"fmt"
	"sync"
	"time"
)

// Status represents the lifecycle state of a goal.
//
// Invariant (spec §1): status==waiting iff len(waits)>0 on every read outside
// the atomic claim step. ClaimFire holds the store lock across the whole
// remove-and-append consume, so no live read observes the interim — there is
// no "suspension," only the pre/post states. Every mutator below restores the
// invariant before releasing the lock.
type Status string

const (
	StatusActive   Status = "active"
	StatusWaiting  Status = "waiting"
	StatusComplete Status = "complete"
	StatusBlocked  Status = "blocked"
)

// Interim stall-judge caps (v1 mutation-count breaker). Slice 1 keeps this
// judge armed for non-parked loops (spec §9 rollout); the repetition ledger
// (Task 6) retires it. GoalTurnMaxRounds bounds the tool rounds within a
// single goal turn.
const (
	NoProgressLimit      = 3
	NeverProgressedLimit = 6
	GoalTurnMaxRounds    = 30
)

// Spend budgets (spec §5). Every park, wake, re-park, and resume path accrues
// against a budget or a bound — no goal is unbounded.
const (
	// DefaultMaxContinuations bounds total continuation turns per goal.
	DefaultMaxContinuations = 200
	// MaxContinuationsCap is the largest --extend value / configured cap.
	MaxContinuationsCap = 1000
	// DefaultGoalDeadline is the wall-clock budget from SetGoal; it runs
	// across waiting (frozen deadlines would let re-registration park forever).
	DefaultGoalDeadline = 4 * time.Hour
	// GoalDeadlineCap is the largest configured/extended deadline.
	GoalDeadlineCap = 24 * time.Hour
	// DefaultMaxParkedTotal caps total parked time per goal. With defaults it
	// is dead code by construction (parked ⊆ elapsed ≤ 4h < 24h); it binds
	// only when configured below the deadline.
	DefaultMaxParkedTotal = 24 * time.Hour
	// MaxParkedTotalCap is the largest configured/extended parked-total cap.
	MaxParkedTotalCap = 24 * time.Hour
)

// Verdicts are distinct — never collapsed (spec §1).
const (
	// VerdictNoProgress is the stall-detector verdict (repetition K or the
	// total non-advancement backstop).
	VerdictNoProgress = "no progress"
	// VerdictBudgetExhausted covers maxContinuations consumed OR
	// maxParkedTotal exhausted (spec §5 layering: at exact 24h/24h ties the
	// parked-total reports here, not deadline).
	VerdictBudgetExhausted = "budget exhausted"
	// VerdictDeadlineExceeded is wall-clock deadline expiry (runs across
	// waiting; distinct from budget and stall verdicts).
	VerdictDeadlineExceeded = "deadline exceeded"
)

// WaitingLostVerdict formats the terminal lost-wait verdict with its persisted
// cause (spec §1 rule 5). Non-rule-5 losses produce the honest notice plus
// re-drive instead (spec §2).
func WaitingLostVerdict(cause string) string { return "waiting lost: " + cause }

// Budgets is the orthogonal spend-budget triple (spec §5).
type Budgets struct {
	MaxContinuations  int
	UsedContinuations int
	Deadline          time.Time
	ParkedTotal       time.Duration
	MaxParkedTotal    time.Duration
}

// DefaultBudgets returns the shipped defaults anchored at SetGoal time: the
// deadline runs from the SetGoal instant (never re-anchored by retarget —
// Set preserves budgets wholesale so a fresh objective cannot launder spend).
func DefaultBudgets(now time.Time) Budgets {
	return Budgets{
		MaxContinuations: DefaultMaxContinuations,
		Deadline:         now.Add(DefaultGoalDeadline),
		MaxParkedTotal:   DefaultMaxParkedTotal,
	}
}

// TurnOutcome is one finished turn's ledger input (spec §4). The repetition
// ledger (Task 6) interprets these fields; the slice-1 table carries the
// value through for signature stability and ignores it.
type TurnOutcome struct {
	ActionFingerprint string
	ObservationClass  string
	ObservationHash   string
	StateDigest       string
	Mutated           bool
}

// AdvancementMarkers carries this turn's loss/advancement evidence to the
// decider (spec §1 rule 5). Check-before-reset ordering: rule 5 evaluates
// against these pre-reset markers; recording a loss resets the persisted
// AdvancementSinceLoss after the check.
type AdvancementMarkers struct {
	LossThisTurn      bool
	LossCause         string
	AdvancedSinceLoss bool
}

// GoalStep is the pure decider's output vocabulary (spec §1 verdict table).
type GoalStep string

const (
	StepDrive GoalStep = "drive"
	StepPark  GoalStep = "park"
	StepNudge GoalStep = "nudge"
	StepBlock GoalStep = "block"
)

// DecideGoalStep is the pure goal-gate table (spec §1 rules 1-8, slice-1
// scope): snapshot (the full persisted GoalSnapshot) × turnOutcome ×
// per-lease predicateTruth (evaluated outside, in GoalSnapshot.Waits order —
// the §3-top pre-read seam) × transient pendingWake claim batch × advancement
// markers × now (the sclock instant; time is an explicit input so the function
// stays pure).
//
// Slice-1 rules implemented: 1 (undelivered pendingWake → drive), 2
// (continuations/parked-total spent → block "budget exhausted"), 3 (deadline
// passed → block "deadline exceeded"), 4 (any live wait → park), 5 (lost this
// turn ∧ no live waits ∧ no advancement since loss → block "waiting lost:
// <cause>"), 8 (else drive). Rules 6-7 (stall-K graduation, total backstop)
// arrive with the ledger in Task 6; StepNudge is declared for that table and
// unreturned until then. The rule-1 terminal-flagged drive and the
// terminalPending latch (spec §1 R7 M-I1) are Task-2 gate state, not pure
// table: slice 1 returns a plain drive for undelivered wakes.
//
// Ties break top-to-bottom. Terminal snapshots are outside the domain
// (terminals short-circuit before the table); behavior on one is still total
// (falls through to drive) but meaningless — callers must not feed terminals.
func DecideGoalStep(snap GoalSnapshot, outcome TurnOutcome, predicateTruth []bool, pending []PendingWake, markers AdvancementMarkers, now time.Time) (GoalStep, string) {
	_ = outcome
	// predicateTruth is claim-routing input for the gate (Task 2 wires
	// claim-before-decide so rule 1 always observes claimable fires);
	// liveness here keys off FiredEpoch only. Unfired-but-expired leases
	// still park: the Task-2/4 claim machinery converts expiry into a
	// pendingWake entry, which rule 1 then drives exactly once.
	_ = predicateTruth
	// Rule 1: undelivered fire (transient claim batch or persisted backlog).
	if len(pending) > 0 || len(snap.PendingWake) > 0 {
		return StepDrive, ""
	}
	// Rule 2: spend budgets. Parked-total precedes the deadline check so an
	// exact 24h/24h tie reports "budget exhausted" (spec §5 layering). Zero
	// caps mean "unset" and never fire, so zero-value snapshots drive.
	if snap.Budgets.MaxContinuations > 0 && snap.Budgets.UsedContinuations >= snap.Budgets.MaxContinuations {
		return StepBlock, VerdictBudgetExhausted
	}
	if snap.Budgets.MaxParkedTotal > 0 && snap.Budgets.ParkedTotal >= snap.Budgets.MaxParkedTotal {
		return StepBlock, VerdictBudgetExhausted
	}
	// Rule 3: wall-clock deadline (runs across waiting).
	if !snap.Budgets.Deadline.IsZero() && !now.Before(snap.Budgets.Deadline) {
		return StepBlock, VerdictDeadlineExceeded
	}
	// Rule 4: any live (unfired) wait parks. A park reached with a same-turn
	// loss attaches the §2 notice to the parking decision (Task 2 gate).
	if hasLiveWait(snap.Waits) {
		return StepPark, ""
	}
	// Rule 5: lost this turn with no live waits and no advancement since the
	// loss blocks with the persisted cause; any other loss notifies and
	// re-drives (spec §§1-2).
	if markers.LossThisTurn && !markers.AdvancedSinceLoss {
		return StepBlock, WaitingLostVerdict(markers.LossCause)
	}
	// Rule 8: else drive. (Rules 6-7: Task 6 ledger.)
	return StepDrive, ""
}

// hasLiveWait reports whether any lease is unfired (FiredEpoch == 0).
func hasLiveWait(waits []Wait) bool {
	for _, w := range waits {
		if w.Live() {
			return true
		}
	}
	return false
}

// Goal is the per-session objective. Guarded by Store.mu.
type Goal struct {
	Objective        string
	Status           Status
	Iterations       int    // goal continuation turns taken
	NoProgressStreak int    // consecutive goal turns with no mutating tool call
	madeProgressOnce bool   // false until the first mutating turn; selects which no-progress limit applies
	StopReason       string // set on blocked or error termination
	reported         bool   // terminal report (EventGoalEnded) already emitted once
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// Waits is the live-lease registry (spec §2). Empty on every terminal.
	Waits []Wait
	// PendingWake is the persisted consumed-but-undelivered fire backlog
	// (spec §1): entries leave here in the same commit as the wake turn's
	// tail fold (Task 2/4), never at delivery.
	PendingWake []PendingWake
	// Budgets is the spend triple (spec §5). Preserved across retarget.
	Budgets Budgets
	// LossCause persists the rule-5 "waiting lost" cause for the terminal
	// verdict and pending loss notices (spec §7).
	LossCause string
	// AdvancementSinceLoss is the persisted advancement flag the loss
	// check-before-reset ordering reads and writes (spec §1 rule 5).
	AdvancementSinceLoss bool
}

// Snapshot is an immutable value copy of the goal for read surfaces (the status
// chip, /goal status, the appwire projection). The full persisted shape for
// the gate is GoalSnapshot; the authoritative step decision is DecideGoalStep.
type Snapshot struct {
	Objective        string
	Status           Status
	Iterations       int
	NoProgressStreak int
	StopReason       string
}

// GoalSnapshot is the full persisted goal read for the gate and the v2
// persistence mapping (spec §§1, 7): status, waits, pendingWake, budgets,
// advancement markers, loss cause, stop. Slices are copies — mutating them
// does not affect the store.
type GoalSnapshot struct {
	Objective            string
	Status               Status
	Iterations           int
	NoProgressStreak     int
	StopReason           string
	Waits                []Wait
	PendingWake          []PendingWake
	Budgets              Budgets
	LossCause            string
	AdvancementSinceLoss bool
}

// Store holds one goal per session behind its own mutex (mirrors agent TaskStore).
type Store struct {
	mu   sync.Mutex
	goal *Goal // nil = no goal set
	// nextWaitID mints wait_ids monotonically (wait_1, wait_2, ...) so ids
	// are deterministic in tests and never reused within the store lifetime
	// (no ABA across cancel/re-register).
	nextWaitID uint64
	// substrate is the session-owned predicate substrate consulted by
	// RegisterWait (spec §2). Set once by the session at wiring time; nil
	// means "no substrate" and every substrate kind rejects fail-closed.
	substrate Substrate
	// lastRejectReason names the most recent registration rejection (empty
	// after a success or Clear). Tests assert it names the failed check;
	// the Task-3 tool surface propagates it as the validation error.
	lastRejectReason string
}

// NewStore returns an empty Store.
func NewStore() *Store { return &Store{} }

// SetSubstrate wires the session-owned predicate substrate consulted by
// RegisterWait validation. It must be called before any substrate-kind
// registration; the UntilTime kind needs no substrate.
func (s *Store) SetSubstrate(sub Substrate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.substrate = sub
}

// LastRejectReason names the most recent registration rejection, or "" when
// the last RegisterWait succeeded (or none ran since Clear).
func (s *Store) LastRejectReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRejectReason
}

// Set creates a new active goal, replacing any previous goal. Retarget
// semantics (spec §§1, 3): live waits cleared, ledger reset, budgets kept (a
// fresh objective must not launder spend — the deadline is not re-anchored).
// Already-claimed pendingWake entries survive, marked Superseded: a retarget
// that wins between claim and kick routes the stale fire to a single no-op
// evaluation on the current objective (spec §3 superseded), never the old
// objective's wake and never a silent drop.
func (s *Store) Set(objective string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	budgets := DefaultBudgets(now)
	var carried []PendingWake
	if s.goal != nil {
		budgets = s.goal.Budgets
		for _, p := range s.goal.PendingWake {
			p.Superseded = true
			carried = append(carried, p)
		}
	}
	s.goal = &Goal{
		Objective:   objective,
		Status:      StatusActive,
		Budgets:     budgets,
		PendingWake: carried,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// Clear removes the current goal.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goal = nil
}

// Snapshot returns a value copy of the current goal, or (zero, false) if no goal is set.
func (s *Store) Snapshot() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil {
		return Snapshot{}, false
	}
	return s.snapLocked(), true
}

// GoalSnapshot returns the full persisted-shape read for the gate, or (zero,
// false) if no goal is set.
func (s *Store) GoalSnapshot() (GoalSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil {
		return GoalSnapshot{}, false
	}
	g := s.goal
	return GoalSnapshot{
		Objective:            g.Objective,
		Status:               g.Status,
		Iterations:           g.Iterations,
		NoProgressStreak:     g.NoProgressStreak,
		StopReason:           g.StopReason,
		Waits:                append([]Wait(nil), g.Waits...),
		PendingWake:          append([]PendingWake(nil), g.PendingWake...),
		Budgets:              g.Budgets,
		LossCause:            g.LossCause,
		AdvancementSinceLoss: g.AdvancementSinceLoss,
	}, true
}

// RegisterWait validates and installs one wait lease, parking the goal (spec
// §2). It returns the live lease and true on success.
//
// Dedupe/replace (spec §2): an identical idempotency key (kind, target
// identity, canonical predicate, deadline) dedupes — no new lease, the
// existing one is returned. The same target (kind + target identity) with a
// different deadline/predicate replaces: the old lease is removed and the new
// one validated fresh.
//
// Slice-1 validation: kind must be known, timeout defaults to 10m when zero
// and rejects above the 24h cap, model-controlled fields are size-capped; the
// UntilTime kind needs no substrate and registers. All substrate kinds
// (job/delegate/approval/event/child) reject fail-closed until Wave B wires
// the Substrate consultation with retained-terminal catch-up routing — never
// parked on an unvalidated target.
func (s *Store) RegisterWait(req WaitKind, now time.Time) (Wait, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.goal
	if g == nil {
		return Wait{}, false
	}
	if g.Status == StatusComplete || g.Status == StatusBlocked {
		return Wait{}, false
	}
	if !validKind(req.Kind) {
		return Wait{}, false
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultWaitTimeout
	}
	if timeout < 0 || timeout > MaxWaitTimeoutCap {
		return Wait{}, false
	}
	if !checkSizeCaps(req) {
		return Wait{}, false
	}
	norm := req
	norm.Timeout = timeout
	deadline := now.Add(timeout)
	key := idempotencyKey(norm, deadline)
	for _, w := range g.Waits {
		if w.Live() && w.Lease.IdempotencyKey == key {
			s.lastRejectReason = ""
			g.Status = StatusWaiting
			return w, true
		}
	}
	kept := make([]Wait, 0, len(g.Waits))
	for _, w := range g.Waits {
		if w.Live() && w.Lease.Kind == norm.Kind && w.Lease.Predicate.Target == norm.Target {
			continue // same-target re-register replaces the old lease
		}
		kept = append(kept, w)
	}
	live := 0
	for _, w := range kept {
		if w.Live() {
			live++
		}
	}
	if live >= MaxLiveWaitsPerGoal {
		s.lastRejectReason = fmt.Sprintf("wait registry full: max %d live waits per goal", MaxLiveWaitsPerGoal)
		return Wait{}, false
	}
	// Kinds with no durable substrate to resolve register substrate-free:
	// UntilTime (pure timer) and external-label events (fire via the
	// notification path or expire; Task 2). Every other kind validates
	// against the session substrate below, fail-closed.
	if norm.Kind == WaitUntilTime ||
		(norm.Kind == WaitUntilEvent && norm.EventSubtype == EventExternalLabel) {
		s.nextWaitID++
		w := Wait{Lease: Lease{
			WaitID:         fmt.Sprintf("wait_%d", s.nextWaitID),
			Kind:           norm.Kind,
			Predicate:      norm,
			Label:          defaultLabelFor(norm),
			Deadline:       deadline,
			RegisteredAt:   now,
			IdempotencyKey: key,
		}}
		g.Waits = append(kept, w)
		g.Status = StatusWaiting
		g.UpdatedAt = now
		s.lastRejectReason = ""
		return w, true
	}
	// Substrate-kind validation (spec §2, fail-closed): the target must exist
	// and be owned by the session tree. A live wake-capable target parks; a
	// retained-terminal job/delegate inside the record-retention window routes
	// to terminal catch-up (fire immediately with the terminal outcome as the
	// trigger excerpt) instead of parking; anything else rejects with the
	// reason named. Approval answers are excluded from catch-up by design: a
	// consumed answer must not refire.
	sub := s.substrate
	if sub == nil {
		s.lastRejectReason = fmt.Sprintf("%s %q: no wait substrate wired", norm.Kind, norm.Target)
		return Wait{}, false
	}
	catchUp := ""
	switch norm.Kind {
	case WaitUntilJob:
		liveT, retained, excerpt, ok := sub.LookupJob(norm.Target)
		if !ok {
			s.lastRejectReason = fmt.Sprintf("unknown job %q: no record in this session tree", norm.Target)
			return Wait{}, false
		}
		switch {
		case liveT:
		case retained:
			catchUp = excerpt
		default:
			s.lastRejectReason = fmt.Sprintf("job %q is neither running nor retained-terminal", norm.Target)
			return Wait{}, false
		}
	case WaitUntilDelegate:
		liveT, retained, excerpt, ok := sub.LookupDelegate(norm.Target)
		if !ok {
			s.lastRejectReason = fmt.Sprintf("unknown delegate %q: no record in this session tree", norm.Target)
			return Wait{}, false
		}
		switch {
		case liveT:
		case retained:
			catchUp = excerpt
		default:
			s.lastRejectReason = fmt.Sprintf("delegate %q is neither running/settling/stopping nor retained-terminal", norm.Target)
			return Wait{}, false
		}
	case WaitUntilApproval:
		if !sub.LookupApproval(norm.Target, norm.AskGeneration) {
			s.lastRejectReason = fmt.Sprintf("no live ask for content key %q generation %q", norm.Target, norm.AskGeneration)
			return Wait{}, false
		}
	case WaitUntilChild:
		if !sub.LookupChild(norm.Target) {
			s.lastRejectReason = fmt.Sprintf("unknown child session %q: not a known descendant", norm.Target)
			return Wait{}, false
		}
	case WaitUntilEvent:
		switch norm.EventSubtype {
		case EventFileModified:
			baseline, ok := sub.StatFile(norm.Target)
			if !ok {
				s.lastRejectReason = fmt.Sprintf("unstatable file %q: not inside the session sandbox or missing", norm.Target)
				return Wait{}, false
			}
			norm.Baseline = baseline
		case EventHTTPMatch:
			if !validHTTPURL(norm.Target) || !sub.CheckURL(norm.Target, timeout) {
				s.lastRejectReason = fmt.Sprintf("rejected URL %q: must be well-formed with an explicit timeout under the session egress policy", norm.Target)
				return Wait{}, false
			}
		default:
			s.lastRejectReason = fmt.Sprintf("unknown event subtype %q", norm.EventSubtype)
			return Wait{}, false
		}
	default:
		s.lastRejectReason = fmt.Sprintf("unknown wait kind %q", norm.Kind)
		return Wait{}, false
	}
	// The file-baseline snapshot above changes the canonical predicate; the
	// precomputed idempotency key and deadline-keyed lookups must follow it,
	// or a same-file re-register would replace instead of dedupe. Recompute
	// the key and re-run the dedupe scan under the same lock.
	if norm.Kind == WaitUntilEvent && norm.EventSubtype == EventFileModified {
		key = idempotencyKey(norm, deadline)
		for _, w := range kept {
			if w.Live() && w.Lease.IdempotencyKey == key {
				s.lastRejectReason = ""
				g.Status = StatusWaiting
				return w, true
			}
		}
	}
	s.nextWaitID++
	w := Wait{Lease: Lease{
		WaitID:         fmt.Sprintf("wait_%d", s.nextWaitID),
		Kind:           norm.Kind,
		Predicate:      norm,
		Label:          defaultLabelFor(norm),
		Deadline:       deadline,
		RegisteredAt:   now,
		IdempotencyKey: key,
	}}
	if catchUp != "" {
		// Terminal catch-up (spec §2): the target is already terminal inside
		// the retention window, so fire immediately with the terminal outcome
		// as the trigger excerpt instead of parking. The pendingWake entry
		// drives exactly one evaluation turn via rule 1; no live lease
		// remains and the goal stays active.
		g.Waits = kept
		g.PendingWake = append(g.PendingWake, PendingWake{WaitID: w.Lease.WaitID, Trigger: catchUp, FiredAt: now})
		g.UpdatedAt = now
		s.lastRejectReason = ""
		return w, true
	}
	g.Waits = append(kept, w)
	g.Status = StatusWaiting
	g.UpdatedAt = now
	s.lastRejectReason = ""
	return w, true
}

// CancelWait removes one live lease by wait_id and disarms it (spec §7). A
// claimed pendingWake entry still drives once with the cancellation noted —
// cancel removes the live lease only and never swallows a consumed fire, so it
// returns false when the id names no live lease. Clearing the last live lease
// returns the goal to active; otherwise it stays waiting.
func (s *Store) CancelWait(waitID string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.goal
	if g == nil {
		return false
	}
	found := false
	kept := make([]Wait, 0, len(g.Waits))
	for _, w := range g.Waits {
		if w.Live() && w.Lease.WaitID == waitID {
			found = true
			continue
		}
		kept = append(kept, w)
	}
	if !found {
		return false
	}
	g.Waits = kept
	if !hasLiveWait(kept) && g.Status == StatusWaiting {
		g.Status = StatusActive
	}
	g.UpdatedAt = now
	return true
}

// ClaimFire atomically consumes one live lease's fire (spec §§2-3: the
// claimWaitFireLocked analogue): the lease leaves waits[] exactly once into
// the persisted pendingWake backlog, which the wake turn consumes. The
// remove-and-append holds the store lock throughout, so the consume mark and
// the backlog write are one atomic mutation — a repeat claim finds no live
// lease and returns false (the fired_epoch dedupe in registry form: exactly
// one kick per fire, no same-tick double-claim). With no live leases left the
// goal returns to active so rule 1 drives; remaining live leases keep it
// waiting while the pending entry still drives once via rule 1.
func (s *Store) ClaimFire(waitID, trigger string, now time.Time) (PendingWake, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.goal
	if g == nil {
		return PendingWake{}, false
	}
	for i, w := range g.Waits {
		if w.Lease.WaitID != waitID || !w.Live() {
			continue
		}
		kept := make([]Wait, 0, len(g.Waits)-1)
		kept = append(kept, g.Waits[:i]...)
		kept = append(kept, g.Waits[i+1:]...)
		g.Waits = kept
		entry := PendingWake{WaitID: waitID, Trigger: trigger, FiredAt: now}
		g.PendingWake = append(g.PendingWake, entry)
		if !hasLiveWait(kept) && g.Status == StatusWaiting {
			g.Status = StatusActive
		}
		g.UpdatedAt = now
		return entry, true
	}
	return PendingWake{}, false
}

// SetTerminal transitions an active or waiting goal to a terminal status (used
// by update_goal and terminateGoalOnError). Waits are cleared on every
// terminal transition (spec §1) so no terminal goal projects a stale
// waiting_on[]; the consumed-fire backlog survives for the Task-2 gate to
// drain. Returns false (no-op) if there is no goal or it is already terminal.
func (s *Store) SetTerminal(status Status, reason string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil || (s.goal.Status != StatusActive && s.goal.Status != StatusWaiting) {
		return false
	}
	s.goal.Status = status
	s.goal.StopReason = reason
	s.goal.Waits = nil
	s.goal.UpdatedAt = now
	return true
}

// DrainPendingWake removes and returns the persisted consumed-but-undelivered
// fire backlog (spec §1): the wake turn's tail fold calls this in the same
// commit that folds the turn, so a crash between kick and fold re-drives a
// safe fresh evaluation on restore instead of losing the wake (spec §7).
// Returns nil when there is no backlog.
func (s *Store) DrainPendingWake(now time.Time) []PendingWake {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.goal
	if g == nil || len(g.PendingWake) == 0 {
		return nil
	}
	drained := append([]PendingWake(nil), g.PendingWake...)
	g.PendingWake = nil
	g.UpdatedAt = now
	return drained
}

// DrainPendingWakeIDs removes and returns the backlog entries naming one of
// ids (spec §1 coalescing): the wake turn's tail fold consumes exactly the
// batch its kick delivered, while entries claimed mid-turn (never marked
// delivered) stay queued to drive their own wake. Returns nil when nothing
// matched.
func (s *Store) DrainPendingWakeIDs(ids []string, now time.Time) []PendingWake {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.goal
	if g == nil || len(g.PendingWake) == 0 || len(ids) == 0 {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var drained, kept []PendingWake
	for _, p := range g.PendingWake {
		if want[p.WaitID] {
			drained = append(drained, p)
		} else {
			kept = append(kept, p)
		}
	}
	if len(drained) == 0 {
		return nil
	}
	g.PendingWake = kept
	g.UpdatedAt = now
	return drained
}

// TakeTerminalReport returns (snapshot, true) exactly once for a terminal goal —
// the first time it is called after the goal stops — and (zero, false) thereafter
// (or when there is no goal, or the goal is still active or waiting). This makes the
// EventGoalEnded terminal report fire exactly once even though a terminated goal
// lingers in the store until /goal clear and the gate runs at every turn tail.
func (s *Store) TakeTerminalReport() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil || s.goal.reported {
		return Snapshot{}, false
	}
	switch s.goal.Status {
	case StatusComplete, StatusBlocked:
	default:
		return Snapshot{}, false
	}
	s.goal.reported = true
	return s.snapLocked(), true
}

// RecordContinuation folds one finished goal turn's progress signal into the
// streak and accrues it against the continuation budget. Returns the
// post-update snapshot and whether the goal is still active (i.e. whether the
// gate should issue another continuation).
//
// A waiting goal skips every fold and burns zero budget (spec §1: parked goals
// accrue zero stall signal) — the Task-2 gate routes wake/evaluation/expiry
// turns around this fold entirely. An active goal folds the interim v1 judge
// (two-tier mutation breaker, kept armed for non-parked loops in slice 1 per
// spec §9); the Task-6 ledger retires it.
func (s *Store) RecordContinuation(progressed bool, now time.Time) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil {
		return Snapshot{}, false
	}
	g := s.goal
	if g.Status == StatusWaiting {
		return s.snapLocked(), false
	}
	if g.Status != StatusActive {
		return s.snapLocked(), false
	}
	g.Iterations++
	g.Budgets.UsedContinuations++
	g.UpdatedAt = now
	if progressed {
		g.madeProgressOnce = true
		g.NoProgressStreak = 0
	} else {
		g.NoProgressStreak++
	}
	// Two-tier bound: stop quickly once the goal has worked then stalled
	// (NoProgressLimit); give a never-progressing goal more leading room
	// (NeverProgressedLimit) but still bound it so it cannot run forever.
	limit := NoProgressLimit
	if !g.madeProgressOnce {
		limit = NeverProgressedLimit
	}
	if g.NoProgressStreak >= limit {
		g.Status = StatusBlocked
		g.StopReason = VerdictNoProgress
	}
	return s.snapLocked(), g.Status == StatusActive
}

// PersistSnapshot returns a full-fidelity read of the current goal including
// madeProgressOnce and timestamps — the fields omitted from the public Snapshot
// type — so the session can persist them to meta.json. ok is false when no goal
// is set. "reported" is intentionally not returned; it is runtime-only state.
//
// Slice-1 note: waits, pendingWake, budgets, and loss fields persist in Task 4
// (GoalSnapshot v2 through agent/schema); until then a restore carries the v1
// shape only and re-registration re-parks.
func (s *Store) PersistSnapshot() (objective, status, stopReason string, iterations, noProgressStreak int, madeProgressOnce bool, created, updated time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil {
		return "", "", "", 0, 0, false, time.Time{}, time.Time{}, false
	}
	g := s.goal
	return g.Objective, string(g.Status), g.StopReason, g.Iterations, g.NoProgressStreak, g.madeProgressOnce, g.CreatedAt, g.UpdatedAt, true
}

// Restore installs a fully-reconstructed goal from persisted primitives, replacing
// any current goal. The "reported" flag is left false (runtime-only state always
// starts fresh on load). It is called by the session restore path to bring the
// goal store back to the state captured in meta.json.
//
// Slice-1 note: Task 4 extends this path to the v2 shape (waits re-validated,
// budgets backfilled per the §7 table, dormant-blocked restore). Until then it
// round-trips the v1 shape; a restored "waiting" status with no waits resolves
// to active on first read so the store never strands a goal parked on nothing.
func (s *Store) Restore(objective, status, stopReason string, iterations, noProgressStreak int, madeProgressOnce bool, created, updated time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status(status)
	if st == StatusWaiting {
		st = StatusActive
	}
	s.goal = &Goal{
		Objective:        objective,
		Status:           st,
		Iterations:       iterations,
		NoProgressStreak: noProgressStreak,
		madeProgressOnce: madeProgressOnce,
		StopReason:       stopReason,
		Budgets:          DefaultBudgets(created),
		CreatedAt:        created,
		UpdatedAt:        updated,
	}
}

// snapLocked returns a Snapshot of s.goal. Caller must hold s.mu.
func (s *Store) snapLocked() Snapshot {
	g := s.goal
	return Snapshot{
		Objective:        g.Objective,
		Status:           g.Status,
		Iterations:       g.Iterations,
		NoProgressStreak: g.NoProgressStreak,
		StopReason:       g.StopReason,
	}
}

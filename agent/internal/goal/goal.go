// Package goal implements the per-session objective engine state for /goal.
package goal

import (
	"sync"
	"time"
)

// Status represents the lifecycle state of a goal.
type Status string

const (
	StatusActive   Status = "active"
	StatusComplete Status = "complete"
	StatusBlocked  Status = "blocked"
)

// Caps. See spec §1. There is no iteration cap — the no-progress breaker is the
// sole automatic stop, and it is two-tier: a goal that has worked then stalled
// stops after NoProgressLimit consecutive no-progress turns; a goal that has never
// made a mutating tool call gets more leading-investigation room
// (NeverProgressedLimit) but is still bounded, so a read-only goal that never
// self-declares cannot run forever. GoalTurnMaxRounds bounds the tool rounds within
// a single goal turn.
const (
	NoProgressLimit      = 3
	NeverProgressedLimit = 6
	GoalTurnMaxRounds    = 30
)

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
}

// Snapshot is an immutable value copy of the goal for read surfaces (the status
// chip, /goal status, the appwire projection). The authoritative continue/stop
// decision lives in RecordContinuation, not a pure predicate over this.
type Snapshot struct {
	Objective        string
	Status           Status
	Iterations       int
	NoProgressStreak int
	StopReason       string
}

// Store holds one goal per session behind its own mutex (mirrors agent TaskStore).
type Store struct {
	mu   sync.Mutex
	goal *Goal // nil = no goal set
}

// NewStore returns an empty Store.
func NewStore() *Store { return &Store{} }

// Set creates a new active goal, replacing any previous goal.
func (s *Store) Set(objective string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goal = &Goal{
		Objective: objective,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
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

// SetTerminal transitions an active goal to a terminal status (used by update_goal
// and terminateGoalOnError). Returns false (no-op) if there is no active goal.
func (s *Store) SetTerminal(status Status, reason string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil || s.goal.Status != StatusActive {
		return false
	}
	s.goal.Status = status
	s.goal.StopReason = reason
	s.goal.UpdatedAt = now
	return true
}

// TakeTerminalReport returns (snapshot, true) exactly once for a terminal goal —
// the first time it is called after the goal stops — and (zero, false) thereafter
// (or when there is no goal, or the goal is still active). This makes the
// EventGoalEnded terminal report fire exactly once even though a terminated goal
// lingers in the store until /goal clear and the gate runs at every turn tail.
func (s *Store) TakeTerminalReport() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil || s.goal.Status == StatusActive || s.goal.reported {
		return Snapshot{}, false
	}
	s.goal.reported = true
	return s.snapLocked(), true
}

// RecordContinuation folds one finished goal turn's progress signal into the streak
// and increments Iterations. Returns the post-update snapshot and whether the goal is
// still active (i.e. whether the gate should issue another continuation).
//
// Every no-progress turn accrues NoProgressStreak; a progressed turn resets it.
// The block threshold is two-tier (NoProgressLimit once the goal has made a mutating
// call, the larger NeverProgressedLimit while it never has).
func (s *Store) RecordContinuation(progressed bool, now time.Time) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil {
		return Snapshot{}, false
	}
	g := s.goal
	if g.Status != StatusActive {
		return s.snapLocked(), false
	}
	g.Iterations++
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
		g.StopReason = "no progress"
	}
	return s.snapLocked(), g.Status == StatusActive
}

// PersistSnapshot returns a full-fidelity read of the current goal including
// madeProgressOnce and timestamps — the fields omitted from the public Snapshot
// type — so the session can persist them to meta.json. ok is false when no goal
// is set. "reported" is intentionally not returned; it is runtime-only state.
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
func (s *Store) Restore(objective, status, stopReason string, iterations, noProgressStreak int, madeProgressOnce bool, created, updated time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goal = &Goal{
		Objective:        objective,
		Status:           Status(status),
		Iterations:       iterations,
		NoProgressStreak: noProgressStreak,
		madeProgressOnce: madeProgressOnce,
		StopReason:       stopReason,
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

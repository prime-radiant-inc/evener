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

// Caps. See spec §1. NoProgressLimit is the primary stuck-detector;
// DefaultMaxIterations is the backstop; GoalTurnMaxRounds bounds per-turn rounds.
const (
	DefaultMaxIterations = 10
	NoProgressLimit      = 3
	GoalTurnMaxRounds    = 30
)

// Goal is the per-session objective. Guarded by Store.mu.
type Goal struct {
	Objective        string
	Status           Status
	Iterations       int    // goal continuation turns taken
	NoProgressStreak int    // consecutive goal turns with no mutating tool call
	madeProgressOnce bool   // grace: NoProgressStreak accrues only after the first progressed turn
	StopReason       string // set on blocked or error termination
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Snapshot is an immutable value copy for the pure gate decision and read fields.
type Snapshot struct {
	Objective        string
	Status           Status
	Iterations       int
	NoProgressStreak int
	StopReason       string
}

// ShouldContinue is pure: the gate continues iff the goal is active and under both caps.
func ShouldContinue(s Snapshot) bool {
	return s.Status == StatusActive &&
		s.Iterations < DefaultMaxIterations &&
		s.NoProgressStreak < NoProgressLimit
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

// RecordContinuation folds one finished goal turn's progress signal into the streak
// and increments Iterations. Returns the post-update snapshot and whether the goal is
// still active (i.e. whether the gate should issue another continuation).
//
// Grace period: NoProgressStreak only accrues after the first progressed turn, so
// read-heavy investigation at the start of a goal is never penalized.
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
	} else if g.madeProgressOnce {
		g.NoProgressStreak++
	}
	if g.NoProgressStreak >= NoProgressLimit {
		g.Status = StatusBlocked
		g.StopReason = "no progress"
	}
	return s.snapLocked(), g.Status == StatusActive
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

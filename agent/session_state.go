package agent

import (
	"context"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
)

// SessionState represents the current lifecycle state of a session.
type SessionState string

const (
	// SessionIdle indicates the session is not currently processing.
	SessionIdle SessionState = "idle"
	// SessionProcessing indicates the session is actively processing.
	SessionProcessing SessionState = "active"
	// SessionAwaiting indicates the session is idle with the ball in the
	// user's court: the last completed turn ended with agent output and no
	// autonomous work (goal kick, pending notifications, queued input, live
	// child subagents) is in flight. It is the daemon-truth source for the
	// hub's "needs you" attention state (spec: attention-status-model v5).
	// The string must stay byte-equal to appwire.ThreadStatusAwaiting
	// ("awaiting"): every status pass-through switch on the wire journey
	// defaults unrecognized strings to idle, so changing this string would
	// silently downgrade an awaiting session to idle across /status, the
	// roster, and the NeedsYou tier.
	SessionAwaiting SessionState = "awaiting"
	// SessionClosed indicates the session has been closed.
	SessionClosed SessionState = "closed"
)

// State returns the current session state.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// WireState is the externally-reported session state. It equals State()
// except for one honest override: an idle session whose autonomy is still in
// flight (live child subagents, undelivered job notifications, queued input)
// reads as "active" — a delegating parent is working through its children,
// not settled (spec v5, round-4 A6).
//
// Precedence: the override upgrades idle ONLY. awaiting always projects as
// awaiting, even with autonomy in flight — a session that asked its user
// (ask-user-question design) cannot proceed without them, and masking the
// question as "working" would deadlock: the wakes that could move the
// session are gated behind the very answer the user was never told to give.
// TestWireState_AwaitingOutranksAutonomy pins this.
func (s *Session) WireState() string {
	state := s.State()
	if state == SessionIdle && s.autonomyInFlight() {
		return string(SessionProcessing)
	}
	return string(state)
}

// autonomyInFlight reports whether autonomous work will move this session
// without user input: pending job notifications, queued input, or live child
// subagents. Reads take each signal's own lock sequentially — never nested —
// per the settle lock discipline (spec v5). A restored-but-unkicked goal is
// deliberately NOT autonomy: nothing will move until the user acts, and amber
// is what surfaces that stall.
func (s *Session) autonomyInFlight() bool {
	if s.peekNotifications() > 0 {
		return true
	}
	if s.QueueDepth() > 0 {
		return true
	}
	return len(s.liveSubagentSessions()) > 0
}

// Meta returns the current session metadata without the conversation history.
func (s *Session) Meta() schema.SessionMeta {
	originalPrompt := s.extractOriginalPrompt()

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.sclock().Now().UTC()
	parentID := s.cfg.spawn.parentSessionID
	divergence := 0
	isSubagent := s.cfg.spawn.parentSessionID != ""
	if s.fork.divergence > 0 {
		parentID = s.fork.parentID
		divergence = s.fork.divergence
		isSubagent = false
	}
	restoreRoot := ""
	if s.worktreeRestoreEnv != nil {
		restoreRoot = s.worktreeRestoreEnv.WorkingDirectory()
	}
	return schema.SessionMeta{
		ID:                  s.id,
		ProfileID:           s.profile.ID(),
		Model:               s.profile.Model(),
		CheapModel:          s.profile.CheapModelRefString(),
		Config:              s.cfg.toSnapshot(),
		EnvInfo:             s.envInfo,
		CreatedAt:           s.createdAt,
		UpdatedAt:           now,
		TurnCount:           s.modelResponses,
		LastInputTokens:     s.contextMgr.LastInputTokens(),
		Name:                s.naming.value,
		NameSource:          s.naming.source,
		NameUpdatedAt:       s.naming.updated,
		OriginalPrompt:      originalPrompt,
		ParentSessionID:     parentID,
		DivergenceTurn:      divergence,
		ForkLabel:           s.fork.label,
		IsSubagent:          isSubagent,
		Goal:                s.goalSnapshotForMeta(),
		PinnedNote:          s.pinnedNote,
		WorktreePath:        s.worktreeCurrentPath,
		WorktreeManaged:     s.worktreeCurrentManaged,
		WorktreeRestoreRoot: restoreRoot,
	}
}

// goalSnapshotForMeta calls PersistSnapshot on the goal store and maps the
// resulting primitives to a *schema.GoalSnapshot. Returns nil when no goal is
// set (PersistSnapshot ok==false). Called from Meta() which holds s.mu; the
// goal store has its own independent mutex, so there is no lock-order issue.
func (s *Session) goalSnapshotForMeta() *schema.GoalSnapshot {
	obj, status, stopReason, iters, streak, madeProgress, created, updated, ok := s.getOrCreateGoalStore().PersistSnapshot()
	if !ok {
		return nil
	}
	return &schema.GoalSnapshot{
		Objective:        obj,
		Status:           status,
		Iterations:       iters,
		NoProgressStreak: streak,
		MadeProgressOnce: madeProgress,
		StopReason:       stopReason,
		CreatedAt:        created,
		UpdatedAt:        updated,
	}
}

// ContextPressure returns the estimated context pressure as a fraction (0.0–1.0).
// Returns 0 if the context manager is not initialized.
func (s *Session) ContextPressure() float64 {
	if s.contextMgr == nil {
		return 0
	}
	s.mu.Lock()
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	return s.contextMgr.Pressure(hist, 0)
}

func (s *Session) closingOrClosedLocked() bool {
	return s.closing || s.state == SessionClosed
}

func (s *Session) setStateIfOpenLocked(state SessionState) {
	if s.closingOrClosedLocked() {
		return
	}
	s.state = state
}

func (s *Session) finishProcessingAtBoundary(ctx context.Context, state SessionState) {
	transitioned := false
	s.mu.Lock()
	if s.state == SessionProcessing && !s.closingOrClosedLocked() {
		s.state = state
		transitioned = true
	}
	s.mu.Unlock()
	if transitioned {
		if err := s.drainPendingWatchSends(ctx); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: "watch send retry at processing boundary failed: " + err.Error()})
		}
		s.finishActiveProvenance()
	}
}

func (s *Session) abortIfClosing(ctx context.Context) error {
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Session) errIfClosing() error {
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		return context.Canceled
	}
	return nil
}

func (s *Session) abortResponseProcessing(ctx context.Context) error {
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		s.emit(events.EventError, errorDataFromError(err))
		return err
	}
	return nil
}

func (s *Session) withResponseSideEffects(ctx context.Context, fn func()) error {
	s.responseSideEffectsMu.Lock()
	defer s.responseSideEffectsMu.Unlock()
	if err := s.abortResponseProcessing(ctx); err != nil {
		return err
	}
	fn()
	return nil
}

func (s *Session) isClosingOrClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closingOrClosedLocked()
}

// settleTerminalState decides the terminal session state at the drain-loop
// settle. It runs ONLY on the clean-completion path (interrupted and failed
// turns return from ProcessInputKind before the settle), so turn outcome is
// implied by reachability. awaiting arms only when the turn produced
// user-visible output and nothing autonomous will move the session next.
func settleTerminalState(hadOutput, goalKicked, notifsPending, queuePending, childrenLive bool) SessionState {
	if !hadOutput || goalKicked || notifsPending || queuePending || childrenLive {
		return SessionIdle
	}
	return SessionAwaiting
}

// recomputeRestoredState reruns deriveRestoredState — the single
// resume-derivation function (session_tools_ask.go) — for a restored
// session, now that history, goal restore, and (unless deferred for nested
// delegate reconstruction) notification/watch-send side effects are all in
// place. It only ever upgrades from idle: the initial derivation in
// RestoreSession already decided from history alone (including awaiting,
// when this second pass is not needed), so this call exists purely to rule
// an upgrade back out once autonomy signals that were not yet restored the
// first time — live children, pending notifications, queued input — are
// available to check. Restored active goals are deliberately not autonomy —
// they are not re-kicked on restore ("loaded but idle"), so amber is what
// surfaces the stall (spec v5, round-3 A2).
func (s *Session) recomputeRestoredState() {
	s.mu.Lock()
	idle := s.state == SessionIdle && !s.closingOrClosedLocked()
	target := deriveRestoredState(s.history)
	s.mu.Unlock()
	if !idle || target != SessionAwaiting {
		return
	}
	if s.autonomyInFlight() {
		return
	}
	s.mu.Lock()
	if s.state == SessionIdle && !s.closingOrClosedLocked() {
		s.state = SessionAwaiting
	}
	s.mu.Unlock()
}

// armAwaitingAtSettle upgrades idle -> awaiting at the drain-loop settle when
// settleTerminalState says the ball is in the user's court. It runs after
// settleGoalOnIdle (so the goal kick is known) and before the EventSessionEnd
// emit (so the emitted State carries the upgrade). The upgrade respects the
// same closed-guard as finishProcessingAtBoundary and only ever upgrades from
// SessionIdle, so interrupt/failure paths (which never reach the settle) and
// closed sessions are untouched.
func (s *Session) armAwaitingAtSettle(hadOutput, goalKicked bool) {
	target := settleTerminalState(hadOutput, goalKicked,
		s.peekNotifications() > 0, s.QueueDepth() > 0, len(s.liveSubagentSessions()) > 0)
	if target != SessionAwaiting {
		return
	}
	s.mu.Lock()
	if s.state == SessionIdle && !s.closingOrClosedLocked() {
		s.state = SessionAwaiting
	}
	s.mu.Unlock()
}

package agent

import (
	"context"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
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
	// silently downgrade an awaiting session to idle across AppWire, the roster,
	// and the NeedsYou tier.
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

// awaitingOrHasPendingAsk reports whether the session is SessionAwaiting or
// has an unresolved ask_user question, sampling state and the pending set
// under one lock. The drain-ladder gate (session_lifecycle.go) needs both
// facts as of the SAME instant: two separate locked calls (State() then
// askPendingCount()) could observe a state transition or an askPending
// mutation land between them.
func (s *Session) awaitingOrHasPendingAsk() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == SessionAwaiting || len(s.askPending) > 0
}

// WireState is the externally-reported session state. It equals State()
// except for one override: an idle session with undelivered job notifications
// or claimable queued input reads as "active" because work the session owns
// can resume it without user input. A queue parked by a Stop is not claimable
// and reads idle -- nothing will move it until the user acts (kata wms7).
// Live child activity belongs to the child's wire state, not the settled
// parent's.
//
// Precedence: the override upgrades idle ONLY. awaiting always projects as
// awaiting, even with autonomy in flight — a session that asked its user
// (ask-user-question design) cannot proceed without them, and masking the
// question as "working" would deadlock: the wakes that could move the
// session are gated behind the very answer the user was never told to give.
// TestWireState_AwaitingOutranksAutonomy pins this.
func (s *Session) WireState() string {
	state := s.State()
	if state == SessionIdle && s.sessionWorkPending() {
		return string(SessionProcessing)
	}
	return string(state)
}

// sessionWorkPending reports whether work owned by this session can resume it
// without user input. Child activity is excluded because it is projected on
// the child session, while its eventual notification is included once queued.
func (s *Session) sessionWorkPending() bool {
	return s.peekNotifications() > 0 || s.pendingQueueDepth() > 0 || s.hasRunnableClientMutationStart() || s.hasPendingDelegateDeliveries() || s.hasPendingRootDelegateAttention() || s.hasPendingDelegateAttentionArmRetry() || s.hasPendingStableDelegateAttention() || (s.jobManager != nil && s.jobManager.hasPendingStableWatchSettlementRetry())
}

func (s *Session) hasPendingStableDelegateAttention() bool {
	return s != nil && s.isRootDelegateAttentionReceiver() && s.delegateController.hasPendingDelegateAttention()
}

// autonomyInFlight reports whether autonomous work will move this session
// without user input: pending job notifications, queued input, or live child
// subagents. Reads take each signal's own lock sequentially — never nested —
// per the settle lock discipline (spec v5). A restored-but-unkicked goal is
// deliberately NOT autonomy: nothing will move until the user acts, and amber
// is what surfaces that stall.
func (s *Session) autonomyInFlight() bool {
	if s.sessionWorkPending() {
		return true
	}
	return len(s.liveSubagentSessions()) > 0
}

// cumulativeUsageSnapshot converts the context manager's llm.Usage total to
// the lossy schema.CumulativeUsage persisted in SessionMeta (nil pointers→0,
// Raw dropped).
func cumulativeUsageSnapshot(u llm.Usage) schema.CumulativeUsage {
	cacheRead := int64(0)
	if u.CacheReadTokens != nil {
		cacheRead = int64(*u.CacheReadTokens)
	}
	return schema.CumulativeUsage{
		InputTokens:     int64(u.InputTokens),
		OutputTokens:    int64(u.OutputTokens),
		CacheReadTokens: cacheRead,
		TotalTokens:     int64(u.TotalTokens),
	}
}

// Meta returns the current session metadata without the conversation history.
// Its notes fields are the published committed cut, so an envelope sample taken
// while a notes mutation is mid-save sees the pre-mutation values instead of
// the staged ones.
func (s *Session) Meta() schema.SessionMeta {
	human, agentNote, urls, _ := s.notesProjectionSnapshot()
	return s.metaWithNotes(human, agentNote, urls)
}

// metaWithNotes is Meta with an explicit notes cut. Meta passes the published
// committed cut; saveSessionMetaLocked passes the live staged store, because
// that write is the durability point for a mutation whose value is still
// staged — handing it the published cut would persist the pre-mutation value
// and lose the mutation on the next restore.
func (s *Session) metaWithNotes(human, agentNote string, urls []schema.SessionURL) schema.SessionMeta {
	originalPrompt := s.extractOriginalPrompt()

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.sclock().Now().UTC()
	parentID := s.cfg.spawn.parentSessionID
	divergence := 0
	// The persisted flag uses the same predicate as the ask_user root-only gate:
	// a bare `serve --resume <delegate-id>` restores with an empty spawn carrier
	// (spawn is json:"-", never persisted), so deriving the flag from cfg.spawn
	// alone erases a resumed delegate's lineage on its next autosave.
	// isSubagentSession() takes no lock, so calling it under s.mu is safe.
	isSubagent := s.isSubagentSession()
	if s.fork.divergence > 0 {
		parentID = s.fork.parentID
		divergence = s.fork.divergence
	} else if parentID == "" {
		// A live spawn wins; the persisted parent covers the resume that left the
		// carrier empty, so the delegate keeps the parent row the hub nests it
		// under instead of being rewritten as a parentless subagent.
		parentID = s.restoredMetaParentSessionID
	}
	restoreRoot := ""
	if s.worktreeRestoreEnv != nil {
		restoreRoot = s.worktreeRestoreEnv.WorkingDirectory()
	}
	jobTreeRootSessionID := ""
	jobTreeRevision := uint64(0)
	if s.jobActivityClock != nil {
		if s.jobActivityClock.rootSessionID != "" && s.jobActivityClock.rootSessionID != s.id {
			jobTreeRootSessionID = s.jobActivityClock.rootSessionID
		}
		jobTreeRevision = s.jobActivityClock.revision.Load()
	}
	skills := s.skillLifecycle.Clone()
	skills.PinnedNoteGen = s.pinnedNoteGen
	return schema.SessionMeta{
		ID:                       s.id,
		ProfileID:                s.profile.ID(),
		Model:                    s.profile.Model(),
		CheapModel:               s.profile.CheapModelRefString(),
		VisionModel:              s.cfg.VisionModel,
		Config:                   s.cfg.toSnapshot(),
		ReasoningEffortEscalated: s.loopEffortEscalated,
		EnvInfo:                  s.envInfo,
		CreatedAt:                s.createdAt,
		UpdatedAt:                now,
		TurnCount:                s.modelResponses,
		AcceptedInputTurns:       s.turns,
		TurnBudgetWarningEmitted: s.turnBudgetWarningEmitted,
		LastInputTokens:          s.contextMgr.LastInputTokens(),
		Name:                     s.naming.value,
		NameSource:               s.naming.source,
		NameUpdatedAt:            s.naming.updated,
		OriginalPrompt:           originalPrompt,
		ParentSessionID:          parentID,
		DivergenceTurn:           divergence,
		ForkLabel:                s.fork.label,
		IsSubagent:               isSubagent,
		Origin:                   s.origin,
		Goal:                     s.goalSnapshotForMeta(),
		PinnedNote:               s.pinnedNote,
		Skills:                   &skills,
		HumanNote:                human,
		AgentNote:                agentNote,
		SessionURLs:              append([]schema.SessionURL(nil), urls...),
		WorktreePath:             s.worktreeCurrentPath,
		WorktreeManaged:          s.worktreeCurrentManaged,
		WorktreeRestoreRoot:      restoreRoot,
		WorkMillis:               s.workMillis,
		CumulativeUsage:          cumulativeUsageSnapshot(s.contextMgr.CumulativeUsage()),
		JobTreeRootSessionID:     jobTreeRootSessionID,
		JobTreeRevision:          jobTreeRevision,
		EnvContext:               s.envContextState,
	}
}

// saveMeta returns persistence failures to this feature's lifecycle-sensitive
// callers (skill activation, delivery admission, compaction receipts). The
// write itself is main's autoSaveMeta: this branch and main independently
// extracted the same inline body into a helper, and the rebase keeps main's one
// implementation under our call sites' name rather than two copies of the
// locking, meta-FS and flush sequence.
func (s *Session) saveMeta() error {
	return s.autoSaveMeta()
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
	s.mu.Lock()
	transitioned, turnMS := s.transitionProcessingAtBoundaryLocked(state)
	s.mu.Unlock()
	s.finishProcessingAtBoundaryEvents(ctx, transitioned, turnMS)
}

// transitionProcessingAtBoundaryLocked publishes a processing boundary while
// the caller holds s.mu. The restored transcript boundary nests this under
// attentionMu so the state assignment cannot race a new durable transcript
// append between restoration and publication.
func (s *Session) transitionProcessingAtBoundaryLocked(state SessionState) (transitioned bool, turnMS int64) {
	if s.state == SessionProcessing && !s.closingOrClosedLocked() {
		s.state = state
		turnMS = s.accumulateWorkLocked()
		transitioned = true
	}
	return transitioned, turnMS
}

func (s *Session) finishProcessingAtBoundaryEvents(ctx context.Context, transitioned bool, turnMS int64) {
	if transitioned {
		s.emit(events.EventTurnEnded, events.TurnEndedData{TurnDurationMS: turnMS})
		if err := s.drainPendingWatchSendsAtBoundary(ctx); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: "watch send retry at processing boundary failed: " + err.Error()})
		}
		s.finishActiveProvenance()
	}
}

// finishProcessingAtFailureBoundary settles a failed turn to the same boundary
// state restore derives from its transcript. A pending ask survives provider,
// retry-budget, and other terminal failures, so those paths must remain
// awaiting instead of reporting idle to the live client.
func (s *Session) finishProcessingAtFailureBoundary(ctx context.Context) {
	state := SessionIdle
	if s.askPendingCount() > 0 {
		state = SessionAwaiting
	}
	s.finishProcessingAtBoundary(ctx, state)
}

// finishProcessingAtRestoredFailureBoundary settles an interrupt whose marker
// was rejected by the same transcript-tail rule restore uses. Marker rejection
// can follow an admitted reply, which clears askPending before a completed
// tool-results turn leaves the durable session awaiting. Generic failures keep
// the pending-set rule above; this path is only for an interrupt marker that
// never became a boundary record.
func (s *Session) finishProcessingAtRestoredFailureBoundary(ctx context.Context) {
	// recordTurn retains the live pair before an ordinary transcript write
	// reports a clean rollback. Read the transcript while attentionMu excludes
	// another append, and hold it through state publication so this boundary
	// sees only recorded or adopted turns. The read parses the whole
	// transcript file under attentionMu — the door every append and fold
	// publication passes — so a large transcript stalls appends for the
	// parse duration. The path is rare (only a rejected interrupt marker)
	// and matches the existing convention (snapshotDelegateContext reads
	// under the same door); if it ever matters in practice, derive the
	// decisive tail from the last compaction anchor (retainedFrom already
	// identifies it) instead of the full file.
	var restoredHistory []schema.Turn
	var restoredRepairInsertions []int
	var restoredEntries []transcript.Entry
	path := s.TranscriptPath()
	s.attentionMu.Lock()
	if path != "" {
		_, entries, _, err := readTranscript(path)
		if err == nil {
			restoredHistory, restoredRepairInsertions = resumeHistoryIndexed(entries)
			restoredEntries = entries
		}
	}
	release := func(transitioned bool, turnMS int64) {
		s.mu.Unlock()
		if hook := s.cfg.testOnly.beforeRestoredFailureBoundaryDoorRelease; hook != nil {
			hook()
		}
		s.attentionMu.Unlock()
		s.finishProcessingAtBoundaryEvents(ctx, transitioned, turnMS)
	}

	s.mu.Lock()
	divergence := s.fork.divergence
	if restoredHistory != nil {
		// Map the immutable full-transcript divergence into the resumed
		// history's coordinates (retained window, skipped transcript-only
		// entries and repair insertions) before consulting journal provenance.
		divergence = resumedDivergence(restoredEntries, divergence, restoredRepairInsertions)
	}
	origins := s.clientMutations.steeringOrigins()
	if restoredHistory == nil {
		// Without a readable transcript there is no confirmed replacement for
		// the live history. Preserve the existing pending-aware failure rule.
		state := SessionIdle
		if len(s.askPending) > 0 {
			state = SessionAwaiting
		}
		transitioned, turnMS := s.transitionProcessingAtBoundaryLocked(state)
		release(transitioned, turnMS)
		return
	}
	state := deriveRestoredState(restoredHistory, divergence, origins)
	pending, isAskRound := deriveRestoredAskPending(restoredHistory, divergence, origins)
	transitioned, turnMS := s.transitionProcessingAtBoundaryLocked(state)
	if transitioned {
		// The settlement is atomic with the state publication: a no-op
		// transition (already settled, or closing) leaves the live pending
		// set untouched rather than half-settling it against an unchanged
		// state.
		s.askPending = pending
	}
	release(transitioned, turnMS)
	if transitioned && isAskRound && len(pending) == 0 {
		// Mirror the restore path's warning — gated on the settlement it
		// describes: an ask round the transcript says was pending, but
		// none of whose questions parsed, left the pending-ask holds
		// inert. On a no-op transition the live pending set was left
		// untouched, so the holds still apply and the warning would be
		// false. An operator hitting that on the interrupt-rejection path
		// gets the same diagnostic a restart emits (session_init.go), and
		// neither may ever fail the boundary.
		s.emit(events.EventWarning, events.WarningData{Message: "interrupt boundary: found a pending ask_user round but could not parse any of its questions; the pending-ask holds will not apply this session"})
	}
}

// accumulateWorkLocked adds the just-ended turn's wall-clock to workMillis and
// returns that turn's duration in ms. Caller holds s.mu; a zero turnStartedAt
// (no turn was timed) contributes nothing.
func (s *Session) accumulateWorkLocked() int64 {
	if s.turnStartedAt.IsZero() {
		return 0
	}
	ms := max(s.sclock().Now().Sub(s.turnStartedAt).Milliseconds(), 0)
	s.workMillis += ms
	s.turnStartedAt = time.Time{}
	return ms
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
// surfaces the stall (spec v5, round-3 A2). divergenceTurn is the same value
// its one caller (RestoreSessionFromMetaWithConfig) already computed for
// escapeHistoryWithSessionProvenance, in the same units as s.history at this
// point: a forked child's inherited prefix must not be decided by this
// session's own journal (steeringOriginBoundary).
func (s *Session) recomputeRestoredState(divergenceTurn int) {
	s.mu.Lock()
	idle := s.state == SessionIdle && !s.closingOrClosedLocked()
	target := deriveRestoredState(s.history, divergenceTurn, s.clientMutations.steeringOrigins())
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
	// Runnable user steering is queued input for this purpose: a carrier that
	// returned its steer undelivered leaves it for the next wake, and a
	// session that will move on its own is not waiting on the user.
	target := SessionAwaiting
	if s.askPendingCount() == 0 {
		target = settleTerminalState(hadOutput, goalKicked,
			s.peekNotifications() > 0, s.QueueDepth() > 0 || s.hasRunnableUserSteering(), len(s.liveSubagentSessions()) > 0)
	}
	if target != SessionAwaiting {
		return
	}
	s.mu.Lock()
	if s.state == SessionIdle && !s.closingOrClosedLocked() {
		s.state = SessionAwaiting
	}
	s.mu.Unlock()
}

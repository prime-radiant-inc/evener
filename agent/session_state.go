package agent

import (
	"context"
	"slices"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// SessionState represents the current lifecycle state of a session.
type SessionState string

const (
	// SessionIdle indicates the session is not currently processing.
	SessionIdle SessionState = "idle"
	// SessionProcessing indicates the session is actively processing.
	SessionProcessing SessionState = "active"
	// SessionAwaiting indicates the session is idle with the ball in its
	// human partner's court: a question is pending, or the last completed
	// turn ended on a communicate that said needs_response and no autonomous
	// work (goal kick, pending notifications, queued input, a pending
	// delegate report, working child subagents) is in flight. A plain reply
	// rests idle. It is the daemon-truth source for the hub's "needs you"
	// attention state.
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

// WireState is the externally-reported session state: RestingWireState, with
// one override. A resting session (idle, or resting on a failed turn) with
// undelivered job notifications or claimable queued input reads as "active",
// because work the session owns can start its next turn without user input. A
// queue parked by a Stop is not claimable and reads as resting -- nothing will
// move it until the user acts (kata wms7). Live child activity belongs to the
// child's wire state, not the settled parent's.
//
// Precedence: the override raises a resting state ONLY. A session awaiting
// its user projects as awaiting even with autonomy in flight: a session that
// asked its user (ask-user-question design) cannot proceed without them, and
// masking the question as "working" would deadlock, since the wakes that
// could move the session are gated behind the very answer the user was never
// told to give. TestWireState_AwaitingOutranksAutonomy pins this.
func (s *Session) WireState() string {
	state := s.RestingWireState()
	if appwire.IsRestingThreadStatus(state) && s.sessionWorkPending() {
		return string(SessionProcessing)
	}
	return state
}

// RestingWireState is State() with one substitution: a session resting on a
// failed turn publishes appwire.ThreadStatusSystemError, which every client
// shows as Failed. It rests on a failed turn when it is idle, or awaiting with
// no pending question, and its history ends in a recorded turn failure
// (restingFailureLocked). A pending question keeps awaiting: answering it
// is what moves the session, and the failure stays readable in the
// transcript. The next turn to start ends the failure, since it records a
// turn-bearing entry; an interrupt never records a failure at all.
//
// It reads state, the pending asks and the history under one lock, and
// restore rebuilds all three from the transcript, so a restored session
// derives the answer the live session published. RestoreSession stamps
// WireState (this state plus the work-pending override) on its SessionStart
// event, and serve publishes the same WireState before the first turn, so the
// bridge and the synchronous startup write agree (#251).
func (s *Session) RestingWireState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, failed := s.restingFailureLocked(); failed {
		return appwire.ThreadStatusSystemError
	}
	return string(s.state)
}

// RestingFailure summarizes the failed turn the session rests on for its row's
// why line (S1c): the failure's headline and its structured cause, never its
// message, which can quote a provider's error body. It reads what
// RestingWireState reads, under the same one hold, so it is present only while
// the session publishes systemError, and a restored session summarizes the
// failure the live one did. It is nil when the session rests on no failed
// turn, or when the failure recorded no diagnostic (a legacy entry): the row
// still reads Failed, and there is nothing more to say.
func (s *Session) RestingFailure() *appwire.ThreadFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, failed := s.restingFailureLocked()
	if !failed || turn.Error == nil {
		return nil
	}
	failure := &appwire.ThreadFailure{Title: appwire.Excerpt(turn.Error.Title, appwire.MaxFailureTitleRunes)}
	if cause := turn.Error.Cause; cause != nil {
		failure.Cause = &appwire.DiagnosticCause{Kind: cause.Kind, Provider: cause.Provider, Model: cause.Model, Status: cause.Status}
	}
	if failure.Title == "" && failure.Cause == nil {
		return nil
	}
	return failure
}

// restingFailureLocked is the recorded turn failure the session rests on: it is
// idle, or awaiting with no pending question, and its history ends in the
// failure (historyTurnFailure). The caller holds s.mu.
func (s *Session) restingFailureLocked() (schema.Turn, bool) {
	resting := s.state == SessionIdle || s.state == SessionAwaiting
	if !resting || len(s.askPending) != 0 {
		return schema.Turn{}, false
	}
	return historyTurnFailure(s.history)
}

// historyTurnFailure returns the recorded turn failure the history ends in,
// when its last turn-bearing record is one (a TurnFailure, written by
// emitTurnFailure and emitSteeringCarrierTurnFailure): the session's last turn
// failed and no turn has started since. A turn-bearing record is one a turn
// writes as it runs: user input, steering (the interrupt marker included), an
// assistant response, tool results. Bookkeeping records (TurnSystem,
// TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnHookCompleted,
// TurnEnvironment, TurnNotesContext, TurnAttentionResolution) are skipped, so
// a model switch or a hook line after the failure leaves the session failed.
func historyTurnFailure(history []schema.Turn) (schema.Turn, bool) {
	for i := range slices.Backward(history) {
		switch history[i].Kind {
		case schema.TurnFailure:
			return history[i], true
		case schema.TurnUserInput, schema.TurnSteering, schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults:
			return schema.Turn{}, false
		}
	}
	return schema.Turn{}, false
}

// sessionWorkPending reports whether work owned by this session can resume it
// without user input. Child activity is excluded because it is projected on
// the child session, while its eventual notification is included once queued.
func (s *Session) sessionWorkPending() bool {
	return s.peekNotifications() > 0 || s.pendingQueueDepth() > 0 || s.hasRunnableClientMutationStart() || s.hasPendingDelegateDeliveries() || s.pendingRootDelegateAttention() || s.hasPendingDelegateAttentionArmRetry() || s.hasPendingStableDelegateAttention() || (s.jobManager != nil && s.jobManager.hasPendingStableWatchSettlementRetry())
}

func (s *Session) hasPendingStableDelegateAttention() bool {
	return s != nil && s.isRootDelegateAttentionReceiver() && s.delegateController.hasPendingDelegateAttention()
}

// hasPendingStableSteering reports whether this session's own delegate
// generation holds an admitted steering message no model request has consumed
// yet. A delegate run parked in its finalization drain reads this to run a turn
// so the steering is acted on rather than stranded until an owned job ends
// (#2796). The root session owns no delegate generation, so it is always false
// there.
func (s *Session) hasPendingStableSteering() bool {
	return s != nil && s.owningDelegateID != "" && s.delegateController != nil && s.delegateController.generationOwesSteering(s.owningDelegateID)
}

// autonomyInFlight reports whether autonomous work will move this session
// without user input: pending job notifications, queued input, a pending
// delegate report, or a working child subagent (hasWorkingSubagent). Reads
// take each signal's own lock sequentially — never nested — per the settle
// lock discipline (spec v5). A restored-but-unkicked goal is deliberately NOT
// autonomy: nothing will move until the user acts, and amber is what surfaces
// that stall.
func (s *Session) autonomyInFlight() bool {
	// Children first: a child's finalize tail delivers its report before it
	// stops reading as working, so a child read as idle has already delivered
	// whatever the pending-work read after it then sees.
	if s.hasWorkingSubagent() {
		return true
	}
	return s.sessionWorkPending()
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
	scratchTreeRootID := ""
	if root := s.scratchTreeRootID(); root != s.id {
		scratchTreeRootID = root
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
		LastTurnEndedAt:          s.lastTurnEndedAt,
		LastMessage:              s.lastMessage,
		CumulativeUsage:          cumulativeUsageSnapshot(s.contextMgr.CumulativeUsage()),
		JobTreeRootSessionID:     jobTreeRootSessionID,
		ScratchTempDir:           sandbox.ProcessScratchTempDir(),
		ScratchTreeRootID:        scratchTreeRootID,
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
		turnMS = s.endTurnLocked()
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
// tool-results turn that ended on needs_response leaves the durable session
// awaiting. Generic failures keep the pending-set rule above; this path is
// only for an interrupt marker that never became a boundary record.
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
		_, entries, _, err := readTranscript(path, s.stateDir)
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
		// state. setAskPendingLocked also clears askPendingCallArgs: pending
		// is a re-derivation from the transcript, carrying no raw call
		// arguments of its own to keep in step with it.
		s.setAskPendingLocked(pending)
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

// accumulateWorkLocked adds the just-ended turn's wall-clock, up to end, to
// workMillis and returns that turn's duration in ms. Caller holds s.mu; a zero
// turnStartedAt (no turn was timed) contributes nothing.
func (s *Session) accumulateWorkLocked(end time.Time) int64 {
	if s.turnStartedAt.IsZero() {
		return 0
	}
	ms := max(end.Sub(s.turnStartedAt).Milliseconds(), 0)
	s.workMillis += ms
	s.turnStartedAt = time.Time{}
	return ms
}

// endTurnLocked settles the turn that just ended: it adds the turn's
// wall-clock to workMillis, stamps lastTurnEndedAt, and returns the turn's
// duration in ms. Both places a turn ends call it: the processing boundary,
// and a Close that lands mid-turn, which never reaches the boundary. Caller
// holds s.mu.
func (s *Session) endTurnLocked() int64 {
	end := s.sclock().Now()
	turnMS := s.accumulateWorkLocked(end)
	s.lastTurnEndedAt = end.UTC()
	return turnMS
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
func settleTerminalState(hadOutput, goalKicked, queuePending, autonomyPending bool) SessionState {
	if !hadOutput || goalKicked || queuePending || autonomyPending {
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
// first time — working children, pending notifications, queued input — are
// available to check. Restored active goals are deliberately not autonomy —
// they are not re-kicked on restore ("loaded but idle"). divergenceTurn is
// the same value its one caller (RestoreSessionFromMetaWithConfig) already
// computed for escapeHistoryWithSessionProvenance, in the same units as
// s.history at this point: a forked child's inherited prefix must not be
// decided by this session's own journal (steeringOriginBoundary).
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
// the session waits on its human partner: a question is pending, or the turn
// ended on a communicate that said needs_response and settleTerminalState
// finds nothing autonomous in flight. A plain reply (done) and a wait on
// work (waiting_on_work) rest idle. It runs after settleGoalOnIdle (so the
// goal kick is known) and before the EventSessionEnd emit. A pending question
// upgrades at once, so the emitted State carries it; a needs_response rest
// upgrades after its quiet period and announces itself with
// EventStatusSettled. The upgrade respects the same closed-guard as
// finishProcessingAtBoundary and only ever upgrades from SessionIdle, and a
// turn starting since the settle cancels a pending one (restGeneration).
func (s *Session) armAwaitingAtSettle(hadOutput, goalKicked bool) {
	s.mu.Lock()
	s.restGeneration++
	generation := s.restGeneration
	if len(s.askPending) > 0 {
		// Only the answer resolves a pending question; queued input waits
		// behind it, so nothing else is read here.
		if s.restStillPendingLocked(generation) {
			s.state = SessionAwaiting
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if s.communicateEndReason() != tool.CommunicateEndReasonNeedsResponse {
		return
	}
	// Runnable user steering is queued input for this purpose: a carrier that
	// returned its steer undelivered leaves it for the next wake, and a
	// session that will move on its own is not waiting on the user.
	moves := s.QueueDepth() > 0 || s.hasRunnableUserSteering()
	if settleTerminalState(hadOutput, goalKicked, moves, s.autonomyInFlight()) != SessionAwaiting {
		return
	}
	// A needs_response rest waits out a quiet period first, so a session that
	// ends a turn and starts the next one at once never flickers to awaiting.
	// The timer re-checks everything the settle checked: a newer settle, a new
	// turn, a close, or work that arrived in the meantime leaves it idle.
	delay := needsResponseQuietPeriodDefault
	if override := s.cfg.testOnly.needsResponseQuietPeriod; override != nil {
		delay = *override
	}
	if delay <= 0 {
		// Nothing moved since the checks above, and the input's SESSION_END,
		// emitted right after this settle, carries the state.
		s.restAwaiting(generation)
		return
	}
	s.sclock().AfterFunc(delay, func() {
		// The cheap check first, so a timer outliving its session or turn
		// reads no work state. restAwaiting re-reads queued input and
		// steering under the transition's hold.
		if !s.restStillPending(generation) || s.autonomyInFlight() {
			return
		}
		// Held across the emit so a turn start can't slip in between.
		// STATUS_SETTLED must stay out of job_watch.go's modelEventKinds: a
		// watch fired from this emit could start a turn, which waits on restMu.
		s.restMu.Lock()
		defer s.restMu.Unlock()
		if s.restAwaiting(generation) {
			if hook := s.cfg.testOnly.needsResponseRestBeforeAnnounce; hook != nil {
				hook()
			}
			s.emit(events.EventStatusSettled, events.StatusSettledData{State: string(SessionAwaiting)})
		}
	})
}

// restStillPending reports whether the rest numbered generation can still
// arm: no turn started and no settle ran since, and the session is idle and
// open.
func (s *Session) restStillPending(generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restStillPendingLocked(generation)
}

func (s *Session) restStillPendingLocked(generation uint64) bool {
	return s.restGeneration == generation && s.state == SessionIdle && !s.closingOrClosedLocked()
}

// restAwaiting moves the session to awaiting if the rest numbered generation
// can still arm, and reports whether it did. Queued input and runnable user
// steering are read again under the same hold as the transition, since
// neither starts a turn the moment it arrives.
func (s *Session) restAwaiting(generation uint64) bool {
	steeringHeld := s.userSteeringHeld()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.restStillPendingLocked(generation) || len(s.inputQueue) > 0 || s.runnableUserSteeringLocked(steeringHeld) {
		return false
	}
	s.state = SessionAwaiting
	return true
}

// needsResponseQuietPeriodDefault is how long a turn that ended on
// needs_response rests idle before it rests awaiting.
const needsResponseQuietPeriodDefault = 5 * time.Second

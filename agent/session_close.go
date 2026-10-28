package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
)

// Close shuts the session down once (subsequent calls are no-ops via
// closeOnce). It marks the session closing/closed, cancels in-flight
// LLM calls, cleans up the environment (killing child processes), runs the
// SessionEnd hooks, emits EventSessionEnd with the final state, closes
// subagents, the MCP manager, and the transcript, closes the root-owned artifact
// store after descendant shutdown, exports the ATIF trajectory when configured
// for the root session, waits for in-flight event emitters to finish, and closes
// the events channel. The bundled skills live in a content-addressed cache
// shared with every other process, so Close leaves it in place.
func (s *Session) Close() {
	s.close(context.Background(), closeOptions{cleanupEnv: true})
}

// CloseForShutdown closes the session with a terminal lifecycle boundary.
// A cancelled in-flight turn may already have published an interrupted idle
// boundary; that boundary must not suppress the session's closed notification.
func (s *Session) CloseForShutdown() {
	s.close(context.Background(), closeOptions{cleanupEnv: true, forceTerminal: true})
}

type closeOptions struct {
	cleanupEnv    bool
	forceTerminal bool
}

// releaseRuntime runs the session's single one-time teardown under an explicit
// policy. A prior terminal close or non-terminal release owns the pass, so a
// later deferred Close can never run a destructive second pass. Terminal
// callers keep the void/logging contract; a retirement caller gets the cleanup
// errors back.
func (s *Session) releaseRuntime(ctx context.Context, options closeOptions, policy runtimeReleasePolicy) error {
	var releaseErr error
	ran := false
	s.closeOnce.Do(func() {
		ran = true
		releaseErr = s.releaseRuntimeOnce(ctx, options, policy)
	})
	if !ran {
		// A terminal Close or an earlier release already owns the one pass.
		// Terminal callers keep their void/no-op contract; a retirement caller
		// must see that its release did not happen.
		if policy == releaseRetirement {
			return errRetirementTeardownSpent
		}
		return nil
	}
	return releaseErr
}

// close is the terminal-policy helper retained for internal callers and tests
// that shuts a session down without needing the cleanup error.
func (s *Session) close(ctx context.Context, options closeOptions) {
	_ = s.releaseRuntime(ctx, options, releaseTerminal)
}

// joinWithinCloseBudget waits for wg, giving up when the close cascade's shared
// budget expires and saying so. The joins it replaces exist for DELIVERY
// ORDERING — an in-flight tool's end event, a detached emitter's event, reaching
// the stream before it closes — which is a nicety, not a safety property:
// sendEvent takes eventsMu and re-checks eventsClosed, so a straggler that
// emits after this gives up is dropped, never a send on a closed channel.
//
// Weighed against that: a goroutine parked in an operation nothing can cancel
// (an uncancellable tool call in a delegate the drain has already abandoned)
// holds the WaitGroup forever, and an unbounded join converts a bounded drain
// into an unbounded process. Losing an event that was never going to arrive is
// the cheaper failure.
//
// The waiter goroutine is not leaked in any lasting sense: it holds nothing and
// exits the moment the WaitGroup does drain.
func (s *Session) joinWithinCloseBudget(ctx context.Context, wg *sync.WaitGroup, what string) {
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		wg.Wait()
	}()
	select {
	case <-joined:
	case <-ctx.Done():
		s.emit(events.WarningData{Message: fmt.Sprintf(
			"close budget expired joining %s; their remaining events are dropped", what)})
	}
}

func (s *Session) releaseAPILogRoute() {
	if err := s.client.ReleaseSessionAPILog(s.id); err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("release session API log: %v", err)})
	}
}

// beginDispose admits an in-turn dispose op iff the session is not yet closing,
// registering it on disposeWG under a single s.mu hold — the sendersWG idiom
// (session.go): the closing check AND the WaitGroup Add happen atomically, so a
// successful Add happens-before Close()'s disposeWG.Wait() join. A check
// followed by an Add outside the lock would reopen the race (spec §P1, rev-9.1
// finding O5). A true return MUST be paired with a (deferred) endDispose().
func (s *Session) beginDispose() bool {
	release, err := s.beginRetirementMutation("environment")
	if err != nil {
		return false
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		release()
		return false
	}
	s.disposeWG.Add(1)
	s.disposeRetirement = append(s.disposeRetirement, release)
	s.mu.Unlock()
	return true
}

// endDispose releases a dispose admission obtained from beginDispose().
func (s *Session) endDispose() {
	s.mu.Lock()
	last := len(s.disposeRetirement) - 1
	release := s.disposeRetirement[last]
	s.disposeRetirement[last] = nil
	s.disposeRetirement = s.disposeRetirement[:last]
	s.mu.Unlock()
	s.disposeWG.Done()
	release()
}

// releaseRuntimeOnce is the body of the single teardown pass. The two policies
// run the same steps in the same order and differ in which steps they take; the
// order is the specification, so each policy is one straight-line method over
// the shared steps below.
func (s *Session) releaseRuntimeOnce(ctx context.Context, options closeOptions, policy runtimeReleasePolicy) error {
	if policy == releaseRetirement {
		return s.releaseForRetirement(ctx, options)
	}
	s.closeTerminal(ctx, options)
	return nil
}

// closeTerminal is the ordinary close: it stops the durable delegate tree and
// jobs, disposes isolation lanes, runs the SessionEnd hook and announces
// session_closed.
func (s *Session) closeTerminal(ctx context.Context, options closeOptions) {
	budgetCtx, cancelBudget := s.publishCloseBudget(ctx)
	defer cancelBudget()
	turns, emitEnd := s.beginClosing(options.forceTerminal, true)

	s.cancelTurnContext()
	// Escalations can use a context independent of the turn. Deny their
	// human-decision waits before joining the retained environment work;
	// closing already prevents any new escalation from registering.
	s.cancelAllEscalations()
	// A delegate result is acknowledged only after its enclosing tool-result
	// turn is durable. Closing refuses that turn, so release any receipts that
	// can no longer reach their commit point before stopping the tree.
	s.abortDelegateDeliveryCommits()
	s.stopLaneTimers()
	s.joinInFlightDisposeAndEnvWork(budgetCtx)

	// The root-owned stable controller is the shutdown authority for its
	// delegate tree. Persist and join recursive stop before generic Session
	// teardown can close a child out from under that durable operation. The
	// store stays open until worktree disposal has recorded its evidence.
	if err := s.closeOwnedDelegateRuntimeTree(budgetCtx); err != nil {
		s.warnIncompleteClose("delegate tree", err)
		// A hopeless stop has already consumed its dedicated half of the
		// cascade budget. Do not spend the remaining half joining the same
		// wedged child again through its generic Session.Close path.
		if errors.Is(err, context.DeadlineExceeded) {
			cancelBudget()
		}
	}

	// Reacquire the pair and drain the subagent map. Marking closing BEFORE
	// draining means a spawn or namer launch racing teardown is either
	// registered here (and cancelled below) or observes closing and refuses —
	// there is no window for a late goroutine to escape the drain. The map is
	// cleared under the lock; children are closed OUTSIDE the lock
	// (teardownChildSession's close acquires the child's own mu).
	s.responseSideEffectsMu.Lock()
	s.mu.Lock()
	subs := s.subagents.drainForClose()
	s.mu.Unlock()
	s.responseSideEffectsMu.Unlock()
	// Flush the just-accumulated work time and usage now, before any
	// teardown below: Close is the terminal event for a turn that died
	// mid-flight above, and also for a turn that was cooperatively interrupted
	// earlier — whose accumulation happened in processInputKindWithProvenance's
	// wrapper AFTER processOneInput's own deferred autosave had already fired
	// with a stale, pre-accumulation value. Neither case has any other flush
	// point once Close runs, so without this the in-memory WorkMillis and
	// CumulativeUsage are correct but never reach meta.json, and a daemon
	// restart silently loses them.
	s.maybeAutoSave()
	s.subagents.waitForReconstructions()
	s.subagents.waitForReconstructionSideEffects()

	// Mark and stop durable jobs before environment cleanup can reap process
	// handles and make deliberate shutdown look like runtime failure. Keep the
	// parent store open while child sessions close; nested child jobs may still
	// need to forward their terminal events to the parent.
	var jobManagerCloseErr error
	if s.jobManager != nil {
		jobManagerCloseErr = s.jobManager.closeRuntimeState(budgetCtx)
	}

	// Close subagents before shared environment cleanup; child sessions can own
	// durable jobs whose process handles live in the parent env. The parent owns
	// cleanup of that env, so a child's teardown never runs it; what a child
	// owns is its scratch, which its teardown ends.
	for _, sub := range subs {
		teardownChildSession(budgetCtx, sub.sess)
	}
	s.warnIncompleteClose("artifact store", s.closeOwnedArtifactStore())
	// Dispose the isolation delegate lanes this session created and unlock its
	// own occupied managed worktree. Both run AFTER child sessions close (a
	// mid-tree delegate parent disposes its own lanes first) and BEFORE the
	// store closes, because disposal's disposed mark is a durable append.
	// Environment cleanup below kills any residual lane process; a residual
	// writer racing the clean check is self-healing since disposal's
	// `git worktree remove` runs without --force and downgrades to keep on a
	// dirty refusal.
	s.disposeDelegateLanesAtClose(budgetCtx)
	// After this session's own disposal, collect foreign residue (other
	// cleanly-closed sessions' unlocked, merged delegate lanes) over the SAME
	// shared close budget. The store must still be open (the own-store Disposed
	// mark is a durable append); it closes below.
	s.disposeLaneResidueAtClose(budgetCtx)
	s.unlockOwnManagedWorktreeAtClose(budgetCtx)

	if s.jobManager != nil {
		jobManagerCloseErr = errors.Join(jobManagerCloseErr, s.jobManager.closeStoreOnly())
	}
	s.warnIncompleteClose("job manager", jobManagerCloseErr)
	s.warnIncompleteClose("delegate store", s.closeOwnedDelegateStoreWithContext(budgetCtx))

	s.closeMemoryEnvironments()
	if options.cleanupEnv {
		s.cleanupEnvironmentAtClose()
	}

	// SessionEnd hooks (best-effort, bounded timeout).
	if s.hookRunner != nil {
		hookCtx, hookCancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.hookRunner.RunSessionEnd(s.apiLogContext(hookCtx), s.hookInput(plugin.HookSessionEnd))
		hookCancel()
	}
	s.announceSessionEnd(budgetCtx, emitEnd, turns)

	if s.mcpMgr != nil {
		s.mcpMgr.Close()
	}
	// Scratch goes last: SessionEnd hooks and MCP servers above still run with
	// TMPDIR inside it, and bubblewrap refuses a missing bind source.
	if options.cleanupEnv {
		s.endOwnedCurrentScratch()
	}
	_ = s.closeAttachedTranscript()

	// Export ATIF trajectory if configured (root session only, after transcript flush).
	if s.cfg.ExportATIFPath != "" && s.stateDir != "" && s.cfg.spawn.depth == 0 {
		tpath := schema.SessionTranscriptPath(s.stateDir, s.id)
		if err := exportATIF(tpath, s.cfg.ExportATIFPath, s.cfg.ExportATIFProviderHandles); err != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("ATIF export failed: %v", err)})
		}
	}
	// Reassert closed in case an in-flight turn reached a late state transition.
	s.mu.Lock()
	s.state = SessionClosed
	s.mu.Unlock()
	s.closeEventStream(budgetCtx)
}

// releaseForRetirement releases only process-local runtime resources and
// returns the cleanup errors. It never stops the durable delegate tree, jobs or
// watches, disposes lanes, runs the SessionEnd hook or announces session_closed,
// and leaves the session state as it was.
func (s *Session) releaseForRetirement(ctx context.Context, options closeOptions) error {
	var releaseErr error
	budgetCtx, cancelBudget := s.publishCloseBudget(ctx)
	defer cancelBudget()
	s.beginClosing(options.forceTerminal, false)

	s.cancelTurnContext()
	s.stopLaneTimers()
	s.joinInFlightDisposeAndEnvWork(budgetCtx)

	// Exact resident child runtimes are released leaf-first by the caller, so
	// no subagent map is drained here.
	var jobManagerCloseErr error
	if s.jobManager != nil {
		jobManagerCloseErr = s.jobManager.releaseQuiescentRuntime()
	}
	collect := func(what string, err error) {
		s.warnIncompleteClose(what, err)
		releaseErr = errors.Join(releaseErr, err)
	}
	collect("artifact store", s.closeOwnedArtifactStore())
	collect("job manager", jobManagerCloseErr)
	collect("delegate store", s.closeOwnedDelegateStoreWithContext(budgetCtx))

	// Retirement ends the parked and abandoned environments' scratch without
	// signalling any process; the current environment's goes below, after MCP
	// shutdown.
	s.endRetirementScratch()
	s.closeMemoryEnvironments()
	if options.cleanupEnv {
		s.cleanupEnvironmentAtClose()
	}

	if s.mcpMgr != nil {
		s.mcpMgr.Close()
	}
	s.endOwnedCurrentScratch()

	// The fault seam is evaluated only under the non-terminal policy, so a set
	// fault can never skip the terminal close's transcript close.
	if err := retirementReleaseFailure("transcript_close"); err != nil {
		releaseErr = errors.Join(releaseErr, err)
	} else if err := s.closeAttachedTranscript(); err != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("close transcript: %w", err))
	}
	s.closeEventStream(budgetCtx)
	return releaseErr
}

// publishCloseBudget returns the close cascade's shared deadline: the
// initiating close mints it, and descendants reached via close(budgetCtx,
// closeOptions{}) reuse it rather than minting their own. The caller defers the
// returned cancel.
func (s *Session) publishCloseBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	budgetCtx, cancelBudget := ensureCloseBudget(ctx)
	// Publish the shared deadline independently of eventsMu: an emitter may
	// already hold eventsMu.RLock while waiting for the authoritative bridge.
	// Its send must observe this context so the final close can acquire the
	// write lock after the deadline instead of waiting behind that emitter.
	s.closeCtxMu.Lock()
	s.closeCtx = budgetCtx
	if s.closeSignal == nil {
		s.closeSignal = make(chan struct{})
	}
	closeSignal := s.closeSignal
	s.closeCtxMu.Unlock()
	go func() {
		<-budgetCtx.Done()
		close(closeSignal)
	}()
	return budgetCtx, cancelBudget
}

// beginClosing sets the monotonic refuse-new-work flags under the documented
// lock pair (responseSideEffectsMu then mu), then releases BOTH before the
// dispose join: holding responseSideEffectsMu across the join would stall every
// tool's side-effect section on one dispose. `closing` is monotonic and every
// reader treats it as a refuse-new-work flag, so splitting the critical section
// is safe. It returns the turn count for the terminal boundary and whether the
// boundary is still owed (a forced terminal is always owed). markClosed also
// moves the state to closed; a retirement leaves the state as it was.
//
// This is the dispose-turn vs own-close protocol's step 1: set-flag → cancel
// → join → drain. An in-turn dispose op admitted via beginDispose() holds
// disposeWG; close must not begin draining/teardown while one runs (its
// dispose steps need s.mu, which drainForClose holds — joining at the drain
// point would deadlock).
func (s *Session) beginClosing(forceTerminal, markClosed bool) (turns int, emitEnd bool) {
	s.responseSideEffectsMu.Lock()
	s.mu.Lock()
	turns = s.modelResponses
	emitEnd = s.claimSessionEndLocked(forceTerminal)
	if s.state == SessionProcessing {
		s.endTurnLocked() // dying turn's work counts and it ends now
	}
	s.closing = true
	if markClosed {
		s.state = SessionClosed
	}
	s.mu.Unlock()
	s.responseSideEffectsMu.Unlock()
	return turns, emitEnd
}

// cancelTurnContext cancels the turn ctx BEFORE the dispose join so a
// mid-dispose git op is actually interruptible — the git runners are ctx-aware,
// which is what bounds the join. `closing` is already set, so beginDispose()
// refuses any new admission past this point; only ops admitted before it are
// joined.
func (s *Session) cancelTurnContext() {
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
}

// stopLaneTimers stops the P3 open-pass and re-lock retry timers now that
// `closing` is set: a close that begins before the open-pass delay elapses
// cancels the pass outright (a timer that already fired is joined via sweepWG).
func (s *Session) stopLaneTimers() {
	s.stopLaneResidueSweepTimer()
	s.stopLaneReLockRetryTimer()
}

// joinInFlightDisposeAndEnvWork joins, with NO locks held, the in-flight
// dispose ops and any in-flight P3 open-pass residue sweep, so the two never
// race on the same lane's git ops, and then the environment work admitted
// before `closing` was set: a whole manage_worktree call, the refresh of any
// swap that call performs, the cut of a delegate's isolation lane and its
// rollback, and the rollback a refused or failed operation still owes.
//
// The environment-work join runs here, before the delegate tree closes and
// before any of this session's own worktree cleanup, because everything after
// acts on the same lanes, locks and durable records an in-flight operation is
// still moving. Nothing admitted may depend on those later steps — an
// operation that waited on the delegate-tree close would deadlock this join
// until the budget expired. It runs for a child close too (cleanupEnv false):
// no such work may outlive the session that admitted it.
func (s *Session) joinInFlightDisposeAndEnvWork(budgetCtx context.Context) {
	s.disposeWG.Wait()
	s.sweepWG.Wait()
	// testOnly seam: observation point exactly after both joins returned. See
	// testConfig.closeAfterDisposeSweepJoin. Nil in production.
	if s.cfg.testOnly.closeAfterDisposeSweepJoin != nil {
		s.cfg.testOnly.closeAfterDisposeSweepJoin()
	}
	s.joinEnvWorkWithinCloseBudget(budgetCtx)
}

// warnIncompleteClose warns that a teardown step left something open.
func (s *Session) warnIncompleteClose(what string, err error) {
	if err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("%s close incomplete: %v", what, err)})
	}
}

// closeOwnedArtifactStore closes the root-owned artifact store after
// descendant shutdown; a session that does not own one has nothing to close.
func (s *Session) closeOwnedArtifactStore() error {
	if s.ownsArtifactStore && s.artifactStore != nil {
		return s.artifactStore.Close()
	}
	return nil
}

// cleanupEnvironmentAtClose kills any remaining child processes (SIGTERM → wait
// 2s → SIGKILL) and ends the scratch of the environments the session no longer
// reaches.
func (s *Session) cleanupEnvironmentAtClose() {
	if observe := s.cfg.testOnly.envCleanupObserved; observe != nil {
		observe(s.currentEnv())
	}
	s.currentEnv().Cleanup()
	// A session closing while entered in a worktree still holds the
	// environment it parked at enter. A child spawned before the enter
	// shares that object and may have minted a scratch there — one the
	// child's teardown skips (not its own) and the current clone's
	// Cleanup never reaches. The parked environment shares the current
	// clone's process table, which was just reaped, so only its scratch
	// is left: removed, never a second Cleanup.
	s.disposeParkedWorktreeEnvironmentScratch()
	// And every environment a later enter dropped, which the parked one
	// does not cover: worktreeRestoreEnv holds only the launch
	// environment, so a switch leaves the clone it came from reachable
	// from nothing.
	s.disposeAbandonedEnvironmentScratch()
}

// announceSessionEnd emits SESSION_END with the final state when it is still
// owed.
//
// Under the transcript door, because the terminal boundary has to be ordered
// against the one other publication that takes it: an environment append holds
// attentionMu across its entry and the event announcing it, so taking it here
// waits for an append already in flight and shuts out any that starts later (it
// re-reads `closing` under this same lock). Without that an ENVIRONMENT event
// can reach a live reader behind the SESSION_END that says the session is over.
// The send is bounded by the shared close budget, and the door is released
// before closeAttachedTranscript takes it again.
func (s *Session) announceSessionEnd(budgetCtx context.Context, emitEnd bool, turns int) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if !emitEnd {
		return
	}
	// A live authoritative bridge gets the terminal boundary with the same
	// lossless backpressure as every other event. A wedged bridge cannot be
	// allowed to hold CloseForShutdown here: the shared close deadline releases
	// sendEventContext, after which the durable closed state and stream close
	// still complete.
	data := events.SessionEndData{
		Reason: "session_closed",
		State:  string(SessionClosed),
		Turns:  turns,
	}
	_, ev, delivered := s.sendEventContext(budgetCtx, data, s.activeCausalProvenance())
	if delivered && s.jobManager != nil {
		s.jobManager.onSessionEvent(ev)
	}
}

// closeEventStream joins the detached event producers and closes the events
// channel.
func (s *Session) closeEventStream(budgetCtx context.Context) {
	s.joinWithinCloseBudget(budgetCtx, &s.toolEventsWG, "in-flight tool events")
	// Join detached event emitters (subagent runs, session namer) so their
	// events are delivered before the channel closes. They are already
	// cancelled above (child Close + cancelFunc), so this normally returns
	// promptly — but "cancelled" is not the same as "returns": a delegate
	// parked inside an uncancellable tool call holds both of these WaitGroups
	// forever, and an unbudgeted join here made the drain's give-up worthless
	// because run.go prints its answer only after Close(). Both joins are
	// therefore bounded by the shared close budget.
	s.joinWithinCloseBudget(budgetCtx, &s.sendersWG, "detached event emitters")
	s.releaseAPILogRoute()
	// Close under eventsMu so a caller-owned emit() (the client-mutation queue
	// handlers or the ProcessInput loop — goroutines the session cannot join)
	// can never send on the closed channel.
	s.eventsMu.Lock()
	s.eventsClosed = true
	close(s.events)
	s.eventsMu.Unlock()
}

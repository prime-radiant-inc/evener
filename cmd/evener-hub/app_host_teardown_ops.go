package hub

// This file owns the two teardown-repair mutations registry spec 08 §6/§11
// defines: `evener/host/teardown-retry`, which resumes one remnant's pinned
// teardown, and `evener/host/teardown-recover`, the operator-attested clearance
// for a remnant whose pinned target cannot be resolved (its attestation is
// recorded as given and never verified where the transport names no principal).
//
// Spec §6 on the retry: "The new `evener/host/teardown-retry` mutation (params
// `{remnantId: string}`, response the outcome union in §11) resumes ONLY that
// named teardown: it looks up the remnant by the opaque `remnantId` (unknown or
// purged ID → typed `teardown-unknown-key` not-found; a cleared-remnant marker
// still present returns the `already-cleared` success arm, never not-found —
// the remnant carries its own pinned teardown target, so lookup never requires
// a current live entry and later mutations cannot strand it), try-acquires the
// host's per-host gate (held → typed busy, same classes as `restart`), and
// holds the process-wide mutation lock only for the marker/remnant/receipt
// state transitions — never across the teardown itself (the same claim pattern
// as the foreign-marker rule: claim under the lock with an attempt token,
// release across the teardown, re-acquire to finalize)."
//
// Spec §6 on the incarnation-scoped execution: "The retry targets handles by
// incarnation-id equality — never by generation alone and never by name lookup:
// a name-based lookup resolving to a handle whose incarnation id differs from
// the remnant's is refused, never executed, even when its generation tag equals
// the remnant's." And on rehydration: "boot rehydrates the remnant's actionable
// handle by loading the persisted `cleanupHandle` — rehydration is record load,
// never live-handle resurrection".
//
// Spec §6 on the timeout: "on timeout the retry releases the host gate in the
// same atomic `hub.toml` write that marks its attempt record timed-out-but-open,
// then reports the terminal `committed-with-teardown-failure` outcome with the
// remnant still open plus its attempt record open for fencing — a stuck remote
// process therefore surfaces a terminal outcome with a live retry handle, never
// an indefinitely held gate." And on the later retry: "A later retry
// try-acquires the freed gate, then adopts the timed-out attempt first: it
// marks the prior attempt record fenced-closed in the same atomic `hub.toml`
// write that claims the remnant under a fresh attempt record, and only then
// runs the pinned teardown again, so a wedged gate never blocks repair. The
// fenced-closed mark is a durable claim marker, not a stop signal: the kill/wait
// and guard advance an earlier revision required here were withdrawn (comp08),
// so the timed-out run's remote cleanup can still be executing — the accepted
// residual."
//
// Spec §6 on the recover: "the call try-acquires the host's per-host gate first
// ... and holds it through the clearance. When a timed-out-but-open attempt
// record stands (gate free, attempt fence open), the recover fences it first
// ... before the safety checks below, so the clearance never lands past
// possibly-live cleanup. Under the gate it claims the remnant atomically ...,
// then verifies the operator attestation is present and well-formed, re-runs
// the safety checks (no live handle tagged with the remnant's `(generation,
// incarnationId)` pair exists, no supervisor or channel binding names the
// remnant's pinned target), re-checks the safety conditions immediately before
// the clearing write, and only then clears the remnant in one atomic `hub.toml`
// write — recording the attestation (operator, statement, observedAt) on the
// original mutation receipt beside `remnantResolvedAt` (outcome becomes
// `committed` with `remnantResolvedAt`)."

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// teardownRunResult is one pinned teardown run's observed result: whether the
// teardown completed, and — when it did not — the seam it failed at.
type teardownRunResult struct {
	Seam string
}

// TeardownRetry is `evener/host/teardown-retry` (registry spec 08 §6/§11).
func (m *hubHostManager) TeardownRetry(ctx context.Context, params appwire.HostTeardownRetryParams) (appwire.HostTeardownRetryResult, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostTeardownRetryResult{}, err
	}
	remnantID := strings.TrimSpace(params.RemnantID)
	if remnantID == "" {
		return appwire.HostTeardownRetryResult{}, appwire.InvalidParams("teardown-retry requires a remnantId")
	}
	// The lookup is by id alone: "lookup never requires a current live entry and
	// later mutations cannot strand it". An unknown or purged id is the typed
	// not-found refusal; a cleared remnant whose resolved record survives is the
	// `already-cleared` success arm instead.
	remnant, ok := m.cfg.store.remnantByID(remnantID)
	if !ok {
		return appwire.HostTeardownRetryResult{}, teardownUnknownKeyRefusal(remnantID)
	}
	if !remnant.open() {
		return m.alreadyClearedArm(remnant), nil
	}
	name := remnant.Host
	// Gate first, then the mutation lock (spec §5's fixed order). A held gate —
	// a live retry attempt holding it, a deploy/restart, an Ensure, a
	// mutation's own reservation — is the typed busy refusal: "a live attempt
	// still holding the gate refuses even that retry with the typed busy error
	// — the gate holder owns the attempt".
	releaseGate, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "teardown-retry"})
	if err != nil {
		return appwire.HostTeardownRetryResult{}, err
	}
	// The gate is held only while this attempt is *claiming and finalizing*; the
	// run itself releases it. It has to: the pinned teardown goes through the
	// manager's self-acquiring paths (RemoveHost/UpdateHost), which take the
	// same non-reentrant per-host gate — holding the reservation across the run
	// would refuse our own call. The mutation paths are the stronger form
	// (spec 08 §4's "gate released last"): their teardown runs through the
	// gate-inheriting entries with the reservation held throughout. The attempt
	// record is the fence for the window this retry releases the reservation in,
	// exactly as it is after a timeout.
	gateHeld := true
	releaseOnce := func() {
		if gateHeld {
			gateHeld = false
			releaseGate()
		}
	}
	defer releaseOnce()

	// A live attempt owns the remnant: refuse before claiming anything, so two
	// retries never run the same cleanup and a recover never overwrites a live
	// retry's resolution.
	if attemptID, live := m.liveAttemptInThisBoot(remnantID); live {
		return appwire.HostTeardownRetryResult{}, attemptBusyRefusal(name, remnantID, attemptID)
	}
	// Claim under the mutation lock: one atomic hub.toml write that fences any
	// prior timed-out (or previous-boot) attempt and writes this attempt's
	// durable record.
	priorAttemptID, priorAttempt, priorOpen := m.cfg.store.openAttemptFor(remnantID)
	attemptID := mintAttemptID()
	now := m.nowTime()
	attempt := HostTeardownAttempt{
		RemnantID:         remnantID,
		State:             hostAttemptStateOpen,
		StartedAt:         now.UTC().Format(time.RFC3339),
		FencingEpochBoot:  m.cfg.bootID,
		FencingEpochOpSeq: uint64(now.UnixNano()),
	}
	m.cfg.mu.Lock()
	// Re-read under the lock: a concurrent retry may have resolved the remnant
	// or claims may have moved since the gate-free read above.
	current, ok := m.cfg.store.remnantByID(remnantID)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostTeardownRetryResult{}, teardownUnknownKeyRefusal(remnantID)
	}
	if !current.open() {
		m.cfg.mu.Unlock()
		return m.alreadyClearedArm(current), nil
	}
	remnant = current
	change := hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: attempt}}
	if priorOpen {
		// The attempt fence: "marks the prior attempt record fenced-closed in the
		// same atomic `hub.toml` write that claims the remnant under a fresh
		// attempt record". The mark is a durable claim marker, not a stop signal:
		// the fencing-epoch takeover (kill/wait plus guard advance) was withdrawn
		// with the crash-fencing program (comp08), so the prior run may still be
		// executing.
		fenced := priorAttempt
		fenced.State = hostAttemptStateFencedClosed
		fenced.FencedAt = now.UTC().Format(time.RFC3339)
		change.fencedAttempt = &pendingHostAttempt{AttemptID: priorAttemptID, Attempt: fenced}
	}
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, change); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostTeardownRetryResult{}, err
	}
	m.cfg.mu.Unlock()

	// The run itself: no mutation lock held, bounded by the owner-set execution
	// deadline. A timeout is the terminal failure arm with the remnant still
	// open and the attempt record timed-out-but-open; the gate is released
	// before the response either way, because it is `defer`red and the timeout
	// path returns through it.
	runCtx, cancel := context.WithTimeout(ctx, m.cfg.policy.teardownTimeout)
	defer cancel()
	releaseOnce()
	if m.testOnlyBeforePinnedRun != nil {
		m.testOnlyBeforePinnedRun(name)
	}
	result, runErr := m.runPinnedTeardown(runCtx, remnantID, remnant)
	// Re-acquire the reservation for the finalizing write. A held gate means
	// another path is inside the name; the attempt record still owns the
	// remnant, so the finalization proceeds and the busy holder hears about the
	// fence through the record rather than through a refusal we would have to
	// swallow.
	if reacquire, gateErr := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "teardown-retry"}); gateErr == nil {
		gateHeld = true
		releaseGate = reacquire
	}

	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	if runErr != nil || runCtx.Err() != nil {
		seam := remnant.Seam
		if result.Seam != "" {
			seam = result.Seam
		}
		// The attempt record is closed or marked by what actually happened: a
		// deadline leaves it open and timed-out (the fence a later retry takes
		// over), while a non-deadline refusal is not a timeout and must not be
		// durably recorded as one — it fences the attempt closed and returns the
		// refusal the caller must see.
		entries := m.cfg.store.snapshot()
		if runErr != nil && !isTeardownDeadline(runErr) {
			closed := fencedAttemptRecord(attempt, m.nowTime())
			if err := m.persistHosts(entries, entries, hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: closed}}); err != nil {
				return appwire.HostTeardownRetryResult{}, err
			}
			return appwire.HostTeardownRetryResult{}, runErr
		}
		timedOut := attempt
		timedOut.TimedOutAt = m.nowTime().UTC().Format(time.RFC3339)
		if err := m.persistHosts(entries, entries, hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: timedOut}}); err != nil {
			return appwire.HostTeardownRetryResult{}, err
		}
		return m.retryArm(appwire.HostTeardownOutcomeFailed, remnant, seam), nil
	}

	// Finalize from the observed result: one atomic write records the typed
	// resolved-remnant record, resolves the original receipt, and closes the
	// attempt.
	clearedAt := m.nowTime()
	resolved := HostResolvedRemnant{
		ClearedAt:      clearedAt.UTC().Format(time.RFC3339),
		ResolutionKind: hostRemnantResolutionRetry,
		HostKind:       hostKindOf(remnant.Kind),
		MutationKey:    remnant.MutationKey,
	}
	stored := remnant
	stored.Resolved = &resolved
	closed := attempt
	closed.State = hostAttemptStateFencedClosed
	closed.FencedAt = clearedAt.UTC().Format(time.RFC3339)
	entries = m.cfg.store.snapshot()
	finalize := hostPersistChange{
		resolved: &pendingHostRemnant{RemnantID: remnantID, Remnant: stored},
		attempt:  &pendingHostAttempt{AttemptID: attemptID, Attempt: closed},
	}
	if receipt, ok := m.resolvedReceipt(remnant, clearedAt, nil); ok {
		finalize.receipt = &pendingHostReceipt{Key: remnant.MutationKey, Receipt: receipt}
	}
	if err := m.persistHosts(entries, entries, finalize); err != nil {
		return appwire.HostTeardownRetryResult{}, err
	}
	// A completed REMOVAL retirement takes the name's derived state with it,
	// exactly as the clean removal path does: the committed-with-teardown-failure
	// branch of `RemoveResult` returns before its own drop, and the retry is the
	// only other path to completion — so without this the removed host's source
	// registration and cached session rows would outlive the removal, and
	// `sourceOnline`'s fail-open for an unregistered source would keep rendering
	// its sessions as live until a restart. An edit's remnant leaves the name
	// live and keeps its state.
	if hostKindOf(remnant.Kind) == hostRemnantHostKindRemoved {
		m.dropHostDerivedState(remnant.Host)
	}
	return m.retryArm(appwire.HostTeardownOutcomeComplete, stored, ""), nil
}

// TeardownRecover is `evener/host/teardown-recover` (registry spec 08 §6/§11).
func (m *hubHostManager) TeardownRecover(ctx context.Context, params appwire.HostTeardownRecoverParams) (appwire.HostTeardownRecoverResult, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostTeardownRecoverResult{}, err
	}
	remnantID := strings.TrimSpace(params.RemnantID)
	if remnantID == "" {
		return appwire.HostTeardownRecoverResult{}, appwire.InvalidParams("teardown-recover requires a remnantId")
	}
	remnant, ok := m.cfg.store.remnantByID(remnantID)
	if !ok {
		return appwire.HostTeardownRecoverResult{}, teardownUnknownKeyRefusal(remnantID)
	}
	if !remnant.open() {
		return recoveredClearedResult(remnant), nil
	}
	name := remnant.Host
	// Gate first: "failing fast with the typed busy error when a retry attempt
	// is live, and holds it through the clearance".
	releaseGate, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "teardown-recover"})
	if err != nil {
		return appwire.HostTeardownRecoverResult{}, err
	}
	defer releaseGate()

	// The attestation is validated before any clearance: the statement must be
	// exactly the one this build accepts, observedAt an RFC3339 instant, and the
	// claimed operator the session identity where the session carries one; where
	// it carries none the attestation is recorded as given, never verified. A
	// mismatch refuses validation before any clearance, naming the check.
	attestation := HostRecoveryAttestation{
		Operator:   strings.TrimSpace(params.Attestation.Operator),
		Statement:  strings.TrimSpace(params.Attestation.Statement),
		ObservedAt: strings.TrimSpace(params.Attestation.ObservedAt),
	}
	if err := validateRecoveryAttestationShape(attestation); err != nil {
		return appwire.HostTeardownRecoverResult{}, appwire.InvalidParams(fmt.Sprintf("teardown-recover %s: %v", remnantID, err))
	}
	if err := m.validateRecoveryOperator(ctx, attestation.Operator); err != nil {
		return appwire.HostTeardownRecoverResult{}, err
	}

	// A live attempt owns the remnant: a recover must not clear past a running
	// retry (nor overwrite its resolution), so it refuses busy exactly as a
	// concurrent retry does.
	if liveID, live := m.liveAttemptInThisBoot(remnantID); live {
		return appwire.HostTeardownRecoverResult{}, attemptBusyRefusal(name, remnantID, liveID)
	}
	priorAttemptID, priorAttempt, priorOpen := m.cfg.store.openAttemptFor(remnantID)
	attemptID := mintAttemptID()
	now := m.nowTime()

	m.cfg.mu.Lock()
	current, ok := m.cfg.store.remnantByID(remnantID)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostTeardownRecoverResult{}, teardownUnknownKeyRefusal(remnantID)
	}
	if !current.open() {
		m.cfg.mu.Unlock()
		return recoveredClearedResult(current), nil
	}
	remnant = current
	// The claim: "claims the remnant atomically (claim under the mutation lock
	// with an attempt token, so a concurrent retry racing the claim loses
	// exactly one of the two)".
	attempt := HostTeardownAttempt{
		RemnantID:         remnantID,
		State:             hostAttemptStateOpen,
		StartedAt:         now.UTC().Format(time.RFC3339),
		FencingEpochBoot:  m.cfg.bootID,
		FencingEpochOpSeq: uint64(now.UnixNano()),
	}
	change := hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: attempt}}
	if priorOpen {
		// "When a timed-out-but-open attempt record stands (gate free, attempt
		// fence open), the recover fences it first ... before the safety checks
		// below, so the clearance never lands past possibly-live cleanup."
		fenced := priorAttempt
		fenced.State = hostAttemptStateFencedClosed
		fenced.FencedAt = now.UTC().Format(time.RFC3339)
		change.fencedAttempt = &pendingHostAttempt{AttemptID: priorAttemptID, Attempt: fenced}
	}
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, change); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostTeardownRecoverResult{}, err
	}
	m.cfg.mu.Unlock()

	// The safety checks run outside the lock (they read the live registry and
	// the manager's bindings), then are re-checked immediately before the
	// clearing write below, under the lock, so a concurrent retry can neither
	// start inside the check nor have its in-progress cleanup marker cleared.
	if err := m.recoverySafetyCheck(remnantID, remnant); err != nil {
		// The refusal releases the claim: the attempt this call wrote is fenced
		// closed, because leaving it open would leave the remnant falsely busy —
		// the fence reads the attempt's boot epoch, so an open attempt of THIS
		// boot makes every later retry and recover refuse busy until a restart.
		// A failed fence write is surfaced beside the refusal rather than
		// swallowed: it leaves exactly that wedge behind.
		m.cfg.mu.Lock()
		entries := m.cfg.store.snapshot()
		fenceErr := m.persistHosts(entries, entries, hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: fencedAttemptRecord(attempt, m.nowTime())}})
		m.cfg.mu.Unlock()
		if fenceErr != nil {
			return appwire.HostTeardownRecoverResult{}, fmt.Errorf("%w; the refused recover left its attempt record open: %w", err, fenceErr)
		}
		return appwire.HostTeardownRecoverResult{}, err
	}

	clearedAt := m.nowTime()
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	// The re-check runs with the lock held, so it reads only the store's own
	// snapshot — liveHost would take the mutation lock again. Its refusal fences
	// the claim closed for the same reason the first check's does: an attempt
	// left open under this boot's epoch fences the name for the rest of the
	// process, so a later retry or recover would refuse busy instead of acting.
	if blocking := m.recoverySafetyCheckLocked(remnantID, remnant); blocking != nil {
		entries := m.cfg.store.snapshot()
		closed := fencedAttemptRecord(attempt, m.nowTime())
		if fenceErr := m.persistHosts(entries, entries, hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: closed}}); fenceErr != nil {
			// The deferred unlock (this branch holds the mutation lock) returns
			// it on the way out.
			return appwire.HostTeardownRecoverResult{}, fmt.Errorf("%w; the refused recover left its attempt record open: %w", blocking, fenceErr)
		}
		return appwire.HostTeardownRecoverResult{}, blocking
	}
	resolved := HostResolvedRemnant{
		ClearedAt:      clearedAt.UTC().Format(time.RFC3339),
		ResolutionKind: hostRemnantResolutionRecover,
		HostKind:       hostKindOf(remnant.Kind),
		MutationKey:    remnant.MutationKey,
		Attestation:    &attestation,
	}
	stored := remnant
	stored.Resolved = &resolved
	closed := attempt
	closed.State = hostAttemptStateFencedClosed
	closed.FencedAt = clearedAt.UTC().Format(time.RFC3339)
	entries = m.cfg.store.snapshot()
	finalize := hostPersistChange{
		resolved: &pendingHostRemnant{RemnantID: remnantID, Remnant: stored},
		attempt:  &pendingHostAttempt{AttemptID: attemptID, Attempt: closed},
	}
	if receipt, ok := m.resolvedReceipt(remnant, clearedAt, &attestation); ok {
		finalize.receipt = &pendingHostReceipt{Key: remnant.MutationKey, Receipt: receipt}
	}
	if err := m.persistHosts(entries, entries, finalize); err != nil {
		return appwire.HostTeardownRecoverResult{}, err
	}
	// Clearing a REMOVAL's remnant completes the removal, so the name's derived
	// state goes with it exactly as it does on the retry's completion: the
	// removal committed (its tombstone and receipt are durable and the fence
	// forbids a re-add while the remnant was open), and the unattested cleanup is
	// the operator's forward path precisely because no handle of the pinned
	// incarnation is reachable — so leaving the source registration and cached
	// session rows behind would keep rendering the removed host's sessions as
	// live until a restart. An edit's remnant leaves the name live and keeps its
	// state.
	if hostKindOf(remnant.Kind) == hostRemnantHostKindRemoved {
		m.dropHostDerivedState(remnant.Host)
	}
	return recoveredClearedResult(stored), nil
}

// liveAttemptInThisBoot reports whether an open attempt for the remnant is
// still live in THIS controller incarnation. Its holder released the host gate
// for the run, so the gate cannot answer that question — the attempt's own
// fencing epoch can: an open attempt whose epoch names this boot and which has
// not timed out is a run in progress, and fencing it would let two retries
// execute the same cleanup (or let a recover rewrite a live retry's
// resolution). Only a timed-out attempt or one from a previous boot is ours to
// take over.
func (m *hubHostManager) liveAttemptInThisBoot(remnantID string) (string, bool) {
	attemptID, attempt, ok := m.cfg.store.openAttemptFor(remnantID)
	if !ok || attempt.timedOut() {
		return "", false
	}
	if strings.TrimSpace(m.cfg.bootID) == "" {
		// No boot identity to compare (tests, embedders): an open attempt is
		// treated as live rather than clobbered, which is the fail-closed
		// direction.
		return attemptID, true
	}
	return attemptID, attempt.FencingEpochBoot == m.cfg.bootID
}

// attemptBusyRefusal is the typed busy refusal a retry or recover emits while a
// live attempt holds the remnant: "a live attempt still holding the gate
// refuses even that retry with the typed busy error — the gate holder owns the
// attempt".
func attemptBusyRefusal(name, remnantID, attemptID string) error {
	return hostBusyWireError(hostops.Busy(name, hostops.Holder{
		Kind:     hostops.HolderManager,
		Activity: "teardown attempt " + attemptID,
	}))
}

// fencedAttemptRecord marks a claim's attempt record fenced-closed after a
// refusal released it, so the name is not left fenced by an attempt that never
// ran.
func fencedAttemptRecord(attempt HostTeardownAttempt, now time.Time) HostTeardownAttempt {
	attempt.State = hostAttemptStateFencedClosed
	attempt.FencedAt = now.UTC().Format(time.RFC3339)
	return attempt
}

// isTeardownDeadline reports whether a run's error is the bounded deadline
// expiring rather than a refusal of the pinned target. It classifies by TYPE —
// errors.Is against context.DeadlineExceeded — never by matching the message
// text, so a refusal that happens to carry the phrase is not recorded as a
// timeout and a real timeout is not read as a refusal.
func isTeardownDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

// validateRecoveryOperator checks the attestation's operator against the
// session's identity when the session carries one (tests and any transport that
// stamps it through withSessionOperator). This build's transport names no
// principal, so the check refuses nothing there and the attestation is recorded
// as given — never verified — which is the unattributed posture the spec
// amends to (registry spec 08 §6, Amended 2026-09-29): the clearance is still
// an explicit operator decision recorded on the receipt, just not an attributed
// one.
func (m *hubHostManager) validateRecoveryOperator(ctx context.Context, operator string) error {
	identity := sessionOperator(ctx)
	if identity == "" {
		return nil
	}
	if operator != identity {
		return appwire.InvalidParams(fmt.Sprintf(
			"teardown-recover attestation operator %q is not the session's authenticated identity %q", operator, identity))
	}
	return nil
}

// sessionOperatorKey carries the session's authenticated identity, when one is
// known, to the handlers that must compare it.
type sessionOperatorKey struct{}

// withSessionOperator stamps the session's authenticated identity onto ctx.
func withSessionOperator(ctx context.Context, operator string) context.Context {
	return context.WithValue(ctx, sessionOperatorKey{}, operator)
}

// sessionOperator returns the authenticated identity the session carries, or ""
// when the transport carries none.
func sessionOperator(ctx context.Context) string {
	operator, _ := ctx.Value(sessionOperatorKey{}).(string)
	return strings.TrimSpace(operator)
}

// recoverySafetyCheckLocked is recoverySafetyCheck's second half: the same two
// conditions, read without taking the mutation lock (the caller holds it), so
// the re-check immediately before the clearing write cannot deadlock against
// the lock it runs under.
func (m *hubHostManager) recoverySafetyCheckLocked(remnantID string, remnant HostTeardownRemnant) error {
	host, ok := m.cfg.store.entryByName(remnant.Host)
	if !ok {
		host, ok = m.cfg.hosts.Get(remnant.Host)
	}
	if ok && host.IncarnationID == remnant.IncarnationID && host.Generation == remnant.Generation {
		return appwire.Conflict(fmt.Sprintf(
			"teardown-recover %s refused: the live entry for %q still carries the remnant's pinned pair (%d, %q)",
			remnantID, remnant.Host, remnant.Generation, remnant.IncarnationID))
	}
	return nil
}

// recoverySafetyCheck re-runs the safety conditions the clearance requires:
// "no live handle tagged with the remnant's `(generation, incarnationId)` pair
// exists, no supervisor or channel binding names the remnant's pinned target".
// A failure names the blocking check.
func (m *hubHostManager) recoverySafetyCheck(remnantID string, remnant HostTeardownRemnant) error {
	// "no supervisor or channel binding names the remnant's pinned target": the
	// channel the manager holds for the name is the observable attachment, and
	// the supervisor binding lives inside that same hold. (The epoch-keyed
	// supervisor roster an earlier revision named here was withdrawn with the
	// crash-fencing program, comp08; this build reads the channel the manager
	// publishes.)
	if m.cfg.manager != nil {
		if _, attached := m.cfg.manager.ChannelIfAttached(remnant.Host); attached {
			return appwire.Conflict(fmt.Sprintf(
				"teardown-recover %s refused: host %q still holds a live channel binding for the pinned target %s",
				remnantID, remnant.Host, remnant.PendingTeardown.Kind))
		}
	}
	// "no live handle tagged with the remnant's `(generation, incarnationId)`
	// pair exists": the live registry entry carries the pair every handle is
	// tagged with — the FULL pair, never the incarnation alone, because an edit
	// preserves the incarnation while advancing the generation and refusing on
	// the incarnation would make every update remnant unrecoverable.
	if host, ok := m.liveHost(remnant.Host); ok &&
		host.IncarnationID == remnant.IncarnationID && host.Generation == remnant.Generation {
		return appwire.Conflict(fmt.Sprintf(
			"teardown-recover %s refused: the live entry for %q still carries the remnant's pinned pair (%d, %q)",
			remnantID, remnant.Host, remnant.Generation, remnant.IncarnationID))
	}
	return nil
}

// ---------------------------------------------------------------------------
// The pinned teardown
// ---------------------------------------------------------------------------

// runPinnedTeardown executes one remnant's pinned teardown target to
// completion: "The retry targets handles by incarnation-id equality — never by
// generation alone and never by name lookup: a name-based lookup resolving to a
// handle whose incarnation id differs from the remnant's is refused, never
// executed, even when its generation tag equals the remnant's."
//
// The pinned handle is the remnant's persisted `cleanupHandle`, and resolution
// is record load — the operation store's mirrored ownership boundary, whose
// triple must still name the remnant's pinned pair — never a live in-process
// handle. A handle that does not resolve is the typed `teardown-unknown-key`
// refusal, which is exactly the state `teardown-recover` exists to clear.
func (m *hubHostManager) runPinnedTeardown(ctx context.Context, remnantID string, remnant HostTeardownRemnant) (teardownRunResult, error) {
	if !remnant.CleanupHandle.resolvable() {
		return teardownRunResult{Seam: remnant.Seam}, teardownUnknownKeyRefusal(remnantID)
	}
	if err := m.resolveCleanupHandle(remnantID, remnant); err != nil {
		return teardownRunResult{Seam: remnant.Seam}, err
	}
	// The pinned incarnation is the only one this run may touch. A live entry
	// that is a *different* incarnation means the pinned handles are already
	// gone — the registry moved on — and re-running the rebind step against the
	// new incarnation would tear down state the remnant does not own, so the run
	// is a no-op that still succeeds: there is nothing of the pinned
	// incarnation left to destroy.
	host, live := m.cfg.hosts.Get(remnant.Host)
	if live && host.IncarnationID != remnant.IncarnationID {
		return teardownRunResult{}, nil
	}
	if !live {
		// Nothing live carries the pinned pair (a post-remove remnant, or a
		// crash). For a removal that is the end of it — there is nothing left to
		// stop. For an edit the staged runtime set may still need re-applying:
		// "every phase re-applies the staged runtime set first (`hub.toml`
		// already holds the new config, so the live runtime must converge to
		// it)", so a name the file still carries is registered back into the live
		// set — the entry the commit landed, never a fresh identity.
		if remnant.PendingTeardown.Kind != hostTeardownKindUpdate {
			return teardownRunResult{}, nil
		}
		entry, ok := m.hubTOMLFileEntry(remnant.Host)
		if !ok {
			return teardownRunResult{}, nil
		}
		result, err := m.runBoundedTeardown(ctx, "update-host", func() error {
			if err := m.reapplyStagedEntry(ctx, entry); err != nil {
				return fmt.Errorf("teardown-retry %s: re-apply host %q: %w", remnantID, remnant.Host, err)
			}
			return nil
		})
		return result, err
	}
	switch remnant.PendingTeardown.Kind {
	case hostTeardownKindRemove:
		if m.cfg.manager != nil {
			result, err := m.runBoundedTeardown(ctx, "remove-host", func() error {
				if err := m.cfg.manager.RemoveHost(remnant.Host); err != nil {
					return fmt.Errorf("teardown-retry %s: remove host %q: %w", remnantID, remnant.Host, err)
				}
				return nil
			})
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return teardownRunResult{Seam: "remove-host"}, err
		}
		if err := m.cfg.hosts.Remove(remnant.Host); err != nil {
			return teardownRunResult{Seam: "remove-host"}, fmt.Errorf("teardown-retry %s: %w", remnantID, err)
		}
		return teardownRunResult{}, nil
	case hostTeardownKindUpdate:
		// The update's post-commit rebind re-applies the entry the file holds
		// for the pinned incarnation — "so an already-applied swap lands on the
		// same values" — and an already-applied rebind is a no-op: where the live
		// entry already carries the file's effective fields and the pinned pair,
		// re-applying would only re-bump the generation an edit already advanced.
		// The entry is passed to the registry EXACTLY as the file holds it: its
		// persisted (generation, incarnationId, presenceEpoch) are the commit's
		// identity, and the registry's Update applies a generation only when its
		// pending stamp matches the entry (hostreg's peekStampLocked). Overwriting
		// the identity with the live host's would break that match and mint a
		// fresh generation that no durable record ever carries — the live row
		// would then report a generation the file, the store, and the receipt do
		// not hold, and the original mutationId could no longer replay its
		// recorded outcome.
		entry, ok := m.hubTOMLFileEntry(remnant.Host)
		if !ok {
			entry = host
		}
		// "Already applied": the live entry carries the file's effective fields
		// AND the committed generation, so the rebind has nothing left to do.
		if sameEffectiveHostEntryFields(host, entry) && host.Generation == entry.Generation {
			return teardownRunResult{}, nil
		}
		if m.cfg.manager != nil {
			result, err := m.runBoundedTeardown(ctx, "update-host", func() error {
				if err := m.cfg.manager.UpdateHost(entry, func(retired hostreg.Host) {
					m.cfg.state.retire(remnant.Host, retired.Generation)
				}); err != nil {
					return fmt.Errorf("teardown-retry %s: update host %q: %w", remnantID, remnant.Host, err)
				}
				return nil
			})
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return teardownRunResult{Seam: "update-host"}, err
		}
		if err := m.cfg.hosts.Update(entry); err != nil {
			return teardownRunResult{Seam: "update-host"}, fmt.Errorf("teardown-retry %s: %w", remnantID, err)
		}
		return teardownRunResult{}, nil
	default:
		return teardownRunResult{Seam: remnant.Seam}, teardownUnknownKeyRefusal(remnantID)
	}
}

// runBoundedTeardown runs one manager teardown step under the retry's bounded
// deadline. The manager's teardown paths take no context — `RemoveHost` blocks
// on the per-host gate and then on the ssh child's exit — so a genuinely wedged
// remote would otherwise hold this call forever: the attempt record would stay
// open under this boot's epoch and every later retry and recover would refuse
// busy until a process restart, which is precisely the state the deadline
// exists to avoid (spec §6: "a stuck remote process therefore surfaces a
// terminal outcome with a live retry handle, never an indefinitely held gate").
//
// The step therefore runs on its own goroutine and this call returns on
// whichever lands first. A step the deadline abandons keeps running to whatever
// end it reaches — nothing can cancel a blocking lock acquisition from outside
// — and that is the honest trade: the attempt record is the fence that keeps a
// second retry off the same cleanup, and the gate it eventually releases is the
// manager's own.
//
// Kill/waiting a superseded run and compare-and-advancing the guard were
// withdrawn with the crash-fencing program (comp08), so a step the deadline
// abandons keeps running: this build records the attempt's epoch (boot id + op
// sequence) and the fenced/timed-out state a later retry takes over, and the
// abandoned run's overlap is the accepted residual (§6).
func (m *hubHostManager) runBoundedTeardown(ctx context.Context, seam string, step func() error) (teardownRunResult, error) {
	done := make(chan error, 1)
	go func() { done <- step() }()
	select {
	case err := <-done:
		if err != nil {
			return teardownRunResult{Seam: seam}, err
		}
		return teardownRunResult{}, nil
	case <-ctx.Done():
		return teardownRunResult{Seam: seam}, ctx.Err()
	}
}

// reapplyStagedEntry registers entry back into the live set: the runtime half of
// the phase-aware recovery's "re-apply the staged runtime set first". It is the
// same insert the mutation's own swap performs (the manager's AddHost when one is
// wired, else the registry's Add), so a crash-window entry converges to the
// configuration hub.toml already holds.
func (m *hubHostManager) reapplyStagedEntry(ctx context.Context, entry hostreg.Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.cfg.manager != nil {
		return m.cfg.manager.AddHost(entry)
	}
	return m.cfg.hosts.Add(entry)
}

// resolveCleanupHandle resolves a remnant's persisted cleanup handle without any
// live in-process handle (spec §6: "rehydration is record load, never
// live-handle resurrection"). The local arm loads the operation store's
// mirrored ownership boundary for the name and requires it to still name the
// remnant's pinned (generation, incarnation id) pair — a boundary that moved on
// is a handle this retry cannot act through, which is the typed
// `teardown-unknown-key` refusal.
func (m *hubHostManager) resolveCleanupHandle(remnantID string, remnant HostTeardownRemnant) error {
	handle := remnant.CleanupHandle
	switch handle.Kind {
	case cleanupHandleKindLocal:
		// Incarnation equality only: generations advance on every edit, so an
		// update remnant pins the RETIRED generation while the mirrored boundary
		// carries the committed one — and §6 is explicit that the retry targets
		// handles "by incarnation-id equality — never by generation alone". The
		// incarnation id is never reused, so it is what keeps a retry off a
		// successor's handles.
		if handle.IncarnationID != remnant.IncarnationID {
			return teardownUnknownKeyRefusal(remnantID)
		}
		if m.cfg.ops == nil {
			// No operation store is wired (tests, embedders): the handle's own
			// record is the whole durable handle, and it is complete.
			return nil
		}
		mark, ok := m.cfg.ops.Boundary(remnant.Host)
		if !ok {
			return teardownUnknownKeyRefusal(remnantID)
		}
		if mark.IncarnationID != handle.IncarnationID {
			return teardownUnknownKeyRefusal(remnantID)
		}
		return nil
	default:
		return teardownUnknownKeyRefusal(remnantID)
	}
}

// ---------------------------------------------------------------------------
// The response arms
// ---------------------------------------------------------------------------

// alreadyClearedArm renders the `already-cleared` success arm from the persisted
// resolved-remnant record: "a retry naming an already-cleared remnant returns
// the already-cleared success arm, a receipt-returned no-op — never a second
// teardown, never not-found", "reconstructable after restart without the live
// entry".
func (m *hubHostManager) alreadyClearedArm(remnant HostTeardownRemnant) appwire.HostTeardownRetryResult {
	return m.retryArm(appwire.HostTeardownOutcomeCleared, remnant, "")
}

// retryArm renders one of the retry's six declared arms from the remnant's
// pinned identity and the resolved record.
func (m *hubHostManager) retryArm(outcome string, remnant HostTeardownRemnant, seam string) appwire.HostTeardownRetryResult {
	escalation := m.escalationAgeSec(remnant)
	if hostKindOf(remnant.Kind) == appwire.HostKindRemoved {
		row := m.removedRowFor(remnant)
		switch outcome {
		case appwire.HostTeardownOutcomeCleared:
			return appwire.HostTeardownRetryResult{HostTeardownRetryClearedRemoved: &appwire.HostTeardownRetryClearedRemoved{
				Outcome: outcome, HostKind: appwire.HostKindRemoved, Host: row, RemnantID: remnantIDFor(remnant), EscalationAgeSec: escalation,
			}}
		case appwire.HostTeardownOutcomeFailed:
			return appwire.HostTeardownRetryResult{HostTeardownRetryFailedRemoved: &appwire.HostTeardownRetryFailedRemoved{
				Outcome: outcome, HostKind: appwire.HostKindRemoved, Host: row, RemnantID: remnantIDFor(remnant), Seam: seam, EscalationAgeSec: escalation,
			}}
		default:
			return appwire.HostTeardownRetryResult{HostTeardownRetryCompleteRemoved: &appwire.HostTeardownRetryCompleteRemoved{
				Outcome: outcome, HostKind: appwire.HostKindRemoved, Host: row, RemnantID: remnantIDFor(remnant), EscalationAgeSec: escalation,
			}}
		}
	}
	row := m.liveRowFor(remnant)
	switch outcome {
	case appwire.HostTeardownOutcomeCleared:
		return appwire.HostTeardownRetryResult{HostTeardownRetryClearedLive: &appwire.HostTeardownRetryClearedLive{
			Outcome: outcome, HostKind: appwire.HostKindLive, Host: row, RemnantID: remnantIDFor(remnant), EscalationAgeSec: escalation,
		}}
	case appwire.HostTeardownOutcomeFailed:
		return appwire.HostTeardownRetryResult{HostTeardownRetryFailedLive: &appwire.HostTeardownRetryFailedLive{
			Outcome: outcome, HostKind: appwire.HostKindLive, Host: row, RemnantID: remnantIDFor(remnant), Seam: seam, EscalationAgeSec: escalation,
		}}
	default:
		return appwire.HostTeardownRetryResult{HostTeardownRetryCompleteLive: &appwire.HostTeardownRetryCompleteLive{
			Outcome: outcome, HostKind: appwire.HostKindLive, Host: row, RemnantID: remnantIDFor(remnant), EscalationAgeSec: escalation,
		}}
	}
}

// remnantIDFor returns the remnant's durable id, carried on the record for
// scans that must name it.
func remnantIDFor(remnant HostTeardownRemnant) string { return remnant.remnantID }

// liveRowFor renders the live arm's row: the live entry when the name is still
// live, else the recorded receipt's row, else the pinned target's own fields —
// never a fabricated live row.
func (m *hubHostManager) liveRowFor(remnant HostTeardownRemnant) appwire.HostRow {
	// The registry's own lock, never the mutation lock: the retry's finalization
	// renders its arm with the mutation lock held, so a row built here must not
	// take it again.
	if host, ok := m.cfg.hosts.Get(remnant.Host); ok && host.IncarnationID == remnant.IncarnationID {
		row := hostEntryRow(host)
		if remnant.open() {
			row.OpenRemnantID = remnant.remnantID
		}
		row.EscalationAgeSec = m.escalationAgeSec(remnant)
		return row
	}
	if receipt, ok := m.cfg.store.receiptsSnapshot()[remnant.MutationKey]; ok {
		row := hostReceiptRow(receipt, hostMutationKind(remnant.Kind))
		row.EscalationAgeSec = m.escalationAgeSec(remnant)
		return row
	}
	return appwire.HostRow{
		Name:             remnant.Host,
		Origin:           hostOriginHubTOML,
		Generation:       remnant.Generation,
		IncarnationID:    remnant.IncarnationID,
		EscalationAgeSec: m.escalationAgeSec(remnant),
	}
}

// removedRowFor renders the removed arm's row from the tombstone the removal
// left, falling back to the recorded receipt's row.
func (m *hubHostManager) removedRowFor(remnant HostTeardownRemnant) appwire.RemovedRow {
	if tombstone, ok := m.cfg.store.tombstoneSnapshot()[remnant.Host]; ok {
		return removedRowForRow(tombstoneRow(tombstone))
	}
	if receipt, ok := m.cfg.store.receiptsSnapshot()[remnant.MutationKey]; ok {
		return removedRowForRow(hostReceiptRow(receipt, hostMutationKind(remnant.Kind)))
	}
	row := appwire.HostRow{
		Name:          remnant.Host,
		Origin:        hostOriginHubTOML,
		Generation:    remnant.Generation,
		IncarnationID: remnant.IncarnationID,
		Removed:       true,
	}
	row.EscalationAgeSec = m.escalationAgeSec(remnant)
	return removedRowForRow(row)
}

// removedRowForRow narrows a tombstone-shaped HostRow into the dedicated
// removed-row arm (registry spec 08 §11: "`RemovedRow` ... is NOT a `HostRow`").
func removedRowForRow(row appwire.HostRow) appwire.RemovedRow {
	return appwire.RemovedRow{
		Name:             row.Name,
		Address:          row.Address,
		User:             row.User,
		KeyPath:          row.KeyPath,
		EvenerPath:       row.EvenerPath,
		ConfigPath:       row.ConfigPath,
		Addr:             row.Addr,
		Roots:            row.Roots,
		Origin:           row.Origin,
		Generation:       row.Generation,
		IncarnationID:    row.IncarnationID,
		Removed:          true,
		RetainedRows:     row.RetainedRows,
		RowsTruncated:    row.RowsTruncated,
		EscalationAgeSec: row.EscalationAgeSec,
		OpenRemnantID:    row.OpenRemnantID,
	}
}

// escalationAgeSec is spec §11's `escalationAgeSec`: "present only on rows ...
// whose name holds an open remnant past the escalation bound — the escalation
// age the expiry-escalation rule promises". It is the whole-second age of the
// remnant's commit instant, and absent while the remnant is inside the bound.
func (m *hubHostManager) escalationAgeSec(remnant HostTeardownRemnant) *int64 {
	if !remnant.open() {
		return nil
	}
	committedAt, err := time.Parse(time.RFC3339, remnant.CommittedAt)
	if err != nil || committedAt.IsZero() {
		return nil
	}
	age := m.nowTime().Sub(committedAt)
	if age < m.cfg.policy.escalationAge {
		return nil
	}
	seconds := int64(age.Seconds())
	return &seconds
}

// resolvedReceipt renders the original mutation's receipt with the clearance
// recorded on it: "recording the attestation (operator, statement, observedAt)
// on the original mutation receipt beside `remnantResolvedAt` (outcome becomes
// `committed` with `remnantResolvedAt`)".
func (m *hubHostManager) resolvedReceipt(remnant HostTeardownRemnant, clearedAt time.Time, attestation *HostRecoveryAttestation) (HostMutationReceipt, bool) {
	receipts := m.cfg.store.receiptsSnapshot()
	receipt, ok := receipts[remnant.MutationKey]
	if !ok {
		return HostMutationReceipt{}, false
	}
	receipt.Outcome = hostReceiptOutcomeCommitted
	receipt.RemnantID = remnantIDFor(remnant)
	receipt.RemnantResolvedAt = clearedAt.UTC().Format(time.RFC3339)
	receipt.RecoveryAttestation = attestation
	return receipt, true
}

// recoveredClearedResult renders `teardown-recover`'s response from the
// persisted record, so a replay after restart returns the same values.
func recoveredClearedResult(remnant HostTeardownRemnant) appwire.HostTeardownRecoverResult {
	resolved := remnant.Resolved
	clearedAt := ""
	if resolved != nil {
		clearedAt = resolved.ClearedAt
	}
	return appwire.HostTeardownRecoverResult{
		Outcome:     appwire.HostTeardownOutcomeRecovered,
		RemnantID:   remnantIDFor(remnant),
		ClearedName: remnant.Host,
		ClearedAt:   clearedAt,
		HostKind:    hostKindOf(remnant.Kind),
	}
}

package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Ensure returns a connected, initialized channel for host, preflighting and
// attaching as needed. It is idempotent while attached: repeated calls return
// the same *Channel.
//
// After a successful attach, Ensure starts a supervisor that watches the link
// and reconnects (fresh ssh bridge + client, never a hub start) with bounded
// exponential backoff and jitter until Manager.Close. ctx bounds this call and
// the handshake, not the channel's lifetime.
func (m *Manager) Ensure(ctx context.Context, name string) (*Channel, error) {
	return m.ensure(ctx, name, nil)
}

// EnsureRegistration is Ensure for one registration: the attach proceeds only
// while the registry still holds captured, checked at the lookup and again under
// the host gate, so a name removed and added again in between is refused with
// ErrHostNotFound instead of the replacement being dialed. A caller restoring a
// connection it lost uses this so it never connects what the user has replaced.
func (m *Manager) EnsureRegistration(ctx context.Context, captured hostreg.Host) (*Channel, error) {
	return m.ensure(ctx, captured.Name, &captured)
}

func (m *Manager) ensure(ctx context.Context, name string, expected *hostreg.Host) (*Channel, error) {
	if !m.beginEnsure() {
		return nil, ErrManagerClosed
	}
	defer m.ensureWG.Done()
	if m.reg == nil {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	host, ok := m.reg.Get(name)
	if !ok || (expected != nil && !hostreg.SameRegistration(host, *expected)) {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	// The registry is the authority on the name's spelling, and every lookup
	// below (and in the supervisor) is keyed off host.Name: normalizing here
	// keeps a caller's stray whitespace from creating a second channel entry and
	// a second per-host lock.
	name = host.Name

	if m.opts.beforeHostGate != nil {
		m.opts.beforeHostGate(name)
	}
	lock := m.hostLock(name)
	// The acquisition is paired here, on the caller's own reference: every
	// return path below releases it, including the ones that hand the gate to
	// a supervisor — startSupervise takes that goroutine's own reference
	// before it exists, so this release never drops an entry a live supervisor
	// still gates on.
	defer m.releaseHostLock(name)
	// Honor the caller's context while waiting for the host gate: a canceled
	// caller must not park behind another Ensure's or a supervisor's long
	// preflight/attach, and must not be handed a channel afterwards.
	if err := lockHostCtx(ctx, lock); err != nil {
		return nil, err
	}
	// The gate is held, so register this attach as its holder: a pipeline
	// try-acquire that fails while the attach runs reports the attach (the
	// manager holder class), never a stale holder or an invented operation.
	lock.holdAs(hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"})
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return nil, err
	}
	// The entry can have been deregistered — or replaced — between the lookup
	// above and this gate: RemoveHost drops the registry entry while holding
	// this same lock, and a remove/re-add of the name swaps in a new entry,
	// so rechecking the name alone would pass while the re-added name is
	// live. Compare the full entry — and its generation: content equality
	// cannot tell a remove/re-add of a byte-identical entry apart, while the
	// registry's insert generation always advances across the cycle. This
	// attach captured one identity, and a name that now resolves to a
	// different entry must be refused before it dials — a channel built from
	// the removed entry would be published under the re-added name. The
	// re-add's own caller attaches the fresh identity.
	if !m.reg.SameRegistration(name, host) {
		lock.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}

	// The mapped channel has to belong to the registration this attach just
	// validated, not merely be mapped under the name: a remove/re-add that
	// swapped the entry without a teardown (a caller writing the registry
	// directly, which is how the hub's own registry behaves with no manager
	// wired) leaves the removed identity's channel under the name, and handing
	// it back would give the re-added host a live channel to the removed
	// address. A channel from another registration falls through to the attach
	// below, which retires it on the way out.
	if ch := m.liveChannel(name); ch != nil && ch.host.Generation == host.Generation {
		err := m.channelUsable(name, ch)
		lock.Unlock()
		if err == nil {
			return ch, nil
		}
		if errors.Is(err, ErrManagerClosed) {
			// Close tore this channel down; closing it again is a no-op.
			_ = ch.Close()
		}
		return nil, err
	}
	// A channel whose link has dropped is no longer usable, but it keeps
	// ownership until a replacement is attached: were this attach to fail, its
	// supervisor must still find the channel mapped so it can run the reconnect
	// loop. Only a successful publish retires it.
	stale := m.currentChannel(name)
	// Retire the replaced channel on every path out of here, early returns
	// included: a Close racing this attach must not leave the old ssh child, its
	// pipes, and its transport alive. It runs outside the host lock, since
	// Channel.Close blocks on that child's exit.
	defer func() {
		if stale != nil {
			_ = stale.Close()
		}
	}()
	// ensureOnce bounds each of its phases itself (preflight, deploy/restart, then
	// the attach handshake), so no outer attemptLimit is applied here; tie the
	// attempt to the manager's lifetime instead, so an in-flight attempt cannot
	// outlive Manager.Close with its ssh child alive.
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(m.baseCtx, cancel)
	defer stopClose()
	ch, err := m.ensureOnce(attemptCtx, host, true)
	if err != nil {
		err = m.mapAttemptErr(err)
		// Shutdown began while this attach was in flight. Close owns the last
		// events for the channels it tears down, and this caller goroutine is only
		// quiesced by ensureWG (see beginEnsure), so emitting here would surface a
		// Disconnected or a Failed for a host nothing will ever announce.
		if m.baseCtx.Err() != nil {
			if isTerminal(err) {
				m.clearChannel(name)
				m.stopSupervisor(name)
			}
			lock.Unlock()
			return nil, err
		}
		// The caller gave up while the attempt was in flight. The transport-shaped
		// failure its cancellation produced is not this host's problem, and the
		// host's state is not this caller's to announce: report the caller's own
		// error and emit nothing, exactly as the already-canceled path above does.
		// A terminal cause found concurrently is kept, because it names what
		// actually went wrong.
		if cerr := ctx.Err(); cerr != nil && !isTerminal(err) {
			lock.Unlock()
			return nil, cerr
		}
		// A terminal failure is the documented EventFailed case, not just a state
		// transition: a consumer has to be able to tell it from a retryable one.
		if isTerminal(err) {
			// A retired channel was announced as attached, so its consumer needs the
			// matching Detached before the terminal Failed — the contract this file's
			// event docs state, and without it component 05 keeps the source.
			if stale != nil {
				m.detachEvent(name, StateDisconnected)
			}
			// Close canceling the attach is not a host failure: consumers shutting the
			// manager down must not see an EventFailed for it, exactly as the early
			// isClosed return above emits nothing.
			if !errors.Is(err, ErrManagerClosed) {
				m.failedEvent(name, err)
			}
			// The replacement cannot be built, so the dropped channel must not keep
			// ownership: its supervisor would treat the host as its own and repeat a
			// failure that cannot succeed. The deferred close reaps it.
			m.clearChannel(name)
			// A supervisor that retired its own channel and is waiting out a
			// backoff must stop too: otherwise it repeats the terminal outcome
			// (a second EventFailed for a host already announced as finished).
			m.stopSupervisor(name)
		}
		m.stateEvent(name, StateDisconnected)
		lock.Unlock()
		return nil, err
	}
	// Validate before publishing, as reconnectOnce does. Publishing first would
	// displace the dropped channel whose supervisor is waiting to recover, and a
	// replacement that died in the handshake would then leave the host with no
	// supervisor at all — the predecessor's gone with the map entry and none
	// started for the replacement.
	if ch.isClosed() || ch.isLost() {
		// A dropped predecessor stays mapped: its supervisor still owns the host and
		// will reconnect. With no predecessor there is nothing to supervise, so the
		// state is honestly disconnected.
		if stale == nil {
			m.stateEvent(name, StateDisconnected)
		}
		lock.Unlock()
		_ = ch.Close()
		return nil, errChannelDropped(name)
	}
	// Recheck the registry under the lock before publishing, on the full
	// entry and its generation: a deregistration that does not pass through
	// this gate (a caller dropping the registry entry directly, as the hub's
	// host manager does when no sshconn manager is wired) must not gain a
	// fresh channel for a host that is gone, and a remove/re-add that swapped
	// the entry mid-attach must not have the channel built from the removed
	// entry published under the re-added name — byte-identical re-adds
	// included, because the registry assigns the re-add a new generation. The
	// replacement is reaped and the dropped predecessor keeps ownership,
	// exactly as a replacement that died in its handshake does.
	if !m.reg.SameRegistration(name, host) {
		lock.Unlock()
		_ = ch.Close()
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	if !m.publishChannel(name, ch, host) {
		// Close landed while this attach was in flight. Handing back a channel
		// that nothing will ever supervise would be a lie, so reap it.
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if m.opts.afterPublish != nil {
		m.opts.afterPublish(name, ch)
	}
	if m.isClosed() || ch.isClosed() {
		// Close landed between publishing and announcing: nothing will supervise
		// this channel, so consumers must not hear about it either.
		// The channel it replaced was announced, so its consumer still needs the
		// matching Detached: Close's own pairing sees the replacement, not it.
		if stale != nil {
			m.detachEvent(name, StateDisconnected)
		}
		// The discarded replacement must give the map slot back: leaving it mapped
		// would keep a closed channel where a consumer treats Close as terminal.
		m.clearChannel(name)
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if ch.isLost() {
		// The link died between the pre-publish validation and here.
		err := m.lostAfterPublish(name, stale)
		lock.Unlock()
		_ = ch.Close()
		return nil, err
	}
	// Announce under the lock, as reconnectOnce does. That is what orders the
	// retired channel's Detached before this Attached, and it keeps a concurrent
	// Ensure or supervisor from interleaving its own transitions with them.
	if stale != nil {
		m.detachEvent(name, StateReconnecting)
	}
	m.attachEvent(name, ch, StateAttached)
	if !m.startSupervise(host, ch, lock) {
		// Close set its flag between the publish and here, so no supervisor will
		// ever own this channel. Pair the Attached just announced so a consumer can
		// drop the source it installed, and reap the channel rather than leave a
		// closed entry mapped.
		m.detachEvent(name, StateDisconnected)
		m.clearChannel(name)
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if m.isClosed() || ch.isClosed() {
		// Close landed between announcing and returning. Consumers must be able to
		// drop what they just installed, so pair the Attached with a Detached while
		// the lock still orders the two.
		m.detachEvent(name, StateDisconnected)
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if ch.isLost() {
		// The link died between validating and returning. The supervisor this call
		// just started owns the reconnect and will pair the Attached with a
		// Detached; the caller must not be handed a dead channel as a success.
		lock.Unlock()
		return nil, errChannelDropped(name)
	}
	if err := ctx.Err(); err != nil {
		// The caller gave up while the attach was finishing — during the
		// EventAttached callback or the final validation. The channel is
		// manager-owned and already supervised, so it is not torn down: report the
		// cancellation rather than hand a channel to a caller that cannot use it.
		lock.Unlock()
		return nil, err
	}
	lock.Unlock()
	return ch, nil
}

// AttachUnderGate re-attaches host under the caller's already-held per-host
// gate: the operation-owned attach/reattach primitive (registry spec 08 §14)
// the deploy/restart worker consumes (deploy pipeline 08b §6 seam (d)). The
// caller holds the gate — Manager.TryAcquire's hold, or the hold a
// connect/attach path took — and this method never acquires it: the gate is
// the same non-reentrant per-host lock, so re-running the normal attach path
// (Ensure) here would deadlock. It re-runs the attach dialing closure
// (ensureOnce) for the registration it was handed, publishes the replacement
// exactly as Ensure and reconnectOnce do, and starts no supervisor: the caller
// owns the channel until terminal verification, then calls the returned
// handoff — still under the held gate — which starts the supervisor for the
// channel so the host keeps automatic reconnect after the operation (§6: the
// suppress-supervisor scope ends at verification).
//
// The caller presents the holder its hold registered (the operation's record
// id): a gate held by some other holder is not the caller's hold, so a call
// presenting a different holder refuses before any dial rather than publish
// under another holder's exclusion. explicit distinguishes the two arms: the
// first attach (true) keeps Ensure's explicit-attach semantics, the
// first-attach bootstrap included; the restart's reattach (false) is
// reconnect-shaped — the restart already produced the serving hub, and a
// reconnect never starts one (Ensure's supervisor path passes false for the
// same reason).
//
// A predecessor mapped under the name (the channel a restart dropped when the
// operation reattaches) is replaced and reaped here, with its outstanding
// Attached paired by a Detached before the replacement's Attached, exactly as
// Ensure's replacement does — otherwise the predecessor's consumer would keep
// a source nothing owns. The handoff always ensures the channel is supervised
// at most once: an already-live channel whose loop exists is left to it, and
// one without a loop gets the missing supervisor. It is safe to call more than
// once — a repeat reports the first result — and it refuses when the gate is no
// longer held by the caller's own holder, so a released or stolen hold never
// starts a supervisor outside the caller's exclusion.
func (m *Manager) AttachUnderGate(ctx context.Context, host hostreg.Host, holder hostops.Holder, explicit bool) (*Channel, func() bool, error) {
	name := strings.TrimSpace(host.Name)
	if name == "" {
		return nil, nil, errors.New("sshconn: an attach-under-gate needs a host name")
	}
	if !m.beginEnsure() {
		return nil, nil, ErrManagerClosed
	}
	defer m.ensureWG.Done()
	// The caller's own hold is this primitive's whole premise: publishing under
	// someone else's exclusion is what the gate-aware entry exists to prevent,
	// so a free gate — or one held by a different holder — refuses before any
	// dial.
	if !m.hostGateHeldBy(name, holder) {
		return nil, nil, fmt.Errorf("%w: host %q; AttachUnderGate runs under the caller's hold and never acquires the gate", hostops.ErrGateNotHeld, name)
	}
	if m.reg == nil || !m.reg.SameRegistration(name, host) {
		// The generation-pinned entry the caller resolved is not the live
		// registration (a remove/re-add, or a direct registry swap, landed under
		// the hold): never attach the superseded identity's configuration.
		return nil, nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	// An already-live channel for this same registration is the host attached:
	// hand it back untouched, exactly the state Ensure reports for an
	// idempotent re-attach. The returned handoff still ensures supervision: a
	// channel this primitive published earlier has no supervisor until a
	// handoff starts one (the attach suppresses startup), so the handoff starts
	// the missing loop; a channel with an existing loop is left to it.
	if ch := m.liveChannel(name); ch != nil && ch.MatchesRegistration(host) {
		return ch, m.attachHandoff(host, ch, holder), nil
	}
	stale := m.currentChannel(name)
	// Tie the attempt to the manager's lifetime, exactly as Ensure does: a
	// Close landing mid-attempt must cancel it rather than let it outlive the
	// manager with its ssh child alive.
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(m.baseCtx, cancel)
	defer stopClose()
	ch, err := m.ensureOnce(attemptCtx, host, explicit)
	if err != nil {
		err = m.mapAttemptErr(err)
		// Mirror Ensure's non-close failure handling: consumers tracking the
		// host's phase must see the attach end, not park on the last
		// intermediate state. A closing manager or a canceled caller owns its
		// own events (Close emits the Detached; a canceled caller emits
		// nothing), exactly as Ensure suppresses them there.
		if m.baseCtx.Err() == nil && ctx.Err() == nil {
			if isTerminal(err) && stale == nil && !errors.Is(err, ErrManagerClosed) {
				// The host row's last-attach-error surfaces EventFailed; with no
				// predecessor to own the terminal outcome, this attach is where
				// the terminal cause is announced, exactly as Ensure announces
				// it. A reattach with a predecessor keeps the predecessor's
				// supervisor (it must still reconnect), so no terminal event is
				// claimed for it here.
				m.failedEvent(name, err)
				// stale == nil does not mean no supervisor exists: a loop that
				// retired its own channel clears the map and waits out its
				// backoff, and it would retry into this same terminal cause and
				// announce a second EventFailed for one host. Ensure's terminal
				// arm stops such a loop; this one must too. With a predecessor
				// (stale != nil) its own supervisor owns the terminal
				// announcement, so it is deliberately left running there.
				m.stopSupervisor(name)
			}
			m.stateEvent(name, StateDisconnected)
		}
		return nil, nil, err
	}
	if ch.isClosed() || ch.isLost() {
		// The link died between the handshake and the publish: reap the
		// replacement and report the drop; the predecessor (if any) keeps the
		// slot, exactly as Ensure's own validation does. With no predecessor
		// the honest state is disconnected, the same event Ensure emits there.
		_ = ch.Close()
		if stale == nil {
			m.stateEvent(name, StateDisconnected)
		}
		return nil, nil, errChannelDropped(name)
	}
	if !m.reg.SameRegistration(name, host) {
		_ = ch.Close()
		return nil, nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	if !m.publishChannel(name, ch, host) {
		_ = ch.Close()
		return nil, nil, ErrManagerClosed
	}
	if m.opts.afterPublish != nil {
		m.opts.afterPublish(name, ch)
	}
	if ch.isLost() || ch.isClosed() {
		// The link died between the validation and the announcement: reap the
		// replacement, hand the slot back (to the predecessor when one exists),
		// and pair nothing — the check runs before the event pair exactly as
		// Ensure's post-publish death path orders it, so no consumer is ever
		// told about a channel nothing will supervise.
		_ = ch.Close()
		return nil, nil, m.lostAfterPublish(name, stale)
	}
	// Order the predecessor's Detached before the replacement's Attached, as
	// Ensure's replacement does: the predecessor's own supervisor is parked on
	// the gate this call holds, so it will stand down without pairing them.
	if stale != nil {
		m.detachEvent(name, StateReconnecting)
	}
	m.attachEvent(name, ch, StateAttached)
	if stale != nil {
		// The map no longer references the predecessor; reaping it can block on
		// its ssh child's exit, which Kill bounds (the child is the dead link
		// the operation's restart left).
		_ = stale.Close()
	}
	return ch, m.attachHandoff(host, ch, holder), nil
}

// ensureOnce runs one full preflight-then-decide-then-attach sequence with the
// state transitions around it. Every branch is chosen by the one decision table
// in ensureDecision; this function owns only the ordering and the recovery.
//
// The ladder keeps the disk contract separate from the selected serving state:
//   - endpoint-capable protocol-broken or flag-less hosts may be upgraded;
//   - a deploy or restart that leaves the selected owner stopped retains intent
//     for the next Ensure, matching ErrRestart's stated contract;
//   - a deploy on a host with no hub present is a fresh install, not a restart:
//     it falls through to the explicit-attach bootstrap (below) and a reconnect
//     never turns it into a hub start.
//
// explicit marks an explicit attach request (component 06's first host action),
// which is the only place the first-attach bootstrap start may run; a reconnect
// passes false so it never starts a hub.
func (m *Manager) ensureOnce(ctx context.Context, host hostreg.Host, explicit bool) (ch *Channel, err error) {
	a := &ensureAttempt{m: m, ctx: ctx, host: host, explicit: explicit}
	// An error while this attempt's durable record is open fails that record, so
	// no exit between the record's creation and its settlement can leave it open.
	defer func() {
		if err != nil {
			a.finishRecord(err)
		}
	}()
	if err := a.collectFacts(); err != nil {
		return nil, err
	}
	if err := a.settlePending(); err != nil {
		return nil, err
	}
	if ch, err := a.attachLive(); ch != nil || err != nil {
		return ch, err
	}
	if err := a.decide(); err != nil {
		return nil, err
	}
	if a.deploy {
		if err := a.runDeployLeg(); err != nil {
			return nil, err
		}
	}
	// Deployment gets its chance to repair the executable first. Every start or
	// restart, including recovery of a recorded command, requires a usable target
	// before it can replace a live predecessor or consume the pending intent.
	if err := validateStartupContract(a.host.Name, a.facts); err != nil {
		return nil, err
	}
	if pending := m.pendingRestart(a.host.Name); pending.selected == nil {
		if err := pending.validateExecutable(a.host.Name, a.facts); err != nil {
			return nil, err
		}
	}
	if a.restart {
		if err := a.runRestartLeg(); err != nil {
			return nil, err
		}
	}
	return a.bootstrapAndAttach()
}

// mapAttemptErr reports a closed manager for a retryable failure of an attempt
// that Close canceled: the canceled base context, not the host, is why, and a
// supervisor must not keep chasing it. A terminal cause found concurrently is
// kept, because it names what actually went wrong.
func (m *Manager) mapAttemptErr(err error) error {
	if m.baseCtx.Err() != nil && !isTerminal(err) {
		return ErrManagerClosed
	}
	return err
}

// ensureAttempt is the state one ensureOnce attempt carries between its phases.
type ensureAttempt struct {
	m        *Manager
	ctx      context.Context
	host     hostreg.Host
	explicit bool

	preflight            preflightResult
	facts                Preflight
	report               hubendpoint.Report
	running              hubIdentity
	runningKnown         bool
	repairUnusableTarget bool
	liveIncompatible     bool
	deploy, restart      bool

	// finish settles the durable operation record this attempt opened (nil when
	// none is open). A deploy-only attempt settles it at the end of its deploy
	// leg; a planned restart leg runs under the same record, and a restart-only
	// attempt mints its own.
	finish func(error)
}

// finishRecord settles the open durable record once; later calls do nothing.
func (a *ensureAttempt) finishRecord(err error) {
	finish := a.finish
	a.finish = nil
	if finish != nil {
		finish(err)
	}
}

// collectFacts probes the host and, when its endpoint can be resolved, the
// serving generation.
func (a *ensureAttempt) collectFacts() error {
	m := a.m
	// Address the executable this Manager already resolved for the host when the
	// registry has no evener_path: a deploy target from an earlier attempt (or a
	// discovered install) is the intended run target. Probing bare `evener`
	// could judge another installation's launch contract instead.
	m.applyResolvedTarget(&a.host)
	m.stateEvent(a.host.Name, StatePreflighting)
	// Bound only the preflight here: deploy/restart below get deployLimit, and
	// attach bounds its own handshake (initTimeout).
	preflightCtx, cancelPreflight := context.WithTimeout(a.ctx, m.opts.attemptLimit())
	preflight, err := m.collectPreflight(preflightCtx, a.host)
	cancelPreflight()
	if err != nil {
		return err
	}
	a.preflight, a.facts = preflight, preflight.facts
	// Preflight may have discovered the executable during THIS attempt (the
	// installer default ~/.local/bin/evener, off the non-interactive PATH) and
	// recorded it. Re-apply it before anything addresses the binary again: the
	// deploy/restart phases and, crucially, attach → channelArgv otherwise build
	// the bridge from an empty evener_path, and evenerCommand falls back to the
	// bare word `evener`, which the host cannot resolve. The first attach to an
	// already-healthy host at that location would then fail with
	// command-not-found and only recover on a later attempt.
	m.applyResolvedTarget(&a.host)

	// The remote resolver, not a guessed listener, selects the serving generation.
	if !a.facts.ExecutableMissing && endpointCapability(a.host, a.facts) == nil {
		probeCtx, cancelProbe := context.WithTimeout(a.ctx, m.opts.attemptLimit())
		a.report, err = m.resolveHostEndpoint(probeCtx, a.host, a.facts)
		cancelProbe()
		if err != nil {
			if m.pendingRestart(a.host.Name).selected != nil {
				return fmt.Errorf("%w: %w", ErrRestart, err)
			}
			return err
		}
	}
	a.running, a.runningKnown = identityFromReport(a.report)
	a.repairUnusableTarget = a.facts.ExecutableMissing
	return nil
}

// settlePending reconciles effects recorded by earlier attempts against what
// the host now proves.
func (a *ensureAttempt) settlePending() error {
	m, host, facts := a.m, a.host, a.facts
	var err error
	if pending := m.pendingRestart(host.Name); pending.deployment {
		if a.preflight.launchErr != nil && !facts.ExecutableMissing {
			if pending.selected != nil {
				probeCtx, cancelProbe := context.WithTimeout(a.ctx, m.opts.attemptLimit())
				a.repairUnusableTarget, err = m.recordedTargetUnusable(probeCtx, host)
				cancelProbe()
			}
			if err != nil || !a.repairUnusableTarget {
				return a.preflight.launchErr
			}
		}
		if !pending.needsDeployment(facts) && !pending.restart {
			// An acknowledged write and matching label do not prove a usable
			// installation. Keep its repair authority until its contract is valid.
			if err := validateStartupContract(host.Name, facts); err != nil {
				return err
			}
			m.clearPendingRestart(host.Name)
		} else if pending.needsDeployment(facts) && !m.canDeploy() {
			return ErrDeployDisabled
		}
	}
	// A replacement can finish before its SSH reply is lost. Reconcile only
	// independently proven successors before an unusable disk contract blocks
	// attachment. Usable contracts retain the existing recorded recovery path.
	// An unproven effect keeps every startup and recovery guard.
	unusableStartup := a.preflight.launchErr != nil || validateStartupContract(host.Name, facts) != nil
	if pending := m.pendingRestart(host.Name); pending.selected != nil && !pending.deployment && unusableStartup {
		if err := checkSelectedRestartRegistration(host, pending.selected.host); err != nil {
			return err
		}
		probeCtx, cancelProbe := context.WithTimeout(a.ctx, m.opts.attemptLimit())
		_, complete, _ := m.proveSelectedRestartComplete(probeCtx, host, facts, pending)
		cancelProbe()
		if complete {
			m.clearPendingRestart(host.Name)
		}
	}
	// Settle against the intent recorded before the effect, never later disk
	// facts. A changed file must not turn a failed deployment into a success.
	if pending := m.pendingRestart(host.Name); a.runningKnown && pending.selected == nil && pending.completedBy(a.running, facts) {
		// A hub already serving exactly the expected build resolves whatever an
		// earlier restart or start left outstanding; do not launch a second hub
		// over it. A recorded START has no predecessor to exclude — bootstrapHub
		// only ever starts a hub where nothing was serving — so the expected build
		// (version and, on the snapshot channel, the pinned commit) alone settles
		// it, exactly as recoverRestart judges a recovered start (a start's zero
		// `replaced` can never satisfy differentProcessFrom, so the build match is
		// the only thing that can clear it). A recorded REPLACEMENT must
		// additionally be provably not the process it meant to replace: the version
		// is not that proof, because two unstamped builds both report "dev", so a
		// restart that never took would otherwise be cleared on the old process's
		// answer and the next Ensure would attach to it. A start time that is
		// missing on either side is not proof either: an old hub whose health body
		// carries no started_at would otherwise clear the pending marker on the
		// same answer, so the identity must be known on BOTH sides and must differ
		// before the marker is cleared (differentProcessFrom).
		m.clearPendingRestart(host.Name)
	}
	return nil
}

// attachLive attaches to the running hub when no pending effect must settle
// first. It returns a nil channel and nil error when the ladder must go on, and
// records in liveIncompatible that the live process needs upgrading.
func (a *ensureAttempt) attachLive() (*Channel, error) {
	m, host, facts := a.m, a.host, a.facts
	// Pending effects must settle before a usable old process can satisfy
	// Connect. Otherwise the live handshake is the connection authority: disk
	// build labels and optional startup flags do not justify replacing it.
	if m.pendingRestart(host.Name).active() || facts.ExecutableMissing || a.report.State == "stopped" {
		return nil, nil
	}
	m.stateEvent(host.Name, StateAttaching)
	channel, attachErr := m.attach(a.ctx, host, facts)
	if attachErr == nil {
		return channel, nil
	}
	refuse := func(err error) error {
		if a.preflight.launchErr != nil {
			return fmt.Errorf("%w: %w", err, a.preflight.launchErr)
		}
		if capabilityErr := endpointCapability(host, facts); capabilityErr != nil {
			return fmt.Errorf("%w: %w", capabilityErr, err)
		}
		return err
	}
	if !errors.Is(attachErr, ErrProtocolIncompatible) {
		return nil, refuse(attachErr)
	}
	// The live process is older, but the installed executable can already
	// be newer: another controller may have upgraded the disk and failed
	// before its restart. Do not answer that skew by installing this
	// controller's older build over the newer one.
	if facts.LaunchCheckKnown && appwire.NewerProtocolVersion(facts.Protocol) {
		return nil, fmt.Errorf("%w: host %q's running hub speaks an older protocol, but its installed executable speaks %q, newer than this controller's %q; update the controller or restart the host into its newer build instead of downgrading it",
			ErrHostProtocolNewer, host.Name, facts.Protocol, appwire.ProtocolVersion)
	}
	if !m.canDeploy() {
		return nil, refuse(attachErr)
	}
	a.liveIncompatible = true
	return nil, nil
}

// decide applies the capability gate and the decision table to choose the deploy
// and restart legs.
func (a *ensureAttempt) decide() error {
	m, host, facts := a.m, a.host, a.facts
	// Only a positively incompatible live handshake authorizes updating an
	// unclassified installation. All other management keeps the capability gate.
	retainedRepair := m.pendingRestart(host.Name)
	retainedReinstall := a.repairUnusableTarget && retainedRepair.deployment && retainedRepair.selected != nil && retainedRepair.needsDeployment(facts)
	if !facts.ExecutableMissing && !a.liveIncompatible && !retainedReinstall {
		if err := endpointCapability(host, facts); err != nil {
			if a.preflight.launchErr != nil {
				return fmt.Errorf("%w: %w", err, a.preflight.launchErr)
			}
			return err
		}
	}
	// Presence is selected-lock ownership, never an unrelated socket.
	hubPresent := !facts.ExecutableMissing && a.report.State != "stopped"

	a.deploy, a.restart = m.ensureDecision(host.Name, facts, a.liveIncompatible, hubPresent)
	if !a.deploy && a.preflight.launchErr != nil {
		return a.preflight.launchErr
	}
	return nil
}

// runDeployLeg installs the controller's build under a durable operation record.
// A deploy with no restart leg planned settles the record here; a planned
// restart leg keeps it open.
func (a *ensureAttempt) runDeployLeg() error {
	m, host := a.m, a.host
	m.stateEvent(host.Name, StateDeploying)
	// The Ensure-triggered deploy is a durable fenced operation (deploy
	// pipeline 08b §6): the record carrying its fencing epoch is persisted
	// before the deploy's first remote write, and the gate hold publishes the
	// operation so a contender's busy refusal names it. A Manager with no
	// recorder wired has no record to persist, so the deploy refuses here —
	// before any mutating command — rather than running unrecorded (§6's
	// durable-record rule; the same contract the restart-only arm holds).
	hook := m.ensureDeployHook()
	if hook == nil {
		return errors.New("sshconn: this Manager has no deploy recorder wired, so an Ensure-triggered deploy performs no mutating SSH command (§6); nothing was launched")
	}
	finish, err := hook(host)
	if err != nil {
		return err
	}
	a.finish = finish
	deployCtx, cancelDeploy := context.WithTimeout(a.ctx, m.opts.deployLimit())
	intent := m.pendingRestart(host.Name)
	if !intent.deployment {
		intent = m.deploymentIntent(a.restart, a.running)
	}
	resolvedTarget, err := m.deploySelected(deployCtx, host, a.facts, deployOpts{
		realEndpointAuthority: a.liveIncompatible || (intent.deployment && intent.selected != nil),
		record:                m.deploymentRecorder(host.Name, intent),
	})
	cancelDeploy()
	if err != nil {
		return err
	}
	a.facts, err = m.completeDeployLeg(a.ctx, &a.host, a.facts, resolvedTarget, intent.expectedVersion, a.restart)
	if err != nil {
		return err
	}
	if !a.restart {
		// A deploy with no restart leg planned is over: finish where the
		// deploy-only path always finished.
		a.finishRecord(nil)
	}
	return nil
}

// runRestartLeg replaces or starts the hub under a durable operation record: the
// deploy's record when a deploy planned the leg, or a restart-only record it
// mints itself (§6's "a reconnect with no durable record performs no mutating
// SSH command").
func (a *ensureAttempt) runRestartLeg() error {
	m, host, facts := a.m, a.host, a.facts
	if !a.deploy {
		// The hook mints the record under the held gate before the leg's first
		// remote command, and the record's scope arms the leg below. With no
		// recorder wired there is no record and no scope, so the attempt refuses
		// here — before any mutating command — rather than restarting unrecorded.
		hook := m.ensureRestartHook()
		if hook == nil {
			return errors.New("sshconn: this Manager has no restart recorder wired, so a restart-only Ensure attempt performs no mutating SSH command (§6); nothing was launched")
		}
		finish, err := hook(host)
		if err != nil {
			return err
		}
		a.finish = finish
	}
	m.stateEvent(host.Name, StateRestarting)
	restartCtx, cancelRestart := context.WithTimeout(a.ctx, m.opts.deployLimit())
	var restartErr error
	if pending := m.pendingRestart(host.Name); pending.selected != nil && !a.runningKnown {
		// A previous restart killed the old hub and left no listener. There is
		// no hub to kill now, so complete the recorded restart instead.
		restartErr = m.recoverSelectedRestart(restartCtx, host, facts, pending)
	} else {
		version, pin := expectedServedBuild(facts, m.opts.controllerVersion())
		replaced := a.running
		if pending.active() {
			version, pin = pending.expectedVersion, pending.expectedGitSHA
			if !pending.start {
				replaced = pending.restartPredecessor(a.running)
			}
		}
		restartErr = m.restartHub(restartCtx, host, facts, version, pin, replaced)
	}
	cancelRestart()
	if restartErr != nil {
		// The record stayed open through the leg, so the leg's failure is
		// recorded on it — never a `complete` record for an operation that
		// did not finish.
		return restartErr
	}
	a.finishRecord(nil)
	if !a.deploy {
		// A restart-only attempt (replacing a stale process, or completing a
		// recorded restart) still needs the post-phase re-read: the serving process
		// was replaced, so the launch contract read before it is stale. A deploy
		// already re-read the on-disk contract before the restart path, where the
		// terminal judgement must win, and the restart serves that same on-disk
		// binary, so there is nothing new to read here.
		var err error
		a.facts, err = m.reReadLaunchContract(a.ctx, host, facts)
		return err
	}
	return nil
}

// bootstrapAndAttach starts a stopped hub for an explicit first attach, then
// attaches.
func (a *ensureAttempt) bootstrapAndAttach() (*Channel, error) {
	m, host, facts := a.m, a.host, a.facts
	expected := m.opts.controllerVersion()
	// Launch-check establishes whether the installed executable can be started;
	// Initialize then verifies the process serving the connection. Build labels
	// do not gate ordinary attachment. A deployment above has its own strict
	// installed-build verification before reaching this point.
	// First attach to a stopped host must be able to start the hub. The probe
	// above only restarts a hub that is already answering, and the bridge is a
	// client that must not start one, so a configured host whose hub is not
	// running could never become usable. Bootstrap only for an explicit attach
	// request (never a reconnect or the background snapshot walk, which is
	// attached-only) and only when this attempt would otherwise attach to an
	// acceptable build: the terminal refusals above have already fired for a host
	// that cannot be upgraded, so there is nothing worth starting.
	if a.explicit && !a.runningKnown && !a.restart && m.pendingRestart(host.Name).selected == nil {
		m.stateEvent(host.Name, StateRestarting)
		startCtx, cancelStart := context.WithTimeout(a.ctx, m.opts.deployLimit())
		startVersion, startPin := facts.Version, ""
		if a.deploy {
			startVersion, startPin = expectedServedBuild(facts, expected)
		}
		bootErr := m.bootstrapHub(startCtx, host, facts, startVersion, startPin)
		cancelStart()
		if bootErr != nil {
			return nil, bootErr
		}
	}
	// The host keeps the build it has: nothing installed this controller's, and the
	// protocol — not the build label — decides compatibility. Say so, so accepting
	// the difference is never silent.
	if hostBuildDiffers(facts, expected) {
		m.logf("sshconn: host %s runs build %q, controller is %q; attaching anyway (a protocol-compatible host need not run this controller's build, and no deploy path replaced it)", host.Name, facts.Version, expected)
	}
	m.stateEvent(host.Name, StateAttaching)
	return m.attach(a.ctx, host, facts)
}

// bootstrapHub starts only an explicitly requested, resolver-verified stopped root.
func (m *Manager) bootstrapHub(ctx context.Context, host hostreg.Host, facts Preflight, expectedVersion, expectedGitSHA string) error {
	owner, err := m.resolveOwnedEndpoint(ctx, host, facts)
	if err != nil {
		return err
	}
	if owner.report.State != "stopped" {
		return fmt.Errorf("%w: selected root is not stopped", errOwnerEvidence)
	}
	pending := m.setSelectedRestart(host, owner, "selected-start", expectedVersion, expectedGitSHA)
	pending.start = true
	m.storePendingRestart(host.Name, pending)
	return m.recoverSelectedRestart(ctx, host, facts, pending)
}

// deployRequired reports whether a stopped installation needs this controller's
// build. Only a proven older protocol authorizes a replacement: a newer protocol
// belongs to the controller's update, and an unparsable one is not proof of an
// upgrade. A running hub is judged by initialize before this gate is used.
func (m *Manager) deployRequired(facts Preflight) bool {
	if !m.canDeploy() {
		return false
	}
	if facts.ExecutableMissing {
		return true
	}
	if !facts.LaunchCheckKnown {
		return false
	}
	if appwire.OlderProtocolVersion(facts.Protocol) {
		return true
	}
	if facts.Protocol != appwire.ProtocolVersion {
		return false
	}
	return !slices.Contains(facts.LaunchFlags, requiredLaunchFlag)
}

// ensureDecision preserves recorded effects and repairs unusable installations.
// Build labels alone never request deployment or process replacement.
func (m *Manager) ensureDecision(name string, facts Preflight, liveIncompatible, hubPresent bool) (deploy, restart bool) {
	pending := m.pendingRestart(name)
	if pending.active() && !pending.deployment {
		// A recorded start/restart authorized only that effect, not installation
		// of the controller's build. Validate its target before retrying it.
		return false, true
	}
	deploy = liveIncompatible || m.deployRequired(facts) || pending.needsDeployment(facts)
	restart = (deploy && hubPresent) || pending.command != ""
	if pending.deployment {
		restart = pending.restart
	}
	return deploy, restart
}

// validateStartupContract checks the executable that will serve the next hub.
// A usable live connection does not imply its on-disk replacement is usable.
func validateStartupContract(hostName string, facts Preflight) error {
	if facts.LaunchCheckKnown && facts.Protocol != appwire.ProtocolVersion {
		if appwire.NewerProtocolVersion(facts.Protocol) {
			return fmt.Errorf("%w: host %q speaks protocol %q, newer than this controller's %q; update the controller instead of downgrading the host",
				ErrHostProtocolNewer, hostName, facts.Protocol, appwire.ProtocolVersion)
		}
		return fmt.Errorf("%w: host %q protocol %q, want %q", ErrProtocolIncompatible, hostName, facts.Protocol, appwire.ProtocolVersion)
	}
	if !slices.Contains(facts.LaunchFlags, requiredLaunchFlag) {
		return fmt.Errorf("%w: host %q launch_flags %v missing %q", ErrLaunchContract, hostName, facts.LaunchFlags, requiredLaunchFlag)
	}
	return nil
}

// refreshLaunchContract re-reads the on-disk launch contract around a deploy or
// restart and refuses an incompatible startup contract. The pre-restart facts
// describe the build the restart replaced, so judging the launch contract on
// them would let a host predating a required flag, or speaking an older
// protocol, never be accepted even after a successful upgrade.
func (m *Manager) refreshLaunchContract(ctx context.Context, host hostreg.Host, facts Preflight) (Preflight, error) {
	refreshed, err := m.probeStartupLaunchCheck(ctx, host)
	if err != nil {
		if errors.Is(err, ErrProtocolIncompatible) {
			// The strict probe refuses before answering, so read the executable's
			// own contract: callers still need to tell an older installation from
			// a newer one. The refusal itself stays authoritative.
			if described, _, describeErr := m.describeLaunchContract(ctx, host); describeErr == nil {
				facts.ExecutableMissing = false
				facts.LaunchCheckKnown = true
				facts.Protocol = described.Protocol
				facts.LaunchFlags = described.LaunchFlags
				facts.Version = described.Version
			}
		}
		return facts, err
	}
	facts.ExecutableMissing = false
	facts.LaunchCheckKnown = true
	facts.HubEndpointSchema = refreshed.HubEndpointSchema
	if err := endpointCapability(host, facts); err != nil {
		return facts, err
	}
	facts.Protocol = refreshed.Protocol
	facts.LaunchFlags = refreshed.LaunchFlags
	// Report the version the re-probe actually read. The restart verified the
	// *running* hub against expected, but this launch-check describes the on-disk
	// binary, which can still differ (a replaced or partially installed file);
	// overwriting it with expected would make the channel claim a match it did
	// not observe. A difference here is what ensureOnce's post-deploy gate
	// refuses on: a deploy that leaves it cannot be retried into converging.
	facts.Version = refreshed.Version
	return facts, validateStartupContract(host.Name, facts)
}

// reReadLaunchContract re-reads the on-disk launch contract under the deploy
// limit. It is refreshLaunchContract plus the phase timeout, so the two call
// sites that judge a deploy or a restart bound the remote probe identically
// instead of each repeating the WithTimeout.
func (m *Manager) reReadLaunchContract(ctx context.Context, host hostreg.Host, facts Preflight) (Preflight, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	return m.refreshLaunchContract(refreshCtx, host, facts)
}

// completeDeployLeg finishes a deploy leg whose install write succeeded: it
// acknowledges the write, remembers where the build landed (so the restart,
// health re-probe, attach and later attempts address that binary), re-reads the
// launch contract and judges the build. The judgement comes before any restart
// path: a wrong operator-supplied artifact is a permanent cause, and restarting
// onto it fails retryably in readiness, which would mask the terminal refusal and
// leave the supervisor pushing and restarting forever without converging. A
// deploy with no restart leg planned is over, so its pending intent is cleared.
func (m *Manager) completeDeployLeg(ctx context.Context, host *hostreg.Host, facts Preflight, target, expectedVersion string, restart bool) (Preflight, error) {
	m.acknowledgeDeployment(host.Name)
	if target != "" {
		// The installer fallback with an empty evener_path installed to its own
		// default location; the supervisor's reconnect starts from the original
		// registry host, so remember it rather than re-resolve and re-deploy.
		host.EvenerPath = target
		m.setResolvedTarget(host.Name, target)
	}
	after, err := m.reReadLaunchContract(ctx, *host, facts)
	if err != nil {
		return facts, err
	}
	if err := m.deployedBuildNotStamped(host.Name, after, expectedVersion); err != nil {
		return facts, err
	}
	if !restart {
		m.clearPendingRestart(host.Name)
	}
	return after, nil
}

// deployedBuildNotStamped is the terminal refusal a deploy that left the host on a
// build other than the controller's must produce, or nil when the fresh facts pin
// the controller's build (or could not be read at all). It judges the launch
// contract re-read after the phase, the only evidence available for an
// operator-supplied artifact built from another tree: it targets the right
// platform and carries a foreign identity, so the pre-push check passes and this
// disagreement is what remains. Attaching anyway would serve a host on a build the
// controller did not stamp, violating the deployment's identity guarantee;
// retrying re-pushes the same artifact, so the refusal is terminal rather
// than the retryable ErrDeploy (the same reason ErrControllerDirty is).
//
// It is judged only where a deploy ran. A pass that merely started or restarted the
// hub launched the build already on disk, and the attach rule allows that build to
// be the host's own, so judging the on-disk version after a restart-only pass would
// refuse exactly the host the rule exists to attach to. What a restart must prove —
// that the process answering is the one the phase produced — belongs to the waits,
// which already do it; what this adds is the on-disk judgement, and only a deploy
// changes what is on disk.
//
// The remedy clause comes through Options.DeployHelp (unstampedRemedy), so a hub
// names the flags to set exactly as its sibling refusals do while an embedder with
// no flags keeps the library's own sentence.
func (m *Manager) deployedBuildNotStamped(hostName string, facts Preflight, expected string) error {
	if !hostBuildDiffers(facts, expected) {
		return nil
	}
	return fmt.Errorf("%w: host %q still reports version %q after this controller deployed its build, want %q; the build the host now holds was not built from this controller's tree, so the host cannot be pinned to the controller's build — %s",
		ErrDeployUnstamped, hostName, facts.Version, expected, m.unstampedRemedy())
}

// protocolMismatch classifies a positive protocol disagreement during the
// initialize handshake. noProtocolMismatch is not a disagreement this
// controller can act on: a missing protocol field, an unparsable version, and
// generic JSON-RPC errors stay transport/response failures.
type protocolMismatch int

const (
	noProtocolMismatch protocolMismatch = iota
	// peerProtocolOlder means the host speaks an older protocol, so Connect may
	// install this controller's build and restart the host into it.
	peerProtocolOlder
	// peerProtocolNewer means the host speaks a newer protocol. This controller
	// cannot speak it, and installing its own older build would downgrade the
	// host, so the refusal is terminal and names the controller update.
	peerProtocolNewer
)

// classifyProtocolMismatch reads explicit protocol evidence from an initialize
// failure. The typed mismatch covers a successful initialize response that
// names another protocol and the legacy InvalidRequest an older server sends
// (translated by appwire.Client.Initialize). upgradeRequired is emitted only by
// a server whose protocol is newer than the announced client's, so it names a
// newer host.
func classifyProtocolMismatch(err error) protocolMismatch {
	if mismatch, ok := errors.AsType[appwire.ProtocolVersionMismatchError](err); ok {
		switch {
		case appwire.OlderProtocolVersion(mismatch.Got):
			return peerProtocolOlder
		case appwire.NewerProtocolVersion(mismatch.Got):
			return peerProtocolNewer
		default:
			return noProtocolMismatch
		}
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		return noProtocolMismatch
	}
	raw, err := json.Marshal(wire.Data)
	if err != nil {
		return noProtocolMismatch
	}
	var data appwire.ErrorData
	if json.Unmarshal(raw, &data) == nil && data.EvenerErrorInfo == appwire.ErrorUpgradeRequired {
		return peerProtocolNewer
	}
	return noProtocolMismatch
}

// attach spawns the bridge, wraps its stdio in a StreamTransport, and
// initializes the AppWire client.
func (m *Manager) attach(ctx context.Context, host hostreg.Host, facts Preflight) (*Channel, error) {
	argv := channelArgv(m.opts, host)
	sink := newDiagSink(m.diagWriter)
	stdio, err := m.runner.Start(ctx, argv, sink)
	if err != nil {
		return nil, sshRunFailure(host.Name, "start bridge", err, sink.tail())
	}

	stream := &stdioReadWriter{in: stdio.Stdin(), out: stdio.Stdout()}
	monitor := newLinkMonitor(stream)
	transport := appwire.NewStreamTransport(monitor)
	ch := &Channel{
		host:      host,
		facts:     facts,
		stdio:     stdio,
		transport: transport,
		lost:      make(chan struct{}),
		done:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	// A short read (EOF/error) is the link-down edge. Wire it before the read
	// loop starts so no early drop is missed. The client reads through a wrapper
	// that reports codec-level receive errors too, because the byte-level monitor
	// cannot see a frame the codec rejects after a successful read.
	monitor.onErr = ch.markLost
	client := appwire.NewClient(&lossWatchingTransport{Transport: transport, onErr: ch.markLost})
	ch.client = client
	// The client read loop must outlive this call's ctx; it ends when the
	// transport is closed (Channel.Close or the link dying).
	client.Start(m.baseCtx)

	// Bound the handshake by initTimeout, never by the caller's or the attempt's
	// deadline: neither caller imposes the attempt limit on this path. Ensure
	// hands ensureOnce plain context.WithCancel (so an in-flight attempt cannot
	// outlive the manager), and reconnectOnce passes the supervisor's own
	// cancellable context, which carries no deadline either — so a hung
	// Initialize stops at initTimeout (default 30s) rather than running to an
	// outer attemptLimit (default 70s) the old comment still described.
	// WithTimeout keeps whichever deadline is earlier.
	initCtx, cancel := context.WithTimeout(ctx, m.opts.initTimeout())
	defer cancel()

	handshake, err := client.Initialize(initCtx, appwire.InitializeParams{
		ProtocolVersion: appwire.ProtocolVersion,
		ClientInfo:      appwire.ClientInfo{Name: m.opts.clientName(), Version: m.opts.clientVersion()},
	})
	if err != nil {
		// Reap synchronously before reading the sink: os/exec copies the child's
		// stderr from its own goroutine and Wait returns only once that copy is
		// done, so a classification cannot read a partial diagnostic.
		_ = reapBridge(transport, stdio)
		if m.baseCtx.Err() != nil {
			// Close landed while the handshake was in flight. The canceled base
			// context, not a transport fault, is why this channel is unusable.
			return nil, ErrManagerClosed
		}
		switch classifyProtocolMismatch(err) {
		case peerProtocolOlder:
			return nil, fmt.Errorf("%w: host %q: %w", ErrProtocolIncompatible, host.Name, err)
		case peerProtocolNewer:
			return nil, fmt.Errorf("%w: host %q: %w", ErrHostProtocolNewer, host.Name, err)
		}
		// A started ssh ran a remote command, so neither its stderr (which the
		// remote command's own output shares) nor its exit status can prove the
		// refusal is ssh's own: ssh forwards the remote command's status unchanged,
		// including 255. Stay retryable, matching the one-shot preflight path
		// (isSSHAuthFailure); only a failure to spawn ssh is unambiguous, and the
		// Start-error branch above already classifies that one.
		return nil, fmt.Errorf("%w: host %q initialize: %w: %s", ErrSSHStart, host.Name, err, sink.tail())
	}
	// Publish the handshake before the channel is handed to any caller, so the
	// attached-only HandshakeIfAttached never races the attach it reports on.
	ch.handshake = handshake

	if m.baseCtx.Err() != nil {
		_ = reapBridge(transport, stdio)
		return nil, ErrManagerClosed
	}

	go func() {
		_ = stdio.Wait()
		// Retire the channel's liveness before announcing the reaping. A
		// consumer that has observed the child's exit (stopped) — or that
		// decides liveness without consulting stopped at all — must never find
		// the already-exited process still offered by ClientIfAttached or
		// installedLiveChannel, so lost is closed before stopped.
		ch.markLost()
		close(ch.stopped)
		if h := m.opts.afterChildExit; h != nil {
			h(host.Name, ch)
		}
	}()
	return ch, nil
}

// reapBridge tears down a bridge that never became a channel, and returns the
// child's own wait error so the caller can read ssh's exit status. The
// synchronous Wait is load-bearing: os/exec copies the child's stderr into the
// diagnostic sink from its own goroutine and returns from Wait only once that
// copy has finished, so a failure cannot be classified off the sink before the
// child is reaped. It also closes the stream, which releases the pipes the child
// held.
func reapBridge(transport *appwire.StreamTransport, stdio Stdio) error {
	if transport != nil {
		_ = transport.Close()
	}
	_ = stdio.Kill()
	return stdio.Wait()
}

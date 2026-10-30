package sshconn

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// lossWatchingTransport reports every receive error as the channel's link-down
// edge. linkMonitor only sees read-level failures, so a frame the codec rejects
// after a successful read — malformed JSON, a bad length, an overflow — would
// otherwise leave the manager holding a channel whose reader is gone. It does not
// forward appwire.Pinger, deliberately: keepalive stays ssh's own ServerAlive*.
//
// Close is a link-down edge too. The client closes the transport itself on a
// notification overflow (client.go's ErrNotificationOverflow path), which never
// surfaces as a Recv error, so without this the manager would keep offering a
// channel whose reader is gone and no supervisor would reconnect.
type lossWatchingTransport struct {
	appwire.Transport
	onErr func()
}

func (t *lossWatchingTransport) Recv(ctx context.Context) (appwire.Message, error) {
	msg, err := t.Transport.Recv(ctx)
	if err != nil {
		t.onErr()
	}
	return msg, err
}

func (t *lossWatchingTransport) Close() error {
	t.onErr()
	return t.Transport.Close()
}

// supervise waits for host's channel to drop, then reconnects with bounded
// exponential backoff. It never issues a hub-start command: re-attach is a
// fresh bridge/client against the already-running host hub.
//
// It holds the host lock only around state inspection, channel teardown, and
// one attach attempt. The backoff sleep happens with the lock released, so a
// concurrent Ensure never waits behind a delay that can reach BackoffMax.
func (m *Manager) supervise(ctx context.Context, host hostreg.Host, ch *Channel, lock *hostLockGate) {
	select {
	case <-ch.lost:
	case <-ctx.Done():
		return
	}

	if m.opts.beforeSuperviseGate != nil {
		m.opts.beforeSuperviseGate(host.Name, ch)
	}
	// A canceled loop no longer owns the host, and standing down needs no gate:
	// return before contending for it. This covers the teardown ordering this
	// path exists for — a loop is canceled, then its link is dropped, so the loop
	// wakes on the drop with its context already canceled — and it stands down
	// here instead of taking (or parking on) the gate it no longer owns. A loop
	// canceled while already parked on Lock still takes the gate once, and the
	// post-acquire check below returns it.
	if ctx.Err() != nil {
		return
	}
	lock.Lock()
	lock.holdAs(hostops.Holder{Kind: hostops.HolderManager, Activity: "reconnect"})
	// Ownership, not liveness, decides whether this supervisor still has work: the
	// manager closing ends it, and a replaced channel means the replacement's
	// supervisor owns the host now. A channel that was closed while still mapped is
	// still ours to reconnect — Ensure may have retired a dropped one, and the host
	// must not be left unsupervised because of that.
	if ctx.Err() != nil || m.currentChannel(host.Name) != ch {
		lock.Unlock()
		return
	}
	m.clearChannel(host.Name)
	m.stateEvent(host.Name, StateReconnecting)
	m.detachEvent(host.Name, StateReconnecting)
	// Tearing the dead channel down can block on the ssh child's exit, which
	// needs no lock; a concurrent Ensure only has to be serialized against the
	// attach below.
	lock.Unlock()
	_ = ch.Close()

	// BackoffMax bounds every reconnect delay, the first one included: a caller
	// that configures a maximum below the base must not have its first retry wait
	// longer than the bound it asked for.
	delay := min(m.opts.backoffBase(), m.opts.backoffMax())
	for {
		if err := m.opts.waitSleep(ctx, m.jitterFor(delay)); err != nil {
			// A canceled loop no longer owns the host, so it announces nothing.
			// stopSupervisor stopped it because the host reached a terminal
			// outcome — announced by the caller that stopped it, and possibly
			// followed by a replacement's Attached — and Close canceled the base
			// context, whose terminal event is the Detached it emits while tearing
			// the channel down (a supervisor on a canceled manager "returns
			// without emitting one", as Close's pairing comment states). Emitting a
			// Disconnected of its own would contradict that announcement or arrive
			// after the replacement's Attached.
			return
		}
		if !m.reconnectOnce(ctx, host, lock) {
			return
		}
		delay = nextBackoff(delay, m.opts.backoffMax())
	}
}

// startSupervise launches the reconnect loop that owns host's channel, or
// stands down when a loop already does, and reports whether the channel is
// supervised (started now, or already owned). The supervisor gets its own
// context, derived from baseCtx, so a terminal failure can end it even while it
// waits out a backoff.
//
// The dedupe decision, the closed check, and the registration are one critical
// section under m.mu, so two callers racing for the same channel — say two
// handoffs using the same held operation — cannot both observe "unsupervised"
// and register duplicate loops: the first registers, the second sees the mark
// and leaves the channel to the loop that owns it. The false return stays
// reserved for a manager that cannot supervise at all (Close already ran), so
// the publish tails that reap on false — Ensure's and reconnectOnce's — are
// untouched, and the supervision mark is stored only after the loop's
// live-user reference is counted: a refused start can never leave a channel
// marked supervised.
//
// Go increments supervisorsWG before it returns, so once Close has set closed
// and released the lock no new supervisor can join the group, and every
// supervisor that did join is observed by Close's Wait.
//
// Every live loop stays registered as its own entry: a replacement attach adds a
// loop rather than overwriting the host's slot, so stopSupervisor can end a
// predecessor still parked in backoff instead of leaking it.
func (m *Manager) startSupervise(host hostreg.Host, ch *Channel, lock *hostLockGate) bool {
	ctx, cancel := context.WithCancel(m.baseCtx)
	loop := &supervisorLoop{host: host, cancel: cancel}
	if m.opts.beforeSuperviseStart != nil {
		m.opts.beforeSuperviseStart(host.Name, ch)
	}
	m.mu.Lock()
	if m.closed {
		// Close already ran. A supervisor started now would only race its Wait, and
		// the caller reaps the channel it could not supervise instead.
		m.mu.Unlock()
		cancel()
		return false
	}
	if ch.supervised.Load() {
		// A loop already owns this channel: a second would only race it to the
		// same reconnect. Stand down and report the channel supervised. The
		// mark is read in the same critical section the owning loop's teardown
		// clears it in, so it cannot be a gone loop's leavings — the
		// already-live handoff path and the duplicate-start race share this
		// decision.
		m.mu.Unlock()
		cancel()
		return true
	}
	if m.supervisors[host.Name] == nil {
		m.supervisors[host.Name] = map[*supervisorLoop]struct{}{}
	}
	m.supervisors[host.Name][loop] = struct{}{}
	// The supervisor goroutine outlives this call and keeps gating on lock, so
	// it takes its own live-user reference here — under m.mu, before the
	// goroutine exists, and while the caller's reference still pins the entry
	// (an entry is deleted only at zero references, so the entry this lock
	// belongs to is the one being counted). Without it, the caller's release
	// with its return could drop the entry while the loop still holds this
	// mutex, and a fresh acquisition would build a second gate for the name.
	m.locks[host.Name].refs++
	// Supervision is tracked on the channel itself, not on the loop's
	// start-time copy: a channel is supervised exactly while a loop owns it,
	// and the loop's teardown deregisters and clears the mark in one critical
	// section. The dedupe above reads it under that same mutex, so a second
	// start attempt cannot miss a loop that has moved past the state it started
	// with, nor read a deregistered loop's mark as live supervision. The mark
	// is stored last, after the loop's live-user reference is counted and after
	// every refusal has already returned, so registration is complete by the
	// time the channel reads as supervised: no failed start can leave a mark
	// behind for a handoff to mistake for a live loop.
	ch.supervised.Store(true)
	// WaitGroup.Go adds, runs, and marks the loop done, so Close waiting on the
	// group observes the fully-finished loop.
	m.supervisorsWG.Go(func() {
		// Deregister and release this loop's context child when it ends. Without the
		// deregistration a replaced loop's handle would stay in the host's set for
		// good — a leak that grows by one with each reconnect — and its WithCancel
		// child would stay registered with baseCtx until Close.
		defer func() {
			m.mu.Lock()
			if set := m.supervisors[host.Name]; set != nil {
				delete(set, loop)
				if len(set) == 0 {
					delete(m.supervisors, host.Name)
				}
			}
			// The mark is cleared inside this critical section, not after it: the
			// handoff's start decision reads it under the same mutex
			// (startSupervise's dedupe), so it can never observe a mark whose
			// loop has already deregistered — the stale-true window the round-6
			// review named. The decision sees the live mark or the cleared one,
			// and a cleared mark sends it to start the missing supervisor.
			if m.opts.beforeSuperviseClear != nil {
				m.opts.beforeSuperviseClear(host.Name, ch)
			}
			ch.supervised.Store(false)
			m.mu.Unlock()
			cancel()
			// The loop no longer touches the gate, so its reference goes with
			// it — the last one out drops the entry.
			m.releaseHostLock(host.Name)
			// Announce the exit only after the gate is released, so a test that
			// waits on it can assert on the supervisor's final state.
			if m.opts.superviseExited != nil {
				m.opts.superviseExited(host.Name)
			}
		}()
		m.supervise(ctx, host, ch, lock)
	})
	m.mu.Unlock()
	return true
}

// supervisorLoop identifies one live supervise goroutine. A cancel func is not a
// valid map key, so loops are keyed by this handle.
type supervisorLoop struct {
	// host is the registration this loop reconnects. The teardown fences read
	// it, so a stale removal cannot cancel the reconnect of a host that was
	// re-added under the same name after the removal looked.
	host   hostreg.Host
	cancel context.CancelFunc
}

// stopSupervisor ends every reconnect loop host still has, if any. Without it a
// terminal failure announced by Ensure would leave the supervisor that retired
// its own channel still looping, to repeat the same terminal outcome — a second
// EventFailed for a host consumers were already told was finished — or, for a
// terminal class the next attempt happens to pass, to announce an Attached after
// that Failed. Cancelling all of them, not just the most recent, is what reaches
// a predecessor parked in backoff after a replacement attach registered over it.
func (m *Manager) stopSupervisor(name string) {
	m.mu.Lock()
	loops := m.supervisors[name]
	delete(m.supervisors, name)
	m.mu.Unlock()
	for loop := range loops {
		loop.cancel()
	}
}

// reconnectOnce runs one re-attach attempt with the host lock held, and reports
// whether another attempt is worth making. Every outcome that ends the
// supervisor emits its own state event first.
func (m *Manager) reconnectOnce(ctx context.Context, host hostreg.Host, lock *hostLockGate) bool {
	lock.Lock()
	lock.holdAs(hostops.Holder{Kind: hostops.HolderManager, Activity: "reconnect"})
	defer lock.Unlock()

	if ctx.Err() != nil {
		// A canceled loop no longer owns the host; the canceller (a terminal
		// Ensure, a terminal reconnect, or Close's Detached) owns the terminal
		// announcement. Emit nothing, exactly as supervise's backoff path does.
		return false
	}
	if m.currentChannel(host.Name) != nil {
		// Any mapped channel means another supervisor owns this host: one whose link
		// has dropped is still owned, because its own supervisor is reconnecting.
		// Keying on liveness here let two supervisors attach for one host, and the
		// loser's channel was clobbered without ever being detached.
		return false
	}
	nch, err := m.ensureOnce(ctx, host, false)
	if err == nil {
		if nch.isLost() || nch.isClosed() {
			// The link died between the handshake and this publish. Announcing it
			// would install a dead channel and leak the old registration, so reap
			// it and take another turn through the backoff.
			_ = nch.Close()
			m.stateEvent(host.Name, StateReconnecting)
			return true
		}
		// Recheck the registry before publishing, as Ensure does — and on the
		// full entry and its generation, not just the name: a host deregistered
		// while this loop was reconnecting it (RemoveHost stops parked loops,
		// but this attempt was already in flight) must not gain a fresh
		// channel, and a remove/re-add that swapped the entry this loop
		// captured must not gain a channel built from the removed entry
		// either — byte-identical re-adds included, because the registry
		// assigns the re-add a new generation. Reap the replacement, report
		// the host honestly disconnected, and stand down.
		if !m.reg.SameRegistration(host.Name, host) {
			_ = nch.Close()
			m.stateEvent(host.Name, StateDisconnected)
			return false
		}
		if !m.publishChannel(host.Name, nch, host) {
			// Close landed mid-attempt; reap the channel it would have orphaned.
			_ = nch.Close()
			m.stateEvent(host.Name, StateDisconnected)
			return false
		}
		if m.opts.afterPublish != nil {
			m.opts.afterPublish(host.Name, nch)
		}
		if nch.isLost() || nch.isClosed() {
			// The link dropped between the pre-publish check and the announcement.
			// Announcing it would install an unusable channel and leak the source a
			// consumer builds from the Attached, so give the slot back, reap it, and
			// take another turn through the backoff. This supervisor owns the host,
			// so clearing the slot does not orphan it.
			m.clearChannel(host.Name)
			_ = nch.Close()
			m.stateEvent(host.Name, StateReconnecting)
			return true
		}
		m.attachEvent(host.Name, nch, StateAttached)
		if !m.startSupervise(host, nch, lock) {
			// Close set its flag before supervision began: pair the Attached and
			// reap the channel, which nothing will ever reconnect.
			m.detachEvent(host.Name, StateDisconnected)
			m.clearChannel(host.Name)
			_ = nch.Close()
			return false
		}
		return false
	}
	// Close canceling the attempt surfaces a preflight that was in flight as a
	// retryable transport failure. The canceled base context, not the host, is
	// why: map it to ErrManagerClosed before the terminal check, exactly as Ensure
	// does, so shutdown does not emit a spurious StateReconnecting and send the
	// supervisor around one more loop iteration.
	err = m.mapAttemptErr(err)
	if isTerminal(err) {
		// Close canceling the attempt is not a host failure: during shutdown a
		// consumer must not receive an EventFailed for it. The mapping to
		// ErrManagerClosed above covers only the retryable transport shape, so a
		// terminal fault found concurrently with the shutdown — a protocol
		// mismatch, an unparseable preflight — must be suppressed directly here,
		// exactly as Ensure's own baseCtx.Err() path suppresses it.
		if m.baseCtx.Err() == nil {
			m.failedEvent(host.Name, err)
		}
		m.stateEvent(host.Name, StateDisconnected)
		m.stopSupervisor(host.Name)
		return false
	}
	m.stateEvent(host.Name, StateReconnecting)
	return true
}

func (m *Manager) jitterFor(d time.Duration) time.Duration {
	if m.opts.jitter != nil {
		return m.opts.jitter(d)
	}
	return defaultJitter(d)
}

// nextBackoff doubles delay, capped at limit. The doubling saturates rather
// than wrapping: a delay near the representable maximum (which a caller can
// seed through BackoffBase/BackoffMax) would otherwise overflow negative, and a
// negative delay fires the sleep timer immediately instead of backing off.
func nextBackoff(delay, limit time.Duration) time.Duration {
	if delay > maxDuration/2 {
		if limit > 0 {
			return limit
		}
		return maxDuration
	}
	delay *= 2
	if limit > 0 && delay > limit {
		return limit
	}
	return delay
}

// isTerminal reports whether err ends the reconnect loop instead of being
// retried. Every contract violation is terminal, matching spec 04's "Error
// handling". An authentication-shaped failure is deliberately not: ssh forwards
// a remote command's stderr and exit status, so the refusal cannot be attributed
// to ssh, and retrying is the safe side of that ambiguity (see ErrSSHAuth).
func isTerminal(err error) bool {
	switch {
	case errors.Is(err, ErrProtocolIncompatible),
		errors.Is(err, ErrHostProtocolNewer),
		errors.Is(err, ErrEndpointMigration),
		errors.Is(err, ErrUnsupportedHost),
		errors.Is(err, ErrLaunchContract),
		errors.Is(err, ErrVersionMismatch),
		errors.Is(err, ErrHostNotFound),
		errors.Is(err, ErrHostAddr),
		errors.Is(err, ErrPreflightDecode),
		errors.Is(err, ErrExecutableMissing),
		errors.Is(err, ErrControllerDirty),
		errors.Is(err, ErrRunTargetUnservable),
		errors.Is(err, ErrDeployArtifactUnusable),
		errors.Is(err, ErrOwnExecutableCannotServe),
		errors.Is(err, ErrDeployUnstamped),
		errors.Is(err, ErrDeployDisabled),
		errors.Is(err, errRetainedInstaller),
		errors.Is(err, errEntryBinding),
		errors.Is(err, ErrManagerClosed):
		return true
	default:
		return false
	}
}

// Terminal reports whether err belongs to the attach ladder's terminal class:
// the causes a retry cannot fix — a protocol or version mismatch, an
// unsupported host, launch-contract and deploy-artifact refusals, a missing
// host, an unusable address, and a closed manager. Callers that bound their own
// retries (the hub's post-restart reattach) use it to fail fast instead of
// waiting out a window on a cause that cannot change.
func Terminal(err error) bool { return isTerminal(err) }

// linkMonitor signals the first read error/EOF on the stream, which is the
// channel's link-down edge. Only the AppWire client's read loop reads this
// stream, so nothing else observes or steals the signal.
type linkMonitor struct {
	rw    io.ReadWriteCloser
	onErr func()
	once  sync.Once
}

func newLinkMonitor(rw io.ReadWriteCloser) *linkMonitor {
	return &linkMonitor{rw: rw, onErr: func() {}}
}

func (l *linkMonitor) Read(p []byte) (int, error) {
	n, err := l.rw.Read(p)
	if err != nil {
		l.once.Do(l.onErr)
	}
	return n, err
}

func (l *linkMonitor) Write(p []byte) (int, error) {
	n, err := l.rw.Write(p)
	if err != nil || n != len(p) {
		// The send-side peer of Read's EOF: a write that failed — or put only part
		// of a frame on the wire — desynchronizes the stream and leaves every later
		// request broken. Without this signal a Send failure left isLost false, and
		// liveChannel kept handing out a channel whose writes all fail.
		l.once.Do(l.onErr)
	}
	return n, err
}

func (l *linkMonitor) Close() error { return l.rw.Close() }

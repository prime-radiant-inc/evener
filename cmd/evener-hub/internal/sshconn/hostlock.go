package sshconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// hostGateEntryFor returns name's per-host gate while it is held by holder:
// the same holder the hold published through TryAcquire/HoldAs (deploy
// pipeline 08b §5's holder vocabulary), compared exactly. It returns nil for a
// free gate, a missing entry, or one carrying a different holder. The entry is
// fetched under the manager mutex; the held flag and holder themselves are
// atomic by design (hostLockGate), so the read does not disturb a contender's
// registration. It is a cooperative precondition, not a security boundary: it
// proves the caller presents the hold the gate currently carries, which is the
// strongest check the try-acquire/release API allows. The caller's own hold
// pins the entry's live-user reference, so a returned entry cannot be dropped
// while the caller uses it.
func (m *Manager) hostGateEntryFor(name string, holder hostops.Holder) *hostLockGate {
	m.mu.Lock()
	entry := m.locks[name]
	m.mu.Unlock()
	if entry == nil || !entry.gate.isHeld() {
		return nil
	}
	held := entry.gate.holderOf()
	if held.Kind != holder.Kind ||
		strings.TrimSpace(held.OperationID) != strings.TrimSpace(holder.OperationID) ||
		strings.TrimSpace(held.Activity) != strings.TrimSpace(holder.Activity) {
		return nil
	}
	return &entry.gate
}

// hostGateHeldBy reports whether name's per-host gate is currently held by
// holder, exactly as hostGateEntryFor compares it.
func (m *Manager) hostGateHeldBy(name string, holder hostops.Holder) bool {
	return m.hostGateEntryFor(name, holder) != nil
}

// hostLockEntry is one per-host gate together with its live-user count.
// refs counts the hostLock acquisitions that have not been released yet —
// holders and parked waiters both — so the entry can be dropped exactly when
// nobody can still be using its mutex.
type hostLockEntry struct {
	gate hostLockGate
	refs int
}

// hostLockGate is the per-host gate itself: the mutex every holder of one
// host's work serializes on (Ensure, the reconnect supervisor, AddHost,
// UpdateHost, RemoveHost, DetachHost, Close — and, through Manager.TryAcquire,
// the deploy pipeline's plan/deploy/restart paths), plus the holder
// registration the pipeline's busy refusal renders (deploy pipeline 08b §5).
//
// The holder is written by whoever holds the mutex, after acquiring it, and
// cleared by Unlock before the mutex is released, so a contender that fails
// TryLock can never read a stale holder for a free gate — a free gate always
// reads the zero Holder. It is an atomic pointer rather than a field under the
// manager mutex so Unlock stays a lock-free operation, exactly as the plain
// sync.Mutex it replaces was. The holder is published as the acquisition's very
// next step (see TryAcquire and each holdAs call site): the one instant between
// a successful TryLock and the store is not observable as a holder, so a
// contender landing exactly there reads the zero Holder and gets the safe
// generic transient form, never a wrong operation id.
type hostLockGate struct {
	mu     sync.Mutex
	held   atomic.Bool
	holder atomic.Pointer[hostops.Holder]
}

// isHeld reports whether the gate is currently held, without disturbing the
// holder a contender may be about to read. HoldAs tests it before publishing a
// promotion.
func (g *hostLockGate) isHeld() bool { return g.held.Load() }

// Lock blocks until the gate is held, clearing any holder left by the previous
// hold: an unregistered hold (this package's own paths register theirs
// immediately after Lock) reads as the generic manager class, never as the
// previous holder's identity.
func (g *hostLockGate) Lock() {
	g.mu.Lock()
	g.held.Store(true)
	g.holder.Store(nil)
}

// TryLock try-acquires the gate, reporting whether it now holds it.
func (g *hostLockGate) TryLock() bool {
	if !g.mu.TryLock() {
		return false
	}
	g.held.Store(true)
	g.holder.Store(nil)
	return true
}

// Unlock clears the holder before releasing the mutex, so no contender can
// observe a stale holder for a gate that is about to be free.
func (g *hostLockGate) Unlock() {
	g.holder.Store(nil)
	g.held.Store(false)
	g.mu.Unlock()
}

// holdAs registers the holder of the current hold. Callers call it immediately
// after Lock/TryLock succeeded.
func (g *hostLockGate) holdAs(holder hostops.Holder) {
	g.holder.Store(&holder)
}

// holderOf returns the current hold's registered holder, or the zero Holder
// when the hold never registered one.
func (g *hostLockGate) holderOf() hostops.Holder {
	if holder := g.holder.Load(); holder != nil {
		return *holder
	}
	return hostops.Holder{}
}

// hostLock returns the per-host gate for name and registers the caller as a
// live user of it: every hostLock call must be paired with exactly one
// releaseHostLock call, made by the caller once neither it nor anyone it handed
// the gate to can touch the mutex again. The pairing keeps the map bounded
// (a permanent gate per name would leak one mutex entry for every host
// name ever attached, for the manager's lifetime) while making that
// cleanup safe: an entry is deleted only when its last user released it,
// so a gate is never dropped while a holder — or a goroutine parked
// waiting on it — still uses it, and two callers can never hold two
// different gates for one name.
func (m *Manager) hostLock(name string) *hostLockGate {
	m.mu.Lock()
	entry := m.locks[name]
	if entry == nil {
		entry = &hostLockEntry{}
		m.locks[name] = entry
	}
	entry.refs++
	m.mu.Unlock()
	return &entry.gate
}

// releaseHostLock pairs one hostLock call for name, dropping the entry when
// the last live user released it: refs reached zero, so no goroutine holds
// the gate or is parked acquiring it, and a later acquisition building a
// fresh entry for the name cannot split one host's exclusion across two
// mutexes. Waiters are counted at acquisition, before they park, so they hold
// the entry open for as long as they wait; lockHostCtx's abandoned acquire
// parks on the mutex without a reference of its own, but its caller's
// reference covers the parking and the abandoned goroutine only hands the
// mutex straight back — it never runs a critical section — so a release that
// deletes the entry under it leaves it touching a mutex nothing else
// references.
func (m *Manager) releaseHostLock(name string) {
	m.mu.Lock()
	if entry := m.locks[name]; entry != nil {
		entry.refs--
		if entry.refs == 0 {
			delete(m.locks, name)
		}
	}
	m.mu.Unlock()
}

// lockHostCtx acquires the per-host gate, giving up when ctx is done. The host
// lock itself is a plain sync.Mutex: Close and the reconnect supervisors take it
// without a context and must not be made to fail, so a canceled waiter cannot be
// woken out of Lock directly. This helper parks a goroutine on Lock instead and,
// when the caller gives up first, has that goroutine hand the lock straight back
// once it acquires it — the gate is never left held by a caller that returned.
func lockHostCtx(ctx context.Context, mu *hostLockGate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Fast path: an uncontended acquire needs neither a goroutine nor a select.
	if mu.TryLock() {
		return nil
	}
	acquired := make(chan struct{})
	abandon := make(chan struct{})
	go func() {
		mu.Lock()
		// The unbuffered send completes only if the caller is still waiting; once
		// it has given up, nothing will receive, so release the lock here.
		select {
		case acquired <- struct{}{}:
		case <-abandon:
			mu.Unlock()
		}
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		close(abandon)
		return ctx.Err()
	}
}

// TryAcquire implements hostops.Gate: it try-acquires name's per-host gate for
// holder, returning the typed busy error naming the current holder when the
// gate is held. The gate it takes is the one gate this Manager's own paths use
// (Ensure, the reconnect supervisor, AddHost, UpdateHost, RemoveHost,
// DetachHost, Close), so a plan cannot interleave with an in-flight attach or
// teardown for the same host.
//
// Nothing waits: the acquisition is a TryLock on the same mutex, and a failed
// attempt reports the holder the successful acquirer registered — the operation
// class with its record id for a deploy/restart (or an Ensure-triggered
// deploy), the transient plan form for a plan's validation-plus-mint window,
// and the manager's activity class for this package's own holds.
func (m *Manager) TryAcquire(host string, holder hostops.Holder) (func(), error) {
	name := strings.TrimSpace(host)
	if name == "" {
		return nil, errors.New("sshconn: a gate acquisition needs a host name")
	}
	lock := m.hostLock(name)
	if !lock.TryLock() {
		held := lock.holderOf()
		m.releaseHostLock(name)
		return nil, hostops.Busy(name, held)
	}
	lock.holdAs(holder)
	// The release is idempotent: a double release must not double-unlock.
	var once sync.Once
	return func() {
		once.Do(func() {
			lock.Unlock()
			m.releaseHostLock(name)
		})
	}, nil
}

// hostGate is the compile-time assertion that the Manager is the production
// hostops.Gate.
var _ hostops.Gate = (*Manager)(nil)

// HoldAs implements hostops.Gate: it replaces the holder published for name's
// currently-held gate, the promotion the deploy/restart paths run once their
// operation record exists (deploy pipeline 08b §5). The gate is the Manager's
// own per-host lock, so the promotion upgrades the very holder a contender's
// TryAcquire reads. The entry is looked up under the manager mutex with a
// live-user reference, so a concurrently released gate cannot drop the entry
// under the promotion; a gate nobody holds — free, or never created — refuses
// with hostops.ErrGateNotHeld.
func (m *Manager) HoldAs(host string, holder hostops.Holder) error {
	name := strings.TrimSpace(host)
	if name == "" {
		return errors.New("sshconn: a gate promotion needs a host name")
	}
	m.mu.Lock()
	entry := m.locks[name]
	if entry == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: host %q", hostops.ErrGateNotHeld, name)
	}
	entry.refs++
	m.mu.Unlock()

	if !entry.gate.isHeld() {
		m.releaseHostLock(name)
		return fmt.Errorf("%w: host %q", hostops.ErrGateNotHeld, name)
	}
	entry.gate.holdAs(holder)
	m.releaseHostLock(name)
	return nil
}

package agent

import (
	"context"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/rendezvous"
)

// Owner liveness for a delegate lane's occupancy lock (issue #5471).
//
// A delegate lane's `evener:dlg:<dlg>:<owner>` lock is released only by the
// owner session's own disposal lifecycle — `dispose`, or the close pass. When
// the owner's process dies without a clean close, that lifecycle never runs,
// and the lock used to outlive the session tree forever: `prune` skipped the
// lane as locked, `remove` refused it as foreign, `dispose` required the dead
// owner itself, and the residue sweeps collect only unlocked lanes. The
// recovery the docs sanctioned was a per-lane manual `git worktree unlock`
// after a human or agent verified the owner is really gone; observed on one
// workstation as 58 locked trees holding ~6.7 GB no bulk prune could touch.
//
// This file implements the verification half of that recovery as a check
// `prune` can run itself. It never guesses: a lane whose owner cannot be
// PROVEN dead keeps its lock and is reported as unverifiable. A live owner's
// lane is never pruned — that is the preservation contract the whole lock
// family exists to keep.
//
// What can prove an owner gone, given that all sessions of one tree share the
// hosting process (delegates are in-process children; a bare delegate resume
// is refused, so only a root ever hosts):
//
//   - The root session's livelock (schema.AcquireSessionLivelock): a flock the
//     hosting process holds for its lifetime and the OS releases at its death.
//     Held means live; present-but-free means dead. This is the primary
//     signal, and the only one that covers one-shot `evener` runs as well as
//     serve daemons.
//   - The rendezvous entry a serve daemon publishes: an entry for the root
//     session id whose PID no longer exists is a dead daemon's residue (a
//     graceful shutdown removes its entry), so a live PID means live and a
//     dead PID means dead. This also covers daemons crashed before livelocks
//     shipped, whose stale entries otherwise linger.
//
// Any live signal wins over any dead signal, so a session resumed by a new
// process — livelock freshly taken, stale rendezvous entry not yet rewritten —
// reads as live.

// ownerLiveness is the verdict on the session tree that owns a delegate
// lane's lock.
type ownerLiveness int

const (
	// ownerUnknown: liveness could not be proven either way. Callers keep the
	// lane exactly as it was.
	ownerUnknown ownerLiveness = iota
	// ownerLive: the owning session tree's process is alive.
	ownerLive
	// ownerDead: the owning session tree's process is verifiably gone.
	ownerDead
)

func (l ownerLiveness) String() string {
	switch l {
	case ownerLive:
		return "live"
	case ownerDead:
		return "dead"
	default:
		return "unknown"
	}
}

// delegateLaneOwnerLiveness decides whether the session tree that owns a
// delegate lane's lock (the marker's parent session id) is still alive, and
// names the tree root it resolved. A nested lane's marker names a subagent
// session that may not be instantiated while its root process lives (a
// restored-idle delegate between a resume and its next delegate_send), so
// liveness is resolved through the parent chain to the root — the one session
// whose process hosts the whole tree. The root is returned even on unknown, so
// a caller that needs to serialize with the root's startup (prune's
// reclamation claim) can do so without walking twice; "" when the walk itself
// failed.
func (s *Session) delegateLaneOwnerLiveness(m worktree.Marker) (ownerLiveness, string) {
	root, ok := s.sessionTreeRootSID(m.SessionID)
	if !ok {
		return ownerUnknown, ""
	}
	if root == s.id {
		// Our own delegates are ours to dispose (or to keep revivable); prune
		// never collects a lane this session's own tree owns.
		return ownerLive, root
	}
	deadSeen := false
	if dir, ok := s.worktreeOwnerRendezvousDir(); ok {
		if entries, err := rendezvous.List(dir); err == nil {
			for _, e := range entries {
				if e.PID <= 0 {
					continue
				}
				if e.SessionID != root && e.ThreadID != root && e.InstanceID != root {
					continue
				}
				if processAlive(e.PID) {
					return ownerLive, root
				}
				deadSeen = true
			}
		}
	}
	held, exists, err := schema.SessionLivelockHeld(s.currentStateDir(), root)
	if err == nil && held {
		return ownerLive, root
	}
	// A committed non-terminal retirement supersedes every older crash signal
	// (review round 3, finding 3). The park marker is written while the flock
	// is still held, so a parked root may carry both a now-free livelock and a
	// stale dead rendezvous entry (the roster retains crash entries) and still
	// be parked-for-resume — preserved, never dead.
	if parked, perr := schema.SessionLivelockParked(s.currentStateDir(), root); perr == nil && parked {
		return ownerUnknown, root
	}
	if err == nil && exists {
		deadSeen = true
	}
	if deadSeen {
		return ownerDead, root
	}
	return ownerUnknown, root
}

// sessionTreeRootSID resolves a session id to the root of its session tree:
// the first ancestor that is not itself a subagent. Subagent metas chain
// upward via parent_session_id; a root or fork meta stops the walk (a fork
// hosts its own process). It reports false when the chain cannot be resolved —
// a missing meta, a subagent with no parent link, or a cycle — and callers
// must treat that as unknown, never as death.
func (s *Session) sessionTreeRootSID(sid string) (string, bool) {
	stateDir := s.currentStateDir()
	if stateDir == "" || sid == "" {
		return "", false
	}
	const hopLimit = 64
	owner := sid
	for range hopLimit {
		meta, err := schema.LoadSessionMeta(stateDir, owner)
		if err != nil {
			return "", false
		}
		if !meta.IsSubagent {
			return owner, true
		}
		if meta.ParentSessionID == "" {
			return "", false
		}
		owner = meta.ParentSessionID
	}
	return "", false
}

// deadOwnerRevalidatedUnderClaim re-reads the owner state while this session
// holds the dead owner root's livelock as its reclamation claim (review round
// 3, finding 2). The claim proves no owner process holds the livelock, and —
// because a resumer's owner-visible lane work is gated on acquisition
// (waitLivelockAcquired) — that none can adopt a lane or run a delegate until
// the claim releases, so the free-file death evidence the listing saw still
// stands. What a listing cannot prove is that nothing superseded its verdict
// since: a non-terminal retirement that committed a park (the marker —
// preserved, not dead), or a serve daemon whose rendezvous entry is live
// again. Only that superseding evidence is re-read; the livelock probe is
// skipped because the claim itself holds the flock.
func (s *Session) deadOwnerRevalidatedUnderClaim(root string) ownerLiveness {
	if parked, err := schema.SessionLivelockParked(s.currentStateDir(), root); err == nil && parked {
		return ownerUnknown
	}
	if dir, ok := s.worktreeOwnerRendezvousDir(); ok {
		if entries, err := rendezvous.List(dir); err == nil {
			for _, e := range entries {
				if e.PID <= 0 {
					continue
				}
				if e.SessionID != root && e.ThreadID != root && e.InstanceID != root {
					continue
				}
				if processAlive(e.PID) {
					return ownerLive
				}
			}
		}
	}
	return ownerDead
}

// worktreeOwnerRendezvousDir is the rendezvous run directory the liveness
// oracle consults: the test seam when set, else the platform default. A
// directory that cannot be resolved answers no rendezvous evidence rather
// than failing the check.
func (s *Session) worktreeOwnerRendezvousDir() (string, bool) {
	s.mu.Lock()
	dir := s.worktreeRendezvousDir
	s.mu.Unlock()
	if dir != "" {
		return dir, true
	}
	dir, err := rendezvous.DefaultDir()
	if err != nil {
		return "", false
	}
	return dir, true
}

// livelockRetryInterval and livelockRetryIntervalMax bound the background
// re-acquisition loop's cadence: fast enough that the window in which a
// contended-but-running session reads as a free livelock stays short, slow
// enough that a pathological permanent holder (a second process on the same
// session id — the documented don't-do-that case) costs a few wakeups per
// second, not a spin.
const (
	livelockRetryInterval    = 25 * time.Millisecond
	livelockRetryIntervalMax = 250 * time.Millisecond
)

// acquireSessionLivelock takes this root session's process livelock: the
// lifetime flock whose release-by-OS at process death is what makes a crashed
// owner's lanes provably dead to a later prune. Root sessions only — a
// subagent is hosted by its root's process, so only roots host.
//
// Contention never abandons acquisition (review round 2, finding 2). The
// contending holders are a liveness probe's momentary take, a prune reclaiming
// this root's lanes, or — the documented don't-do-that case — a second process
// on the same session id; a session that ran on without its livelock would
// leave an existing-but-free file behind, which a later prune reads as a
// proven-dead owner while the session is live. Contention therefore spawns a
// background re-acquisition loop that persists until the lock lands (or the
// session's close stops it), closing s.livelockAcquired when it does. A real
// failure (not contention) still degrades silently: a session that could not
// take the lock is one whose lanes other sessions keep locked (the safe
// direction), never one that refuses to start.
//
// What contention must never let happen is owner-visible lane work before the
// lock lands (review round 3, finding 1): a resumed owner that adopts its
// unchanged delegate marker, or runs a delegate, while a prune holds the
// root's reclamation claim is racing that prune's removal. The livelockGate
// armed here is therefore the production waiter's view of the landing —
// waitLivelockAcquired blocks lane adoption and delegate execution until the
// acquisition resolves, and is the waiter livelockAcquired's test signal
// mirrors.
func (s *Session) acquireSessionLivelock() {
	if s.isSubagentSession() {
		return
	}
	stateDir := s.currentStateDir()
	if stateDir == "" {
		return
	}
	s.mu.Lock()
	if s.livelockGate == nil {
		s.livelockGate = make(chan struct{})
	}
	s.mu.Unlock()
	release, applied, err := schema.AcquireSessionLivelock(stateDir, s.id)
	if err == nil && applied && release != nil {
		s.mu.Lock()
		s.livelockRelease = release
		s.closeLivelockGateLocked()
		s.mu.Unlock()
		// The resume is live again: drop the park marker a previous
		// retirement wrote, so a later crash of THIS process leaves honest
		// residue instead of being preserved forever. Init cannot race this
		// session's own park (park runs only on the close path, after init).
		_ = schema.ClearSessionLivelockParked(stateDir, s.id)
		return
	}
	if err == nil || !schema.LivelockContended(err) {
		abandonSessionLivelock(stateDir, s.id)
		s.mu.Lock()
		s.closeLivelockGateLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	if s.livelockRetryStop || s.livelockAcquired != nil {
		// Already closing, or a retry loop is already running (init is once).
		s.mu.Unlock()
		return
	}
	s.livelockAcquired = make(chan struct{})
	s.mu.Unlock()
	go s.retrySessionLivelock(stateDir)
}

// waitLivelockAcquired blocks until this session's tree root holds its
// process livelock — or its acquisition has resolved without one — and is the
// gate every owner-visible lane operation (resume lane re-lock, delegate
// creation, delegate revival) passes before touching a lane. A session that
// has not taken the livelock must not adopt a lane or run a delegate while a
// prune may hold the root's reclamation claim (review round 3, finding 1).
// ctx cancellation unblocks the wait with the context's error, so a closing
// session's gate waiters abort rather than hang: retirement can only start
// after the restore tail that host the gated operations has completed, so a
// park never finds a still-blocked waiter of its own session.
//
// Subagents share their root's process, so only the root's gate counts; the
// shared delegate controller names that root. A fixture subagent without a
// reachable root proceeds ungated.
func (s *Session) waitLivelockAcquired(ctx context.Context) error {
	root := s
	if s.isSubagentSession() {
		c := s.delegateController
		if c == nil || c.rootRuntime == nil {
			return nil
		}
		root = c.rootRuntime
	}
	root.mu.Lock()
	gate := root.livelockGate
	root.mu.Unlock()
	if gate == nil || channelClosed(gate) {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closeLivelockGateLocked wakes every livelock-gate waiter: the acquisition
// resolved (held, or abandoned on a real failure). Guarded by s.mu;
// livelockGateClosed keeps the single close.
func (s *Session) closeLivelockGateLocked() {
	if s.livelockGate != nil && !s.livelockGateClosed {
		s.livelockGateClosed = true
		close(s.livelockGate)
	}
}

// retrySessionLivelock re-attempts livelock acquisition until it lands or the
// session's release path stops it. Every holder of a root's livelock is either
// momentary (a probe, a prune's reclamation hold) or a same-sid process, so
// the loop always ends in acquired-or-stopped, never in give-up.
func (s *Session) retrySessionLivelock(stateDir string) {
	interval := livelockRetryInterval
	for {
		s.mu.Lock()
		stopped := s.livelockRetryStop
		seam := s.testOnlyBeforeLivelockRetryAcquire
		s.mu.Unlock()
		if stopped {
			return
		}
		if seam != nil {
			seam()
		}
		release, applied, err := schema.AcquireSessionLivelock(stateDir, s.id)
		if err == nil && applied && release != nil {
			s.mu.Lock()
			signal := s.livelockAcquired
			if s.livelockRetryStop {
				// A close or park completed while this attempt was in flight:
				// the lock is no longer this session's to hold. The livelock
				// FILE stays — a parked root keeps it, and the park marker is
				// what reads it as retired — so this late landing can neither
				// orphan the file nor resurrect a removed one (review round 3,
				// finding 4).
				s.mu.Unlock()
				release()
				return
			}
			s.livelockRelease = release
			// The park-marker clear joins the stop re-check's critical
			// section on purpose: a park sets livelockRetryStop under this
			// same mutex, so either the park's stop-set precedes this section
			// — and the attempt aborts above without clearing — or it follows
			// it, and the marker the park writes lands after this clear. No
			// interleave leaves a parked root's marker cleared.
			_ = schema.ClearSessionLivelockParked(stateDir, s.id)
			s.closeLivelockGateLocked()
			s.mu.Unlock()
			if signal != nil {
				close(signal)
			}
			return
		}
		if err == nil || !schema.LivelockContended(err) {
			// A real failure, not contention: degrade silently, exactly like
			// the non-contended acquisition path.
			abandonSessionLivelock(stateDir, s.id)
			s.mu.Lock()
			s.closeLivelockGateLocked()
			s.mu.Unlock()
			return
		}
		time.Sleep(interval)
		if interval < livelockRetryIntervalMax {
			interval *= 2
		}
	}
}

// abandonSessionLivelock is what acquisition does when it gives up on a real
// (non-contention) failure: silently run without a livelock, but never leave
// the created file behind — an existing-but-free livelock is a dead owner's
// signature, and a running session that could not take the lock must answer
// unknown (absent), never dead. Best-effort: the removal's own failure only
// warns.
func abandonSessionLivelock(stateDir, id string) {
	_ = schema.RemoveSessionLivelock(stateDir, id)
}

// releaseSessionLivelock drops this session's process livelock, called when a
// close pass has finished releasing everything the process hosted — after
// lane disposal and the own-marker unlock, so no window exists where this
// session still owes worktree work but reads as dead to another session's
// prune. It also stops a pending re-acquisition loop, so a closing session can
// never acquire behind its own close's back. Idempotent.
func (s *Session) releaseSessionLivelock() {
	s.mu.Lock()
	release := s.livelockRelease
	s.livelockRelease = nil
	s.livelockRetryStop = true
	s.mu.Unlock()
	if release != nil {
		release()
	}
}

// parkSessionLivelock is releaseForRetirement's variant of the release
// (review round 2, finding 3; round 3, findings 3 and 4): a retired root is
// parked-for-resume, not dead, and must never read as a dead one to a foreign
// prune. The park is a DURABLE MARKER written beside the livelock, in this
// order:
//
//   - halt the re-acquisition loop first: an attempt already in flight
//     observes the stop at its landing re-check and releases without clearing
//     the marker this park is about to write; an attempt that landed earlier
//     cleared the marker before this write, so this write wins;
//   - write the marker while the flock is still held — the commit point. A
//     crash before it leaves exactly a real crash's state (honestly
//     collectible); a crash after it leaves a committed park (preserved);
//   - release the flock. The livelock FILE stays: the marker supersedes the
//     free-flock death signature to every later probe, and never removing
//     the file means no in-flight acquisition attempt can orphan or
//     resurrect it.
//
// A replacement process resuming the session id re-takes the livelock at init
// and clears the marker, making the owner live again and its later crash
// honest residue.
func (s *Session) parkSessionLivelock() {
	if s.isSubagentSession() {
		return
	}
	stateDir := s.currentStateDir()
	if stateDir == "" {
		return
	}
	s.mu.Lock()
	s.livelockRetryStop = true
	s.mu.Unlock()
	if err := schema.MarkSessionLivelockParked(stateDir, s.id, time.Now()); err != nil {
		// Fall through to the release: without the marker, the state left
		// behind is exactly a real crash's, which is then honestly collectible
		// rather than silently preserved.
		s.emit(events.WarningData{Message: "parking the retired session livelock failed: " + err.Error()})
	}
	s.releaseSessionLivelock()
}

package agent

import (
	"context"
	"errors"
	"fmt"
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

const (
	livelockRetryInterval    = 25 * time.Millisecond
	livelockRetryIntervalMax = 250 * time.Millisecond
)

// livelockGateWaitBudget bounds how long a turn-facing lane operation
// (delegate creation, delegate revival) waits for the session's livelock
// acquisition to land before failing honestly (review round 4, finding 0).
// It is a hang guard sitting far above every legitimate hold — a liveness
// probe's momentary take, a prune's reclamation claim, itself one lane's git
// operations — and never the mechanism: the awaited completion is the
// acquisition itself. A wait that outlives it means the lock is held by
// something that is not going away (the documented same-id second process),
// and the honest answer there is a refused operation, not a stalled one.
const livelockGateWaitBudget = 5 * time.Second

// errLivelockGatePending is what a non-waiting gate check answers when the
// acquisition is still pending: the caller defers or refuses the lane work
// rather than proceeding unrepresented.
var errLivelockGatePending = errors.New("the session's process livelock acquisition is still pending")

// acquireSessionLivelock takes this root session's process livelock: the
// lifetime flock whose release-by-OS at process death is what makes a crashed
// owner's lanes provably dead to a later prune. Root sessions only — a
// subagent is hosted by its root's process, so only roots host.
//
// Neither contention nor a real failure ever abandons acquisition (review
// round 2, finding 2; round 4, finding 1). The contending holders are a
// liveness probe's momentary take, a prune reclaiming this root's lanes, or —
// the documented don't-do-that case — a second process on the same session id;
// a real failure (EMFILE at the open, a lock-table exhaustion at the flock)
// is recoverable in principle. Every one of those spawns the background
// re-acquisition loop, which persists until the lock lands or the session's
// close stops it, closing s.livelockAcquired when it does. The old fallback —
// unlinking the file and running on without a livelock — is gone: the unlink
// never released a holder's flock (it only hid the evidence), and running
// lane work unrepresented is exactly what the gate exists to prevent.
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
		s.clearParkedMarkerForLiveRootLocked(stateDir)
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

// clearParkedMarkerForLiveRootLocked drops the park marker a previous
// retirement wrote, now that this session holds the livelock and is live
// again — a later crash of THIS process must leave honest residue instead of
// being preserved forever. The marker's presence is remembered in
// livelockParkedCleared so a construction that fails after this point can put
// the retired root back (livelockConstructionFailureCleanup, review round 4,
// finding 3). A clear that fails leaves the marker in place, which merely
// over-preserves a live root the flock already proves live. Guarded by s.mu:
// the retry landing's clear and a park's marker write must be totally ordered
// (see retrySessionLivelock).
func (s *Session) clearParkedMarkerForLiveRootLocked(stateDir string) {
	if parked, err := schema.SessionLivelockParked(stateDir, s.id); err == nil && parked {
		s.livelockParkedCleared = true
		_ = schema.ClearSessionLivelockParked(stateDir, s.id)
	}
}

// waitLivelockAcquired waits — for at most budget, and cancellably by ctx —
// for this session's tree root to hold its process livelock, and is the gate
// the turn-facing lane operations (delegate creation, delegate revival) pass
// before touching a lane. A session that has not taken the livelock must not
// adopt a lane or run a delegate while a prune may hold the root's
// reclamation claim (review round 3, finding 1), and a wait must never be
// able to stall agent work indefinitely (review round 4, finding 0): budget
// zero polls once and answers errLivelockGatePending without waiting, and a
// positive budget that expires answers a plain error so the caller refuses
// the operation honestly and the turn stays cancellable.
//
// Subagents share their root's process, so only the root's gate counts; the
// shared delegate controller names that root. A fixture subagent without a
// reachable root proceeds ungated.
func (s *Session) waitLivelockAcquired(ctx context.Context, budget time.Duration) error {
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
	if budget <= 0 {
		return errLivelockGatePending
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("the session's process livelock did not resolve within %s: %w", budget, errLivelockGatePending)
	}
}

// livelockGateReady reports whether this session's tree root's livelock
// acquisition has resolved — held, or never armed. A poll, never a wait: the
// resume re-lock defers on a pending gate instead of blocking (review round 4,
// finding 0).
func (s *Session) livelockGateReady() bool {
	root := s
	if s.isSubagentSession() {
		c := s.delegateController
		if c == nil || c.rootRuntime == nil {
			return true
		}
		root = c.rootRuntime
	}
	root.mu.Lock()
	gate := root.livelockGate
	root.mu.Unlock()
	return gate == nil || channelClosed(gate)
}

// livelockConstructionFailureCleanup is what initSessionState's failure path
// owes the livelock (review round 4, finding 3): a construction that never
// came to life must not leave the process holding a flock for it, nor a
// retired root's park marker cleared. It stops any pending acquisition retry
// (a failed construction must not acquire behind its own cleanup), puts the
// park marker back under the still-held flock when this acquisition removed
// one, and releases. A re-mark that itself fails leaves honest crash residue.
func (s *Session) livelockConstructionFailureCleanup() {
	if s.isSubagentSession() {
		return
	}
	stateDir := s.currentStateDir()
	if stateDir == "" {
		return
	}
	s.mu.Lock()
	cleared := s.livelockParkedCleared
	release := s.livelockRelease
	s.livelockRelease = nil
	s.livelockRetryStop = true
	s.mu.Unlock()
	if release == nil {
		// Never held: only the pending retry needed stopping.
		return
	}
	if cleared {
		if err := schema.MarkSessionLivelockParked(stateDir, s.id, time.Now()); err != nil {
			s.emit(events.WarningData{Message: "restoring the retired root's park marker on the failed construction: " + err.Error()})
		}
	}
	release()
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
// session's release path stops it. Contention and real failures alike are
// retried (review round 4, finding 1): every holder of a root's livelock is
// momentary or pathological, and every fs error is recoverable in principle,
// so the loop always ends in acquired-or-stopped, never in give-up — and
// never in an unrepresented session running lane work.
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
			s.clearParkedMarkerForLiveRootLocked(stateDir)
			s.closeLivelockGateLocked()
			s.mu.Unlock()
			if signal != nil {
				close(signal)
			}
			return
		}
		// Contention and a real failure (a transient EMFILE at the open, a
		// lock-table exhaustion at the flock) alike retry on this cadence:
		// both are recoverable in principle. The gate stays closed: the
		// session's lane work waits (bounded) for the lock rather than
		// proceeding unrepresented, and the file the attempt created is
		// never unlinked as a fallback (review round 4, finding 1).
		time.Sleep(interval)
		if interval < livelockRetryIntervalMax {
			interval *= 2
		}
	}
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

// commitLivelockPark is releaseForRetirement's FIRST step (review round 2,
// finding 3; round 3, findings 3 and 4; round 4, finding 4): a retired root
// is parked-for-resume, not dead, and must never read as a dead one to a
// foreign prune. The park is a DURABLE MARKER (fsatomic's temp-fsync-rename-
// dirsync publication) written beside the livelock, in this order:
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
// The commit runs while the flock is still held and BEFORE retirement commits:
// a persistence failure here ABORTS the retirement (the caller keeps the
// session resident, its flock held, its lanes protected) instead of
// committing a park the machine can forget. The stop-set and the durable
// write share one critical section: a retry landing either commits (and
// clears) before this section — the marker written here lands after its clear
// — or observes the stop and aborts without touching it; no interleave leaves
// a parked root's marker cleared. The transient stop of an aborted commit is
// invisible to the loop (readers hold the same mutex), so nothing re-arms.
//
// A replacement process resuming the session id re-takes the livelock at init
// and clears the marker, making the owner live again and its later crash
// honest residue.
func (s *Session) commitLivelockPark() error {
	if s.isSubagentSession() {
		return nil
	}
	stateDir := s.currentStateDir()
	if stateDir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.livelockRetryStop = true
	if err := schema.MarkSessionLivelockParked(stateDir, s.id, time.Now()); err != nil {
		// Abort the retirement: keep residency, keep the flock, keep the
		// retry loop exactly as it was (the transient stop was invisible
		// under this mutex). The root stays live or pending, never dead.
		s.livelockRetryStop = false
		return err
	}
	return nil
}

// finalizeLivelockPark is releaseForRetirement's LAST step: with the park
// durably committed, release the flock. The livelock FILE stays — the marker
// supersedes the free-flock death signature to every later probe, and never
// removing the file means no in-flight acquisition attempt can orphan or
// resurrect it.
func (s *Session) finalizeLivelockPark() {
	s.releaseSessionLivelock()
}

package agent

import (
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
// delegate lane's lock (the marker's parent session id) is still alive. A
// nested lane's marker names a subagent session that may not be instantiated
// while its root process lives (a restored-idle delegate between a resume and
// its next delegate_send), so liveness is resolved through the parent chain to
// the root — the one session whose process hosts the whole tree.
func (s *Session) delegateLaneOwnerLiveness(m worktree.Marker) ownerLiveness {
	root, ok := s.sessionTreeRootSID(m.SessionID)
	if !ok {
		return ownerUnknown
	}
	if root == s.id {
		// Our own delegates are ours to dispose (or to keep revivable); prune
		// never collects a lane this session's own tree owns.
		return ownerLive
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
					return ownerLive
				}
				deadSeen = true
			}
		}
	}
	held, exists, err := schema.SessionLivelockHeld(s.currentStateDir(), root)
	if err == nil {
		if held {
			return ownerLive
		}
		if exists {
			deadSeen = true
		}
	}
	if deadSeen {
		return ownerDead
	}
	return ownerUnknown
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
	for hop := 0; hop < hopLimit; hop++ {
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

// acquireSessionLivelock takes this root session's process livelock: the
// lifetime flock whose release-by-OS at process death is what makes a crashed
// owner's lanes provably dead to a later prune. Root sessions only — a
// subagent is hosted by its root's process, so only roots host. Failure
// degrades silently: a session without a livelock is one whose lanes other
// sessions keep locked (the safe direction), never one that refuses to start,
// and the second process on one session id is the documented case the
// non-blocking take reports as contention.
func (s *Session) acquireSessionLivelock() {
	if s.isSubagentSession() {
		return
	}
	stateDir := s.currentStateDir()
	if stateDir == "" {
		return
	}
	release, applied, err := schema.AcquireSessionLivelock(stateDir, s.id)
	if err != nil || !applied || release == nil {
		return
	}
	s.mu.Lock()
	s.livelockRelease = release
	s.mu.Unlock()
}

// releaseSessionLivelock drops this session's process livelock, called when a
// close pass has finished releasing everything the process hosted — after
// lane disposal and the own-marker unlock, so no window exists where this
// session still owes worktree work but reads as dead to another session's
// prune. Idempotent.
func (s *Session) releaseSessionLivelock() {
	s.mu.Lock()
	release := s.livelockRelease
	s.livelockRelease = nil
	s.mu.Unlock()
	if release != nil {
		release()
	}
}

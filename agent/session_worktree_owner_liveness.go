package agent

import (
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/rendezvous"
)

// The owner-liveness oracle serves REPORTING only (issue #5471's pain: locked
// delegate lanes whose owner trees are long gone read back as plain "locked",
// so 58 trees and ~6.7 GB could not be told apart from live ones). It answers
// whether the session tree that owns a delegate lane's lock still has a live
// process, from evidence that needs no protocol: the root's process livelock
// (a lifetime flock the OS releases at process death) and its serve-daemon
// rendezvous entries. No code path may collect, unlock, gate or reclaim on a
// verdict — prune reports it, the docs' manual recipe consumes it, and the
// decision stays with the model or the human running it.

// ownerLiveness is the oracle's verdict on a delegate lane's owning session
// tree.
type ownerLiveness int

const (
	// ownerUnknown: liveness could not be proven either way — the tree root
	// could not be resolved, the livelock file is absent (a session from
	// before livelocks), or a probe could not answer. Callers keep the lane
	// exactly as it was and report the doubt.
	ownerUnknown ownerLiveness = iota
	// ownerLive: the owning session tree's process is alive.
	ownerLive
	// ownerDeadLooking: no live process hosts the owning tree's root — the
	// livelock file exists but its flock is free, and no rendezvous entry
	// names a live pid. "Dead-looking", not "dead": retirement also releases
	// a livelock, so a resumable-but-idle root can look exactly like this,
	// and nothing but reporting may act on the verdict.
	ownerDeadLooking
)

// String renders the verdict for the prune skip reason; the words are the
// model-facing contract tokens (owner-live / owner-dead-looking /
// owner-unverifiable), pinned by the reporting tests.
func (l ownerLiveness) String() string {
	switch l {
	case ownerLive:
		return "owner-live"
	case ownerDeadLooking:
		return "owner-dead-looking"
	default:
		return "owner-unverifiable"
	}
}

// delegateLaneOwnerLiveness decides whether the session tree that owns a
// delegate lane's lock (the marker's parent session id) still has a live
// process. A nested lane's marker names a subagent session that may not be
// instantiated while its root process lives, so liveness is resolved through
// the parent chain to the root — the one session whose process hosts the
// whole tree. Any live signal outranks any dead-looking one: a live
// rendezvous pid proves liveness even when the livelock is absent, and a
// held livelock proves it whatever a stale rendezvous entry says.
func (s *Session) delegateLaneOwnerLiveness(m worktree.Marker) ownerLiveness {
	root, ok := s.sessionTreeRootSID(m.SessionID)
	if !ok {
		return ownerUnknown
	}
	if root == s.id {
		// Our own tree hosts us: its lanes are ours to dispose or keep, and
		// their verdict is trivially live.
		return ownerLive
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
	held, exists, err := schema.SessionLivelockHeld(s.currentStateDir(), root)
	if err != nil {
		return ownerUnknown
	}
	switch {
	case held:
		return ownerLive
	case exists:
		// The file is there and its flock is free: the OS released it at
		// process death. No live signal was found above, so the tree reads
		// dead-looking — the reporting word, never a collection verdict.
		return ownerDeadLooking
	default:
		// No livelock at all: a session from before livelocks, or one that
		// never hosted a process here. Answers nothing.
		return ownerUnknown
	}
}

// sessionTreeRootSID resolves a session id to the root of its session tree:
// the first ancestor that is not itself a subagent. Subagent metas chain
// upward via parent_session_id; a root or fork meta stops the walk (a fork
// hosts its own process). It reports false when the chain cannot be resolved
// — a missing meta, a subagent with no parent link, or a cycle — and callers
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
// lifetime flock whose release-by-OS at process death is what lets a later
// probe tell a live owner from a dead-looking one. Root sessions only — a
// subagent is hosted by its root's process. The take is non-blocking and
// silent on every failure: nothing gates, retries or blocks on this lock, and
// a session that could not take it (a same-id second process holds it, a
// transient fs error) simply runs with liveness visibility only — the cost is
// a less certain verdict on ITS lanes, never a lost lane or stalled work.
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

// releaseSessionLivelock drops this session's process livelock. Called last
// in both close policies, after every piece of worktree-adjacent work a live
// process might still owe. Idempotent. The FILE stays: a present-but-free
// livelock is exactly the dead-looking evidence a later probe reports.
func (s *Session) releaseSessionLivelock() {
	s.mu.Lock()
	release := s.livelockRelease
	s.livelockRelease = nil
	s.mu.Unlock()
	if release != nil {
		release()
	}
}

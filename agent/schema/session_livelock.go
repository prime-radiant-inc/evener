package schema

import "path/filepath"

// SessionLivelockSuffix names a root session's process-livelock file beside
// its meta: <dir>/sessions/<id>.livelock.
const SessionLivelockSuffix = ".livelock"

// SessionLivelockPath is where a session's process livelock lives:
// <dir>/sessions/<id>.livelock (dir is the project state directory, the same
// base SessionsDir uses).
func SessionLivelockPath(dir, id string) string {
	return filepath.Join(SessionsDir(dir), id+SessionLivelockSuffix)
}

// The livelock is the one durable signal that answers "is there a live
// process hosting this session?" from outside that process (issue #5471):
//
//   - A root session takes an exclusive advisory flock on its livelock file
//     at init and holds it until its runtime is released; the OS drops the
//     flock when the process dies, however it dies. Subagent sessions never
//     take one: they are hosted by their root's process, and only roots
//     outlive a spawn. A session id resumed by a new process re-takes the
//     same file; a second process on the same id — the documented
//     "can't be told apart" case — finds the flock held and runs without
//     one, which costs liveness visibility, never correctness.
//
//   - A probe (SessionLivelockHeld) distinguishes three states WITHOUT ever
//     taking the lock — the hostlock marker/query pattern: the mint sets a
//     query-visible marker beside its flock, and the probe is a plain
//     F_GETLK inspection that acquires nothing, so it can never delay or
//     deny a concurrent mint. The states: held (a live process — its marker
//     is there), the file exists but nothing is held (no live process — the
//     OS released it at process death), and no file (the session predates
//     livelocks, or never hosted a process here), which answers nothing and
//     must never be read as death. Platforms without a non-taking
//     inspection answer an error instead of guessing.
//
// The platform arms below carry the same API as lockSessionMetaCrossProcess:
// unix takes a real flock, windows takes the LockFileEx equivalent, and the
// remaining GOOS (none of them shipped targets) have no livelock at all —
// AcquireSessionLivelock reports not-applied and SessionLivelockHeld reports
// absent, so callers degrade to "unknown" rather than guessing.
//
// One automated use is allowed, and it withholds rather than takes: a HELD
// livelock stops the prune's orphan arm from releasing a live owner's lane on
// stale-evidence inactivity — a daemon from before the activity-evidence
// refresh leaves a live tree's transcript stale for a whole long command's
// run, and the livelock is the one durable witness against that clock. The
// opposite direction stays forbidden: a livelock that answers "not held" or
// "unknown" is not a death verdict, and no automated path may collect,
// unlock or reclaim a lane because the livelock is absent or unreadable.
// Everything else is reporting — a prune can REPORT whether a locked
// delegate lane's owner tree still has a live process, and the manual
// recovery recipe in docs/developing-evener/worktrees.md can be applied with
// evidence; other acting on the verdict stays a human or model decision, and
// the documented recipe carries the cautions.

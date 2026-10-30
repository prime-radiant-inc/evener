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
//   - A probe (SessionLivelockHeld) distinguishes three states: the flock is
//     held (a live process), the file exists but the flock is free (no live
//     process — the OS released it at process death), and no file (the
//     session predates livelocks, or never hosted a process here), which
//     answers nothing and must never be read as death.
//
// The platform arms below carry the same API as lockSessionMetaCrossProcess:
// unix takes a real flock, windows takes the LockFileEx equivalent, and the
// remaining GOOS (none of them shipped targets) have no livelock at all —
// AcquireSessionLivelock reports not-applied and SessionLivelockHeld reports
// absent, so callers degrade to "unknown" rather than guessing.

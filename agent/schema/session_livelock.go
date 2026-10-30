package schema

import (
	"os"
	"path/filepath"
	"time"
)

// SessionLivelockSuffix names a root session's process-livelock file beside
// its meta: <dir>/sessions/<id>.livelock.
const SessionLivelockSuffix = ".livelock"

// SessionLivelockPath is where a session's process livelock lives:
// <dir>/sessions/<id>.livelock (dir is the project state directory, the same
// base SessionsDir uses).
func SessionLivelockPath(dir, id string) string {
	return filepath.Join(SessionsDir(dir), id+SessionLivelockSuffix)
}

// SessionLivelockParkedSuffix names the durable marker a non-terminal
// retirement writes beside the livelock: <dir>/sessions/<id>.livelock.parked.
// The marker is the park (issue #5471 review round 3): the livelock FILE
// stays, so the marker is what a liveness probe reads to tell a retired root
// (preserved, resumable) from a crashed one (honestly collectible residue).
const SessionLivelockParkedSuffix = ".livelock.parked"

// SessionLivelockParkedPath is where the session's park marker lives:
// <dir>/sessions/<id>.livelock.parked.
func SessionLivelockParkedPath(dir, id string) string {
	return filepath.Join(SessionsDir(dir), id+SessionLivelockParkedSuffix)
}

// MarkSessionLivelockParked durably records a committed non-terminal
// retirement: the timestamped marker supersedes every older crash signal —
// a stale dead rendezvous entry (the roster retains crash entries) or the
// now-free livelock flock — so a retired root answers unknown, never dead,
// to a later probe. Write it before releasing the flock: the marker is the
// commit point, and a crash before it leaves exactly a real crash's state.
func MarkSessionLivelockParked(dir, id string, at time.Time) error {
	if err := os.MkdirAll(SessionsDir(dir), 0o700); err != nil {
		return err
	}
	return os.WriteFile(SessionLivelockParkedPath(dir, id), []byte(at.UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// SessionLivelockParked reports whether the root carries a committed park
// marker. A probe error must be treated as not-parked only together with
// never reading the root as dead on that same evidence.
func SessionLivelockParked(dir, id string) (bool, error) {
	_, err := os.Stat(SessionLivelockParkedPath(dir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ClearSessionLivelockParked removes the park marker: a replacement process
// that has taken the livelock is live again, so the tree's post-crash residue
// is honestly collectible rather than preserved forever. A missing marker is
// already clear: not an error.
func ClearSessionLivelockParked(dir, id string) error {
	err := os.Remove(SessionLivelockParkedPath(dir, id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveSessionLivelock deletes the session's livelock file. A root that
// failed its acquisition for a real (non-contention) reason removes the file
// this way (issue #5471): a live session must never leave an existing-but-free
// livelock behind — that is a dead owner's signature — so the session answers
// unknown (absent) instead. Retirement does NOT remove the file: it parks via
// the marker above. A missing file is already gone: not an error.
func RemoveSessionLivelock(dir, id string) error {
	err := os.Remove(SessionLivelockPath(dir, id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

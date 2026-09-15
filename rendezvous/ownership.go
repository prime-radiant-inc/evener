package rendezvous

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// ownership.go implements exact-ownership rendezvous removal. A daemon's
// deferred exit cleanup runs long after a replacement may have rewritten
// <pid>.json for the same PID (PID reuse, or a hub respawn racing a slow
// exit). Deleting by PID alone would destroy the live replacement's
// rendezvous, so removal re-reads the file under the per-PID ownership lock
// and only unlinks when every field still matches what this process
// registered. Writes take the same lock so a write never interleaves into a
// remove's check-then-unlink window.
//
// Platform strength varies: on Linux/Darwin the lock is an flock on a
// persistent <pid>.lock inode (never deleted, so a held fd can never be
// orphaned onto a fresh inode); elsewhere withOwnershipLock is a no-op and
// StrongOwnershipAvailable reports false so the daemon refuses ownership
// claims it cannot uphold.
//
// Retention bound: the run directory accumulates one empty <pid>.lock inode
// per PID this host has ever run a daemon as. They are intentionally not
// garbage-collected — deleting a lock inode lets a concurrent opener create a
// fresh inode and take a second, non-mutual lock, silently breaking exclusion
// (see withOwnershipLock) — so the bound is the host's process-ID space, and
// the inodes are reclaimed only when the run directory itself is removed.

// ownershipLockPath is the persistent lock inode guarding <pid>.json.
func ownershipLockPath(dir string, pid int) string {
	return filepath.Join(dir, fmt.Sprintf("%d.lock", pid))
}

// entryMatchesOwned reports whether disk carries exactly the identity the
// caller registered. StartedAt compares by instant (time.Time.Equal): JSON
// persistence strips the monotonic reading, so == would refuse the caller's
// own entry.
func entryMatchesOwned(disk, expected Entry) bool {
	return disk.PID == expected.PID &&
		disk.Address == expected.Address &&
		disk.Protocol == expected.Protocol &&
		disk.Endpoint == expected.Endpoint &&
		disk.SourceID == expected.SourceID &&
		disk.ThreadID == expected.ThreadID &&
		disk.SessionID == expected.SessionID &&
		disk.WorkspaceRef == expected.WorkspaceRef &&
		disk.InstanceID == expected.InstanceID &&
		disk.WorkingDir == expected.WorkingDir &&
		disk.StateDir == expected.StateDir &&
		disk.Agent == expected.Agent &&
		disk.Model == expected.Model &&
		disk.Provider == expected.Provider &&
		disk.HubToken == expected.HubToken &&
		disk.StartedAt.Equal(expected.StartedAt) &&
		disk.SpawnedBy == expected.SpawnedBy
}

func readOwnershipEntry(dir string, pid int) (Entry, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)))
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("read rendezvous file: %w", err)
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false, fmt.Errorf("parse rendezvous file: %w", err)
	}
	return entry, true, nil
}

// RemoveIfOwned deletes <dir>/<pid>.json only while it still carries exactly
// expected. A missing file is nil (an exit race already lost is not an
// error); a mismatched file is an error and is left untouched — the live
// replacement's rendezvous survives the stale daemon's cleanup. The
// check-and-remove runs under the per-PID ownership lock shared with Write,
// so it is atomic against a replacement write on strong-ownership platforms.
func RemoveIfOwned(dir string, expected Entry) error {
	return withOwnershipLock(dir, expected.PID, func() error {
		disk, present, err := readOwnershipEntry(dir, expected.PID)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		if !entryMatchesOwned(disk, expected) {
			return fmt.Errorf("rendezvous file for pid %d is owned by a different daemon; leaving it in place", expected.PID)
		}
		return removeFS(afero.NewOsFs(), dir, expected.PID)
	})
}

// RemoveUnlessRegular deletes <dir>/<pid>.json only while it is not a regular
// file, and reports a regular file as an error without touching it. A missing
// artifact is nil: an exit race another cleanup already won is not a failure.
//
// It exists for the one reconciliation RemoveIfOwned cannot make. A stale
// cleanup whose pid-named artifact is not the regular file Write publishes (a
// directory, a leftover socket) fails the exact ownership read and would
// otherwise fall back to an unguarded Remove. Write always publishes a live
// entry as a regular file, so a non-regular artifact cannot be a replacement's
// entry to protect -- but the decision has to be made under the same per-PID
// ownership lock Write takes, because a replacement daemon reusing the PID can
// rename its regular entry over the artifact between an unlocked stat and the
// unlink, and the stale cleanup would then delete the live replacement's
// rendezvous: the precise loss RemoveIfOwned exists to prevent.
func RemoveUnlessRegular(dir string, pid int) error {
	return withOwnershipLock(dir, pid, func() error {
		target := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
		fi, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat rendezvous file: %w", err)
		}
		if fi.Mode().IsRegular() {
			return fmt.Errorf("rendezvous file for pid %d is a regular file; leaving it in place", pid)
		}
		return removeFS(afero.NewOsFs(), dir, pid)
	})
}

// DiskHoldsReplacement reports whether <dir>/<pid>.json currently carries a
// valid rendezvous entry for expected.PID that is not expected's exact identity
// -- i.e. a replacement daemon reused the PID and rewrote the record, leaving
// the caller's own entry already gone.
//
// It answers with the very comparison RemoveIfOwned refuses on
// (entryMatchesOwned), read under the same per-PID ownership lock Write and
// RemoveIfOwned take, so the re-check can never disagree with the guard and
// there is no second definition of ownership to keep in sync. Unlike
// OwnershipFingerprint -- which deliberately omits HubToken, SpawnedBy, Agent,
// Model and Provider so a fingerprint is safe to share with a peer -- this
// comparison covers every published field, so a same-PID record differing in
// even one of those is a different daemon's.
//
// A missing artifact reports (false, nil). An unreadable or unparseable
// artifact, or a failure to take the ownership lock, reports a non-nil error
// together with false: callers must treat that as "not a replacement" so a
// transient filesystem failure stays retryable, never as a completed no-op.
func DiskHoldsReplacement(dir string, expected Entry) (bool, error) {
	var replacement bool
	err := withOwnershipLock(dir, expected.PID, func() error {
		disk, present, err := readOwnershipEntry(dir, expected.PID)
		if err != nil {
			return err
		}
		if present && !entryMatchesOwned(disk, expected) {
			replacement = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return replacement, nil
}

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

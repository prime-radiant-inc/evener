package skill

import (
	"fmt"
	"os"
	"path/filepath"
)

// skillsLockDirName holds one lock file per cache directory. Locks live beside
// the directories rather than inside them, so a published copy stays exactly the
// embedded content and the digest walk sees nothing extra.
const skillsLockDirName = ".locks"

// skillsLease is a held lock on one cache directory. A reader holds a shared
// lease for as long as it may read that copy; the reaper must take an exclusive
// lease before removing anything, so a copy a live process is using is never
// removed.
type skillsLease interface{ Release() error }

// acquireSkillsLease opens path (creating it) and takes a shared lock, or an
// exclusive one when exclusive is true. contended reports that another process
// already holds a conflicting lock. It is a variable so tests can substitute a
// lease without a real lock file.
var acquireSkillsLease = platformAcquireSkillsLease

// skillsLockPath returns the lock file that guards the cache directory named
// name inside base, creating the lock directory first when create is set.
func skillsLockPath(base, name string, create bool) (string, error) {
	locks := filepath.Join(base, skillsLockDirName)
	if create {
		if err := os.MkdirAll(locks, 0o700); err != nil {
			return "", fmt.Errorf("creating skill lock dir: %w", err)
		}
	}
	return filepath.Join(locks, name+".lock"), nil
}

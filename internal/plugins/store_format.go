package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// storeFormatFileName records the last complete audit of the managed content
// roots. It is store metadata covered by Evener's config mask.
const storeFormatFileName = "store-format.json"

const (
	managedContentClean      = "clean"
	managedContentUnverified = "unverified"
)

type storeFormat struct {
	Version        int    `json:"version"`
	ManagedContent string `json:"managed_content"`
}

// managedContentClean reports whether the default managed content roots have a
// completed private-metadata audit. Missing, malformed, or unknown state means
// unverified, which keeps the existing per-spawn walk.
func (m *Manager) managedContentClean() bool {
	data, err := os.ReadFile(filepath.Join(m.Root, storeFormatFileName))
	if err != nil {
		return false
	}
	var state storeFormat
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	return state.Version == 1 && state.ManagedContent == managedContentClean
}

func (m *Manager) writeManagedContentState(state string) error {
	data, err := json.Marshal(storeFormat{Version: 1, ManagedContent: state})
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(m.Root, storeFormatFileName), append(data, '\n'), 0o600)
}

// managedRoot names one content root and its archive namespace.
type managedRoot struct {
	name string
	path string
}

func (m *Manager) managedRoots() []managedRoot {
	return []managedRoot{
		{name: cacheDirName, path: m.cacheDir()},
		{name: bundledDirName, path: m.bundledDir()},
	}
}

// ensureManagedContentMigrated performs the one-time, bounded metadata
// migration while the caller holds the store lock. It is best effort: a lock
// or I/O failure leaves the store unverified so the old scan continues, and
// the next mutation retries. It never rewrites the registry or a saved path.
func (m *Manager) ensureManagedContentMigrated(ctx context.Context) {
	if m.managedContentClean() {
		return
	}
	// Store lock then bundled lock, never the reverse: bundled publication
	// takes only the bundled lock.
	release, err := m.acquireBundledLock(ctx, migrationBundledLockWait)
	if err != nil {
		return
	}
	defer release()
	if m.managedContentClean() {
		return
	}
	problems := m.migrateManagedGitMetadata()
	_, auditProblems := m.auditManagedContentRoots()
	problems = append(problems, auditProblems...)
	state := managedContentClean
	if len(problems) > 0 {
		state = managedContentUnverified
	}
	if err := m.writeManagedContentState(state); err != nil {
		_, _ = m.stderr().Write([]byte("warning: recording managed plugin content state failed: " + err.Error() + "\n"))
		return
	}
	if state == managedContentUnverified {
		for _, problem := range problems {
			_, _ = m.stderr().Write([]byte("warning: managed plugin content needs another migration pass: " + problem + "\n"))
		}
	}
}

//go:build unix

package plugins

import (
	"os"
	"testing"
)

// A store the running user may read but not write — root-owned, or on a
// read-only mount — still has an answer to give: the walk waits on the lock
// file, it never writes to it. Opening the lock for writing turned such a
// store's orphan report into a lock complaint whose remediation would never
// come true, where an unlocked walk reported the orphans fine.
func TestDoctor_OrphanCacheDir_WalksAStoreWithAReadOnlyLockFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a mode that denies write still opens for writing")
	}
	m := NewManager(t.TempDir())
	orphan := m.pluginCacheDir("acme", "widget", "deadbeef")
	writePlugin(t, orphan, "widget", nil)
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.lockPath(), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(m.lockPath(), 0o644); err != nil {
			t.Error(err)
		}
	})

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if f := findFinding(t, findings, orphan); f.Level != LevelWarn {
		t.Errorf("orphan cache dir level = %s, want %s", f.Level, LevelWarn)
	}
	if hasFinding(findings, "unreferenced cache directories") {
		t.Errorf("a lock file that denies writes was reported as contention: %+v", findings)
	}
}

package hub

import (
	"path/filepath"
	"testing"
	"time"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
)

// writeSessionUpdatedAt writes a session's meta last active at updated, so
// age-based archiving sees it as that old.
func writeSessionUpdatedAt(t *testing.T, stateDir, id, wd string, updated time.Time) {
	t.Helper()
	meta := schema.SessionMeta{ID: id, CreatedAt: updated, UpdatedAt: updated, EnvInfo: schema.EnvironmentInfo{WorkingDir: wd}}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
}

// mintEndedScratchTree opens and releases the scratch of rootID and each of its
// children in the system temp dir, as sessions that have ended leave them, and
// returns the tree's path.
func mintEndedScratchTree(t *testing.T, rootID string, childIDs ...string) string {
	t.Helper()
	var dir string
	for _, id := range append([]string{rootID}, childIDs...) {
		scratch, err := agentsandbox.OpenSessionScratch("", t.TempDir(), rootID, id)
		if err != nil {
			t.Fatalf("open scratch %s/%s: %v", rootID, id, err)
		}
		dir = scratch.Dir
		if err := scratch.Retain(); err != nil {
			t.Fatalf("release scratch %s/%s: %v", rootID, id, err)
		}
	}
	return filepath.Dir(dir)
}

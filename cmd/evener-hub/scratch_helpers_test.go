package hub

import (
	"path/filepath"
	"testing"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
)

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

package hub

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// hubScratchPinName mirrors sandbox's immutable per-directory identity pin. The
// name is a durable on-disk contract, so the test asserts the exact file.
const hubScratchPinName = ".evener-retained-session.json"

// pinHubScratch mints a retained, pinned scratch directory for owner and
// releases its live lease so a deletion release can acquire it.
func pinHubScratch(t *testing.T, owner sandboxpkg.ScratchOwner) string {
	t.Helper()
	base, workspace := t.TempDir(), t.TempDir()
	scratch, err := sandboxpkg.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("new session scratch: %v", err)
	}
	ref := sandboxpkg.ScratchReference{Dir: scratch.Dir, Kind: sandboxpkg.ScratchKindUnsandboxed}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatalf("pin scratch: %v", err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatalf("retain scratch: %v", err)
	}
	return scratch.Dir
}

// TestScratchRetentionDeleteReleasesOnlyTargetRoot proves explicit deletion
// releases exactly the target root's scratch-retention manifest (and its
// available directory pin) while a sibling root's manifest stays unreleased and
// pinned. A deletion must never release a surviving root's retained scratch.
func TestScratchRetentionDeleteReleasesOnlyTargetRoot(t *testing.T) {
	stateDir := t.TempDir()
	targetID := projectDeleteCanonicalSessionIDs[0]
	survivorID := projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, targetID, "/tmp/del-project")
	writeSession(t, stateDir, survivorID, "/tmp/del-project")

	targetOwner := sandboxpkg.ScratchOwner{StateDir: stateDir, RootSessionID: targetID}
	survivorOwner := sandboxpkg.ScratchOwner{StateDir: stateDir, RootSessionID: survivorID}
	targetScratch := pinHubScratch(t, targetOwner)
	survivorScratch := pinHubScratch(t, survivorOwner)

	runDir := filepath.Join(stateDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: hubcore.NewRosterWithEntries()})

	deleted, skip, decisionErrors := web.cleanupProjectDeletionTargetAndDecisions(stateDir, targetID)
	if !deleted || skip != nil {
		t.Fatalf("target deletion failed: deleted=%v skip=%+v", deleted, skip)
	}
	if len(decisionErrors) != 0 {
		t.Fatalf("target deletion decision errors: %v", decisionErrors)
	}

	targetManifest, err := sandboxpkg.LoadScratchRetention(targetOwner)
	if err != nil {
		t.Fatal(err)
	}
	if !targetManifest.Released {
		t.Fatal("deleted target root manifest was not released")
	}
	survivorManifest, err := sandboxpkg.LoadScratchRetention(survivorOwner)
	if err != nil {
		t.Fatal(err)
	}
	if survivorManifest.Released {
		t.Fatal("surviving root manifest was released by a deletion")
	}
	if _, err := os.Stat(filepath.Join(targetScratch, hubScratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("deleted target pin survived release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(survivorScratch, hubScratchPinName)); err != nil {
		t.Fatalf("surviving root pin was removed: %v", err)
	}
}

// TestScratchRetentionReleaseWaitsForArtifactRemoval proves the retention
// manifest is released only after the deletion it authorizes actually removed
// the session's artifacts. A failed removal reports skip and must leave the
// manifest and its directory pin intact, so the session stays resumable at its
// originally retained scratch instead of silently minting replacement scratch.
func TestScratchRetentionReleaseWaitsForArtifactRemoval(t *testing.T) {
	stateDir := t.TempDir()
	targetID := projectDeleteCanonicalSessionIDs[0]
	writeSession(t, stateDir, targetID, "/tmp/del-project")

	owner := sandboxpkg.ScratchOwner{StateDir: stateDir, RootSessionID: targetID}
	targetScratch := pinHubScratch(t, owner)

	runDir := filepath.Join(stateDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: hubcore.NewRosterWithEntries()})

	oldRemove := removeProjectSessionFile
	removeProjectSessionFile = func(path string) error {
		if filepath.Base(path) == targetID+".future-artifact" {
			return errors.New("flat-file removal failed")
		}
		return oldRemove(path)
	}
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })

	deleted, skip, _ := web.cleanupProjectDeletionTargetAndDecisions(stateDir, targetID)
	if deleted || skip == nil {
		t.Fatalf("failed artifact removal must skip: deleted=%v skip=%+v", deleted, skip)
	}
	manifest, err := sandboxpkg.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Released {
		t.Fatal("retention manifest was released before the deletion it authorizes succeeded")
	}
	if _, err := os.Stat(filepath.Join(targetScratch, hubScratchPinName)); err != nil {
		t.Fatalf("retained directory pin was removed by a failed deletion: %v", err)
	}
}

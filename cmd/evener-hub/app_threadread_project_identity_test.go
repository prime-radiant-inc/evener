package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// A live read projects a past entry only to fill fields the daemon did not
// send, and mergePastThreadForRead never copies ProjectID/ProjectPath. Resolving
// the past CWD's project identity there is work whose answer is discarded
// (#4692), so only the complete pastEntryThread wrapper annotates.
func TestLiveMergeDoesNotResolveDiscardedProjectIdentity(t *testing.T) {
	root := t.TempDir()
	workingDir := filepath.Join(root, "project")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	cfg := seedPastProjectSession(t, root, workingDir, "project-live-merge-0000000000", sessionID)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}

	var resolved []string
	oldResolve := resolveThreadProject
	resolveThreadProject = func(path string) (identifier.Project, error) {
		resolved = append(resolved, path)
		return oldResolve(path)
	}
	t.Cleanup(func() { resolveThreadProject = oldResolve })

	live, err := mergePastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID}, appwire.Thread{
		ID:          sessionID,
		SessionID:   sessionID,
		CWD:         workingDir,
		ProjectID:   "live-project",
		ProjectPath: "/live/project",
	}, false)
	if err != nil {
		t.Fatalf("mergePastThreadForRead: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("live merge resolved project identity %d times (%v); the merged thread cannot carry it", len(resolved), resolved)
	}
	if live.ProjectID != "live-project" || live.ProjectPath != "/live/project" {
		t.Fatalf("merged project identity = %q %q, want the live thread's own", live.ProjectID, live.ProjectPath)
	}

	archived, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != workingDir {
		t.Fatalf("archived read resolved %v, want one resolution of %q", resolved, workingDir)
	}
	wantProject, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	if archived.ProjectID != wantProject.ID || archived.ProjectPath != wantProject.CanonicalPath {
		t.Fatalf("archived project identity = %q %q, want %q %q", archived.ProjectID, archived.ProjectPath, wantProject.ID, wantProject.CanonicalPath)
	}
}

//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// TestProjectName_WorkspaceSpellingNeverOpensMeta pins the review finding that a
// workspace directory merely spelling an Evener layout is not a state bucket.
// The workspace sits outside every configured state root, so its spelling proves
// nothing and its files must never be read: a readable meta there would let a
// workspace file name the project, and a FIFO at the same path would block the
// entire search inside LoadSessionMeta's open.
func TestProjectName_WorkspaceSpellingNeverOpensMeta(t *testing.T) {
	workspace := t.TempDir()
	// An ordinary non-Git workspace whose path spells .../evener/projects/<name>.
	stateish := filepath.Join(workspace, "evener", "projects", "demo-0123456789")
	sid := identifier.MustNewSessionID()
	lane := filepath.Join(stateish, "sessions", sid, "worktrees", "notes")
	if err := os.MkdirAll(filepath.Dir(schema.SessionMetaPath(stateish, sid)), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	metaPath := schema.SessionMetaPath(stateish, sid)
	bucket := newBucket(t)
	m := schema.SessionMeta{}
	m.EnvInfo.WorkingDir = lane

	// A perfectly readable workspace file that names a different repository must
	// not supply the displayed project.
	t.Run("ReadableMeta", func(t *testing.T) {
		attacker := schema.SessionMeta{ID: sid}
		attacker.EnvInfo.GitOriginURL = "git@github.com:attacker/workspace-repo.git"
		if err := schema.SaveSessionMeta(stateish, attacker); err != nil {
			t.Fatalf("save workspace meta: %v", err)
		}
		if got := projectName(m, bucket); got != "notes" {
			t.Fatalf("projectName(workspace spelling state layout, readable meta) = %q, want %q from the working directory", got, "notes")
		}
	})

	// The load-bearing half: a FIFO where the inferred owner's metadata would be.
	// Opening it blocks forever, so this runs last and returns only because the
	// path is settled before any open. It runs on the test goroutine, so a
	// resolver that does open it hangs here rather than failing.
	t.Run("FIFO", func(t *testing.T) {
		if err := os.Remove(metaPath); err != nil {
			t.Fatalf("remove readable meta: %v", err)
		}
		if err := syscall.Mkfifo(metaPath, 0o600); err != nil {
			t.Fatalf("Mkfifo meta: %v", err)
		}
		if got := projectName(m, bucket); got != "notes" {
			t.Fatalf("projectName(workspace spelling state layout, FIFO meta) = %q, want %q from the working directory", got, "notes")
		}
	})
}

// TestProjectName_RestoreRootGitFIFONeverBlocks pins the review finding that a
// deleted delegate's owner metadata can point its restore root at a directory
// whose ".git" entry is a FIFO. The shared Git-root resolver reads every
// non-directory ".git" entry, so opening it blocks the whole search. The
// resolver must decline that entry and the label must fall back to the restore
// directory's own name. It runs on the test goroutine: a blocking open hangs
// here rather than failing.
func TestProjectName_RestoreRootGitFIFONeverBlocks(t *testing.T) {
	restore := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(restore, 0o755); err != nil {
		t.Fatalf("mkdir restore: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(restore, ".git"), 0o600); err != nil {
		t.Fatalf("Mkfifo .git: %v", err)
	}

	bucket := newBucket(t)
	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID, WorktreeRestoreRoot: restore}
	owner.EnvInfo.WorkingDir = restore
	if err := schema.SaveSessionMeta(bucket, owner); err != nil {
		t.Fatalf("save owner meta: %v", err)
	}

	m := schema.SessionMeta{}
	m.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", ownerID, "worktrees", "notes")
	if got := projectName(m, bucket); got != "repo" {
		t.Fatalf("projectName(owner restore root with .git FIFO) = %q, want %q from the restore directory", got, "repo")
	}
}

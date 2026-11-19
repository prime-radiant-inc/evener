//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/gittest"
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

// TestProjectName_LabelNeverExecutesGit pins the review finding that a label
// lookup must not run a process. A read-only transcript search takes its inputs
// from workspaces: the owner's recorded restore root can sit inside a Git
// submodule, and the command runner resolves "git" from a PATH that workspace
// directories can reach, so executing Git here would run a repository-supplied
// binary with the daemon's permissions.
//
// The canary is a fake git first on PATH that records its own execution. A
// label path that runs Git leaves the marker; one that reads the .git layout
// structurally does not. The submodule is the case that made the resolver run
// Git in the first place (it used to fall through to the binary), and the label
// still names the recorded directory because the layout alone cannot say more.
func TestProjectName_LabelNeverExecutesGit(t *testing.T) {
	canaryDir := t.TempDir()
	canary := filepath.Join(canaryDir, "git-ran")
	fakeGit := filepath.Join(canaryDir, "git")
	// The marker path is quoted: TMPDIR can contain a space, and an unquoted
	// redirect fails under /bin/sh, which would leave the canary unwritten and
	// the test passing without proving anything.
	script := "#!/bin/sh\n: > " + execenv.ShellEscapeArgs(canary) + "\nexit 1\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", canaryDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// A restore root inside a Git submodule: a .git pointer file whose target is
	// a .git/modules directory. Structurally that names the submodule's own
	// checkout, which is what the label should use.
	parent := t.TempDir()
	sub := filepath.Join(parent, "sub")
	subGitDir := filepath.Join(parent, ".git", "modules", "sub")
	if err := os.MkdirAll(subGitDir, 0o755); err != nil {
		t.Fatalf("mkdir submodule git dir: %v", err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir submodule checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+subGitDir+"\n"), 0o644); err != nil {
		t.Fatalf("write submodule pointer: %v", err)
	}

	bucket := newBucket(t)
	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID, WorktreeRestoreRoot: sub}
	owner.EnvInfo.WorkingDir = sub
	if err := schema.SaveSessionMeta(bucket, owner); err != nil {
		t.Fatalf("save owner meta: %v", err)
	}

	m := schema.SessionMeta{}
	m.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", ownerID, "worktrees", "notes")
	got := projectName(m, bucket)

	if _, err := os.Stat(canary); err == nil {
		t.Fatalf("projectName(restore root in a Git submodule) executed git from PATH; a read-only search must never run workspace code (label = %q)", got)
	}
	if got != "sub" {
		t.Fatalf("projectName(restore root in a Git submodule) = %q, want %q from the submodule checkout", got, "sub")
	}
}

// TestProjectName_GitSymlinkToDirectoryResolves pins the review finding that a
// checkout whose ".git" is a symlink to a Git DIRECTORY was sent to the pointer
// reader, which refuses directories, so /repo/pkg displayed "pkg" instead of
// "repo". Following the link once and classifying its target keeps that layout
// working without opening anything it should not.
func TestProjectName_GitSymlinkToDirectoryResolves(t *testing.T) {
	main, _ := gittest.NewLinkedWorktree(t)
	realGit := filepath.Join(main, ".git")
	if info, err := os.Stat(realGit); err != nil || !info.IsDir() {
		t.Fatalf("fixture %q is not a directory: %v", realGit, err)
	}

	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo/pkg: %v", err)
	}
	if err := os.Symlink(realGit, filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("symlink .git to the real Git directory: %v", err)
	}

	m := schema.SessionMeta{}
	m.EnvInfo.WorkingDir = filepath.Join(repo, "pkg")
	if got, want := projectName(m), "repo"; got != want {
		t.Fatalf("projectName(checkout whose .git is a symlink to a Git directory) = %q, want %q, not the subdirectory leaf", got, want)
	}
}

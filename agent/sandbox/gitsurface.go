package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// gitSurfaceInertContent returns the content serf writes when it must materialize
// an absent protected git surface BEFORE bubblewrap pins it, and false for a
// surface that needs no such preparation.
//
// Why any of this is needed: bubblewrap grants and denies by mounting, and a mount
// needs a target, so pinning a MISSING protected file (`--ro-bind /dev/null
// <path>`) makes bwrap create that mountpoint on the REAL filesystem. The file it
// leaves behind survives the sandbox. Seatbelt matches path strings and creates
// nothing, so this is a Linux-only hazard.
//
// The residue is therefore part of the contract, and each protected leaf gets a
// ruling — can it be absent AND under a write root, and if so is an EMPTY file
// there harmless?
//
//   - commondir → ".", and it MUST be pre-created. In a main checkout
//     <cwd>/.git/commondir is absent and sits under the worktree write root, so it
//     was pinned, and git treats an EMPTY commondir as fatal ("failed to read
//     .../commondir") for every later command in that repo, permanently. "." is the
//     only content git accepts, and it is exactly right: git writes commondir only
//     into a LINKED worktree's git dir, so a git dir missing the file is by
//     definition its own common dir, which is what "." says. (An empty file and a
//     directory are both fatal; verified on git 2.43 and 2.50.)
//   - gitdir → not pre-created. It is the back-pointer to a linked worktree's .git
//     FILE, so a git dir that lacks it has no such worktree and there is no correct
//     content to write. An empty <main>/.git/gitdir residue is inert: git reads the
//     back-pointer only from .git/worktrees/<id>/gitdir, and status, worktree
//     list/prune and fsck are unaffected by the file's presence in a main .git.
//   - config → not pre-created. `git init` always creates it, so it is not absent
//     in any repo serf can classify; were it absent, an empty file is a valid
//     (empty) git config.
//   - config.worktree → not pre-created. It is usually absent, and its residue is
//     an empty file, which is a valid git config carrying no directive. git reads
//     it only under extensions.worktreeConfig, and reads it as empty either way.
//   - hooks → not pre-created. It is a DIRECTORY, pinned as an empty read-only
//     tmpfs; bwrap materializes that mountpoint too, so an absent hooks dir comes
//     back as an empty directory, which is exactly what git expects of a repo with
//     no hooks.
func gitSurfaceInertContent(path string) (string, bool) {
	if filepath.Base(path) == "commondir" {
		return ".\n", true
	}
	return "", false
}

// prepareGitSurfaces materializes the absent protected git surfaces whose empty
// residue would break the repo, so the bwrap argv pins a real file with correct
// content instead of pinning /dev/null over a name that does not exist.
//
// It touches a path only when ALL of: the surface has inert content defined
// (today, commondir alone), the path does not exist, and the path sits under a
// spawned write root — i.e. exactly the paths buildBwrapArgv would otherwise pin
// into existence. A read-only session, or a surface under a read-only parent, is
// never touched at all.
//
// It fails CLOSED. A surface serf cannot prepare is one bwrap would materialize
// empty, so the caller refuses to build the wrapper rather than start a sandbox
// that corrupts the repo it is confining.
func prepareGitSurfaces(rp ResolvedPolicy) error {
	writeRoots := rp.Spawned.WriteRoots
	for _, p := range rp.Git.ProtectedPaths {
		content, ok := gitSurfaceInertContent(p)
		if !ok || pathExists(p) || !isUnderAnyRoot(p, writeRoots) {
			continue
		}
		if err := createFileIfAbsent(p, content); err != nil {
			return fmt.Errorf("sandbox: preparing git surface %s: %w", p, err)
		}
	}
	return nil
}

// createFileIfAbsent creates path holding content and does NOTHING when path
// already exists — it never truncates, appends to, or rewrites an existing file.
//
// It stages the content in a sibling temp file and hardlinks it into place rather
// than creating the name and then writing it, so the name never exists with
// partial or empty content: an empty commondir is fatal to git, and a concurrent
// unsandboxed git command in the same repo must never be able to observe one. The
// link is the atomic claim, so two sessions starting on the same repo at once are
// safe — the loser sees EEXIST, which is success (the winner wrote the same
// bytes). The staged file is removed either way.
//
// The result is deliberately NOT cleaned up at teardown: a commondir holding "."
// is semantically correct for that git dir and inert to leave behind, whereas
// deleting it would race every concurrent session that has it bind-mounted.
func createFileIfAbsent(path, content string) error {
	dir := filepath.Dir(path)
	staged, err := os.CreateTemp(dir, ".serf-sandbox-*")
	if err != nil {
		return err
	}
	name := staged.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := staged.WriteString(content); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	// git writes these surfaces world-readable; CreateTemp makes them 0600.
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

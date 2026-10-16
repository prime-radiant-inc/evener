package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/afero"
)

// LocalExecutionEnvironment implements the FileMutator capability so apply_patch
// routes its file mutations through the same enforcement seam as the other file
// tools. When the environment carries a file-tool-confined sandbox policy, each
// method uses the fd-anchored, symlink-refusing layer (e.sandbox()); otherwise it
// resolves relative paths against the working directory without restricting
// unrestricted off sessions to that root. Allocated scratch keeps its own
// fd-anchored contract.

// ErrWorkingDirectoryUnavailable reports that a relative-path mutation was
// refused because the environment's working directory is not an existing
// directory — deleted, or otherwise not a usable directory. A deleted directory
// must never be recreated by a write that silently lands outside any checkout
// (issue #4671). Absolute-path mutations never raise it.
var ErrWorkingDirectoryUnavailable = errors.New("working directory unavailable")

// requireLiveWorkingDirForRelative refuses a relative-path mutation when the
// environment's working directory is not an existing directory, so a write
// cannot silently recreate a deleted directory and land outside any checkout
// (issue #4671). It
// is called only on the plain, unrestricted mutation arm — the one that
// joins the working directory and `MkdirAll`s parents — because that is the
// path that recreates a deleted directory. The sandbox and scratch arms resolve
// through a captured root descriptor; the scratch arm in particular serves the
// confined-file root, whose logical path may legitimately not exist while its
// captured descriptor is live, so a working-directory liveness check cannot be
// applied there. It is a no-op for an absolute path, an empty path, or an empty
// RootDir (whose relative handling is already a bare join).
func (e *LocalExecutionEnvironment) requireLiveWorkingDirForRelative(path string) error {
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) || e.RootDir == "" {
		return nil
	}
	if info, err := e.filesystem().Stat(e.RootDir); err == nil && info.IsDir() {
		return nil
	}
	return errDeadWorkingDir(p, e.RootDir)
}

// errDeadWorkingDir builds the named-recovery refusal a relative mutation gets
// when the directory it anchors on has gone: it names the requested path and
// the dead anchor and tells the caller to re-anchor. Shared by the plain arm's
// requireLiveWorkingDirForRelative (issue #4671) and the sandbox arm's
// requireLiveSandboxRootForRelative (issue #4680) so both surfaces report one
// shape.
func errDeadWorkingDir(path, anchor string) error {
	return fmt.Errorf(
		"%s: working directory %s is not an existing directory; re-anchor the session to an existing directory or operate from a valid working directory (%w)",
		path, anchor, ErrWorkingDirectoryUnavailable)
}

// requireLiveSandboxRootForRelative refuses a relative-path mutation whose
// sandbox root's directory is gone — deleted out from under a running
// file-tool-confined session (issue #4680). The plain arm's path-based check
// above cannot be used on this arm: the confined-file root used for memory
// storage deliberately carries a live descriptor whose logical path does not
// exist, so the descriptor must tell "unlinked" from "path missing"
// (sandboxFS.rootDead). A root never opened yet is opened here, so a relative
// mutation against a session that never wrote under its root still refuses with
// the named recovery rather than the bare ENOENT (or the silent no-op a
// best-effort remove would otherwise report). A renamed-away or swap-recreated
// root keeps a positive link count and is left to the normal fd-anchored
// resolution — rearranging the directory is outside the sandbox's threat model
// (docs/sandboxing.md, "Threat model"). It is a no-op for an absolute path, an
// empty path, an empty RootDir, or a path that anchors on no writable root
// (whose enforcement is a denial anyway). Under a dead root, a masked or
// git-protected target returns that target's own typed denial instead of the
// recovery, so a masked secret keeps its redaction; the policy may spell the
// mask through the canonical path while the target is spelled through a
// symlinked ancestor, so both spellings are checked.
func (e *LocalExecutionEnvironment) requireLiveSandboxRootForRelative(sfs *sandboxFS, tool, path string) error {
	if sfs == nil {
		return nil
	}
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) || e.RootDir == "" {
		return nil
	}
	abs := filepath.Clean(e.resolve(p))
	root, ok := e.sandboxAnchorRoot(abs, sfs.policy.FileTool.WriteRoots)
	if !ok {
		return nil
	}
	if !sfs.rootDead(root) {
		return nil
	}
	// The mask may be spelled through the canonical root while the target is
	// spelled through a symlinked ancestor, so check both, plus the canonical
	// root joined with the literal in-root tail (never following a symlink
	// INSIDE the root, which could otherwise point a masked entry away from its
	// mask).
	candidates := []string{abs, resolveSymlinksBestEffort(abs), filepath.Clean(filepath.Join(root, p))}
	for _, cand := range candidates {
		if sfs.underMaskedForWrite(cand) {
			return sfs.deny(tool, p, denyReasonMasked)
		}
		if sfs.underProtected(cand) {
			return sfs.deny(tool, p, denyReasonProtected)
		}
	}
	return errDeadWorkingDir(p, root)
}

// sandboxAnchorRoot returns the writable root a resolved absolute path anchors
// on. containingRoot answers the common case directly. When it cannot — the
// path was spelled through a symlinked ancestor while the policy root was
// canonicalized (the macOS /var → /private/var analogue), so its real-ancestor
// fallback needs the root on disk and that root is exactly what has gone — the
// relative path still anchors on RootDir, so fall back to the most specific
// writable root that contains abs under best-effort (nearest-existing-ancestor)
// symlink resolution. A path under no writable root still anchors on none, and
// its own out-of-root denial stands.
func (e *LocalExecutionEnvironment) sandboxAnchorRoot(abs string, roots []string) (string, bool) {
	if root, _, ok := containingRoot(roots, abs); ok {
		return root, true
	}
	best := ""
	for _, r := range roots {
		if pathWithinRoot(abs, r) && len(r) > len(best) {
			best = r
		}
	}
	if best != "" {
		return best, true
	}
	// RootDir may itself be a symlink to the (now-deleted) root, so it dangles
	// once the target is gone and every path-based match above fails. os.Readlink
	// still reports the target, so the anchor can be recovered and matched.
	if target, lerr := os.Readlink(e.RootDir); lerr == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(e.RootDir), target)
		}
		target = resolveSymlinksBestEffort(filepath.Clean(target))
		best = ""
		for _, r := range roots {
			if (filepath.Clean(r) == target || pathWithinRoot(target, r)) && len(r) > len(best) {
				best = r
			}
		}
		if best != "" {
			return best, true
		}
	}
	return "", false
}

// ReadFileRaw returns the raw bytes of path subject to the active policy.
func (e *LocalExecutionEnvironment) ReadFileRaw(path string) ([]byte, error) {
	return e.ReadFileRawCtx(context.Background(), path)
}

// ReadFileRawCtx is ReadFileRaw with the caller's context threaded to the
// read, so a cancelled call ends a read parked on a stream.
func (e *LocalExecutionEnvironment) ReadFileRawCtx(ctx context.Context, path string) ([]byte, error) {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.readFileCtx(ctx, "apply_patch", e.resolve(path))
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		defer sfs.release()
		return sfs.readFileCtx(ctx, "apply_patch", abs)
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return nil, err
	}
	return readFileCappedCtx(ctx, e.filesystem(), abs)
}

// WriteFileRaw writes data to path, creating missing parents, confined to the
// active policy (sandboxed: atomic temp+renameat beneath a writable root).
func (e *LocalExecutionEnvironment) WriteFileRaw(path string, data []byte, perm os.FileMode) error {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		if err := e.requireLiveSandboxRootForRelative(sfs, "apply_patch", path); err != nil {
			return err
		}
		return sfs.writeFile("apply_patch", e.resolve(path), data, perm)
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		defer sfs.release()
		return sfs.writeFile("apply_patch", abs, data, perm)
	}
	if err := e.requireLiveWorkingDirForRelative(path); err != nil {
		return err
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return err
	}
	if err := e.filesystem().MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return afero.WriteFile(e.filesystem(), abs, data, perm)
}

// RemovePath deletes path best-effort (a missing target is not an error), but an
// out-of-policy target is a denial. Any other remove failure — permission, a
// read-only filesystem, a nonempty directory — is surfaced, so a delete that did
// not happen is never reported as success (issue #2376).
func (e *LocalExecutionEnvironment) RemovePath(path string) error {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		if err := e.requireLiveSandboxRootForRelative(sfs, "apply_patch", path); err != nil {
			return err
		}
		return sfs.remove("apply_patch", e.resolve(path))
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		defer sfs.release()
		return sfs.remove("apply_patch", abs)
	}
	if err := e.requireLiveWorkingDirForRelative(path); err != nil {
		return err
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return err
	}
	if err := e.filesystem().Remove(abs); err != nil && !isAbsentRemove(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// isAbsentRemove reports whether a remove error means the target is already
// absent: ENOENT (missing target or parent) or ENOTDIR (an ancestor is not a
// directory). Those are the only remove failures a best-effort delete may treat
// as a no-op success; every other failure must propagate (issue #2376).
func isAbsentRemove(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// RenamePath moves oldPath to newPath, creating newPath's parents. Both endpoints
// must satisfy the active policy.
func (e *LocalExecutionEnvironment) RenamePath(oldPath, newPath string) error {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		if err := e.requireLiveSandboxRootForRelative(sfs, "apply_patch", oldPath); err != nil {
			return err
		}
		if err := e.requireLiveSandboxRootForRelative(sfs, "apply_patch", newPath); err != nil {
			return err
		}
		return sfs.rename("apply_patch", e.resolve(oldPath), e.resolve(newPath))
	}
	oldAbs := e.resolve(oldPath)
	newAbs := e.resolve(newPath)
	if sfs := e.scratchSandboxFor(oldAbs); sfs != nil {
		defer sfs.release()
		if targetFS := e.scratchSandboxFor(newAbs); targetFS != nil {
			defer targetFS.release()
			// Both endpoints are inside the same one-session scratch root. Reuse
			// the first layer's cached root fd; its policy root is identical.
			return sfs.rename("apply_patch", oldAbs, newAbs)
		}
		return fmt.Errorf("%s: cannot rename from the session scratch root outside that root", newPath)
	}
	if targetFS := e.scratchSandboxFor(newAbs); targetFS != nil {
		defer targetFS.release()
		return fmt.Errorf("%s: cannot rename into the session scratch root from outside that root", newPath)
	}
	var err error
	if err := e.requireLiveWorkingDirForRelative(oldPath); err != nil {
		return err
	}
	oldAbs, err = e.resolveWrite(oldPath)
	if err != nil {
		return err
	}
	if err := e.requireLiveWorkingDirForRelative(newPath); err != nil {
		return err
	}
	newAbs, err = e.resolveWrite(newPath)
	if err != nil {
		return err
	}
	if err := e.filesystem().MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return err
	}
	return e.filesystem().Rename(oldAbs, newAbs)
}

package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	canonicalCwd := resolveDanglingSymlinks(e.RootDir)
	root, rel, ok := e.sandboxAnchor(abs, canonicalCwd, p, sfs.policy.FileTool.WriteRoots)
	if !ok {
		return nil
	}
	if !sfs.rootDead(root) {
		return nil
	}
	// The mask may be spelled through the canonical root while the target is
	// spelled through a symlinked ancestor, so check every spelling. rel is the
	// literal in-root tail (it never follows a symlink inside the tree) when the
	// per-root containment could derive it; otherwise fall back to the cwd's
	// canonical spelling.
	candidates := []string{abs, resolveSymlinksBestEffort(abs)}
	if rel != "" {
		candidates = append(candidates,
			filepath.Clean(filepath.Join(root, rel)),
			// The selected root may be spelled through a symlink while the mask
			// is spelled through that root's canonical location (RootDir=/alias
			// -> /real/wt/extra, mask at /real/wt/extra/secret). Joining the
			// CANONICAL root with the literal in-root tail — without resolving
			// symlinks inside that tail — spells the mask too, so a canonically
			// spelled mask is still found after the captured root is deleted and
			// recreated (issue #4703, roborev 42975).
			filepath.Clean(filepath.Join(resolveDanglingSymlinks(root), rel)),
		)
	} else {
		candidates = append(candidates, filepath.Clean(filepath.Join(canonicalCwd, filepath.FromSlash(p))))
	}
	// Masking is decided across EVERY spelling before git protection is: a
	// candidate can resolve into a protected surface (secret -> .git/hooks)
	// while a later candidate is the one that names the mask, and the protected
	// denial is not sensitive, so testing the two reasons together would answer
	// with the one that exposes the masked basename (issue #4703, roborev 43007).
	if slices.ContainsFunc(candidates, sfs.underMaskedForWrite) {
		return sfs.deny(tool, p, denyReasonMasked)
	}
	if slices.ContainsFunc(candidates, sfs.underProtected) {
		return sfs.deny(tool, p, denyReasonProtected)
	}
	return errDeadWorkingDir(p, root)
}

// sandboxAnchor returns the writable root a relative target anchors on and the
// literal in-root tail when one can be derived. Two independent answers combine:
//
//   - containingRoot, run per root, resolves only ANCESTORS and keeps the tail
//     literal, so it spells the mask correctly even when a symlink inside the
//     root points elsewhere. It cannot match a root no longer on disk.
//   - The session's cwd resolved through symlinks (dangling and ancestor
//     components included) names the anchor even after its root is deleted, and
//     matching the cwd under a root keeps an overlapping broader grant — a live
//     parent of the deleted workspace — from answering for the dead one.
//
// The most specific (longest) root either answer matches wins. rel is filled
// from the per-root containingRoot answer for that root; a root matched only by
// the cwd has none. A target under no writable root anchors on none.
func (e *LocalExecutionEnvironment) sandboxAnchor(abs, canonicalCwd, p string, roots []string) (root, rel string, ok bool) {
	containRel := map[string]string{}
	for _, r := range roots {
		if _, rl, mok := containingRoot([]string{r}, abs); mok {
			containRel[r] = rl
		}
	}
	// opRoot is the root the subsequent file operation will resolve abs under
	// (containingRoot: its first lexical match, then its real-ancestor match).
	// Roots that canonicalize to the same directory cannot be ranked by
	// specificity, so the tie is broken toward opRoot: the guard then checks the
	// descriptor the operation will actually use, and a check through one
	// equivalent spelling cannot disagree with a use through another (issue
	// #4703, roborev 42975). A tie the operation does not resolve (no root
	// contains abs) leaves the first considered root, which is harmless because
	// the operation denies the same abs as outside every root.
	opRoot, _, _ := containingRoot(roots, abs)
	bestCanon := ""
	consider := func(r string) {
		// Specificity is judged on the CANONICAL root, not the raw spelling: a
		// broader grant reached through a longer symlink alias must not outrank a
		// shorter canonical workspace root beneath it.
		canon := resolveDanglingSymlinks(r)
		switch {
		case root == "" || len(canon) > len(bestCanon):
			root, rel, bestCanon = r, containRel[r], canon
		case len(canon) == len(bestCanon) && r == opRoot:
			root, rel = r, containRel[r]
		}
	}
	for r := range containRel {
		consider(r)
	}
	for _, r := range roots {
		if pathWithinRoot(canonicalCwd, r) {
			consider(r)
		}
	}
	if root == "" {
		return "", "", false
	}
	return root, rel, true
}

// resolveDanglingSymlinks resolves path through symlinks even when the eventual
// target no longer exists: os.Readlink still reports each link's target, so a
// link left dangling by a root's deletion is followed to its final spelling.
// A work queue walks components in order; when a component is a symlink its
// target's OWN components are pushed to the front, so a multi-component target
// (middle/sub) is walked the same way and a link inside a link target is
// resolved too.
//
// A ".." is applied to the ALREADY-RESOLVED path: the queue holds a target's
// components un-cleaned, so ".." is never collapsed lexically before the
// components before it are resolved. For start -> middle/../wt with middle ->
// /real/parent the real cwd is /real/wt, not the /base/wt a lexical clean would
// derive, so the anchor is still found after the root is deleted (issue #4703,
// roborev 42975). maxLinks bounds a cyclic chain. The result is then resolved
// best-effort through its nearest existing ancestor.
func resolveDanglingSymlinks(path string) string {
	const maxLinks = 16
	cur := string(filepath.Separator)
	pending := pathComponents(path)
	links := 0
	for len(pending) > 0 {
		comp := pending[0]
		pending = pending[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		cur = filepath.Join(cur, comp)
		target, err := os.Readlink(cur)
		if err != nil {
			continue
		}
		links++
		if links > maxLinks {
			continue
		}
		// Queue the target's components WITHOUT lexical cleaning and resolve them
		// from the link's directory (or from "/" when absolute), so a ".." inside
		// the target is applied after the components preceding it are resolved.
		if filepath.IsAbs(target) {
			cur = string(filepath.Separator)
		} else {
			cur = filepath.Dir(cur)
		}
		pending = append(pathComponents(target), pending...)
	}
	return resolveSymlinksBestEffort(cur)
}

// pathComponents splits an absolute path into its components WITHOUT cleaning
// it, so "." and ".." survive for resolveDanglingSymlinks to process in order
// against the components already resolved before them.
func pathComponents(path string) []string {
	return strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
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

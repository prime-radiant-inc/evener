package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sessionScratchTreePrefix names a root session's scratch tree in a scratch
// base: <base>/evener-scratch-<rootSessionID>/<sessionID>/ holds the root's and
// each child's scratch side by side. The crashed-scratch sweep matches only
// sessionScratchPrefix, so it never touches a tree: a tree goes when the hub
// archives or deletes its root, or when a one-shot run exits.
const sessionScratchTreePrefix = "evener-scratch-"

// The cache directories the sandbox's env floor points into a session's
// scratch under the session-private cache strategy.
const (
	goCacheDirName    = "gocache"
	goModCacheDirName = "gomodcache"
	npmCacheDirName   = "npm"
	cargoHomeDirName  = "cargo"
)

// SessionCacheDirNames are those cache directories. They are regenerable, so a
// session end prunes them while the rest of the scratch stays.
var SessionCacheDirNames = []string{goCacheDirName, goModCacheDirName, npmCacheDirName, cargoHomeDirName}

// OpenSessionScratch creates, or reopens, sessionID's scratch in rootID's tree
// in the first usable scratch base, and holds its lease until Retain or
// Cleanup. The path depends only on the two IDs, so a resumed session or
// delegate reopens the directory it had. A tree or scratch that is a symlink or
// owned by another user is refused: scratch bases are shared temp dirs.
func OpenSessionScratch(base, workspaceRoot, rootID, sessionID string) (*SessionScratch, error) {
	if !safeScratchName(rootID) || !safeScratchName(sessionID) {
		return nil, fmt.Errorf("sandbox: unsafe session scratch identity %q/%q", rootID, sessionID)
	}
	canonicalWorkspace, err := canonicalScratchRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	cleanBase, err := sessionScratchBase(base, canonicalWorkspace)
	if err != nil {
		return nil, err
	}
	tree := filepath.Join(cleanBase, sessionScratchTreePrefix+rootID)
	if err := ensureOwnedScratchDir(tree); err != nil {
		return nil, err
	}
	dir := filepath.Join(tree, sessionID)
	if err := ensureOwnedScratchDir(dir); err != nil {
		return nil, err
	}
	lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
	if err != nil {
		return nil, fmt.Errorf("sandbox: acquire session scratch lease: %w", err)
	}
	if contended {
		return nil, fmt.Errorf("sandbox: session scratch %q is in use by another process", dir)
	}
	return &SessionScratch{Dir: dir, base: tree, lease: lease}, nil
}

// Named reports whether this scratch lives in a session's tree
// (OpenSessionScratch) rather than being a disposable one (NewSessionScratch).
// A named scratch outlives its session's end; a disposable one does not.
func (s *SessionScratch) Named() bool {
	return s != nil && strings.HasPrefix(filepath.Base(s.base), sessionScratchTreePrefix)
}

// PruneCaches removes the regenerable cache directories (SessionCacheDirNames)
// from the scratch and keeps everything else.
func (s *SessionScratch) PruneCaches() error {
	if s == nil || s.Dir == "" {
		return nil
	}
	var errs []error
	for _, name := range SessionCacheDirNames {
		errs = append(errs, removeTree(filepath.Join(s.Dir, name)))
	}
	return errors.Join(errs...)
}

// RemoveSessionScratchTree removes rootID's whole scratch tree — the root's and
// every child's scratch — from every scratch base a session may have used. An
// absent tree is not an error; one that is a symlink or owned by another user is
// left alone.
func RemoveSessionScratchTree(rootID string) error {
	if !safeScratchName(rootID) {
		return fmt.Errorf("sandbox: unsafe session scratch identity %q", rootID)
	}
	var errs []error
	for _, base := range sessionScratchTreeBases() {
		tree := filepath.Join(base, sessionScratchTreePrefix+rootID)
		info, err := os.Lstat(tree)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if owned, ownerErr := scratchEntryOwnedByProcess(tree); ownerErr != nil || !owned {
			continue
		}
		errs = append(errs, removeTree(tree))
	}
	return errors.Join(errs...)
}

// sessionScratchTreeBases are the bases a tree may live in: the temp dir and
// the user cache dir, the two OpenSessionScratch chooses between.
func sessionScratchTreeBases() []string {
	var bases []string
	for _, candidate := range []func() (string, error){
		func() (string, error) { return sessionScratchTempDir(), nil },
		sessionScratchUserCacheDir,
	} {
		dir, err := candidate()
		if err != nil || dir == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		if !slices.Contains(bases, dir) {
			bases = append(bases, dir)
		}
	}
	return bases
}

// ensureOwnedScratchDir creates dir 0700, or accepts an existing real
// directory this process owns.
func ensureOwnedScratchDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("sandbox: create session scratch %q: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("sandbox: session scratch %q: %w", dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sandbox: session scratch %q is not a directory", dir)
	}
	if owned, err := scratchEntryOwnedByProcess(dir); err != nil || !owned {
		return fmt.Errorf("sandbox: session scratch %q is not owned by this user", dir)
	}
	return nil
}

// safeScratchName reports whether id can name one path component.
func safeScratchName(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`) && !strings.ContainsRune(id, 0)
}

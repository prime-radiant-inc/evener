package execenv

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	"primeradiant.com/serf/agent/sandbox"
)

var (
	canonicalPathForFd   = canonicalPathOfFd
	secureRandRead       = rand.Read
	secureOpenat         = unix.Openat
	secureWrite          = unix.Write
	secureClose          = unix.Close
	secureRenameat       = unix.Renameat
	secureReadDirEntries = readDirEntries
	secureEntryInfo      = func(entry os.DirEntry) (os.FileInfo, error) { return entry.Info() }
	securePathRel        = filepath.Rel
)

// sandboxFS enforces a resolved sandbox policy on file operations using
// fd-anchored, symlink-refusing primitives. It is the in-process (privileged)
// enforcement layer described in the sandboxing design's "Race-safe path
// enforcement" section: it never re-opens a path after checking it, so a
// concurrent symlink swap between the check and the I/O makes the operation fail
// rather than redirect.
//
// A sandboxFS is built once per LocalExecutionEnvironment from its resolved
// policy (see e.sandbox()) and is only ever constructed for an ENFORCED policy —
// off mode / a nil policy never builds one, so those paths keep today's
// afero/os behavior byte-for-byte. It caches an O_DIRECTORY fd for each allowed
// root the first time that root is used, so a later swap of the root directory
// itself cannot redirect resolution.
//
// The file-operation methods are safe for concurrent use with each other. close()
// is NOT — it releases the cached root fds and must run only at environment
// teardown (Cleanup), after all file tools for the environment have returned; the
// session lifecycle guarantees this ordering.
type sandboxFS struct {
	policy *sandbox.ResolvedPolicy

	// grant, when non-empty, is a single per-invocation granted absolute path (M7
	// escalation approve). It widens ONLY root-containment for EXACTLY this one
	// path: a read/write of grant resolves as if grant's parent were an allowed
	// root, through a "/"-anchored open that refuses EVERY symlink in the path
	// (parent and leaf), so the grant widens containment only — never symlink
	// resolution, and never anchors on an unvetted parent directory. It never
	// overrides masking or git-protection, and never widens a sibling or a subtree.
	// It is set only on a short-lived clone (WithSandboxInvocationGrant) used for one
	// tool re-dispatch, never on a durable env, so it cannot outlive the invocation.
	grant string

	mu      sync.Mutex
	rootFds map[string]int // canonical root path → cached O_DIRECTORY fd
}

// isGranted reports whether abs is exactly this fs's single per-invocation granted
// path (never a sibling, never a subtree — precisely one leaf).
func (s *sandboxFS) isGranted(abs string) bool {
	return s.grant != "" && abs == s.grant
}

// newSandboxFS builds a sandboxFS for an enforced resolved policy. The caller
// (e.sandbox()) guarantees policy != nil and policy.Enforced().
func newSandboxFS(p *sandbox.ResolvedPolicy) *sandboxFS {
	return &sandboxFS{policy: p, rootFds: map[string]int{}}
}

// close releases every cached root fd. Safe to call more than once.
func (s *sandboxFS) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, fd := range s.rootFds {
		_ = unix.Close(fd)
		delete(s.rootFds, k)
	}
}

// Denial reasons. The masked/protected reasons drive audit-log redaction to a
// <denied> token (the basename of a secret path is itself sensitive); the others
// redact to a basename.
const (
	denyReasonMasked       = "credential or pseudo-filesystem path is masked"
	denyReasonProtected    = "git config/hook surface is read-only under sandbox"
	denyReasonOutsideRead  = "outside the sandbox's readable roots"
	denyReasonOutsideWrite = "outside the sandbox's writable roots"
	denyReasonWriteDenied  = "writes are denied in this sandbox mode"
	denyReasonSymlink      = "refuses to traverse a symlinked or non-directory path component"
	denyReasonEscape       = "path resolves outside the sandbox root"
	denyReasonRootTarget   = "cannot operate on a sandbox root itself"
)

// errEscapesRoot is the sentinel a component walk returns when a path would
// escape its anchoring root (the macOS/other analogue of openat2's EXDEV under
// RESOLVE_BENEATH). Kept package-level so both the darwin walk and the shared
// error mapper agree on it.
var errEscapesRoot = errors.New("path escapes sandbox root")

// errSymlinkComponent is the sentinel a component walk returns when it refused to
// traverse a component: a symlinked directory opened with O_DIRECTORY|O_NOFOLLOW
// surfaces as either ELOOP or ENOTDIR depending on kernel check order (and a real
// swap during a race can flip which), so the walks map BOTH to this one sentinel
// without a second, race-prone lstat — the open already refused to follow, and
// this only classifies the resulting error into a legible typed denial.
var errSymlinkComponent = errors.New("sandbox: refused non-traversable path component")

// isTraversalRefusal reports whether an open error from a component walk means the
// component could not be traversed as a real directory beneath the root (a symlink
// → ELOOP, or a non-directory → ENOTDIR). Both are sandbox refusals, not genuine
// I/O errors, and are classified without a second syscall so a concurrent swap
// cannot change the classification after the fact.
func isTraversalRefusal(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)
}

// deny builds a typed *sandbox.DeniedError for a file-tool denial and emits one
// redacted audit line. Command/OutputSoFar are left empty — those belong to the
// shell/kernel denials M3 populates.
func (s *sandboxFS) deny(tool, denyPath, reason string) *sandbox.DeniedError {
	auditDenial(s.policy.Mode, tool, denyPath, reason)
	// A masked (credential/pseudo-fs) denial is Sensitive: neither the model
	// message nor the audit log may echo even the basename (id_rsa, credentials,
	// environ would reveal which secret was probed). Other reasons — outside a
	// root, a read-only git surface, a symlink component — carry an informative
	// non-secret path, so they keep the basename.
	sensitive := reason == denyReasonMasked
	modelReason := reason
	if !sensitive {
		// Tell the model the box is immutable — a per-session policy no tool call can
		// relax — so it stops retrying the same denied path. Mode-agnostic and
		// accurate for every mode; the audit record above keeps the terse reason.
		modelReason = reason + "; this sandbox policy is fixed for the session"
	}
	return &sandbox.DeniedError{
		Mode:       s.policy.Mode,
		Tool:       tool,
		Path:       denyPath,
		Reason:     modelReason,
		Sensitive:  sensitive,
		ReasonKind: denialReasonKind(reason),
	}
}

// denialReasonKind maps a display-text reason to its typed classification, so the
// two never diverge from one place (deny() is the single construction site). M7's
// escalation eligibility keys on the typed kind, never on this text.
func denialReasonKind(reason string) sandbox.DenialReason {
	switch reason {
	case denyReasonOutsideRead:
		return sandbox.DenialOutsideReadRoots
	case denyReasonOutsideWrite:
		return sandbox.DenialOutsideWriteRoots
	case denyReasonWriteDenied:
		return sandbox.DenialWritesDisabled
	case denyReasonMasked:
		return sandbox.DenialMasked
	case denyReasonProtected:
		return sandbox.DenialGitProtected
	case denyReasonSymlink:
		return sandbox.DenialSymlink
	case denyReasonEscape:
		return sandbox.DenialEscape
	case denyReasonRootTarget:
		return sandbox.DenialRootTarget
	default:
		return sandbox.DenialUnspecified
	}
}

// mapOpenErr turns a raw open error into a typed denial when it is a
// sandbox-caused refusal (a symlink traversal or a root escape) and otherwise
// returns it verbatim so genuine ENOENT/EACCES surface exactly as today.
func (s *sandboxFS) mapOpenErr(tool, p string, err error) error {
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, errSymlinkComponent):
		return s.deny(tool, p, denyReasonSymlink)
	case errors.Is(err, unix.EXDEV), errors.Is(err, errEscapesRoot):
		return s.deny(tool, p, denyReasonEscape)
	default:
		return err
	}
}

// rootFd returns the cached O_DIRECTORY fd for a canonical allowed root, opening
// and caching it on first use. The fd is captured once so a later swap of the
// root directory cannot redirect resolution beneath it.
func (s *sandboxFS) rootFd(root string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fd, ok := s.rootFds[root]; ok {
		return fd, nil
	}
	fd, err := openRootDir(root)
	if err != nil {
		return -1, err
	}
	s.rootFds[root] = fd
	return fd, nil
}

// underMasked reports whether abs is at or beneath any masked (secrets/pseudo-fs)
// path from the resolved policy.
func (s *sandboxFS) underMasked(abs string) bool {
	for _, m := range s.policy.MaskedPaths {
		if abs == m || pathUnder(abs, m) {
			return true
		}
	}
	return false
}

// isGrantedRoot reports whether abs is exactly one of the policy's granted roots
// (file-tool read or write roots). Such a root exists by construction even when
// its parent lies outside the policy.
func (s *sandboxFS) isGrantedRoot(abs string) bool {
	for _, r := range s.policy.FileTool.ReadRoots {
		if abs == r {
			return true
		}
	}
	for _, r := range s.policy.FileTool.WriteRoots {
		if abs == r {
			return true
		}
	}
	return false
}

// underProtected reports whether abs is at or beneath any git config/hook surface
// the resolved policy protects (write-denied even inside a writable root).
func (s *sandboxFS) underProtected(abs string) bool {
	for _, p := range s.policy.Git.ProtectedPaths {
		if abs == p || pathUnder(abs, p) {
			return true
		}
	}
	return false
}

// openRead opens abs for reading per the policy's file-tool read shape, returning
// an fd (caller closes) or a typed denial. A read that lands within a granted root
// is resolved the SAME way writes are — anchored at the cached root fd, walking
// only the in-root relative tail (RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS on Linux; an
// O_NOFOLLOW tail walk on darwin) — so a symlinked ANCESTOR of the root is
// tolerated while a symlink COMPONENT inside the root is still refused. The two
// shapes differ only in what falls outside every granted root:
//   - ReadWorktreeOnly (restricted): an out-of-root target is denied.
//   - ReadAnywhere (read-only/workspace-write): an out-of-root target is opened with
//     RESOLVE_NO_SYMLINKS from "/" (refused if masked), never following any symlink.
func (s *sandboxFS) openRead(tool, abs string, flags int) (int, error) {
	abs = filepath.Clean(abs)
	// A per-invocation grant permits EXACTLY this one leaf, resolved from "/" with
	// every symlink refused (parent and leaf) — the same anywhere-minus-denylist
	// shape an out-of-root read uses, restricted to the one path. It never anchors
	// on an unvetted parent directory, so a symlinked parent cannot redirect the
	// grant off the approved path; masking still applies.
	if s.isGranted(abs) {
		return s.openAnywhereMinusMasked(tool, abs, flags)
	}
	if s.policy.FileTool.Read == sandbox.ReadWorktreeOnly {
		root, rel, ok := containingRoot(s.policy.FileTool.ReadRoots, abs)
		if !ok {
			return -1, s.deny(tool, abs, denyReasonOutsideRead)
		}
		return s.openInRoot(tool, abs, root, rel, flags)
	}

	// ReadAnywhere: anchor an in-root read at its granted root exactly like a write,
	// so a symlinked ancestor of the root does not make an in-root file unreadable.
	// The granted roots are the writable roots (the worktree and any extra writable
	// roots); read-only mode has none, so all its reads take the out-of-root path.
	if root, rel, ok := containingRoot(s.policy.FileTool.WriteRoots, abs); ok {
		return s.openInRoot(tool, abs, root, rel, flags)
	}

	// A target outside every granted root: allowed anywhere minus masked.
	return s.openAnywhereMinusMasked(tool, abs, flags)
}

// openAnywhereMinusMasked opens abs from "/" refusing EVERY symlink
// (RESOLVE_NO_SYMLINKS), so the cleaned textual denylist check is authoritative and
// no symlink anywhere in the path is followed. Shared by the ReadAnywhere
// out-of-root read shape and the per-invocation grant — both must open a path that
// lies outside every anchored root without ever trusting a symlinked component.
func (s *sandboxFS) openAnywhereMinusMasked(tool, abs string, flags int) (int, error) {
	if s.underMasked(abs) {
		return -1, s.deny(tool, abs, denyReasonMasked)
	}
	fd, err := openAbsNoSymlinks(abs, flags, 0)
	if err != nil {
		return -1, s.mapOpenErr(tool, abs, err)
	}
	if rerr := s.recheckMaskedFd(tool, abs, fd); rerr != nil {
		_ = unix.Close(fd)
		return -1, rerr
	}
	return fd, nil
}

// openInRoot opens rel (a cleaned, "..-free" slash-relative tail) beneath the
// cached fd for a granted root, the shared root-fd-anchored path both read shapes
// and every write use. It refuses a symlink component inside the root while
// tolerating a symlinked ancestor of the root itself, and enforces the masked
// denylist both textually before the open and against the fd's canonical path after.
func (s *sandboxFS) openInRoot(tool, abs, root, rel string, flags int) (int, error) {
	if s.underMasked(abs) {
		return -1, s.deny(tool, abs, denyReasonMasked)
	}
	rootFd, err := s.rootFd(root)
	if err != nil {
		return -1, err
	}
	fd, err := openBeneathRoot(rootFd, rel, flags, 0)
	if err != nil {
		return -1, s.mapOpenErr(tool, abs, err)
	}
	if rerr := s.recheckMaskedFd(tool, abs, fd); rerr != nil {
		_ = unix.Close(fd)
		return -1, rerr
	}
	return fd, nil
}

// recheckMaskedFd re-runs the masked-path denylist against the kernel's canonical
// path for an already-open fd. On a case- or normalization-insensitive filesystem
// the pre-open textual check is not authoritative (the kernel resolved ".SSH" to
// ".ssh"); re-checking the fd's true path — reported with real casing — closes
// that bypass while staying TOCTOU-safe (the fd is pinned to the opened inode).
// Where canonicalization is unavailable it fails closed only on platforms that
// require it (darwin); on Linux the textual pre-check already held.
func (s *sandboxFS) recheckMaskedFd(tool, orig string, fd int) error {
	canon, err := canonicalPathForFd(fd)
	if err != nil || canon == "" {
		if canonicalRecheckRequired {
			return s.deny(tool, orig, denyReasonMasked)
		}
		return nil
	}
	if s.underMasked(canon) {
		return s.deny(tool, orig, denyReasonMasked)
	}
	return nil
}

// recheckWriteTargetFd re-runs the masked AND git-protected checks against the
// kernel's canonical path for an already-open parent directory fd joined with the
// leaf, closing the same case/normalization-insensitivity bypass for writes (e.g.
// a ".GIT/hooks" plant on APFS). Fails closed on platforms that require the
// re-check when canonicalization is unavailable.
func (s *sandboxFS) recheckWriteTargetFd(tool, orig string, parentFd int, leaf string) error {
	canon, err := canonicalPathForFd(parentFd)
	if err != nil || canon == "" {
		if canonicalRecheckRequired {
			return s.deny(tool, orig, denyReasonProtected)
		}
		return nil
	}
	target := filepath.Join(canon, leaf)
	if s.underMasked(target) {
		return s.deny(tool, orig, denyReasonMasked)
	}
	if s.underProtected(target) {
		return s.deny(tool, orig, denyReasonProtected)
	}
	return nil
}

// openWriteParent resolves the parent directory of abs beneath a writable root,
// returning that directory fd (caller closes) and the leaf name. Writes never
// happen "anywhere": WriteRoots is always the confining set (empty ⇒ read-only
// mode ⇒ all writes denied). When create is true, missing intermediate
// directories are created beneath the root fd (each component openat'd with
// O_NOFOLLOW so a symlinked component is refused, never followed).
func (s *sandboxFS) openWriteParent(tool, abs string, create bool) (int, string, error) {
	abs = filepath.Clean(abs)
	// A per-invocation grant permits EXACTLY this one leaf, even in read-only mode
	// (empty WriteRoots): resolve its parent from "/" with every symlink refused, so
	// a symlinked parent cannot redirect the write off the approved path, and no
	// out-of-policy intermediate directory is created. Masking, git-protection, and
	// the leaf's symlink refusal still apply.
	if s.isGranted(abs) {
		return s.grantedWriteParent(tool, abs)
	}
	if len(s.policy.FileTool.WriteRoots) == 0 {
		return -1, "", s.deny(tool, abs, denyReasonWriteDenied)
	}
	root, rel, ok := containingRoot(s.policy.FileTool.WriteRoots, abs)
	if !ok {
		return -1, "", s.deny(tool, abs, denyReasonOutsideWrite)
	}
	if s.underMasked(abs) {
		return -1, "", s.deny(tool, abs, denyReasonMasked)
	}
	if s.underProtected(abs) {
		return -1, "", s.deny(tool, abs, denyReasonProtected)
	}
	dir, leaf := splitLeaf(rel)
	if leaf == "" {
		return -1, "", s.deny(tool, abs, denyReasonRootTarget)
	}
	rootFd, err := s.rootFd(root)
	if err != nil {
		return -1, "", err
	}
	if create {
		if err := ensureDirsBeneath(rootFd, dir); err != nil {
			return -1, "", s.mapOpenErr(tool, abs, err)
		}
	}
	parentFd, err := openBeneathRoot(rootFd, dirOrDot(dir), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, "", s.mapOpenErr(tool, abs, err)
	}
	if rerr := s.recheckWriteTargetFd(tool, abs, parentFd, leaf); rerr != nil {
		_ = unix.Close(parentFd)
		return -1, "", rerr
	}
	return parentFd, leaf, nil
}

// grantedWriteParent resolves the parent directory of the single granted leaf
// WITHOUT following any symlink: openAbsNoSymlinks refuses every symlink in the
// parent path (a symlinked parent → ELOOP → typed denial), so the grant can never
// redirect the write off the approved path. It does NOT create missing intermediate
// directories — an out-of-policy path's parents are not the grant's to create — and
// still enforces masking and git-protection (the grant widens containment only).
// The leaf's own symlink refusal is enforced by writeFile's AT_SYMLINK_NOFOLLOW
// Fstatat, exactly as for an in-root write.
func (s *sandboxFS) grantedWriteParent(tool, abs string) (int, string, error) {
	if s.underMasked(abs) {
		return -1, "", s.deny(tool, abs, denyReasonMasked)
	}
	if s.underProtected(abs) {
		return -1, "", s.deny(tool, abs, denyReasonProtected)
	}
	leaf := filepath.Base(abs)
	if leaf == "" || leaf == "." || leaf == string(filepath.Separator) {
		return -1, "", s.deny(tool, abs, denyReasonRootTarget)
	}
	parentFd, err := openAbsNoSymlinks(filepath.Dir(abs), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, "", s.mapOpenErr(tool, abs, err)
	}
	if rerr := s.recheckWriteTargetFd(tool, abs, parentFd, leaf); rerr != nil {
		_ = unix.Close(parentFd)
		return -1, "", rerr
	}
	return parentFd, leaf, nil
}

// readFile reads the whole file at abs through a race-safe fd.
func (s *sandboxFS) readFile(tool, abs string) ([]byte, error) {
	fd, err := s.openRead(tool, abs, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), abs) // takes ownership of fd; f.Close closes it
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// writeFile atomically writes data to abs: it resolves the parent beneath a
// writable root, creates a temp file in that directory fd, writes it, and
// renameat's it onto the leaf beneath the SAME directory fd. The rename target is
// never re-resolved by path, so a concurrent swap of the parent cannot land the
// write outside a writable root.
func (s *sandboxFS) writeFile(tool, abs string, data []byte, perm os.FileMode) error {
	parentFd, leaf, err := s.openWriteParent(tool, abs, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parentFd) }()
	var st unix.Stat_t
	if serr := unix.Fstatat(parentFd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW); serr == nil {
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			// The leaf is a symlink. Refuse rather than replace it: the off path
			// (O_TRUNC) would follow it and write to its (possibly out-of-tree)
			// target, and silently clobbering it with a fresh in-tree file would be
			// a surprising, ambiguous result. A symlink leaf is a denial.
			return s.deny(tool, abs, denyReasonSymlink)
		case unix.S_IFREG:
			// Preserve an existing regular file's mode: the off path rewrites in
			// place (O_TRUNC keeps the mode), but the atomic temp+rename creates a
			// fresh inode, so without this an edit would silently strip a script's
			// executable bit or loosen a restrictive mode. A fresh file keeps the
			// caller's perm.
			perm = os.FileMode(st.Mode & 0o777)
		}
	}
	return atomicWriteAt(parentFd, leaf, data, perm)
}

// remove deletes abs if it is beneath a writable root. A path outside the
// writable set (or under a masked/protected surface) is a typed denial; an
// in-root unlink that fails because the target is absent is not an error
// (matching apply_patch's best-effort delete).
func (s *sandboxFS) remove(tool, abs string) error {
	parentFd, leaf, err := s.openWriteParent(tool, abs, false)
	if err != nil {
		// Best-effort delete, matching off-mode RemovePath (which swallows a missing
		// target): when the target's parent directory is simply absent, the target is
		// already gone, so a missing-parent ENOENT/ENOTDIR is a no-op success rather
		// than a failed apply_patch. Genuine policy denials (outside a writable root,
		// masked, git-protected, a refused symlink component) still propagate.
		var denied *sandbox.DeniedError
		if !errors.As(err, &denied) && (errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR)) {
			return nil
		}
		return err
	}
	defer func() { _ = unix.Close(parentFd) }()
	if uerr := unix.Unlinkat(parentFd, leaf, 0); uerr != nil {
		if errors.Is(uerr, unix.EISDIR) {
			_ = unix.Unlinkat(parentFd, leaf, unix.AT_REMOVEDIR)
		}
	}
	return nil
}

// rename moves oldAbs to newAbs. Both endpoints must resolve beneath a writable
// root; the destination's parents are created beneath its root fd. The rename is
// a single renameat between the two checked directory fds.
func (s *sandboxFS) rename(tool, oldAbs, newAbs string) error {
	oldParent, oldLeaf, err := s.openWriteParent(tool, oldAbs, false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(oldParent) }()
	newParent, newLeaf, err := s.openWriteParent(tool, newAbs, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(newParent) }()
	return unix.Renameat(oldParent, oldLeaf, newParent, newLeaf)
}

// mkdirAll creates abs (and any missing parents) beneath a writable root.
func (s *sandboxFS) mkdirAll(tool, abs string) error {
	abs = filepath.Clean(abs)
	if len(s.policy.FileTool.WriteRoots) == 0 {
		return s.deny(tool, abs, denyReasonWriteDenied)
	}
	root, rel, ok := containingRoot(s.policy.FileTool.WriteRoots, abs)
	if !ok {
		return s.deny(tool, abs, denyReasonOutsideWrite)
	}
	if s.underMasked(abs) {
		return s.deny(tool, abs, denyReasonMasked)
	}
	if s.underProtected(abs) {
		return s.deny(tool, abs, denyReasonProtected)
	}
	rootFd, err := s.rootFd(root)
	if err != nil {
		return err
	}
	if err := ensureDirsBeneath(rootFd, rel); err != nil {
		return s.mapOpenErr(tool, abs, err)
	}
	return nil
}

// exists reports whether abs names an existing, sandbox-reachable file or
// directory. A masked/out-of-policy path, a symlinked leaf (refused), or a
// genuinely absent entry all report false — without leaking which.
func (s *sandboxFS) exists(tool, abs string) bool {
	abs = filepath.Clean(abs)
	if s.underMasked(abs) {
		return false
	}
	// A granted root itself exists even though its parent may be outside policy
	// (under restricted, the worktree root's parent is not a read root).
	if s.isGrantedRoot(abs) {
		return true
	}
	// Resolve the parent for reading, then Fstatat the leaf without following a
	// final symlink. A symlinked leaf reports false (sandbox refuses symlinks).
	parent := filepath.Dir(abs)
	leaf := filepath.Base(abs)
	fd, err := s.openRead(tool, parent, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstatat(fd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return st.Mode&unix.S_IFMT != unix.S_IFLNK
}

// listDir returns the entries beneath abs, recursing up to depth levels, using
// only fd-anchored operations: the top directory is resolved beneath the correct
// root, and each subdirectory is re-opened beneath its parent's fd with
// O_NOFOLLOW — never re-resolved from the root by a joined path. Masked entries
// are skipped so a denylisted subtree is never enumerated.
func (s *sandboxFS) listDir(tool, abs string, depth int) ([]DirEntry, error) {
	abs = filepath.Clean(abs)
	if s.underMasked(abs) {
		return nil, s.deny(tool, abs, denyReasonMasked)
	}
	fd, err := s.openRead(tool, abs, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	var out []DirEntry
	if err := s.walkDirFd(fd, "", abs, depth, &out); err != nil { // walkDirFd closes fd
		return nil, err
	}
	return out, nil
}

// walkDirFd reads the entries of the directory referenced by dirFd (which it
// closes), appends them to out, and recurses into real subdirectories beneath
// dirFd. relPrefix is the path prefix reported to the caller; baseAbs is the real
// absolute path of dirFd, used only to skip masked entries.
func (s *sandboxFS) walkDirFd(dirFd int, relPrefix, baseAbs string, depth int, out *[]DirEntry) error {
	defer func() { _ = unix.Close(dirFd) }()
	ents, err := secureReadDirEntries(dirFd)
	if err != nil {
		return err
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, ent := range ents {
		name := ent.Name()
		childAbs := filepath.Join(baseAbs, name)
		if s.underMasked(childAbs) {
			continue
		}
		relName := name
		if relPrefix != "" {
			relName = filepath.Join(relPrefix, name)
		}
		de := DirEntry{Name: relName, IsDir: ent.IsDir()}
		if ent.Type()&os.ModeSymlink != 0 {
			de.IsSymlink = true
		}
		if !ent.IsDir() {
			if info, ierr := secureEntryInfo(ent); ierr == nil {
				de.Size = info.Size()
				if info.Mode()&0o111 != 0 {
					de.IsExec = true
				}
			}
		}
		*out = append(*out, de)
		if ent.IsDir() && depth > 1 {
			// Re-open the subdir beneath dirFd (O_NOFOLLOW): a symlinked dir is
			// refused, and resolution stays anchored at the checked parent.
			childFd, cerr := secureOpenat(dirFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if cerr != nil {
				continue // unreadable/symlinked subdir: skip, keep listing
			}
			if err := s.walkDirFd(childFd, relName, childAbs, depth-1, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// openReadBaseFd resolves a browse base directory (glob/grep) beneath the policy,
// returning a directory fd (caller closes) and the canonical absolute base path.
// It applies the same read-shape check as file reads, so a base outside the
// readable roots (restricted) or under the denylist (any mode) is refused.
func (s *sandboxFS) openReadBaseFd(tool, base string) (int, string, error) {
	base = filepath.Clean(base)
	fd, err := s.openRead(tool, base, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return -1, "", err
	}
	return fd, base, nil
}

// ---- shared, platform-independent primitives ----

// tempName returns an unpredictable temp basename for the atomic write. The
// randomness avoids a griefing pre-create DoS (a predictable name could be
// pre-planted to make O_EXCL creation fail); combined with O_EXCL, a collision
// fails the create rather than clobbering anything.
func tempName() string {
	var b [12]byte
	if _, err := secureRandRead(b[:]); err != nil {
		// crypto/rand should never fail; fall back to a pid-tagged name rather than
		// panicking. Still O_EXCL-guarded at the create site.
		return fmt.Sprintf(".serf-sbtmp-%d", os.Getpid())
	}
	return ".serf-sbtmp-" + hex.EncodeToString(b[:])
}

// atomicWriteAt writes data to a temp file created in dirFd, then renameat's it
// onto leaf within the SAME dirFd. On any failure the temp file is unlinked. All
// operations use dirFd — the path is never re-resolved after the parent check.
func atomicWriteAt(dirFd int, leaf string, data []byte, perm os.FileMode) error {
	tmp := tempName()
	fd, err := secureOpenat(dirFd, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return err
	}
	if werr := writeAllFd(fd, data); werr != nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return werr
	}
	if cerr := secureClose(fd); cerr != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return cerr
	}
	if rerr := secureRenameat(dirFd, tmp, dirFd, leaf); rerr != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return rerr
	}
	return nil
}

// writeAllFd writes all of data to fd, retrying short writes and EINTR.
func writeAllFd(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := secureWrite(fd, data)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		data = data[n:]
	}
	return nil
}

// readDirEntries reads all directory entries from dirFd without consuming it: it
// dups the fd (F_DUPFD_CLOEXEC), reads through the dup, and closes only the dup,
// leaving dirFd valid for subsequent openat recursion.
func readDirEntries(dirFd int) ([]os.DirEntry, error) {
	dup, err := unix.FcntlInt(uintptr(dirFd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "")
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

// ensureDirsBeneath creates each component of relDir beneath rootFd if missing,
// descending one component at a time with O_NOFOLLOW so a symlinked component is
// refused rather than followed. relDir is slash-relative and cleaned; "" or "."
// is a no-op.
func ensureDirsBeneath(rootFd int, relDir string) error {
	comps := relComponents(relDir)
	if len(comps) == 0 {
		return nil
	}
	cur := rootFd
	curOwned := false
	closeCur := func() {
		if curOwned {
			_ = unix.Close(cur)
		}
	}
	for _, comp := range comps {
		if comp == ".." {
			closeCur()
			return errEscapesRoot
		}
		if err := unix.Mkdirat(cur, comp, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			closeCur()
			return err
		}
		next, err := unix.Openat(cur, comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if isTraversalRefusal(err) {
				err = errSymlinkComponent
			}
			closeCur()
			return err
		}
		closeCur()
		cur, curOwned = next, true
	}
	closeCur()
	return nil
}

// containingRoot returns the first root that contains abs, the slash-relative
// path from that root to abs (".": abs is the root itself), and ok.
//
// It first tries a direct lexical match — the common case where a root and abs
// share a spelling — which needs no syscalls. When every root misses lexically it
// retries tolerating an ANCESTOR-spelling difference: the resolver canonicalizes a
// granted root to its real path (e.g. macOS /var → /private/var, or a symlinked
// project/home parent) while the env still spells the target through the symlinked
// ancestor, so `/private/var/…/wt` and `/var/…/wt/file` name the same worktree yet
// mismatch textually. relUnderRealAncestor resolves that mismatch by the roots'
// real paths while keeping the in-root tail LITERAL, so a symlinked ancestor of the
// root is tolerated but a symlink COMPONENT inside the root stays in the tail for
// the fd walk to refuse. A genuinely out-of-root path matches no root's real
// ancestor and stays denied (containment is not weakened).
func containingRoot(roots []string, abs string) (root, rel string, ok bool) {
	abs = filepath.Clean(abs)
	for _, r := range roots {
		if abs == r {
			return r, ".", true
		}
		if pathUnder(abs, r) {
			rl, err := securePathRel(r, abs)
			if err != nil {
				continue
			}
			return r, filepath.ToSlash(rl), true
		}
	}
	for _, r := range roots {
		if rel, ok := relUnderRealAncestor(r, abs); ok {
			return r, rel, true
		}
	}
	return "", "", false
}

// relUnderRealAncestor reports whether abs lies under root when only their ANCESTOR
// spelling differs (a symlinked ancestor of the root), returning the slash-relative
// tail from root to abs. It resolves root to its real path, then walks up abs's
// ancestors for the SHALLOWEST one whose real path equals it — shallowest so the
// tail keeps the most components literal, so an in-root symlink that resolves back
// under the root is left in the tail (the fd walk refuses it) rather than silently
// collapsed. The tail is built by lexical string splitting of abs, never from a
// symlink-resolved path, so no in-root component is ever followed here. A root that
// cannot be resolved (does not exist) contains nothing by real path.
func relUnderRealAncestor(root, abs string) (rel string, ok bool) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	cur := abs
	var tail []string
	found := false
	rel = ""
	for {
		if curReal, rerr := filepath.EvalSymlinks(cur); rerr == nil && curReal == realRoot {
			found = true
			if len(tail) == 0 {
				rel = "."
			} else {
				rel = strings.Join(tail, "/")
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	return rel, found
}

// splitLeaf splits a slash-relative path into its parent directory and leaf. The
// root itself (".") yields an empty leaf, which callers treat as "no leaf".
func splitLeaf(rel string) (dir, leaf string) {
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return "", ""
	}
	d, l := path.Split(rel)
	return strings.TrimSuffix(d, "/"), l
}

// dirOrDot returns "." for an empty directory (the root itself), else dir.
func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// relComponents splits a cleaned slash-relative path into components, returning
// nil for "" or ".".
func relComponents(rel string) []string {
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}

// pathUnder reports whether p is strictly beneath (or equal to) dir. It mirrors
// sandbox.pathUnder, kept local so execenv does not reach into an unexported
// helper of another package.
func pathUnder(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

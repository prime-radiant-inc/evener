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
// itself cannot redirect resolution. All methods are safe for concurrent use.
type sandboxFS struct {
	policy *sandbox.ResolvedPolicy

	mu      sync.Mutex
	rootFds map[string]int // canonical root path → cached O_DIRECTORY fd
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
	return &sandbox.DeniedError{Mode: s.policy.Mode, Tool: tool, Path: denyPath, Reason: reason}
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
// an fd (caller closes) or a typed denial. Two shapes:
//   - ReadWorktreeOnly (restricted): abs must resolve beneath one of ReadRoots via
//     openBeneathRoot (RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS on Linux).
//   - ReadAnywhere (read-only/workspace-write): abs is refused if under a masked
//     path, otherwise opened with RESOLVE_NO_SYMLINKS so no symlink is traversed.
func (s *sandboxFS) openRead(tool, abs string, flags int) (int, error) {
	abs = filepath.Clean(abs)
	if s.policy.FileTool.Read == sandbox.ReadWorktreeOnly {
		root, rel, ok := containingRoot(s.policy.FileTool.ReadRoots, abs)
		if !ok {
			return -1, s.deny(tool, abs, denyReasonOutsideRead)
		}
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

	// ReadAnywhere.
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

// recheckMaskedFd re-runs the masked-path denylist against the kernel's canonical
// path for an already-open fd. On a case- or normalization-insensitive filesystem
// the pre-open textual check is not authoritative (the kernel resolved ".SSH" to
// ".ssh"); re-checking the fd's true path — reported with real casing — closes
// that bypass while staying TOCTOU-safe (the fd is pinned to the opened inode).
// Where canonicalization is unavailable it fails closed only on platforms that
// require it (darwin); on Linux the textual pre-check already held.
func (s *sandboxFS) recheckMaskedFd(tool, orig string, fd int) error {
	canon, err := canonicalPathOfFd(fd)
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
	canon, err := canonicalPathOfFd(parentFd)
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

// readFile reads the whole file at abs through a race-safe fd.
func (s *sandboxFS) readFile(tool, abs string) ([]byte, error) {
	fd, err := s.openRead(tool, abs, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), abs) // takes ownership of fd; f.Close closes it
	defer f.Close()
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
	defer unix.Close(parentFd)
	return atomicWriteAt(parentFd, leaf, data, perm)
}

// remove deletes abs if it is beneath a writable root. A path outside the
// writable set (or under a masked/protected surface) is a typed denial; an
// in-root unlink that fails because the target is absent is not an error
// (matching apply_patch's best-effort delete).
func (s *sandboxFS) remove(tool, abs string) error {
	parentFd, leaf, err := s.openWriteParent(tool, abs, false)
	if err != nil {
		return err
	}
	defer unix.Close(parentFd)
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
	defer unix.Close(oldParent)
	newParent, newLeaf, err := s.openWriteParent(tool, newAbs, true)
	if err != nil {
		return err
	}
	defer unix.Close(newParent)
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
	// Resolve the parent for reading, then Fstatat the leaf without following a
	// final symlink. A symlinked leaf reports false (sandbox refuses symlinks).
	parent := filepath.Dir(abs)
	leaf := filepath.Base(abs)
	fd, err := s.openRead(tool, parent, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
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
	defer unix.Close(dirFd)
	ents, err := readDirEntries(dirFd)
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
			if info, ierr := ent.Info(); ierr == nil {
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
			childFd, cerr := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
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
	if _, err := rand.Read(b[:]); err != nil {
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
	fd, err := unix.Openat(dirFd, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return err
	}
	if werr := writeAllFd(fd, data); werr != nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return werr
	}
	if cerr := unix.Close(fd); cerr != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return cerr
	}
	if rerr := unix.Renameat(dirFd, tmp, dirFd, leaf); rerr != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return rerr
	}
	return nil
}

// writeAllFd writes all of data to fd, retrying short writes and EINTR.
func writeAllFd(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
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
	defer f.Close()
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
func containingRoot(roots []string, abs string) (root, rel string, ok bool) {
	for _, r := range roots {
		if abs == r {
			return r, ".", true
		}
		if pathUnder(abs, r) {
			rl, err := filepath.Rel(r, abs)
			if err != nil {
				continue
			}
			return r, filepath.ToSlash(rl), true
		}
	}
	return "", "", false
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

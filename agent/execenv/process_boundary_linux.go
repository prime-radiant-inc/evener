//go:build linux

package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// cgroup2Mount is the conventional cgroup v2 mount. A process's own delegated
// subtree is discovered from /proc/self/cgroup relative to it: the boundary is
// created inside whatever the process is authorized to write, never at the
// mount root.
const cgroup2Mount = "/sys/fs/cgroup"

// boundaryDirPrefix is the controller-created boundary directory's name prefix.
// It is the provenance a reopened path must carry: only a directory this
// package created is a boundary whose members may be enumerated, signaled or
// removed.
const boundaryDirPrefix = "evener-boundary-"

// createBoundary pre-creates the Linux cgroup boundary.
//
// With an explicit root the root must be a cgroup2 directory and creation
// happens only under it. With an empty root the process's own cgroup subtree is
// used, walking up to the nearest writable ancestor: systemd hands a user
// session the delegated subtree, and the process's own scope is frequently
// root-owned, so the walk finds the delegation the session actually holds. Every
// candidate is verified to be a cgroup2 mount first, so a fallback can never
// land the boundary on an ordinary directory whose membership no kernel
// enforces.
func createBoundary(root string) (*Boundary, error) {
	if root != "" {
		if err := requireCgroup2(root); err != nil {
			return nil, err
		}
		return createLinuxCgroup(root)
	}
	own, err := ownCgroupDir()
	if err != nil {
		return nil, err
	}
	var lastErr error
	for dir := own; ; dir = filepath.Dir(dir) {
		if err := requireCgroup2(dir); err != nil {
			lastErr = err
		} else if boundary, err := createLinuxCgroup(dir); err == nil {
			return boundary, nil
		} else {
			lastErr = err
		}
		if dir == cgroup2Mount || dir == "/" || dir == "." {
			break
		}
	}
	return nil, fmt.Errorf("%w: no writable cgroup2 subtree below %s: %w", ErrBoundaryUnavailable, cgroup2Mount, lastErr)
}

// createLinuxCgroup creates one fresh child cgroup under parent. The directory
// starts empty — a boundary is only ever populated by its own spawns — and is
// removed again if it cannot be opened.
func createLinuxCgroup(parent string) (*Boundary, error) {
	dir, err := os.MkdirTemp(parent, boundaryDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: create a cgroup under %s: %w", ErrBoundaryUnavailable, parent, err)
	}
	boundary, err := newLinuxCgroupBoundary(dir)
	if err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	return boundary, nil
}

// requireCgroup2 refuses a path that is not a cgroup2 directory. The boundary's
// whole guarantee is kernel-enforced membership, and only a cgroup2 mount has
// it.
func requireCgroup2(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return fmt.Errorf("%w: stat %s: %w", ErrBoundaryUnavailable, path, err)
	}
	if stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("%w: %s is not a cgroup2 directory", ErrBoundaryUnavailable, path)
	}
	return nil
}

// ownCgroupDir resolves this process's own cgroup v2 directory from
// /proc/self/cgroup's `0::` line.
func ownCgroupDir() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("%w: read this process's cgroup membership: %w", ErrBoundaryUnavailable, err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && path != "" {
			return filepath.Join(cgroup2Mount, filepath.Clean(path)), nil
		}
	}
	return "", fmt.Errorf("%w: this process has no cgroup v2 membership", ErrBoundaryUnavailable)
}

// openBoundary reopens a persisted Linux boundary for the boot reap. It proves
// the path lies inside a reachable cgroup2 hierarchy first, and only then reads
// the child's absence: a path under a mount this process cannot reach (a
// vanished mount, a namespace, a persisted path that never named a cgroup) is
// unverifiable and fails closed, while a child missing from an otherwise
// verifiable cgroup2 hierarchy is genuinely gone — the kernel removes only
// empty cgroups, so it holds no process.
func openBoundary(id BoundaryIdentity) (*Boundary, error) {
	if id.Platform != BoundaryPlatformLinux {
		return nil, fmt.Errorf("%w: platform %q is not the linux arm", ErrBoundaryUnavailable, id.Platform)
	}
	// A persisted path is data, not authority: it must be the clean, absolute
	// name of a directory this controller created before anything is
	// enumerated, signaled or removed through it. A `..` escape, a relative
	// path, or a name outside the boundary prefix is unverifiable, never a
	// boundary this pass may act on.
	if id.CgroupID != filepath.Clean(id.CgroupID) || !filepath.IsAbs(id.CgroupID) {
		return nil, fmt.Errorf("%w: %s is not a clean absolute boundary path", ErrBoundaryUnavailable, id.CgroupID)
	}
	if !strings.HasPrefix(filepath.Base(id.CgroupID), boundaryDirPrefix) {
		return nil, fmt.Errorf("%w: %s does not carry the controller's boundary name", ErrBoundaryUnavailable, id.CgroupID)
	}
	if err := verifyCgroup2Parent(filepath.Dir(id.CgroupID)); err != nil {
		return nil, err
	}
	return newLinuxCgroupBoundary(id.CgroupID)
}

// verifyCgroup2Parent requires the boundary's immediate parent to exist and be
// a cgroup2 directory. The pre-created boundary is always a direct child of a
// verifiable cgroup2 parent, so a missing parent means the path cannot be a
// boundary this controller created, and the absence of the child under it
// proves nothing: that is fail-closed (ErrBoundaryUnavailable), never a
// vanished boundary.
//
// Residual: a persisted path that never named a cgroup, but whose immediate
// parent is a reachable cgroup2 directory on this mount, is still
// indistinguishable from a genuinely removed boundary without a persisted
// hierarchy token (the mount's fsid/root). §9's BoundaryEntry carries no such
// token, so closing that case is a follow-up that would extend the schema.
func verifyCgroup2Parent(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: the parent %s of the persisted boundary does not exist", ErrBoundaryUnavailable, dir)
	}
	if err != nil {
		return fmt.Errorf("%w: stat %s: %w", ErrBoundaryUnavailable, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: the parent %s of the persisted boundary is not a directory", ErrBoundaryUnavailable, dir)
	}
	return requireCgroup2(dir)
}

// newLinuxCgroupBoundary binds the operations to one cgroup directory. It
// deliberately does not re-verify the mount: its two callers do (createBoundary
// for a fresh child, openBoundary for a persisted path), and the tests use it
// with a plain temporary directory whose membership file stands in for the
// kernel's.
func newLinuxCgroupBoundary(dir string) (*Boundary, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		// The kernel refuses to remove a populated cgroup, so a vanished
		// boundary holds no process: already clean, never unverifiable.
		return nil, fmt.Errorf("%w: %s", ErrBoundaryGone, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: stat boundary %s: %w", ErrBoundaryUnavailable, dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: boundary %s is not a directory", ErrBoundaryUnavailable, dir)
	}
	return &Boundary{
		id: BoundaryIdentity{Platform: BoundaryPlatformLinux, CgroupID: dir},
		ops: boundaryOps{
			spawnAttr: func() (*syscall.SysProcAttr, func(), error) { return linuxCgroupSpawnAttr(dir) },
			members:   func() ([]BoundaryMember, error) { return linuxCgroupMembers(dir) },
			observe:   observeLinuxStartToken,
			signal:    signalLinuxMember,
			release:   func() error { return linuxCgroupRelease(dir) },
			// cgroup membership is kernel-enforced: a process is a member from
			// clone time and cannot leave it, so an empty cgroup is proof.
			enforces: true,
		},
	}, nil
}

// linuxCgroupSpawnAttr opens this spawn's handle on the cgroup and returns the
// attributes that place the child inside it at clone time
// (CLONE_INTO_CGROUP): the child is a member before its first instruction, so
// it can neither run outside the boundary nor leave it. The attributes also
// carry §3's baseline — its own process group, and parent-death cleanup — and
// the release closes the cgroup handle once Start has returned.
//
// Pdeathsig is delivered on the spawning OS thread's termination, not
// necessarily the process's (Go's own documentation of the field; see
// SpawnAttr's note): the caller must hold the calling thread with
// runtime.LockOSThread for the child's lifetime.
func linuxCgroupSpawnAttr(dir string) (*syscall.SysProcAttr, func(), error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open cgroup %s: %w", ErrBoundaryUnavailable, dir, err)
	}
	attr := &syscall.SysProcAttr{
		Setpgid:     true,
		Pdeathsig:   syscall.SIGKILL,
		UseCgroupFD: true,
		CgroupFD:    fd,
	}
	return attr, func() { _ = unix.Close(fd) }, nil
}

// linuxCgroupMembers enumerates cgroup.procs with each member's kernel start
// token. A member that exits between the membership read and the token read is
// omitted: it is gone, and gone reads as already clean. A membership file that
// cannot be read is ErrBoundaryUnavailable — an enumeration that cannot prove
// anything fails closed, never reads as empty.
func linuxCgroupMembers(dir string) ([]BoundaryMember, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil, fmt.Errorf("%w: read the membership of %s: %w", ErrBoundaryUnavailable, dir, err)
	}
	var members []BoundaryMember
	for field := range strings.FieldsSeq(string(raw)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			// A membership row no kernel writes: the enumeration cannot prove
			// what it read, so it fails closed rather than silently narrowing the
			// candidate set.
			return nil, fmt.Errorf("%w: the membership of %s carries an unparseable pid %q", ErrBoundaryUnavailable, dir, field)
		}
		token, err := observeLinuxStartToken(pid)
		if errors.Is(err, ErrBoundaryMemberGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		members = append(members, BoundaryMember{PID: pid, StartToken: token})
	}
	slices.SortFunc(members, func(a, b BoundaryMember) int { return a.PID - b.PID })
	if len(members) == 0 {
		// cgroup v2 allows processes in descendant cgroups, so an empty direct
		// cgroup.procs is not proof the boundary subtree is empty. cgroup.events'
		// `populated` covers the subtree; when it says populated while direct
		// membership is empty, the enumeration cannot claim clean.
		populated, err := linuxCgroupSubtreePopulated(dir)
		if err != nil {
			return nil, err
		}
		if populated {
			return nil, fmt.Errorf("%w: the boundary subtree of %s is populated but its direct membership is empty", ErrBoundaryUnavailable, dir)
		}
	}
	return members, nil
}

// linuxCgroupSubtreePopulated reads cgroup.events' `populated` flag, which the
// kernel sets while any process is in the cgroup or its descendants. A boundary
// without the file (a test directory standing in for a cgroup) reports false;
// a file that cannot be read is fail-closed.
func linuxCgroupSubtreePopulated(dir string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: read %s/cgroup.events: %w", ErrBoundaryUnavailable, dir, err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if field, value, ok := strings.Cut(line, " "); ok && field == "populated" {
			return strings.TrimSpace(value) == "1", nil
		}
	}
	return false, nil
}

// observeLinuxStartToken reads a process's kernel-owned start token:
// /proc/<pid>/stat field 22, in clock ticks since boot (fields[19] after the
// parenthesized comm field). The raw tick value is the identity — it is
// kernel-written, not derivable from the pid, and a recycled pid never carries
// its predecessor's value. A process that has exited — including one the parent
// has not yet reaped — reports ErrBoundaryMemberGone: it can no longer run, so
// it is already clean.
func observeLinuxStartToken(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
		}
		return "", fmt.Errorf("execenv: read process %d stat: %w", pid, err)
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return "", fmt.Errorf("execenv: process %d stat is malformed", pid)
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("execenv: process %d stat is incomplete", pid)
	}
	if fields[0] == "Z" {
		return "", fmt.Errorf("%w: pid %d is a zombie", ErrBoundaryMemberGone, pid)
	}
	token := fields[19]
	if _, err := strconv.ParseUint(token, 10, 64); err != nil {
		return "", fmt.Errorf("execenv: process %d start token %q: %w", pid, token, err)
	}
	return token, nil
}

// signalLinuxMember terminates one already-verified member through an
// identity-stable kernel handle, closing the check-then-kill race §3's rule
// would otherwise leave open: pidfd_open pins the pid so the kernel cannot
// recycle the number while the handle is open, the start token is re-read
// through the pinned handle, and pidfd_send_signal delivers to exactly that
// process. A pid whose token differs at the second read is a reused id and is
// refused; a pid that no longer exists is gone. A kernel without pidfd fails
// closed rather than falling back to kill(2), where the race is unclosable.
func signalLinuxMember(pid int, startToken string) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
		}
		if errors.Is(err, unix.ENOSYS) {
			return fmt.Errorf("%w: pidfd_open is unavailable on this kernel, refusing a non-atomic signal: %w", ErrBoundaryUnavailable, err)
		}
		return fmt.Errorf("execenv: open pidfd for %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(fd) }()
	current, err := observeLinuxStartToken(pid)
	if err != nil {
		return err
	}
	if current != startToken {
		return fmt.Errorf("%w: pid %d carried token %s, expected %s", ErrBoundaryIdentityChanged, pid, current, startToken)
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
		}
		return fmt.Errorf("execenv: signal boundary member %d: %w", pid, err)
	}
	return nil
}

// linuxCgroupRelease removes the boundary directory. The kernel refuses to
// remove a populated cgroup, so a failure here is a boundary that is not proven
// dead and is reported, never swallowed.
func linuxCgroupRelease(dir string) error {
	if !strings.HasPrefix(filepath.Base(filepath.Clean(dir)), boundaryDirPrefix) {
		// Never remove a directory this package did not create, whatever a
		// hand-edited store claims.
		return fmt.Errorf("%w: %s is not a controller-created boundary", ErrBoundaryUnavailable, dir)
	}
	if err := os.Remove(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("execenv: remove boundary %s: %w", dir, err)
	}
	return nil
}

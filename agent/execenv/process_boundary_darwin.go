//go:build darwin

package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// darwinEnumerationTimeout bounds the process-table read the Darwin arm makes.
const darwinEnumerationTimeout = 5 * time.Second

// createBoundary returns the Darwin boundary: the (process group id, session
// id) pair the worker's setsid-detached launcher holds (§3). There is no Darwin
// cgroup or job-object primitive, so the launcher itself must have called
// setsid — the pair is its own session, and every spawn it makes with the
// attributes SpawnAttr returns inherits that pair. A launcher that is not a
// session leader has no boundary to offer and fails closed with
// ErrBoundaryUnavailable.
//
// root is a Linux cgroup concept; Darwin ignores it.
func createBoundary(root string) (*Boundary, error) {
	_ = root
	pid := os.Getpid()
	sessionID, err := unix.Getsid(pid)
	if err != nil {
		return nil, fmt.Errorf("%w: read this launcher's session: %v", ErrBoundaryUnavailable, err)
	}
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		return nil, fmt.Errorf("%w: read this launcher's process group: %v", ErrBoundaryUnavailable, err)
	}
	if sessionID != pid || pgid != pid {
		return nil, fmt.Errorf("%w: the launcher must hold its own session (setsid) before a boundary exists", ErrBoundaryUnavailable)
	}
	return newDarwinBoundary(pgid, sessionID), nil
}

// openBoundary reopens a persisted Darwin boundary for the boot reap: the pair
// is enumerable from the process table, so no directory or handle survives the
// crash.
func openBoundary(id BoundaryIdentity) (*Boundary, error) {
	if id.Platform != BoundaryPlatformDarwin {
		return nil, fmt.Errorf("%w: platform %q is not the darwin arm", ErrBoundaryUnavailable, id.Platform)
	}
	return newDarwinBoundary(id.PGID, id.SessionID), nil
}

// newDarwinBoundary binds the operations to one (pgid, session id) pair.
func newDarwinBoundary(pgid, sessionID int) *Boundary {
	return &Boundary{
		id: BoundaryIdentity{Platform: BoundaryPlatformDarwin, PGID: pgid, SessionID: sessionID},
		ops: boundaryOps{
			spawnAttr: func() (*syscall.SysProcAttr, func(), error) {
				// The child must inherit the launcher's (pgid, session id), so the
				// attributes carry no Setsid and no Setpgid of their own.
				return &syscall.SysProcAttr{}, func() {}, nil
			},
			members: func() ([]BoundaryMember, error) { return darwinBoundaryMembers(pgid, sessionID) },
			observe: darwinStartToken,
			signal:  signalDarwinMember,
			// A session boundary has no handle to release: the pair is the
			// launcher's own, and it goes away with the launcher.
			release: func() error { return nil },
			// Not enforcing: a descendant that calls setsid leaves the pair, so
			// an empty enumeration is not proof and the reap must never clear an
			// intent on it (§3's Darwin arm is asymmetric for exactly this).
			enforces: false,
		},
	}
}

// darwinBoundaryMembers enumerates the process table and keeps every process
// whose (process group id, session id) pair equals the boundary's. BSD `ps`
// reports both columns; the kernel start token for each candidate comes from
// the process's kinfo_proc, so a member that exits mid-enumeration is omitted
// (gone reads as already clean). The boundary owner itself — this launcher,
// whose own pid equals the pair — is excluded: counting it would make a live
// launcher's boundary never enumerate empty. A row whose columns do not parse
// fails closed: `ps` succeeded but its output no longer carries the numbers
// this verification needs, and guessing around it would be a boundary read as
// clean for the wrong reason.
func darwinBoundaryMembers(pgid, sessionID int) ([]BoundaryMember, error) {
	// The process-table read is bounded: a hung `ps` must not stall the boot
	// reap, and an expired read is fail-closed (never an empty boundary).
	ctx, cancel := context.WithTimeout(context.Background(), darwinEnumerationTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=,sess=").Output()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate the process table: %v", ErrBoundaryUnavailable, err)
	}
	var members []BoundaryMember
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return nil, fmt.Errorf("%w: unparseable process row %q", ErrBoundaryUnavailable, line)
		}
		if fields[0] == strconv.Itoa(os.Getpid()) {
			// The boundary owner (this launcher): not a member to reap.
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		group, errGroup := strconv.Atoi(fields[1])
		session, errSession := strconv.Atoi(fields[2])
		if errPID != nil || errGroup != nil || errSession != nil {
			return nil, fmt.Errorf("%w: process row %q does not carry numeric columns", ErrBoundaryUnavailable, line)
		}
		if group != pgid || session != sessionID {
			continue
		}
		token, err := darwinStartToken(pid)
		if errors.Is(err, ErrBoundaryMemberGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		members = append(members, BoundaryMember{PID: pid, StartToken: token})
	}
	slices.SortFunc(members, func(a, b BoundaryMember) int { return a.PID - b.PID })
	return members, nil
}

// darwinStartToken reads a process's kernel-owned start time from its
// kinfo_proc (kern.proc.pid), formatted as seconds.microseconds since the
// epoch. The value is the kernel's, not derivable from the pid, and a recycled
// pid never carries its predecessor's value.
func darwinStartToken(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
			return "", fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
		}
		return "", fmt.Errorf("execenv: read process %d info: %v", pid, err)
	}
	if info == nil {
		return "", fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
	}
	// A process that has exited reports no start time until the kernel reaps the
	// row: no live instance, already clean.
	started := info.Proc.P_starttime
	if started.Sec == 0 && started.Usec == 0 {
		return "", fmt.Errorf("%w: pid %d has no start time", ErrBoundaryMemberGone, pid)
	}
	return fmt.Sprintf("%d.%06d", started.Sec, started.Usec), nil
}

// ObserveProcess reads one process's kernel-owned start time without a boundary
// handle, so a recorded instance can be proved gone when its boundary no longer
// exists.
func ObserveProcess(pid int) (string, error) {
	return darwinStartToken(pid)
}

// signalDarwinMember refuses to signal: Darwin offers no identity-stable handle
// that pins a process across the start-token check and the signal (checked
// golang.org/x/sys/unix, which has no pidfd equivalent on darwin; the
// pidfd_open/pidfd_send_signal pair is Linux-only). A plain kill(2) would be
// check-then-kill, and §3 forbids signaling an instance that cannot be proven,
// so the seam fails closed: the reap marks the record `orphan-unverified` and
// keeps the intent for a platform arm that can verify atomically.
func signalDarwinMember(pid int, startToken string) error {
	_ = startToken
	return fmt.Errorf("%w: no identity-stable handle exists on darwin to signal pid %d atomically", ErrBoundaryUnavailable, pid)
}

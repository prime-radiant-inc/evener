//go:build linux

package execenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startSleeper spawns a real localhost process the boundary tests use as the
// crash orphan's stand-in: the tests write its pid into the boundary the way the
// kernel writes cgroup membership, so the signal path under test is the real
// one.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// fakeBoundary builds a boundary over a temp directory: the directory stands in
// for the kernel-created cgroup, and the test writes the cgroup.procs contents
// the kernel would have written. CreateBoundary itself only ever returns a
// boundary over a real cgroup2 directory (see
// TestBoundaryCreateRefusesANonCgroupRoot); this seam is the in-package one the
// enumeration and signaling tests drive.
func fakeBoundary(t *testing.T) *Boundary {
	t.Helper()
	dir := t.TempDir()
	boundary, err := newLinuxCgroupBoundary(dir)
	if err != nil {
		t.Fatalf("open fake boundary: %v", err)
	}
	return boundary
}

// setMembers writes the boundary's membership file the kernel would expose.
func setMembers(t *testing.T, boundary *Boundary, pids ...int) {
	t.Helper()
	var content strings.Builder
	for _, pid := range pids {
		fmt.Fprintf(&content, "%d\n", pid)
	}
	if err := os.WriteFile(filepath.Join(boundary.Identity().CgroupID, "cgroup.procs"), []byte(content.String()), 0o644); err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}
}

// TestBoundaryCreateRefusesANonCgroupRoot pins the fail-closed half of the
// creation contract: a root that is not a cgroup2 mount can never become an
// ownership boundary, so CreateBoundary refuses it instead of returning a
// handle whose membership no kernel enforces.
func TestBoundaryCreateRefusesANonCgroupRoot(t *testing.T) {
	_, err := CreateBoundary(t.TempDir())
	if !errors.Is(err, ErrBoundaryUnavailable) {
		t.Fatalf("CreateBoundary(temp dir) = %v, want ErrBoundaryUnavailable", err)
	}
}

// TestBoundaryCgroupSpawnMembershipAndReap is the real-cgroup end-to-end arm:
// the pre-created boundary takes the spawned child as a kernel member, the
// kernel start token observes it, and signaling the verified member empties the
// boundary within the bound. It skips on hosts with no writable cgroup2
// subtree, which is exactly the "where available" the spec names.
func TestBoundaryCgroupSpawnMembershipAndReap(t *testing.T) {
	boundary, err := CreateBoundary("")
	if errors.Is(err, ErrBoundaryUnavailable) {
		t.Skipf("no writable cgroup2 subtree: %v", err)
	}
	if err != nil {
		t.Fatalf("CreateBoundary: %v", err)
	}
	t.Cleanup(func() { _ = boundary.Close() })
	if boundary.Identity().Platform != BoundaryPlatformLinux {
		t.Fatalf("platform = %q, want %q", boundary.Identity().Platform, BoundaryPlatformLinux)
	}
	if boundary.Identity().CgroupID == "" {
		t.Fatal("the created boundary carries no cgroup id")
	}
	attr, release, err := boundary.SpawnAttr()
	if err != nil {
		t.Fatalf("SpawnAttr: %v", err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		release()
		t.Fatalf("start into the boundary: %v", err)
	}
	release()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	token, err := boundary.Observe(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if token == "" || strings.HasSuffix(token, "x") {
		t.Fatalf("observed start token %q is not the kernel's", token)
	}
	members, err := boundary.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 || members[0].PID != cmd.Process.Pid || members[0].StartToken != token {
		t.Fatalf("members = %+v, want the spawned child with token %q", members, token)
	}
	if err := boundary.SignalVerified(cmd.Process.Pid, token); err != nil {
		t.Fatalf("SignalVerified: %v", err)
	}
	if err := boundary.Await(10*time.Second, func(current []BoundaryMember) bool { return len(current) == 0 }); err != nil {
		t.Fatalf("Await clean: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		// The child was killed by the boundary signal; a non-nil wait status is
		// expected, only a still-running child is a failure.
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			t.Fatalf("wait: %v", err)
		}
	}
	if err := boundary.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(boundary.Identity().CgroupID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the boundary directory survived Close: %v", err)
	}
}

// TestBoundaryMarkerlessEnumerationIsEmptyBeforeSpawn pins §3's pre-spawn crash
// case: a boundary created but never populated enumerates empty, and empty is
// already clean — no member, nothing to signal.
func TestBoundaryMarkerlessEnumerationIsEmptyBeforeSpawn(t *testing.T) {
	boundary := fakeBoundary(t)
	setMembers(t, boundary)
	members, err := boundary.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("members = %+v, want an empty pre-spawn boundary", members)
	}
}

// TestBoundaryRefusesAMismatchedStartToken pins the reused-pid rule: a member
// whose pid matches but whose kernel start token differs is a different process,
// so the boundary refuses to signal it and the process stays alive.
func TestBoundaryRefusesAMismatchedStartToken(t *testing.T) {
	boundary := fakeBoundary(t)
	cmd := startSleeper(t)
	setMembers(t, boundary, cmd.Process.Pid)

	if err := boundary.SignalVerified(cmd.Process.Pid, "not-the-kernel-token"); !errors.Is(err, ErrBoundaryIdentityChanged) {
		t.Fatalf("SignalVerified(mismatched token) = %v, want ErrBoundaryIdentityChanged", err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the mismatched member was signaled: %v", err)
	}
}

// TestBoundarySignalsAVerifiedMember pins the positive arm: a member whose
// kernel start token still matches the persisted pair is signaled, and the
// bounded await proves the boundary settled.
func TestBoundarySignalsAVerifiedMember(t *testing.T) {
	boundary := fakeBoundary(t)
	cmd := startSleeper(t)
	setMembers(t, boundary, cmd.Process.Pid)
	token, err := boundary.Observe(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := boundary.SignalVerified(cmd.Process.Pid, token); err != nil {
		t.Fatalf("SignalVerified: %v", err)
	}
	if err := boundary.Await(10*time.Second, func(current []BoundaryMember) bool { return len(current) == 0 }); err != nil {
		t.Fatalf("Await: %v", err)
	}
}

// TestBoundaryAwaitIsBounded pins the deadline half of the dead proof: a member
// that never leaves the boundary must surface as unsettled within the wait,
// never as a clean boundary.
func TestBoundaryAwaitIsBounded(t *testing.T) {
	boundary := fakeBoundary(t)
	cmd := startSleeper(t)
	setMembers(t, boundary, cmd.Process.Pid)
	if err := boundary.Await(50*time.Millisecond, func(current []BoundaryMember) bool { return len(current) == 0 }); !errors.Is(err, ErrBoundaryNotSettled) {
		t.Fatalf("Await(live member) = %v, want ErrBoundaryNotSettled", err)
	}
}

// TestBoundaryObserveReportsAGoneMember pins the gone arm: a pid with no
// process behind it is ErrBoundaryMemberGone, which the reap reads as already
// clean.
func TestBoundaryObserveReportsAGoneMember(t *testing.T) {
	boundary := fakeBoundary(t)
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if _, err := boundary.Observe(pid); !errors.Is(err, ErrBoundaryMemberGone) {
		t.Fatalf("Observe(dead pid) = %v, want ErrBoundaryMemberGone", err)
	}
}

// TestBoundaryOpenReportsAGoneBoundary pins the vanished-boundary arm: a child
// missing from an otherwise verifiable cgroup2 hierarchy is gone — the kernel
// only removes an empty cgroup — and the reap may read it as already clean.
func TestBoundaryOpenReportsAGoneBoundary(t *testing.T) {
	if err := requireCgroup2(cgroup2Mount); err != nil {
		t.Skipf("no cgroup2 mount: %v", err)
	}
	// A direct child of the verified mount: the parent exists and verifies, so
	// the missing child is a genuinely removed boundary.
	dir := filepath.Join(cgroup2Mount, fmt.Sprintf("evener-missing-%d", os.Getpid()))
	_, err := OpenBoundary(BoundaryIdentity{Platform: BoundaryPlatformLinux, CgroupID: dir})
	if !errors.Is(err, ErrBoundaryGone) {
		t.Fatalf("OpenBoundary(missing dir) = %v, want ErrBoundaryGone", err)
	}
}

// TestBoundaryOpenRefusesAMissingParent pins the stronger reading: a child whose
// immediate parent does not exist cannot be a boundary this controller created,
// so its absence proves nothing and the open fails closed.
func TestBoundaryOpenRefusesAMissingParent(t *testing.T) {
	if err := requireCgroup2(cgroup2Mount); err != nil {
		t.Skipf("no cgroup2 mount: %v", err)
	}
	dir := filepath.Join(cgroup2Mount, fmt.Sprintf("evener-missing-%d", os.Getpid()), "child")
	if _, err := OpenBoundary(BoundaryIdentity{Platform: BoundaryPlatformLinux, CgroupID: dir}); !errors.Is(err, ErrBoundaryUnavailable) {
		t.Fatalf("OpenBoundary(child of a missing parent) = %v, want ErrBoundaryUnavailable", err)
	}
}

// TestBoundaryOpenRefusesAnUnverifiablePath pins the fail-closed half: a
// persisted path outside any reachable cgroup2 hierarchy (a vanished mount, a
// namespace, a path that never named a cgroup) must never read as a clean,
// vanished boundary.
func TestBoundaryOpenRefusesAnUnverifiablePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "child")
	if _, err := OpenBoundary(BoundaryIdentity{Platform: BoundaryPlatformLinux, CgroupID: dir}); !errors.Is(err, ErrBoundaryUnavailable) {
		t.Fatalf("OpenBoundary(outside a cgroup2 hierarchy) = %v, want ErrBoundaryUnavailable", err)
	}
}

// TestBoundaryMembersFailClosedOnAnUnparseablePid pins the enumeration rule: a
// membership row that is not a pid cannot be silently skipped, because that
// would narrow the candidate set behind the caller's back.
func TestBoundaryMembersFailClosedOnAnUnparseablePid(t *testing.T) {
	boundary := fakeBoundary(t)
	if err := os.WriteFile(filepath.Join(boundary.Identity().CgroupID, "cgroup.procs"), []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}
	if _, err := boundary.Members(); !errors.Is(err, ErrBoundaryUnavailable) {
		t.Fatalf("Members(unparseable row) = %v, want ErrBoundaryUnavailable", err)
	}
}

// TestBoundaryObserveReadsKernelStartToken pins that the observed token is the
// kernel's own raw start tick from /proc/<pid>/stat field 22: a value this
// process can cross-check without trusting the boundary machinery.
func TestBoundaryObserveReadsKernelStartToken(t *testing.T) {
	boundary := fakeBoundary(t)
	cmd := startSleeper(t)
	token, err := boundary.Observe(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
	if err != nil {
		t.Fatalf("read stat: %v", err)
	}
	end := strings.LastIndexByte(string(raw), ')')
	fields := strings.Fields(string(raw[end+1:]))
	want := fields[19]
	if token != want {
		t.Fatalf("observed token = %q, want the kernel start tick %q", token, want)
	}
	if _, err := strconv.ParseUint(token, 10, 64); err != nil {
		t.Fatalf("observed token %q is not a start tick: %v", token, err)
	}
}

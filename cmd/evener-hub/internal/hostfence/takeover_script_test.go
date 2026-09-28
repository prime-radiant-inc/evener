package hostfence

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for S18's fencing-worker contract at the scripted-remote seam: the
// remote kill path (`kill`), the guard high-water's uint64 bound (S17's
// recorded Low), the takeover rule that supersedes a crashed incarnation's
// pending fence, and the nonce-ownership recheck the bounded wait reads. No
// host and no ssh: the local helper is the remote.

// crashPerformLeavingCommand starts a perform whose command outlives the
// wrapper, then kills the wrapper: the crashed incarnation's orphan, tracked by
// a running lease entry that still reads live.
func crashPerformLeavingCommand(t *testing.T, remote *fenceRemote, epoch Epoch, extraEnv []string, command string) LeaseEntry {
	t.Helper()
	cmd, err := remote.start(extraEnv, "perform", epoch.BootID, strconv.FormatUint(epoch.OpSeq, 10), command)
	if err != nil {
		t.Fatalf("start perform: %v", err)
	}
	entry := waitForRunningEntry(t, remote)
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		_ = cmd.Wait()
	}()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the wrapper: %v", err)
	}
	<-reaped
	if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
		t.Fatalf("recheck(orphaned entry) = %+v, want live (fail closed)", recheck)
	}
	return entry
}

// writeLeaseEntry writes one lease entry file by hand, in the exact field set
// the wrapper's writer emits, so a test can place an identity the wrapper never
// observed.
func writeLeaseEntry(t *testing.T, remote *fenceRemote, id, state, kind, pid, start, nonce, cgroup string) {
	t.Helper()
	body := "id\t" + id + "\n" +
		"command\tsleep 30\n" +
		"registeredAt\t2026-09-28T00:00:00Z\n" +
		"state\t" + state + "\n" +
		"ownershipKind\t" + kind + "\n" +
		"pid\t" + pid + "\n" +
		"pidStartTime\t" + start + "\n" +
		"nonce\t" + nonce + "\n" +
		"cgroupId\t" + cgroup + "\n" +
		"exit\t\n" +
		"exitedAt\t\n" +
		"descendants\t\n"
	if err := os.WriteFile(filepath.Join(remote.state, "leases", id), []byte(body), 0o600); err != nil {
		t.Fatalf("write lease entry %s: %v", id, err)
	}
}

// takeover starts one epoch's preemptive fence-takeover.
func takeover(t *testing.T, remote *fenceRemote, epoch Epoch) {
	t.Helper()
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, strconv.FormatUint(epoch.OpSeq, 10)); code != 0 {
		t.Fatalf("takeover %+v exited %d: %s", epoch, code, stderr)
	}
}

// TestScriptKillSignalsVerifiedInstanceAndMarksKilled pins S18's remote kill
// path (08c §4:101): under the taken-over lease, the entry's stored ownership
// identity is revalidated on the remote, the recorded instance is signaled, and
// the kill is recorded through the same entry file before the advance lands.
func TestScriptKillSignalsVerifiedInstanceAndMarksKilled(t *testing.T) {
	remote := newFenceRemote(t)
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(old)
	entry := crashPerformLeavingCommand(t, remote, old, nil, "exec sleep 30")
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	takeover(t, remote, next)
	stdout, stderr, code := remote.run(nil, "kill", next.BootID, "2", entry.ID)
	if code != 0 {
		t.Fatalf("kill exited %d: %s", code, stderr)
	}
	report, err := DecodeKillReport([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeKillReport(%q) = %v", stdout, err)
	}
	if !report.Signaled || report.Live || report.State != LeaseKilled {
		t.Fatalf("kill report = %+v, want signaled, not live, killed", report)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(t, *entry.Ownership.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("recorded instance %d survived the kill", *entry.Ownership.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if recheck := recheckID(t, remote, entry.ID); recheck.Live || recheck.State != LeaseKilled {
		t.Fatalf("recheck after kill = %+v, want killed and not live", recheck)
	}
	// The kill/wait is what gates the advance: it lands now.
	if _, stderr, code := remote.run(nil, "advance", next.BootID, "2"); code != 0 {
		t.Fatalf("advance after kill exited %d: %s", code, stderr)
	}
	if settled := remote.status(); settled.Epoch == nil || *settled.Epoch != next || settled.Fence != nil {
		t.Fatalf("status after advance = %+v, want %+v settled", settled, next)
	}
}

// TestScriptKillRequiresTheTakenOverLease pins the guard the kill runs under:
// only the epoch holding the pending fence and the lease holder may signal. A
// superseded or unfenced epoch's kill is dead, and it never signals work.
func TestScriptKillRequiresTheTakenOverLease(t *testing.T) {
	remote := newFenceRemote(t)
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(old)
	entry := crashPerformLeavingCommand(t, remote, old, nil, "exec sleep 30")
	defer killProcess(*entry.Ownership.PID)
	// A settled guard carries no fence for any epoch.
	if _, stderr, code := remote.run(nil, "kill", old.BootID, "1", entry.ID); code == 0 {
		t.Fatal("kill under a settled guard succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("kill under a settled guard = %v, want ErrStaleEpoch", err)
	}
	// A new epoch takes over: the old epoch's kill is stale.
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 2})
	if _, stderr, code := remote.run(nil, "kill", old.BootID, "1", entry.ID); code == 0 {
		t.Fatal("a superseded epoch's kill succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("superseded epoch's kill = %v, want ErrStaleEpoch", err)
	}
	if !processAlive(t, *entry.Ownership.PID) {
		t.Fatal("a refused kill signaled the tracked instance")
	}
}

// TestScriptKillNeverSignalsAReusedIdentity pins §4:101 and §9: a recorded pid
// whose start token no longer matches is a reused id naming unrelated work: the
// member reads as already clean and is never signaled.
func TestScriptKillNeverSignalsAReusedIdentity(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("start the unrelated process: %v", err)
	}
	defer func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() }()
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 2})
	// The recorded start token is deliberately not the live process's.
	writeLeaseEntry(t, remote, "n1", LeaseRunning, "pid", strconv.Itoa(victim.Process.Pid), "999999999999", "", "")
	stdout, stderr, code := remote.run(nil, "kill", "boot-1", "2", "n1")
	if code != 0 {
		t.Fatalf("kill exited %d: %s", code, stderr)
	}
	report, err := DecodeKillReport([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeKillReport(%q) = %v", stdout, err)
	}
	if report.Signaled || report.Live {
		t.Fatalf("kill report = %+v, want no signal and already clean", report)
	}
	if !processAlive(t, victim.Process.Pid) {
		t.Fatal("a reused pid was signaled")
	}
}

// TestScriptKillMissingAndSettledEntriesAreClean pins the two benign answers:
// an id nothing was registered under and an entry that already settled are both
// clean, never a refusal and never a signal.
func TestScriptKillMissingAndSettledEntriesAreClean(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", "true"); code != 0 {
		t.Fatalf("perform exited %d: %s", code, stderr)
	}
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v (err %v), want one settled entry", entries, err)
	}
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 2})
	for name, id := range map[string]string{"missing": "n0", "settled": entries[0].ID} {
		stdout, stderr, code := remote.run(nil, "kill", "boot-1", "2", id)
		if code != 0 {
			t.Fatalf("%s: kill exited %d: %s", name, code, stderr)
		}
		report, err := DecodeKillReport([]byte(stdout))
		if err != nil {
			t.Fatalf("%s: DecodeKillReport(%q) = %v", name, stdout, err)
		}
		if report.Signaled || report.Live {
			t.Fatalf("%s: kill report = %+v, want a clean, unsignaled answer", name, report)
		}
		want := ""
		if name == "settled" {
			want = LeaseExited
		}
		if report.State != want {
			t.Fatalf("%s: kill state = %q, want %q", name, report.State, want)
		}
	}
}

// TestScriptKillNonceOwnedEntrySignalsTheExactCarrier pins the nonce ownership
// verification: a process carrying the entry's exact nonce is the wrapper's
// work and is signaled; the entry is then marked killed through its own file.
func TestScriptKillNonceOwnedEntrySignalsTheExactCarrier(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("nonce verification needs /proc (Linux)")
	}
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	nonce := "0123456789abcdef0123456789abcdef"
	carrier := exec.Command("sh", "-c", "exec sleep 30")
	carrier.Env = append(os.Environ(), "EVENER_FENCE_NONCE="+nonce)
	if err := carrier.Start(); err != nil {
		t.Fatalf("start the nonce carrier: %v", err)
	}
	// One Wait owns the reap; the deferred kill only releases a failed test, so
	// the carrier never lingers as an unreaped zombie.
	exited := make(chan struct{})
	go func() {
		_ = carrier.Wait()
		close(exited)
	}()
	defer func() { _ = carrier.Process.Kill() }()
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 2})
	writeLeaseEntry(t, remote, nonce, LeaseRunning, "nonce", "", "", nonce, "")
	stdout, stderr, code := remote.run(nil, "kill", "boot-1", "2", nonce)
	if code != 0 {
		t.Fatalf("kill exited %d: %s", code, stderr)
	}
	report, err := DecodeKillReport([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeKillReport(%q) = %v", stdout, err)
	}
	if !report.Signaled || report.Live || report.State != LeaseKilled {
		t.Fatalf("kill report = %+v, want the exact carrier signaled and the entry killed", report)
	}
	// The carrier is this test's child: reap it rather than poll, because an
	// unreaped child stays visible to kill -0 as a zombie.
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the nonce carrier survived the kill")
	}
}

// TestScriptKillUnverifiableIdentityStaysLiveAndUnsignaled pins the fail-closed
// arm: an identity the remote cannot verify (an unreadable start token, or a
// cgroup membership this platform cannot attest) is never signaled, and the
// member reads live for the worker's bounded wait to judge.
func TestScriptKillUnverifiableIdentityStaysLiveAndUnsignaled(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("start the process: %v", err)
	}
	defer func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() }()
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 2})
	writeLeaseEntry(t, remote, "n1", LeaseRunning, "pid", strconv.Itoa(victim.Process.Pid), "0", "", "")
	stdout, stderr, code := remote.run([]string{"EVENER_FENCE_FAULT_UNREADABLE_START=1"}, "kill", "boot-1", "2", "n1")
	if code != 0 {
		t.Fatalf("kill exited %d: %s", code, stderr)
	}
	report, err := DecodeKillReport([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeKillReport(%q) = %v", stdout, err)
	}
	if report.Signaled || !report.Live {
		t.Fatalf("kill report = %+v, want unsignaled and live (fail closed)", report)
	}
	if !processAlive(t, victim.Process.Pid) {
		t.Fatal("an unverifiable identity was signaled")
	}
	writeLeaseEntry(t, remote, "n2", LeaseRunning, "cgroup", "", "", "", "cg-1")
	stdout, stderr, code = remote.run(nil, "kill", "boot-1", "2", "n2")
	if code != 0 {
		t.Fatalf("cgroup kill exited %d: %s", code, stderr)
	}
	report, err = DecodeKillReport([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeKillReport(%q) = %v", stdout, err)
	}
	if report.Signaled || !report.Live {
		t.Fatalf("cgroup kill report = %+v, want unsignaled and live (no remote attestation)", report)
	}
}

// TestScriptOutOfRangeHighWaterRefuses pins S17's recorded Low: the per-boot
// high-water is a uint64 (the Go decoder unmarshals it into one), so a value
// above the maximum is state-corrupt — never an accepted value that skips the
// stale-epoch refusal, and never an emitted value the Go decoder rejects.
func TestScriptOutOfRangeHighWaterRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 3})
	remote.settle(Epoch{BootID: "boot-2", OpSeq: 1})
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	corrupt := strings.Replace(string(raw), "boot.boot-1\t3\n", "boot.boot-1\t99999999999999999999\n", 1)
	if corrupt == string(raw) {
		t.Fatalf("guard carried no boot.boot-1 high-water to corrupt: %q", raw)
	}
	if err := os.WriteFile(guard, []byte(corrupt), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	work := t.TempDir()
	for _, op := range [][]string{
		{"status"},
		{"takeover", "boot-1", "2"},
		{"advance", "boot-2", "1"},
		{"kill", "boot-2", "1", "n1"},
		{"perform", "boot-2", "1", "printf x > " + work + "/sideeffect"},
	} {
		stdout, stderr, code := remote.run(nil, op...)
		if code == 0 {
			t.Fatalf("%v on an out-of-range high-water succeeded: %s", op, stdout)
		}
		if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("%v on an out-of-range high-water = %v, want ErrStateCorrupt", op, err)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "sideeffect")); err == nil {
		t.Fatal("a corrupt guard authorized a side effect")
	}
	// The in-range bound is exact, not the shell's integer width: the uint64
	// maximum is admitted and still refuses an epoch at or below it, while a
	// value one above the maximum is corrupt.
	maxHighWater := strings.Replace(string(raw), "boot.boot-1\t3\n", "boot.boot-1\t18446744073709551615\n", 1)
	if err := os.WriteFile(guard, []byte(maxHighWater), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	stdout, stderr, code := remote.run(nil, "status")
	if code != 0 {
		t.Fatalf("status with a uint64-maximum high-water exited %d: %s", code, stderr)
	}
	if _, err := DecodeStatus([]byte(stdout)); err != nil {
		t.Fatalf("DecodeStatus(uint64-maximum high-water) = %v", err)
	}
	if _, stderr, code := remote.run(nil, "takeover", "boot-1", "2"); code == 0 {
		t.Fatal("takeover at or below a uint64-maximum high-water succeeded, want the stale refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("takeover below a uint64-maximum high-water = %v, want ErrStaleEpoch", err)
	}
	above := strings.Replace(string(raw), "boot.boot-1\t3\n", "boot.boot-1\t18446744073709551616\n", 1)
	if err := os.WriteFile(guard, []byte(above), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	if _, stderr, code := remote.run(nil, "status"); code == 0 {
		t.Fatal("status with a high-water one above uint64 max succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("status one above uint64 max = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptTakeoverSupersedesAPendingFence pins S18's extension of §4's
// preemptive takeover: a crashed incarnation's pending fence must not brick the
// host. The next epoch supersedes it — "revokes the superseded epoch" (§4:99) —
// and only it may kill and advance from there; §4:107's "The next
// `deploy`/`restart` past the cleared marker runs its kill/wait plus guard
// advance under a fresh epoch" is this step.
func TestScriptTakeoverSupersedesAPendingFence(t *testing.T) {
	remote := newFenceRemote(t)
	first := Epoch{BootID: "boot-1", OpSeq: 1}
	takeover(t, remote, first)
	if pending := remote.status(); pending.Fence == nil || pending.Fence.Epoch != first {
		t.Fatalf("status after the crashed takeover = %+v, want %+v's fence pending", pending, first)
	}
	next := Epoch{BootID: "boot-1", OpSeq: 2}
	stdout, stderr, code := remote.run(nil, "takeover", next.BootID, "2")
	if code != 0 {
		t.Fatalf("takeover over a pending fence exited %d: %s", code, stderr)
	}
	status, err := DecodeStatus([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeStatus(%q) = %v", stdout, err)
	}
	if status.Fence == nil || status.Fence.Epoch != next || status.Fence.Superseded == nil || *status.Fence.Superseded != first {
		t.Fatalf("takeover fence = %+v, want %+v superseding %+v", status.Fence, next, first)
	}
	if status.Holder == nil || *status.Holder != next {
		t.Fatalf("takeover holder = %+v, want %+v", status.Holder, next)
	}
	if status.GuardEpoch != 2 {
		t.Fatalf("takeover guardEpoch = %d, want 2", status.GuardEpoch)
	}
	// The superseded epoch can no longer act: its perform, kill and advance all
	// refuse, and its side effect never lands.
	work := t.TempDir()
	if _, stderr, code := remote.run(nil, "perform", first.BootID, "1", "printf x > "+work+"/old"); code == 0 {
		t.Fatalf("a superseded epoch performed after the takeover: %s", stderr)
	}
	if _, stderr, code := remote.run(nil, "advance", first.BootID, "1"); code == 0 {
		t.Fatalf("a superseded epoch advanced after the takeover: %s", stderr)
	}
	if _, stderr, code := remote.run(nil, "kill", first.BootID, "1", "n1"); code == 0 {
		t.Fatalf("a superseded epoch killed after the takeover: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(work, "old")); err == nil {
		t.Fatal("a superseded epoch landed a side effect")
	}
	// A later boot's epoch supersedes a pending fence too.
	if _, stderr, code := remote.run(nil, "takeover", "boot-2", "1"); code != 0 {
		t.Fatalf("cross-boot takeover over a pending fence exited %d: %s", code, stderr)
	}
	if after := remote.status(); after.Fence == nil || after.Fence.Epoch != (Epoch{BootID: "boot-2", OpSeq: 1}) {
		t.Fatalf("status after the cross-boot takeover = %+v", after)
	}
}

// TestScriptGuardSequenceComparesAtUint64Width pins that the guard validator's
// comparisons are exact at the schema bound: a fence sequence at the uint64
// maximum is in range — not the shell's "Illegal number" corruption — so the
// helper admits exactly what is_uint64 and the Go decoder admit.
func TestScriptGuardSequenceComparesAtUint64Width(t *testing.T) {
	remote := newFenceRemote(t)
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 1})
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	const maxSeq = "18446744073709551615"
	big := strings.Replace(string(raw), "guardEpoch\t1\n", "guardEpoch\t"+maxSeq+"\n", 1)
	big = strings.Replace(big, "fenceGuardEpoch\t1\n", "fenceGuardEpoch\t"+maxSeq+"\n", 1)
	if big == string(raw) {
		t.Fatalf("guard carried no sequence to widen: %q", raw)
	}
	if err := os.WriteFile(guard, []byte(big), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	stdout, stderr, code := remote.run(nil, "status")
	if code != 0 {
		t.Fatalf("status with uint64-maximum sequences exited %d: %s", code, stderr)
	}
	status, err := DecodeStatus([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeStatus(%q) = %v", stdout, err)
	}
	if status.GuardEpoch != 18446744073709551615 || status.Fence == nil || status.Fence.GuardEpoch != 18446744073709551615 {
		t.Fatalf("status with uint64-maximum sequences = %+v", status)
	}
}

// TestScriptRecheckNonceVerifiesTheExactCarrier pins the nonce arm of the
// recheck the bounded wait reads: a nonce-owned entry is live only while a
// process carries its exact nonce, and proven gone when none does. Where /proc
// cannot enumerate, the entry stays live (fail closed).
func TestScriptRecheckNonceVerifiesTheExactCarrier(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("nonce verification needs /proc (Linux)")
	}
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	nonce := "fedcba9876543210fedcba9876543210"
	// No process carries the nonce: the member is provably gone, never live
	// forever.
	writeLeaseEntry(t, remote, nonce, LeaseRunning, "nonce", "", "", nonce, "")
	if recheck := recheckID(t, remote, nonce); recheck.Live {
		t.Fatalf("recheck(nonce with no carrier) = %+v, want not live", recheck)
	}
	// An impostor carrying an extension of the nonce is not the carrier: the
	// match is exact.
	impostor := exec.Command("sh", "-c", "exec sleep 30")
	impostor.Env = append(os.Environ(), "EVENER_FENCE_NONCE="+nonce+"EXTRA")
	if err := impostor.Start(); err != nil {
		t.Fatalf("start the impostor: %v", err)
	}
	defer func() { _ = impostor.Process.Kill(); _, _ = impostor.Process.Wait() }()
	if recheck := recheckID(t, remote, nonce); recheck.Live {
		t.Fatalf("recheck(extension carrier) = %+v, want not live (exact match)", recheck)
	}
	// The exact carrier reads live.
	carrier := exec.Command("sh", "-c", "exec sleep 30")
	carrier.Env = append(os.Environ(), "EVENER_FENCE_NONCE="+nonce)
	if err := carrier.Start(); err != nil {
		t.Fatalf("start the carrier: %v", err)
	}
	defer func() { _ = carrier.Process.Kill(); _, _ = carrier.Process.Wait() }()
	if recheck := recheckID(t, remote, nonce); !recheck.Live {
		t.Fatalf("recheck(exact carrier) = %+v, want live", recheck)
	}
}

// TestScriptKillReportShapeIsClosed pins the decoder's trust-boundary posture:
// the kill report is one closed object, and anything outside it is refused.
func TestScriptKillReportShapeIsClosed(t *testing.T) {
	good := `{"version":1,"id":"n1","signaled":true,"live":false,"state":"killed"}`
	report, err := DecodeKillReport([]byte(good))
	if err != nil {
		t.Fatalf("DecodeKillReport(%s) = %v", good, err)
	}
	if !report.Signaled || report.Live || report.State != LeaseKilled {
		t.Fatalf("DecodeKillReport(%s) = %+v", good, report)
	}
	for name, raw := range map[string]string{
		"version only":     `{"version":1}`,
		"unknown key":      `{"version":1,"id":"n1","signaled":false,"live":false,"state":"","extra":1}`,
		"duplicate key":    `{"version":1,"id":"n1","id":"n2","signaled":false,"live":false,"state":""}`,
		"wrong version":    `{"version":2,"id":"n1","signaled":false,"live":false,"state":""}`,
		"unknown state":    `{"version":1,"id":"n1","signaled":false,"live":false,"state":"gone"}`,
		"live while dead":  `{"version":1,"id":"n1","signaled":false,"live":true,"state":"exited"}`,
		"trailing bytes":   good + " junk",
		"missing id":       `{"version":1,"signaled":false,"live":false,"state":""}`,
		"missing signaled": `{"version":1,"id":"n1","live":false,"state":""}`,
		"bad remaining":    `{"version":1,"id":"n1","signaled":false,"live":true,"state":"running","remaining":[{"pid":0,"startToken":"1"}]}`,
	} {
		if _, err := DecodeKillReport([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeKillReport = nil error, want refusal", name)
		}
	}
}

// TestScriptKillEntryWasNotMutatedByARefusal pins that a refused kill leaves
// the lease file exactly as it was: no state transition without a verified,
// signaled kill.
func TestScriptKillEntryWasNotMutatedByARefusal(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("start the process: %v", err)
	}
	defer func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() }()
	takeover(t, remote, Epoch{BootID: "boot-1", OpSeq: 2})
	writeLeaseEntry(t, remote, "n1", LeaseRunning, "pid", strconv.Itoa(victim.Process.Pid), "999999999999", "", "")
	path := filepath.Join(remote.state, "leases", "n1")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if _, _, code := remote.run(nil, "kill", "boot-1", "2", "n1"); code != 0 {
		t.Fatalf("kill exited %d", code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read entry after kill: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a clean-read kill rewrote the entry: before %q after %q", before, after)
	}
	assertNoTempFiles(t, remote.state)
}

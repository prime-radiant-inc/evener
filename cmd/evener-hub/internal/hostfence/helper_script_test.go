package hostfence

import (
	"bytes"
	"encoding/json"
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

// The script tests run the real embedded helper locally against a temporary
// state root. That local process is the scripted-remote seam: the fencing
// protocol — guard-file compare-and-swap, lease registration, takeover, guard
// advance, nonce re-presentation — is exercised end to end without a host and
// without ssh, per crash-fencing §10's helper-state rows.

// fenceRemote is one test's installed helper plus its state root.
type fenceRemote struct {
	t      *testing.T
	script string
	state  string
}

// newFenceRemote installs the embedded helper bytes into a temp dir the way
// S21's out-of-band install will, and gives it a temp state root.
func newFenceRemote(t *testing.T) *fenceRemote {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "evener-fence")
	if err := os.WriteFile(script, HelperScript(), 0o700); err != nil {
		t.Fatalf("install helper: %v", err)
	}
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create state root: %v", err)
	}
	return &fenceRemote{t: t, script: script, state: state}
}

// helperCommand builds one helper invocation through a POSIX shell, exactly as
// the remote exec seam will. Extra environment entries are appended last.
func (f *fenceRemote) helperCommand(extraEnv []string, args ...string) *exec.Cmd {
	cmd := exec.Command("sh", append([]string{f.script}, args...)...)
	cmd.Env = append(append(os.Environ(), "EVENER_FENCE_STATE="+f.state), extraEnv...)
	return cmd
}

// run executes one helper invocation and collects its streams and exit status.
func (f *fenceRemote) run(extraEnv []string, args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	cmd := f.helperCommand(extraEnv, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			f.t.Fatalf("run helper %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	return out.String(), errOut.String(), code
}

// start launches one helper invocation without waiting, so a test can kill the
// wrapper mid-command and observe the crash posture. The streams are left to
// the test process: a pipe here would keep Wait blocked until the orphaned
// command also closed it, defeating the mid-command kill.
func (f *fenceRemote) start(extraEnv []string, args ...string) (*exec.Cmd, error) {
	f.t.Helper()
	cmd := f.helperCommand(extraEnv, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// status runs `status` and decodes it with the package's own decoder.
func (f *fenceRemote) status() Status {
	f.t.Helper()
	stdout, stderr, code := f.run(nil, "status")
	if code != 0 {
		f.t.Fatalf("status exited %d: %s", code, stderr)
	}
	status, err := DecodeStatus([]byte(stdout))
	if err != nil {
		f.t.Fatalf("DecodeStatus(%q) = %v", stdout, err)
	}
	return status
}

// settle drives the helper to the state a finished fencing leaves: the epoch
// took over the lease and the guard advanced to it.
func (f *fenceRemote) settle(epoch Epoch) {
	f.t.Helper()
	if _, stderr, code := f.run(nil, "takeover", epoch.BootID, strconv.FormatUint(epoch.OpSeq, 10)); code != 0 {
		f.t.Fatalf("takeover %+v exited %d: %s", epoch, code, stderr)
	}
	if _, stderr, code := f.run(nil, "advance", epoch.BootID, strconv.FormatUint(epoch.OpSeq, 10)); code != 0 {
		f.t.Fatalf("advance %+v exited %d: %s", epoch, code, stderr)
	}
}

func TestScriptVersionSelfTestAndAbsentHelper(t *testing.T) {
	remote := newFenceRemote(t)
	stdout, _, code := remote.run(nil, "version")
	if code != 0 {
		t.Fatalf("version exited %d, want 0", code)
	}
	version, ok := ParseHelperVersion(stdout)
	if !ok || version != HelperVersion {
		t.Fatalf("ParseHelperVersion(%q) = (%d, %v), want (%d, true)", stdout, version, ok, HelperVersion)
	}
	if err := VerifyHelper("h1", HelperVersion, HelperProbe{Present: true, Reported: true, Version: version}); err != nil {
		t.Fatalf("VerifyHelper(script) = %v, want nil", err)
	}
	// A helper that is not there answers nothing: the probe is absent, and the
	// gate refuses before any remote mutation.
	missing := filepath.Join(t.TempDir(), "evener-fence")
	cmd := exec.Command("sh", missing, "version")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("running a missing helper succeeded, want a failure")
	}
	var refusal *HelperGateError
	if err := VerifyHelper("h1", HelperVersion, HelperProbe{Present: false}); !errors.As(err, &refusal) ||
		refusal.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("VerifyHelper(absent) = %v, want %s", err, DiscriminatorHelperAbsent)
	}
}

func TestScriptTakeoverAdvanceAndReplay(t *testing.T) {
	remote := newFenceRemote(t)
	fresh := remote.status()
	if fresh.Version != 1 || fresh.GuardEpoch != 0 || fresh.Epoch != nil || fresh.Fence != nil || fresh.Holder != nil {
		t.Fatalf("fresh status = %+v, want an empty guard", fresh)
	}
	first := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run(nil, "takeover", first.BootID, "1"); code != 0 {
		t.Fatalf("takeover exited %d: %s", code, stderr)
	}
	pending := remote.status()
	if pending.Fence == nil || pending.Fence.Epoch != first || pending.Fence.Superseded != nil {
		t.Fatalf("after takeover fence = %+v, want %+v superseding nothing", pending.Fence, first)
	}
	if pending.GuardEpoch != 1 || pending.Epoch != nil {
		t.Fatalf("after takeover guardEpoch = %d epoch = %+v, want 1 and no advanced epoch", pending.GuardEpoch, pending.Epoch)
	}
	if pending.Holder == nil || *pending.Holder != first {
		t.Fatalf("after takeover holder = %+v, want %+v", pending.Holder, first)
	}
	// A replay of the same takeover is idempotent: the sequence does not advance
	// twice for one fencing.
	if _, stderr, code := remote.run(nil, "takeover", first.BootID, "1"); code != 0 {
		t.Fatalf("takeover replay exited %d: %s", code, stderr)
	}
	if replay := remote.status(); replay.GuardEpoch != pending.GuardEpoch {
		t.Fatalf("takeover replay guardEpoch = %d, want %d", replay.GuardEpoch, pending.GuardEpoch)
	}
	if _, stderr, code := remote.run(nil, "advance", first.BootID, "1"); code != 0 {
		t.Fatalf("advance exited %d: %s", code, stderr)
	}
	settled := remote.status()
	if settled.Epoch == nil || *settled.Epoch != first || settled.Fence != nil {
		t.Fatalf("after advance epoch = %+v fence = %+v, want %+v and no fence", settled.Epoch, settled.Fence, first)
	}
	if settled.GuardEpoch != 2 {
		t.Fatalf("after advance guardEpoch = %d, want 2", settled.GuardEpoch)
	}
	// The advance replay is idempotent too.
	if _, stderr, code := remote.run(nil, "advance", first.BootID, "1"); code != 0 {
		t.Fatalf("advance replay exited %d: %s", code, stderr)
	}
	if replay := remote.status(); replay.GuardEpoch != settled.GuardEpoch {
		t.Fatalf("advance replay guardEpoch = %d, want %d", replay.GuardEpoch, settled.GuardEpoch)
	}
	// A second takeover supersedes the first and records it.
	second := Epoch{BootID: "boot-1", OpSeq: 2}
	if _, stderr, code := remote.run(nil, "takeover", second.BootID, "2"); code != 0 {
		t.Fatalf("second takeover exited %d: %s", code, stderr)
	}
	held := remote.status()
	if held.Fence == nil || held.Fence.Epoch != second || held.Fence.Superseded == nil || *held.Fence.Superseded != first {
		t.Fatalf("second takeover fence = %+v, want %+v superseding %+v", held.Fence, second, first)
	}
	if held.Holder == nil || *held.Holder != second {
		t.Fatalf("second takeover holder = %+v, want %+v", held.Holder, second)
	}
	// An epoch the guard already superseded never takes over again.
	if _, stderr, code := remote.run(nil, "takeover", first.BootID, "1"); code == 0 {
		t.Fatal("takeover of a superseded epoch succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("takeover superseded = %v, want ErrStaleEpoch", err)
	}
	// An older guard advance never overwrites the newer fence.
	if _, stderr, code := remote.run(nil, "advance", first.BootID, "1"); code == 0 {
		t.Fatal("advance of a superseded epoch succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("advance superseded = %v, want ErrStaleEpoch", err)
	}
	// And the newer epoch settles the guard past the old one.
	if _, stderr, code := remote.run(nil, "advance", second.BootID, "2"); code != 0 {
		t.Fatalf("advance of the newer epoch exited %d: %s", code, stderr)
	}
	if final := remote.status(); final.Epoch == nil || *final.Epoch != second || final.Fence != nil {
		t.Fatalf("final status = %+v, want the newer epoch settled", final)
	}
}

func TestScriptAdvanceWithoutTakeoverRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run(nil, "advance", epoch.BootID, "1"); code == 0 {
		t.Fatal("advance without a takeover succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("advance without takeover = %v, want ErrStaleEpoch", err)
	}
	if after := remote.status(); after.GuardEpoch != 0 || after.Epoch != nil {
		t.Fatalf("refused advance changed the guard: %+v", after)
	}
}

// TestScriptPerformRegistersBeforeSideEffects pins §4's atomicity rule: "The
// wrapper atomically registers each command in the per-host remote lease file
// before its side effects start." The command itself checks that its lease
// entry — tagged by the per-spawn nonce in its environment — is already there.
func TestScriptPerformRegistersBeforeSideEffects(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	command := fmt.Sprintf(
		`printf '%%s' "$EVENER_FENCE_NONCE" > %s/nonce; `+
			`if [ -f "$EVENER_FENCE_STATE/leases/$EVENER_FENCE_NONCE" ]; then printf present; else printf absent; fi > %s/registered; `+
			`printf done > %s/sideeffect`,
		work, work, work)
	stdout, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", command)
	if code != 0 {
		t.Fatalf("perform exited %d: stdout=%q stderr=%q", code, stdout, stderr)
	}
	nonce, err := os.ReadFile(filepath.Join(work, "nonce"))
	if err != nil || len(nonce) == 0 {
		t.Fatalf("the spawned command saw no nonce tag: err=%v bytes=%d", err, len(nonce))
	}
	registered, err := os.ReadFile(filepath.Join(work, "registered"))
	if err != nil || string(registered) != "present" {
		t.Fatalf("lease entry at side-effect time = %q (err %v), want present", registered, err)
	}
	if _, err := os.Stat(filepath.Join(work, "sideeffect")); err != nil {
		t.Fatalf("the side effect did not land: %v", err)
	}
	entriesStdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(entriesStdout))
	if err != nil {
		t.Fatalf("DecodeEntries(%q) = %v", entriesStdout, err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the one spawned command", entries)
	}
	entry := entries[0]
	if entry.ID != string(nonce) || entry.State != "exited" || entry.Exit == nil || *entry.Exit != 0 {
		t.Fatalf("entry = %+v, want the nonce-tagged exited entry", entry)
	}
	if !strings.Contains(entry.Command, "sideeffect") || entry.RegisteredAt == "" {
		t.Fatalf("entry = %+v, want the command and registration time recorded", entry)
	}
}

// TestScriptPerformRefusesStaleAndFencedEpochs is §10's interleaving pin from
// the advance-first side: an old epoch's check refuses server-side and aborts
// the operation, so no side effect lands past the advance.
func TestScriptPerformRefusesStaleAndFencedEpochs(t *testing.T) {
	remote := newFenceRemote(t)
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	newer := Epoch{BootID: "boot-1", OpSeq: 2}
	remote.settle(old)
	work := t.TempDir()
	// While a takeover pends (fence recorded, guard not advanced), every
	// mutating step refuses — including the takeover's own epoch.
	if _, stderr, code := remote.run(nil, "takeover", newer.BootID, "2"); code != 0 {
		t.Fatalf("takeover exited %d: %s", code, stderr)
	}
	_, stderr, code := remote.run(nil, "perform", newer.BootID, "2", "printf x > "+work+"/fenced")
	if code == 0 {
		t.Fatal("perform under a pending fence succeeded, want refusal")
	}
	if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrFenced) {
		t.Fatalf("perform under a fence = %v, want ErrFenced", err)
	}
	if _, err := os.Stat(filepath.Join(work, "fenced")); err == nil {
		t.Fatal("a fenced perform landed its side effect")
	}
	// After the advance the old epoch is stale: it refuses server-side before
	// any side effect.
	if _, stderr, code := remote.run(nil, "advance", newer.BootID, "2"); code != 0 {
		t.Fatalf("advance exited %d: %s", code, stderr)
	}
	_, stderr, code = remote.run(nil, "perform", old.BootID, "1", "printf x > "+work+"/stale")
	if code == 0 {
		t.Fatal("perform with a stale epoch succeeded, want refusal")
	}
	if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale perform = %v, want ErrStaleEpoch", err)
	}
	if _, err := os.Stat(filepath.Join(work, "stale")); err == nil {
		t.Fatal("a stale perform landed its side effect")
	}
}

// TestScriptInterleavingLockSerializesCheckAndAdvance pins the other half of
// §10's interleaving row: a command that passed its check and holds the lease
// lands its side effect before the concurrent takeover and guard advance, and
// the lock never lets the advance overtake the act.
func TestScriptInterleavingLockSerializesCheckAndAdvance(t *testing.T) {
	remote := newFenceRemote(t)
	old := Epoch{BootID: "boot-1", OpSeq: 1}
	newer := Epoch{BootID: "boot-1", OpSeq: 2}
	remote.settle(old)
	work := t.TempDir()
	goFile := filepath.Join(work, "go")
	command := fmt.Sprintf(`touch %s/started; i=0; while [ ! -f %s ]; do i=$((i+1)); [ "$i" -gt 200 ] && exit 9; sleep 0.05; done; touch %s/sideeffect`,
		work, goFile, work)
	performDone := make(chan struct{})
	perform := func() {
		defer close(performDone)
		if _, stderr, code := remote.run(nil, "perform", old.BootID, "1", command); code != 0 {
			t.Errorf("interleaved perform exited %d: %s", code, stderr)
		}
	}
	go perform()
	waitForFile(t, filepath.Join(work, "started"))

	// The takeover is issued while the old command holds the lease: it must
	// serialize behind it, so the superseded command's side effect still lands
	// before any advance.
	takeoverDone := make(chan string, 1)
	go func() {
		_, stderr, code := remote.run(nil, "takeover", newer.BootID, "2")
		if code != 0 {
			takeoverDone <- fmt.Sprintf("takeover exited %d: %s", code, stderr)
			return
		}
		takeoverDone <- ""
	}()
	select {
	case failure := <-takeoverDone:
		t.Fatalf("takeover returned while the superseded command held the lease: %s", failure)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(work, "sideeffect")); err == nil {
		t.Fatal("the side effect landed before the command was released, want it held at the gate")
	}
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("release the command: %v", err)
	}
	<-performDone
	if failure := <-takeoverDone; failure != "" {
		t.Fatal(failure)
	}
	if _, err := os.Stat(filepath.Join(work, "sideeffect")); err != nil {
		t.Fatalf("the superseded command's side effect did not land before the advance: %v", err)
	}
	if _, stderr, code := remote.run(nil, "advance", newer.BootID, "2"); code != 0 {
		t.Fatalf("advance after the interleaving exited %d: %s", code, stderr)
	}
	if status := remote.status(); status.Epoch == nil || *status.Epoch != newer || status.Fence != nil {
		t.Fatalf("after the interleaving status = %+v, want the newer epoch settled", status)
	}
}

// TestScriptRecheckRePresentsOwnership pins §9's enumeration rule: a persisted
// lease entry is verified "against its stored ownership identity", and a
// crashed wrapper's entry keeps reading live — fail closed — never clean.
func TestScriptRecheckRePresentsOwnership(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	goFile := filepath.Join(work, "go")
	command := fmt.Sprintf(`touch %s/started; i=0; while [ ! -f %s ]; do i=$((i+1)); [ "$i" -gt 200 ] && exit 9; sleep 0.05; done`, work, goFile)
	performDone := make(chan struct{})
	go func() {
		defer close(performDone)
		if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", command); code != 0 {
			t.Errorf("perform exited %d: %s", code, stderr)
		}
	}()
	waitForFile(t, filepath.Join(work, "started"))
	entry := waitForRunningEntry(t, remote)
	if entry.Ownership.PID == nil || entry.Ownership.PIDStartTime == "" {
		t.Fatalf("live entry = %+v, want pid ownership", entry)
	}
	recheck := recheckID(t, remote, entry.ID)
	if !recheck.Live {
		t.Fatalf("recheck(live entry) = %+v, want live", recheck)
	}
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("release the command: %v", err)
	}
	<-performDone
	recheck = recheckID(t, remote, entry.ID)
	if recheck.Live || recheck.State != "exited" {
		t.Fatalf("recheck(exited entry) = %+v, want exited and not live", recheck)
	}
}

// TestScriptPerformNonceOwnership exercises the fallback ownership identity:
// where no kernel-owned start time is taken, the wrapper registers its
// per-spawn nonce, and the nonce re-presentation stays fail-closed while the
// command is live and clean once it exits.
func TestScriptPerformNonceOwnership(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	goFile := filepath.Join(work, "go")
	command := fmt.Sprintf(`touch %s/started; i=0; while [ ! -f %s ]; do i=$((i+1)); [ "$i" -gt 200 ] && exit 9; sleep 0.05; done`, work, goFile)
	performDone := make(chan struct{})
	go func() {
		defer close(performDone)
		if _, stderr, code := remote.run([]string{"EVENER_FENCE_OWNERSHIP=nonce"}, "perform", epoch.BootID, "1", command); code != 0 {
			t.Errorf("perform exited %d: %s", code, stderr)
		}
	}()
	waitForFile(t, filepath.Join(work, "started"))
	entry := waitForRunningEntry(t, remote)
	if entry.Ownership.Kind() != OwnershipNonce || entry.Ownership.Nonce != entry.ID {
		t.Fatalf("nonce-owned entry = %+v, want ownership %s keyed by the entry id", entry, OwnershipNonce)
	}
	if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
		t.Fatalf("recheck(nonce entry) = %+v, want live", recheck)
	}
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("release the command: %v", err)
	}
	<-performDone
	if recheck := recheckID(t, remote, entry.ID); recheck.Live || recheck.State != LeaseExited {
		t.Fatalf("recheck(nonce exited) = %+v, want exited and not live", recheck)
	}
}

// TestScriptCrashLeavesEntryLive pins the fail-closed crash posture: a wrapper
// that dies mid-command leaves its entry registered, and the re-presented
// identity reads live until the recorded process instance is demonstrably gone.
// Nothing here kills on membership or a reused pid alone: the recorded start
// time is the proof.
func TestScriptCrashLeavesEntryLive(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	done := make(chan struct{})
	cmd, err := remote.start(nil, "perform", epoch.BootID, "1", "exec sleep 30")
	if err != nil {
		t.Fatalf("start perform: %v", err)
	}
	go func() { defer close(done); _ = cmd.Wait() }()
	entry := waitForRunningEntry(t, remote)
	if entry.Ownership.PID == nil {
		t.Fatalf("live entry = %+v, want pid ownership", entry)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the wrapper: %v", err)
	}
	<-done
	// The wrapper is dead; its command still runs. The entry must read live.
	if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
		t.Fatalf("recheck(orphaned entry) = %+v, want live (fail closed)", recheck)
	}
	// Signal the exact recorded instance: its kernel-owned start time proves it.
	if err := syscall.Kill(*entry.Ownership.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the recorded command instance: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry %s still reads live after its process was killed", entry.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestScriptStateCorruptFailsClosed(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	guard := filepath.Join(remote.state, "guard")
	if err := os.WriteFile(guard, []byte("this is not the guard file"), 0o600); err != nil {
		t.Fatalf("corrupt guard: %v", err)
	}
	before, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	work := t.TempDir()
	for _, args := range [][]string{
		{"takeover", "boot-1", "2"},
		{"advance", "boot-1", "2"},
		{"perform", "boot-1", "2", "printf x > " + work + "/sideeffect"},
	} {
		_, stderr, code := remote.run(nil, args...)
		if code == 0 {
			t.Fatalf("%v on a corrupt guard succeeded, want refusal", args)
		}
		if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("%v = %v, want ErrStateCorrupt", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "sideeffect")); err == nil {
		t.Fatal("a corrupt guard authorized a side effect")
	}
	after, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard after refusals: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a refusal rewrote the corrupt guard: before %q after %q", before, after)
	}
	// The refusal path never leaves a partial temp file behind.
	assertNoTempFiles(t, remote.state)
}

func TestScriptStateFilesAreOwnerOnly(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	guard := filepath.Join(remote.state, "guard")
	info, err := os.Stat(guard)
	if err != nil {
		t.Fatalf("stat guard: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("guard mode = %04o, want 0600", perm)
	}
	if err := os.WriteFile(filepath.Join(remote.state, "leases", "holder"), []byte("boot-1 1\n"), 0o600); err != nil {
		t.Fatalf("seed holder: %v", err)
	}
	assertNoTempFiles(t, remote.state)
}

// helpers

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// waitForRunningEntry polls until the spawned command's entry carries its
// post-spawn identity. The registering entry is written before the command's
// side effects (which is the point of the registration), so an observation may
// legitimately land between the two writes.
func waitForRunningEntry(t *testing.T, remote *fenceRemote) LeaseEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stdout, stderr, code := remote.run(nil, "entries")
		if code != 0 {
			t.Fatalf("entries exited %d: %s", code, stderr)
		}
		entries, err := DecodeEntries([]byte(stdout))
		if err != nil {
			t.Fatalf("DecodeEntries(%q) = %v", stdout, err)
		}
		if len(entries) == 1 && entries[0].State == LeaseRunning {
			return entries[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the running lease entry")
	return LeaseEntry{}
}

func recheckID(t *testing.T, remote *fenceRemote, id string) Recheck {
	t.Helper()
	stdout, stderr, code := remote.run(nil, "recheck", id)
	if code != 0 {
		t.Fatalf("recheck exited %d: %s", code, stderr)
	}
	recheck, err := DecodeRecheck([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeRecheck(%q) = %v", stdout, err)
	}
	return recheck
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	var leftovers []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".tmp") || strings.HasSuffix(entry.Name(), ".tmp") {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk state root: %v", err)
	}
	if len(leftovers) > 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestScriptExitCodesAndStderrShape is the small print of the protocol the Go
// side decodes: refusals are one JSON object on stderr with a nonzero exit.
func TestScriptExitCodesAndStderrShape(t *testing.T) {
	remote := newFenceRemote(t)
	_, stderr, code := remote.run(nil, "bogus-op")
	if code == 0 {
		t.Fatal("unknown op exited 0, want a refusal")
	}
	if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown op = %v, want ErrMalformed", err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(stderr), &raw); err != nil {
		t.Fatalf("refusal is not one JSON object: %q", stderr)
	}
	_, _, code = remote.run(nil, "takeover")
	if code == 0 {
		t.Fatal("missing epoch exited 0, want a refusal")
	}
	_, _, code = remote.run(nil, "takeover", "bad/boot", "1")
	if code == 0 {
		t.Fatal("malformed boot id exited 0, want a refusal")
	}
}

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
	"sync"
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
// runFile executes one helper invocation with its streams on a file rather than
// a pipe: a surviving descendant would otherwise hold the pipe open and keep
// the wait blocked until it exits, which is exactly what the descendant tests
// must observe before it does.
func (f *fenceRemote) runFile(extraEnv []string, args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	log, err := os.CreateTemp("", "evener-fence-run-")
	if err != nil {
		f.t.Fatalf("create run log: %v", err)
	}
	defer func() { _ = os.Remove(log.Name()) }()
	cmd := f.helperCommand(extraEnv, args...)
	cmd.Stdout, cmd.Stderr = log, log
	runErr := cmd.Run()
	_ = log.Close()
	raw, _ := os.ReadFile(log.Name())
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			f.t.Fatalf("run helper %v: %v", args, runErr)
		}
		code = exit.ExitCode()
	}
	return string(raw), "", code
}

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
	instance, err := os.FindProcess(*entry.Ownership.PID)
	if err != nil {
		t.Fatalf("find the recorded command instance: %v", err)
	}
	if err := instance.Kill(); err != nil {
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

// TestScriptCorruptHolderFailsClosed pins that a malformed holder file — the
// lease holder record is written by hand in this test, so a one-token or
// truncated form is exactly what corruption looks like — refuses cleanly
// instead of tripping the shell's unset-variable handling.
func TestScriptCorruptHolderFailsClosed(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	holder := filepath.Join(remote.state, "leases", "holder")
	for name, content := range map[string]string{
		"one token":        "boot-1\n",
		"bad boot id":      "boot/1 3\n",
		"zero op seq":      "boot-1 0\n",
		"extra token":      "boot-1 3 extra\n",
		"garbage":          "not a holder\n",
		"empty boot":       " 3\n",
		"absent with seq":  "- 5\n",
		"leading zero seq": "boot-1 03\n",
	} {
		if err := os.WriteFile(holder, []byte(content), 0o600); err != nil {
			t.Fatalf("%s: write holder: %v", name, err)
		}
		for _, op := range [][]string{{"status"}, {"takeover", "boot-1", "2"}} {
			_, stderr, code := remote.run(nil, op...)
			if code == 0 {
				t.Fatalf("%s: %v on a corrupt holder succeeded, want refusal", name, op)
			}
			if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("%s: %v = %v, want ErrStateCorrupt", name, op, err)
			}
		}
	}
}

// TestScriptEmptyBootIDRefuses pins the explicit empty-field rule: an empty
// epoch/lease boot value is corrupt even when the line still splits into two
// awk fields, never the "-" absence form.
func TestScriptEmptyBootIDRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	broken := strings.Replace(string(raw), "epochBootId\tboot-1\n", "epochBootId\t\n", 1)
	if broken == string(raw) {
		t.Fatalf("guard did not carry the expected epoch line: %q", raw)
	}
	if err := os.WriteFile(guard, []byte(broken), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	for _, op := range [][]string{{"status"}, {"advance", "boot-1", "1"}} {
		if _, stderr, code := remote.run(nil, op...); code == 0 {
			t.Fatalf("%v on an empty boot id succeeded, want refusal", op)
		} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("%v = %v, want ErrStateCorrupt", op, err)
		}
	}
}

// TestScriptStaleLockRecovery pins the lock's crash recovery: a lock left by a
// dead owner (or one whose file carries no claim) is stolen exactly once and
// does not wedge the host, while a live owner's lock is never taken.
func TestScriptStaleLockRecovery(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	lockFile := filepath.Join(remote.state, "lock")
	// A dead PID with a matching start token: the classic crash leftover.
	dead := 999999
	deadStart := "0"
	if err := os.WriteFile(lockFile, []byte(fmt.Sprintf("%d %s\n", dead, deadStart)), 0o600); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "1"); code != 0 {
		t.Fatalf("takeover behind a stale lock exited %d: %s", code, stderr)
	}
	if _, stderr, code := remote.run(nil, "advance", epoch.BootID, "1"); code != 0 {
		t.Fatalf("advance exited %d: %s", code, stderr)
	}
	// A contentless lock is a crash artifact too: nothing claims it.
	if err := os.WriteFile(lockFile, []byte(""), 0o600); err != nil {
		t.Fatalf("seed empty lock: %v", err)
	}
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "2"); code != 0 {
		t.Fatalf("takeover behind an empty lock exited %d: %s", code, stderr)
	}
	// A live owner's lock is held, not stolen: the operation refuses busy.
	holder := &scriptedLockHolder{}
	holder.start(t, remote.state, lockFile)
	defer holder.stop()
	_, stderr, code := remote.run([]string{"EVENER_FENCE_LOCK_ATTEMPTS=3"}, "takeover", epoch.BootID, "3")
	if code == 0 {
		t.Fatal("takeover stole a live lock, want busy")
	}
	if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrHelperBusy) {
		t.Fatalf("takeover behind a live lock = %v, want ErrHelperBusy", err)
	}
	// And the live owner's lock is still intact after the refusal.
	raw, err := os.ReadFile(lockFile)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		t.Fatalf("live lock file = %q (err %v), want the owner's claim kept", raw, err)
	}
}

// scriptedLockHolder writes a live-pid lock file and keeps its process alive,
// so the helper sees a holder that cannot be stolen.
type scriptedLockHolder struct{ cmd *exec.Cmd }

func (h *scriptedLockHolder) start(t *testing.T, _ string, lockFile string) {
	t.Helper()
	h.cmd = exec.Command("sh", "-c", "sleep 30")
	if err := h.cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	start := "unknown"
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", h.cmd.Process.Pid)); err == nil {
		// Field 22 of /proc/<pid>/stat is the start time: index 21 after the
		// pid and the parenthesized comm.
		fields := strings.Fields(string(raw))
		if len(fields) > 21 {
			start = fields[21]
		}
	}
	if err := os.WriteFile(lockFile, []byte(fmt.Sprintf("%d %s\n", h.cmd.Process.Pid, start)), 0o600); err != nil {
		t.Fatalf("write live lock: %v", err)
	}
}

func (h *scriptedLockHolder) stop() {
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
		_, _ = h.cmd.Process.Wait()
	}
}

// TestScriptFenceGuardBoundRefuses pins validator parity: a fence whose guard
// sequence is outside the guard's own sequence is corrupt, exactly as the Go
// validator decides.
func TestScriptFenceGuardBoundRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "1"); code != 0 {
		t.Fatalf("takeover exited %d: %s", code, stderr)
	}
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	broken := strings.Replace(string(raw), "fenceGuardEpoch\t2\n", "fenceGuardEpoch\t9\n", 1)
	if broken == string(raw) {
		broken = strings.Replace(string(raw), "fenceGuardEpoch\t1\n", "fenceGuardEpoch\t9\n", 1)
	}
	if broken == string(raw) {
		t.Fatalf("guard carried no fenceGuardEpoch to break: %q", raw)
	}
	if err := os.WriteFile(guard, []byte(broken), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	if _, stderr, code := remote.run(nil, "status"); code == 0 {
		t.Fatal("status with an out-of-range fence guard succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("status = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptUnprovableIdentityStaysLive pins the fail-closed recheck: when the
// live process's start token cannot be read, the entry reads live, never clean.
func TestScriptUnprovableIdentityStaysLive(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	goFile := filepath.Join(work, "go")
	command := fmt.Sprintf(`touch %s/started; i=0; while [ ! -f %s ]; do i=$((i+1)); [ "$i" -gt 200 ] && exit 9; sleep 0.05; done`, work, goFile)
	performDone := make(chan struct{})
	go func() {
		defer close(performDone)
		if _, stderr, code := remote.run([]string{"EVENER_FENCE_FAULT_UNREADABLE_START=1"}, "perform", epoch.BootID, "1", command); code != 0 {
			t.Errorf("perform exited %d: %s", code, stderr)
		}
	}()
	waitForFile(t, filepath.Join(work, "started"))
	entry := waitForRunningEntry(t, remote)
	if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
		t.Fatalf("recheck(unreadable start token) = %+v, want live (fail closed)", recheck)
	}
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("release the command: %v", err)
	}
	<-performDone
}

// TestScriptDescendantsStayLive pins the descendant-tracking rule: a command
// whose own children outlive it never leaves a clean entry, so a later takeover
// cannot proceed as though no work survived.
func TestScriptDescendantsStayLive(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("descendant tracking needs /proc (Linux)")
	}
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	command := fmt.Sprintf("sleep 30 & echo $! > %s/descendant; echo done", work)
	if combined, _, code := remote.runFile(nil, "perform", epoch.BootID, "1", command); code != 0 {
		t.Fatalf("perform exited %d: %s", code, combined)
	}
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v (err %v), want one entry", entries, err)
	}
	entry := entries[0]
	if entry.State != LeaseRunning || len(entry.Descendants) == 0 {
		t.Fatalf("entry = %+v, want running with descendants recorded", entry)
	}
	if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
		t.Fatalf("recheck(entry with descendants) = %+v, want live (fail closed)", recheck)
	}
	// Once the survivor is gone, the entry reads clean.
	raw, err := os.ReadFile(filepath.Join(work, "descendant"))
	if err != nil {
		t.Fatalf("read descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse descendant pid %q: %v", raw, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find descendant: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill descendant: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		recheck := recheckID(t, remote, entry.ID)
		if !recheck.Live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry %s still reads live after its descendant died: %+v", entry.ID, recheck)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestScriptHighWaterIsFreshAfterTakeover pins that a takeover's own report
// carries the high-water record it just wrote, not the pre-write snapshot.
func TestScriptHighWaterIsFreshAfterTakeover(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-fresh", OpSeq: 3}
	stdout, stderr, code := remote.run(nil, "takeover", epoch.BootID, "3")
	if code != 0 {
		t.Fatalf("takeover exited %d: %s", code, stderr)
	}
	status, err := DecodeStatus([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeStatus = %v", err)
	}
	if got := status.BootHighWater[epoch.BootID]; got != 3 {
		t.Fatalf("takeover report high-water[%s] = %d, want 3", epoch.BootID, got)
	}
}

// TestScriptNonNumericExitRefuses pins the entry validator: an exit status that
// is not a number is corrupt, never emitted raw into the JSON.
func TestScriptNonNumericExitRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", "true"); code != 0 {
		t.Fatalf("perform exited %d: %s", code, stderr)
	}
	entriesDir := filepath.Join(remote.state, "leases")
	names, err := os.ReadDir(entriesDir)
	if err != nil {
		t.Fatalf("read leases: %v", err)
	}
	var entryPath string
	for _, name := range names {
		if name.Name() != "holder" {
			entryPath = filepath.Join(entriesDir, name.Name())
		}
	}
	if entryPath == "" {
		t.Fatal("no lease entry to corrupt")
	}
	raw, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	broken := strings.Replace(string(raw), "\nexit\t0\n", "\nexit\tnot-a-number\n", 1)
	if broken == string(raw) {
		t.Fatalf("entry carried no numeric exit to break: %q", raw)
	}
	if err := os.WriteFile(entryPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if _, stderr, code := remote.run(nil, "entries"); code == 0 {
		t.Fatal("entries with a non-numeric exit succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("entries = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptPostSpawnFailureKillsDescendants pins the tracking-failure path:
// when the wrapper cannot record the spawned command, it kills the command and
// every descendant still carrying its nonce before refusing, so no side-effect
// work survives untracked.
func TestScriptPostSpawnFailureKillsDescendants(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("descendant tracking needs /proc (Linux)")
	}
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	command := fmt.Sprintf("sleep 60 & echo $! > %s/descendant; sleep 60", work)
	start := time.Now()
	_, stderr, code := remote.run([]string{"EVENER_FENCE_FAULT_AFTER_SPAWN=1"}, "perform", epoch.BootID, "1", command)
	if code != 69 {
		t.Fatalf("faulted perform exited %d, want 69: %s", code, stderr)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("faulted perform took %s, want the command and its descendants killed", elapsed)
	}
	raw, err := os.ReadFile(filepath.Join(work, "descendant"))
	if err != nil {
		t.Fatalf("the command never ran: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse descendant pid %q: %v", raw, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(t, pid) {
		if time.Now().After(deadline) {
			killProcess(pid)
			t.Fatalf("descendant %d survived the tracking-failure kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processAlive reports whether pid names a live process, through kill -0 so the
// test needs no platform-specific syscalls.
func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	return exec.Command("sh", "-c", fmt.Sprintf("kill -0 %d 2>/dev/null", pid)).Run() == nil
}

// killProcess signals a pid the test started, for cleanup.
func killProcess(pid int) {
	_ = exec.Command("sh", "-c", fmt.Sprintf("kill -9 %d 2>/dev/null", pid)).Run()
}

// TestScriptDescendantIdentityRevalidated pins that a recorded descendant is
// ours only while it still carries the exact nonce and the recorded start
// token: a reused pid naming unrelated work reads clean and is never signaled.
func TestScriptDescendantIdentityRevalidated(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("descendant tracking needs /proc (Linux)")
	}
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
	// An unrelated live process: its pid is real, but it carries neither the
	// entry's nonce nor its recorded start token.
	unrelated := exec.Command("sh", "-c", "sleep 30")
	if err := unrelated.Start(); err != nil {
		t.Fatalf("start unrelated process: %v", err)
	}
	defer func() { _ = unrelated.Process.Kill(); _, _ = unrelated.Process.Wait() }()
	entryPath := filepath.Join(remote.state, "leases", entry.ID)
	raw, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	broken := string(raw)
	broken = strings.Replace(broken, fmt.Sprintf("pid\t%d\n", *entry.Ownership.PID),
		fmt.Sprintf("pid\t%d\n", unrelated.Process.Pid), 1)
	broken = strings.Replace(broken, fmt.Sprintf("pidStartTime\t%s\n", entry.Ownership.PIDStartTime),
		"pidStartTime\t999999999\n", 1)
	broken = strings.Replace(broken, "descendants\t\n",
		fmt.Sprintf("descendants\t%d:999999999\n", unrelated.Process.Pid), 1)
	if broken == string(raw) {
		t.Fatalf("entry carried nothing to rewrite: %q", raw)
	}
	if err := os.WriteFile(entryPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	stdout, stderr, code := remote.run(nil, "recheck", entry.ID)
	if code != 0 {
		t.Fatalf("recheck exited %d: %s", code, stderr)
	}
	recheck, err := DecodeRecheck([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeRecheck(%q) = %v", stdout, err)
	}
	if recheck.Live {
		t.Fatalf("recheck(unrelated descendant) = %+v, want clean: the pid is not ours", recheck)
	}
	if !processAlive(t, unrelated.Process.Pid) {
		t.Fatal("the unrelated process was signaled or killed")
	}
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("release the command: %v", err)
	}
	<-performDone
}

// TestScriptHolderMismatchRefuses pins that advance and perform require the
// lease holder to name the epoch they act on: a crash between the guard and
// holder writes leaves the takeover replay (which repairs it) as the only way
// forward.
func TestScriptHolderMismatchRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run([]string{"EVENER_FENCE_FAULT_AFTER_GUARD=1"}, "takeover", epoch.BootID, "1"); code == 0 {
		t.Fatalf("faulted takeover exited 0: %s", stderr)
	}
	if _, stderr, code := remote.run(nil, "advance", epoch.BootID, "1"); code == 0 {
		t.Fatal("advance past a missing holder succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("advance past a missing holder = %v, want ErrStateCorrupt", err)
	}
	// The takeover replay repairs the holder, and the advance may then run.
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "1"); code != 0 {
		t.Fatalf("takeover replay exited %d: %s", code, stderr)
	}
	if _, stderr, code := remote.run(nil, "advance", epoch.BootID, "1"); code != 0 {
		t.Fatalf("advance after repair exited %d: %s", code, stderr)
	}
	// A holder naming another epoch is corrupt too.
	if err := os.WriteFile(filepath.Join(remote.state, "leases", "holder"), []byte("boot-9 9\n"), 0o600); err != nil {
		t.Fatalf("overwrite holder: %v", err)
	}
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", "true"); code == 0 {
		t.Fatal("perform with a mismatched holder succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("perform with a mismatched holder = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptLeadingZeroNumbersRefuse pins that every numeric field the helper
// persists is canonical: a leading-zero value would be emitted verbatim into
// JSON, where it is not a number, so it is refused before it reaches the guard.
func TestScriptLeadingZeroNumbersRefuse(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "01"); code == 0 {
		t.Fatal("takeover with a leading-zero op sequence succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("takeover with a leading-zero op sequence = %v, want ErrMalformed", err)
	}
	remote.settle(epoch)
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	broken := strings.Replace(string(raw), "guardEpoch\t2\n", "guardEpoch\t02\n", 1)
	if broken == string(raw) {
		t.Fatalf("guard carried no guardEpoch to break: %q", raw)
	}
	if err := os.WriteFile(guard, []byte(broken), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	if _, stderr, code := remote.run(nil, "status"); code == 0 {
		t.Fatal("status with a leading-zero guard epoch succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("status with a leading-zero guard epoch = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptDualIdentityEntryRefuses pins the one-variant ownership rule on the
// shell side, matching Ownership.Validate: a file naming two identities is
// corrupt, never laundered into single-variant JSON.
func TestScriptDualIdentityEntryRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", "true"); code != 0 {
		t.Fatalf("perform exited %d: %s", code, stderr)
	}
	entriesDir := filepath.Join(remote.state, "leases")
	names, err := os.ReadDir(entriesDir)
	if err != nil {
		t.Fatalf("read leases: %v", err)
	}
	var entryPath string
	for _, name := range names {
		if name.Name() != "holder" {
			entryPath = filepath.Join(entriesDir, name.Name())
		}
	}
	if entryPath == "" {
		t.Fatal("no lease entry")
	}
	raw, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	// A pid-owned entry relabelled nonce-owned keeps its pid fields: two
	// identities in one entry.
	broken := strings.Replace(string(raw), "ownershipKind\tpid\n", "ownershipKind\tnonce\n", 1)
	if broken == string(raw) {
		t.Fatalf("entry carried no pid ownership to dual-identity: %q", raw)
	}
	if err := os.WriteFile(entryPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if _, stderr, code := remote.run(nil, "entries"); code == 0 {
		t.Fatal("entries with a dual-identity entry succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("entries = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptBootDashHighWaterRefuses pins validator parity with the Go boot-id
// rule: "-" is the absence sentinel, never a high-water boot id.
func TestScriptBootDashHighWaterRefuses(t *testing.T) {
	remote := newFenceRemote(t)
	remote.settle(Epoch{BootID: "boot-1", OpSeq: 1})
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	if err := os.WriteFile(guard, append(raw, []byte("boot.-\t1\n")...), 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	if _, stderr, code := remote.run(nil, "status"); code == 0 {
		t.Fatal("status with a boot.- high-water succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("status with a boot.- high-water = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptSignalExitsAndReleasesLock pins the interrupt semantics chosen
// from 08c §4/§9: an interrupt is a local transport failure, not a fencing
// decision, so the wrapper releases the exclusive lease, exits, and leaves any
// command it started running and tracked — its lease entry stays non-terminal
// and reads live, and a later fencing takeover's kill/wait addresses it. The
// command is started before the signal, so the test is deterministic and needs
// no fixed sleep.
func TestScriptSignalExitsAndReleasesLock(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	command := fmt.Sprintf("touch %s/started; exec sleep 30", work)
	cmd, err := remote.start(nil, "perform", epoch.BootID, "1", command)
	if err != nil {
		t.Fatalf("start perform: %v", err)
	}
	// The command has provably started (and the lease is held) before the
	// signal, so the test never races the spawn.
	waitForFile(t, filepath.Join(work, "started"))
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal helper: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the interrupted helper did not exit")
	}
	// The release runs on the exit path; poll rather than assume a schedule.
	lockFile := filepath.Join(remote.state, "lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(lockFile); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the interrupted helper left the exclusive lease held")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The started command stays tracked: its entry is non-terminal and reads
	// live under its recorded identity, never clean.
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v (err %v), want the interrupted command's entry", entries, err)
	}
	entry := entries[0]
	if entry.State != LeaseRunning && entry.State != LeaseRegistering {
		t.Fatalf("entry after interrupt = %+v, want a non-terminal, tracked command", entry)
	}
	if recheck := recheckID(t, remote, entry.ID); !recheck.Live {
		t.Fatalf("recheck(after interrupt) = %+v, want live: the command is still tracked", recheck)
	}
	if entry.Ownership.PID != nil {
		killProcess(*entry.Ownership.PID)
	}
}

// TestScriptNonceMatchIsExact pins the nonce identity boundary: a process whose
// EVENER_FENCE_NONCE value merely starts with (or extends) the command's nonce
// is not a descendant, never recorded, and never signaled.
func TestScriptNonceMatchIsExact(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("descendant tracking needs /proc (Linux)")
	}
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	// Two impostors: one carrying a proper prefix of the real nonce, one
	// carrying an extension of it. Neither may be read as the command's child.
	command := fmt.Sprintf(
		`EVENER_FENCE_NONCE=${EVENER_FENCE_NONCE%%??} sh -c 'sleep 30' & echo $! > %s/prefix.pid; `+
			`EVENER_FENCE_NONCE=${EVENER_FENCE_NONCE}EXTRA sh -c 'sleep 30' & echo $! > %s/extended.pid; echo done`,
		work, work)
	// File-backed streams: the surviving impostors would otherwise hold the
	// pipes open and keep the wait blocked for their full sleep.
	if combined, _, code := remote.runFile(nil, "perform", epoch.BootID, "1", command); code != 0 {
		t.Fatalf("perform exited %d: %s", code, combined)
	}
	defer func() {
		for _, name := range []string{"prefix.pid", "extended.pid"} {
			if raw, err := os.ReadFile(filepath.Join(work, name)); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
					killProcess(pid)
				}
			}
		}
	}()
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v (err %v), want one settled entry", entries, err)
	}
	entry := entries[0]
	if entry.State != LeaseExited || len(entry.Descendants) != 0 {
		t.Fatalf("entry = %+v, want exited with no descendants: the impostor nonces are not ours", entry)
	}
	if recheck := recheckID(t, remote, entry.ID); recheck.Live {
		t.Fatalf("recheck(prefix nonce) = %+v, want clean", recheck)
	}
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

// TestScriptCommandEscapingRoundTrip pins the lease file's JSON escaping: a
// command carrying backslashes, quotes, tabs, and newlines must survive
// perform -> entries -> decode byte-for-byte, with the file still one field per
// line.
func TestScriptCommandEscapingRoundTrip(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	command := "true # back\\slash \"double\" 'single' $HOME `tick`\n: # tab\there \\d"
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", command); code != 0 {
		t.Fatalf("perform exited %d: %s", code, stderr)
	}
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeEntries(%q) = %v", stdout, err)
	}
	if len(entries) != 1 || entries[0].Command != command {
		t.Fatalf("command round-trip = %q, want %q", entries[0].Command, command)
	}
}

// TestScriptTakeoverReplayRepairsHolder pins the takeover retry's
// reconciliation: a replay of a pending fence must leave the lease holder
// naming the fencing epoch, never a half-landed takeover.
func TestScriptTakeoverReplayRepairsHolder(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "1"); code != 0 {
		t.Fatalf("takeover exited %d: %s", code, stderr)
	}
	// A lost holder write: the fence is durable, the holder is not.
	holder := filepath.Join(remote.state, "leases", "holder")
	if err := os.WriteFile(holder, []byte("boot-9 9\n"), 0o600); err != nil {
		t.Fatalf("overwrite holder: %v", err)
	}
	stdout, stderr, code := remote.run(nil, "takeover", epoch.BootID, "1")
	if code != 0 {
		t.Fatalf("takeover replay exited %d: %s", code, stderr)
	}
	status, err := DecodeStatus([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeStatus = %v", err)
	}
	if status.Holder == nil || *status.Holder != epoch {
		t.Fatalf("replay holder = %+v, want %+v", status.Holder, epoch)
	}
	raw, err := os.ReadFile(holder)
	if err != nil || strings.TrimSpace(string(raw)) != "boot-1 1" {
		t.Fatalf("holder after replay = %q (err %v), want the fencing epoch", raw, err)
	}
}

// TestScriptTakeoverCrashBetweenWrites injects the crash the reconciliation
// exists for: the guard write lands, the holder write does not, and the retry
// repairs before reporting success.
func TestScriptTakeoverCrashBetweenWrites(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, stderr, code := remote.run([]string{"EVENER_FENCE_FAULT_AFTER_GUARD=1"}, "takeover", epoch.BootID, "1"); code == 0 {
		t.Fatalf("faulted takeover exited 0: %s", stderr)
	}
	holder := filepath.Join(remote.state, "leases", "holder")
	if _, err := os.Stat(holder); err == nil {
		raw, _ := os.ReadFile(holder)
		t.Fatalf("faulted takeover left a holder %q, want the crash window", raw)
	}
	// The fence is durable, so the retry is a replay that repairs the holder.
	if _, stderr, code := remote.run(nil, "takeover", epoch.BootID, "1"); code != 0 {
		t.Fatalf("retry exited %d: %s", code, stderr)
	}
	raw, err := os.ReadFile(holder)
	if err != nil || strings.TrimSpace(string(raw)) != "boot-1 1" {
		t.Fatalf("holder after retry = %q (err %v), want the fencing epoch", raw, err)
	}
	if status := remote.status(); status.Holder == nil || *status.Holder != epoch || status.Fence == nil {
		t.Fatalf("status after retry = %+v, want the fence with the repaired holder", status)
	}
}

// TestScriptOldBootEpochStaysStale pins the durable per-boot high-water: after
// boots A, B, and C settle, the old boot-A epoch can never take over again,
// while a newer epoch of boot A still may.
func TestScriptOldBootEpochStaysStale(t *testing.T) {
	remote := newFenceRemote(t)
	for _, boot := range []string{"boot-a", "boot-b", "boot-c"} {
		remote.settle(Epoch{BootID: boot, OpSeq: 1})
	}
	_, stderr, code := remote.run(nil, "takeover", "boot-a", "1")
	if code == 0 {
		t.Fatal("old boot-A takeover succeeded, want refusal")
	}
	if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("old boot-A takeover = %v, want ErrStaleEpoch", err)
	}
	if _, stderr, code := remote.run(nil, "takeover", "boot-a", "2"); code != 0 {
		t.Fatalf("newer boot-A epoch refused: %d: %s", code, stderr)
	}
}

// TestScriptUnknownStateKeysRefuse pins the validator parity with the Go
// decoder's DisallowUnknownFields: a guard or entry file carrying a key the
// writer never emits is corrupt, never silently accepted.
func TestScriptUnknownStateKeysRefuse(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	guard := filepath.Join(remote.state, "guard")
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	if err := os.WriteFile(guard, append(raw, []byte("unexpectedKey\t1\n")...), 0o600); err != nil {
		t.Fatalf("add unknown guard key: %v", err)
	}
	for _, op := range [][]string{{"status"}, {"advance", "boot-1", "1"}} {
		if _, stderr, code := remote.run(nil, op...); code == 0 {
			t.Fatalf("%v on an unknown guard key succeeded, want refusal", op)
		} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("%v = %v, want ErrStateCorrupt", op, err)
		}
	}
	// Put a valid guard back and do the same for a lease entry.
	if err := os.WriteFile(guard, raw, 0o600); err != nil {
		t.Fatalf("restore guard: %v", err)
	}
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", "true"); code != 0 {
		t.Fatalf("perform exited %d: %s", code, stderr)
	}
	entriesDir := filepath.Join(remote.state, "leases")
	names, err := os.ReadDir(entriesDir)
	if err != nil {
		t.Fatalf("read leases: %v", err)
	}
	var entryPath string
	for _, name := range names {
		if name.Name() != "holder" {
			entryPath = filepath.Join(entriesDir, name.Name())
		}
	}
	if entryPath == "" {
		t.Fatal("no lease entry to corrupt")
	}
	entryRaw, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if err := os.WriteFile(entryPath, append(entryRaw, []byte("unexpectedKey\t1\n")...), 0o600); err != nil {
		t.Fatalf("add unknown entry key: %v", err)
	}
	if _, stderr, code := remote.run(nil, "entries"); code == 0 {
		t.Fatal("entries with an unknown entry key succeeded, want refusal")
	} else if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("entries = %v, want ErrStateCorrupt", err)
	}
}

// TestScriptIgnoresStrayTempFiles pins the reader contract for the atomic-write
// temp files: a stray dot-named temp in the lease directory is never an entry,
// so entries/status can not read a half-written record.
func TestScriptIgnoresStrayTempFiles(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", "true"); code != 0 {
		t.Fatalf("perform exited %d: %s", code, stderr)
	}
	for _, name := range []string{".tmp.entry.stray", ".tmp.stray"} {
		if err := os.WriteFile(filepath.Join(remote.state, "leases", name), []byte("junk"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v (err %v), want the one real entry", entries, err)
	}
	if status := remote.status(); status.Entries != 1 {
		t.Fatalf("status entry count = %d, want 1 (the strays are not entries)", status.Entries)
	}
}

// TestScriptPostSpawnWriteFailureKillsChild pins the fail-closed I/O path: when
// the running-entry write fails after the spawn, the wrapper kills the command
// it just started and refuses, never returning while the command runs
// untracked.
func TestScriptPostSpawnWriteFailureKillsChild(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	command := fmt.Sprintf("touch %s/started; exec sleep 30", work)
	start := time.Now()
	_, stderr, code := remote.run([]string{"EVENER_FENCE_FAULT_AFTER_SPAWN=1"}, "perform", epoch.BootID, "1", command)
	if code != 69 {
		t.Fatalf("faulted perform exited %d, want 69: %s", code, stderr)
	}
	if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrHelperIO) {
		t.Fatalf("faulted perform = %v, want ErrHelperIO", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("faulted perform took %s, want the child killed rather than awaited", elapsed)
	}
	if _, err := os.Stat(filepath.Join(work, "started")); err != nil {
		t.Fatalf("the command never started: %v", err)
	}
}

// TestScriptConcurrentPerformsSerialize pins the exclusive lease: concurrent
// wrapper invocations never overlap, so exactly one critical section runs at a
// time.
func TestScriptConcurrentPerformsSerialize(t *testing.T) {
	remote := newFenceRemote(t)
	epoch := Epoch{BootID: "boot-1", OpSeq: 1}
	remote.settle(epoch)
	work := t.TempDir()
	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for i := range 4 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mine := filepath.Join(work, fmt.Sprintf("in-cs-%d", n))
			command := fmt.Sprintf(
				`touch %s; others=0; for f in %s/in-cs-*; do [ "$f" = "%s" ] || others=$((others+1)); done; `+
					`if [ "$others" -ne 0 ]; then exit 3; fi; sleep 0.1; rm -f %s`,
				mine, work, mine, mine)
			if _, stderr, code := remote.run(nil, "perform", epoch.BootID, "1", command); code != 0 {
				failures <- fmt.Sprintf("perform %d exited %d: %s", n, code, stderr)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	stdout, stderr, code := remote.run(nil, "entries")
	if code != 0 {
		t.Fatalf("entries exited %d: %s", code, stderr)
	}
	entries, err := DecodeEntries([]byte(stdout))
	if err != nil || len(entries) != 4 {
		t.Fatalf("entries = %d (err %v), want the four serialized commands", len(entries), err)
	}
}

// TestScriptRefusesDashBootID pins that the state files' absence sentinel is
// never a fencing epoch: "-" refuses as malformed rather than serializing as an
// absent epoch.
func TestScriptRefusesDashBootID(t *testing.T) {
	remote := newFenceRemote(t)
	for _, op := range [][]string{{"takeover", "-", "1"}, {"advance", "-", "1"}, {"perform", "-", "1", "true"}} {
		_, stderr, code := remote.run(nil, op...)
		if code == 0 {
			t.Fatalf("%v with boot id \"-\" succeeded, want refusal", op)
		}
		if err := DecodeRefusal([]byte(stderr)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%v = %v, want ErrMalformed", op, err)
		}
	}
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
	// Refusals wear the helper's own marker, so a wrapped command's stderr can
	// never masquerade as one: the prefix, then exactly one JSON object.
	if !strings.HasPrefix(stderr, RefusalPrefix) {
		t.Fatalf("refusal %q does not start with %q", stderr, RefusalPrefix)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(stderr, RefusalPrefix)), &raw); err != nil {
		t.Fatalf("refusal is not one JSON object after its prefix: %q", stderr)
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

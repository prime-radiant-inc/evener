package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// scratchRetentionBase overrides the allocator's base discovery to a single
// test-owned base and returns a workspace root outside it.
func scratchRetentionBase(t *testing.T) (base, workspace string) {
	t.Helper()
	base, workspace = t.TempDir(), t.TempDir()
	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return base }
	sessionScratchUserCacheDir = func() (string, error) { return base, nil }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })
	// SweepCrashedSessionScratch also walks every world-usable host temp base a
	// session temp container may live in. These tests assert that an aged fixture
	// is (or is not) collected, so letting the sweep reach the machine's real /tmp
	// would make them depend on ambient state — and a stale container this process
	// cannot remove would turn "err == nil" assertions into flakes. Confine it.
	t.Cleanup(SetWorldTempBasesForTesting(nil))
	return base, workspace
}

// TestScratchRetentionStartupSweepKeepsAgedRequiredArtifact is the plan's
// collector test: a pinned, aged, unreleased required artifact survives the
// startup sweep and restores at its original path; once released, the same
// directory is collected.
func TestScratchRetentionStartupSweepKeepsAgedRequiredArtifact(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
	artifact := filepath.Join(scratch.Dir, "required.bin")
	want := []byte("opaque-required-artifact")
	if err := os.WriteFile(artifact, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep with a retained pin: %v", err)
	}
	restored, err := OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) || restored.Dir != scratch.Dir {
		t.Fatalf("original required artifact lost: bytes=%q dir=%s err=%v", got, restored.Dir, err)
	}
	if err := restored.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep after release: %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("unreferenced scratch not collected: %v", err)
	}
}

// TestScratchRetentionUnreferencedReleasedStillCollected proves a Released
// tombstone authorizes ordinary age-based collection while an unreleased
// reference (covered above) does not.
func TestScratchRetentionUnreferencedReleasedStillCollected(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "sandbox"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("LoadScratchRetention: %v", err)
	}
	if manifest.Released || len(manifest.References) != 1 || manifest.Revision == 0 {
		t.Fatalf("pinned manifest = %+v", manifest)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(scratch.Dir); !os.IsNotExist(err) {
		t.Fatalf("released scratch not collected: %v", err)
	}
}

// TestScratchRetentionReleaseFinishesCleanupWhenTheWriteReportsACommittedFailure
// pins round 77's Medium: ReleaseScratchRetention returned on a write error
// even when the manifest rename had already committed — the post-rename
// failure class every other writer recognizes — so the tombstone was durable
// while the pin cleanup it authorizes was skipped, leaving
// otherwise-reclaimable directories pinned under the committed release.
func TestScratchRetentionReleaseFinishesCleanupWhenTheWriteReportsACommittedFailure(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "sandbox"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	// Free the directory's own lease so the release's cleanup can acquire it.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	before, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	restore := SetScratchManifestWriteProbeForTesting(func() error {
		return errors.New("probe: post-rename fsync failure")
	})
	defer restore()
	err = ReleaseScratchRetention(owner)
	// The pin cleanup the committed tombstone authorizes must have run: the
	// directory's own pin is gone, so the sweep can actually collect the
	// scratch the release released.
	if _, statErr := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(statErr) {
		t.Fatalf("the committed release left the directory pinned (release err=%v, stat=%v): the tombstone authorizes collection the pin now blocks", err, statErr)
	}
	if err != nil {
		t.Fatalf("the release reported its committed write as failed: %v", err)
	}
	after, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !after.Released || after.Revision != before.Revision+1 {
		t.Fatalf("the committed release is not durable: %+v", after)
	}
}

// TestSweepRemovalSerializesWithTheManifestReset pins the round-31 finding:
// the sweep's retention check reads the Released tombstone without any lock
// the reset's resurrection takes, so a ResetScratchRetentionIfReleased that
// carries the directory's rows into an unreleased manifest can commit between
// the check and the removal. The sweep would then RemoveAll the scratch the
// resurrected manifest names — the retained allocation is lost with the
// manifest permanently pointing at deleted scratch. The serialization makes
// both orders safe: a reset that runs first leaves !Released for the sweep's
// check to read, and one that comes second finds the directory gone and the
// pair dies with the tombstone.
func TestSweepRemovalSerializesWithTheManifestReset(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	artifact := filepath.Join(scratch.Dir, "retained.bin")
	if err := os.WriteFile(artifact, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	// The carry needs the full graph: the lease-owning binding plus a
	// consumer naming it (round 16).
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Tombstone while the lease is still held, so the pin survives the
	// release (round 25): Released:true is exactly what makes the sweep read
	// the aged directory collectible.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}

	atWindow := make(chan struct{})
	proceed := make(chan struct{})
	sweepDone := make(chan error, 1)
	scratchSweepBeforeRemove = func() {
		atWindow <- struct{}{}
		<-proceed
	}
	t.Cleanup(func() { scratchSweepBeforeRemove = nil })

	go func() { sweepDone <- SweepCrashedSessionScratch(workspace) }()
	<-atWindow

	// The serialized window now holds the pin owner's MANIFEST lock too
	// (round 67): the reset runs under that same lock, so nothing — in this
	// process or any other sharing the state directory — can commit a carried
	// manifest while the sweep is inside the window. A lock the test can take
	// here means the sweep never held it, which is the deserialized tree.
	err := WithScratchRetentionLock(owner, func() error { return nil })
	if err == nil {
		t.Fatal("the sweep entered its removal window without the manifest lock: a concurrent reset carries and commits inside it")
	}
	if !errors.Is(err, ErrScratchRetentionLockHeld) {
		t.Fatalf("probe lock error = %v, want the lock-held sentinel", err)
	}
	close(proceed)

	select {
	case err := <-sweepDone:
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sweep did not finish")
	}

	// The reset that comes second finds the directory gone and its pair dies
	// with the tombstone: nothing the committed manifest names was removed.
	if _, _, err := ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("reset after the sweep: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	namesDir := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			namesDir = true
		}
	}
	_, statErr := os.Stat(scratch.Dir)
	if !manifest.Released && namesDir && os.IsNotExist(statErr) {
		t.Fatalf("the sweep removed a directory the concurrent reset had carried: the resurrected manifest names deleted scratch %s", scratch.Dir)
	}
}

// TestSweepRemovalDoesNotHoldTheManifestLockThroughTheRemoval pins the cost of
// holding the pin owner's manifest lock across the sweep's removal: the
// removal's duration scales with the removed directory's contents, while
// every other operation on the same root — here a sibling allocation's cold
// open — retries the lock with a writer-sized budget. A large candidate's
// removal outlasts that budget, so the open exhausts and the restore it
// serves fails. The sweep must invalidate the candidate with an atomic rename
// while the locks are held and remove the dead tombstone outside every lock,
// so the open contends only with millisecond-scale checks and reaches the
// manifest's own released verdict.
func TestSweepRemovalDoesNotHoldTheManifestLockThroughTheRemoval(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	victim := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	sibling := pinnedScratch(t, base, workspace, owner, ScratchKindUnsandboxed)
	// The victim's removal must outlast a writer-sized retry budget many times
	// over, so plant enough empty files for the recursive delete to take
	// hundreds of milliseconds. Plant before aging: creating files refreshes
	// the directory's mtime, which is the sweep's age gate.
	for i := range 100_000 {
		f, err := os.Create(filepath.Join(victim.Dir, fmt.Sprintf("filler-%06d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// One binding carries both slots, so both allocations answer to the same
	// manifest and the same lock.
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox:     {Dir: victim.Dir, OwnsLease: true},
		ScratchKindUnsandboxed: {Dir: sibling.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Tombstone while both leases are still held, so the pins survive the
	// release (round 25); then free both leases — the sweep needs the
	// victim's, the sibling's open needs its own.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := victim.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := sibling.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(victim.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	// The sibling stays un-aged: the sweep must never consider it.

	atWindow := make(chan struct{})
	proceed := make(chan struct{})
	sweepDone := make(chan error, 1)
	scratchSweepBeforeRemove = func() {
		atWindow <- struct{}{}
		<-proceed
	}
	t.Cleanup(func() { scratchSweepBeforeRemove = nil })

	go func() { sweepDone <- SweepCrashedSessionScratch(workspace) }()
	<-atWindow
	close(proceed)

	// The sibling's cold open retries the same manifest lock with the standard
	// writer-sized budget. While the sweep held the lock across its removal
	// this exhausted; with the invalidating rename the lock frees in
	// milliseconds and the open reaches the manifest's released verdict.
	openErr := RetryScratchLockContention(func() error {
		_, err := OpenRetainedSessionScratch(owner, ScratchReference{Dir: sibling.Dir, Kind: ScratchKindUnsandboxed})
		return err
	})
	if errors.Is(openErr, ErrScratchRetentionLockHeld) {
		t.Fatalf("the sibling allocation's cold-open exhausted its lock budget while the sweep held the manifest lock through its removal: %v", openErr)
	}
	if !errors.Is(openErr, ErrScratchRetentionReleased) {
		t.Fatalf("the sibling allocation's cold-open must reach the manifest's released verdict, not fail otherwise: %v", openErr)
	}

	select {
	case err := <-sweepDone:
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sweep did not finish")
	}
	if _, statErr := os.Stat(victim.Dir); !os.IsNotExist(statErr) {
		t.Fatalf("the aged victim survived the sweep: stat error = %v", statErr)
	}
}

// TestSweepReclaimsCrashedReclaimingTombstones pins the crash-window half of
// the tombstone design: the sweep renames a candidate to a dot-prefixed
// tombstone before removing it, and a sweep that crashes between the rename
// and the removal leaves that tombstone behind — a shape the sweep's own
// prefix filter can never enumerate, so without explicit reclamation the
// directory is orphaned forever. A later sweep must recognize and remove
// stale tombstones, while a tombstone whose lease is still held — a live
// remover mid-removal in another pass or process — must be left alone.
func TestSweepReclaimsCrashedReclaimingTombstones(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	tombstoneName := func(dir string) string {
		return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".reclaiming")
	}

	// A crashed sweeper's leftover: the candidate passed every gate, was
	// renamed, and the process died before the removal. Its lease is free
	// (process death closes the flock) and its mtime is the aged candidate's.
	crashed := pinnedScratch(t, base, workspace, retentionOwner(t), ScratchKindSandbox)
	if err := crashed.Retain(); err != nil {
		t.Fatal(err)
	}
	crashedTombstone := tombstoneName(crashed.Dir)
	if err := os.Rename(crashed.Dir, crashedTombstone); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(crashedTombstone, aged, aged); err != nil {
		t.Fatal(err)
	}

	// A live remover's tombstone: the rename happened, the removal has not,
	// and the remover still holds the directory lease.
	live := pinnedScratch(t, base, workspace, retentionOwner(t), ScratchKindUnsandboxed)
	liveTombstone := tombstoneName(live.Dir)
	if err := os.Rename(live.Dir, liveTombstone); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(liveTombstone, aged, aged); err != nil {
		t.Fatal(err)
	}
	// live's lease stays held — Retain is deliberately not called.

	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, statErr := os.Stat(crashedTombstone); !os.IsNotExist(statErr) {
		t.Fatalf("the crashed sweeper's tombstone survived: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(liveTombstone); statErr != nil {
		t.Fatalf("a tombstone whose lease is held must be left for its live remover: %v", statErr)
	}
}

// TestSweepRemovalSerializesWithAnotherProcessReset pins the cross-process
// half of the round-67 serialization: the reclamation mutex is process-local,
// so a reset in ANOTHER process sharing the state directory used to carry the
// reference and commit an unreleased manifest while this process's sweep held
// the directory lease mid-removal — the sweep then deleted the scratch the
// resurrected manifest named. The pin owner's manifest lock is the durable
// mutex: the child process's sweep holds it across its window, and the reset
// below runs in THIS process, so nothing about its serialization rides the
// in-process mutex. On the serialized tree the in-window reset exhausts its
// lock retries and reports the retryable lock-held verdict; on the
// deserialized tree it commits, and the assertions see the removed directory
// the committed manifest still names.
func TestSweepRemovalSerializesWithAnotherProcessReset(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	artifact := filepath.Join(scratch.Dir, "retained.bin")
	if err := os.WriteFile(artifact, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}

	signals := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSweepReclaimChildProcess$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		"EVENER_TEST_SWEEP_CHILD=1",
		"EVENER_TEST_SWEEP_BASE="+base,
		"EVENER_TEST_SWEEP_WORKSPACE="+workspace,
		"EVENER_TEST_SWEEP_SIGNALS="+signals,
	)
	var childLog bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childLog, &childLog
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the sweep child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	// Wait for the child to park inside its removal window. The poll is a
	// POSITIVE wait on a file the child writes under every lock the window
	// holds, so the instant it appears the window is entered.
	waitForFile(t, filepath.Join(signals, "at-window"), 30*time.Second)

	// The reset from THIS process, inside the window.
	_, _, resetErr := ResetScratchRetentionIfReleased(owner)
	committedEarly := resetErr == nil
	if resetErr != nil && !errors.Is(resetErr, ErrScratchRetentionLockHeld) {
		t.Fatalf("reset in the sweep's window: %v", resetErr)
	}

	if err := os.WriteFile(filepath.Join(signals, "proceed"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		t.Fatalf("sweep child: %v\n%s", waitErr, childLog.String())
	}

	// The reset that came second finds the directory gone and its pair dies
	// with the tombstone.
	if !committedEarly {
		if _, _, err := ResetScratchRetentionIfReleased(owner); err != nil {
			t.Fatalf("reset after the sweep: %v", err)
		}
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	namesDir := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			namesDir = true
		}
	}
	_, statErr := os.Stat(scratch.Dir)
	if !manifest.Released && namesDir && os.IsNotExist(statErr) {
		t.Fatalf("the sweep removed a directory the concurrent reset had carried: the resurrected manifest names deleted scratch %s", scratch.Dir)
	}
}

// TestSweepReclaimChildProcess is the child half of
// TestSweepRemovalSerializesWithAnotherProcessReset: it re-execs the test
// binary to run the sweep in a separate process, confined to the parent
// fixture's base, parked inside its removal window until the parent proceeds.
func TestSweepReclaimChildProcess(t *testing.T) {
	base := os.Getenv("EVENER_TEST_SWEEP_BASE")
	workspace := os.Getenv("EVENER_TEST_SWEEP_WORKSPACE")
	signals := os.Getenv("EVENER_TEST_SWEEP_SIGNALS")
	if os.Getenv("EVENER_TEST_SWEEP_CHILD") == "" || base == "" || workspace == "" || signals == "" {
		t.Skip("the reclamation child runs only under the cross-process test")
	}
	// Confine this process's discovery exactly the way the parent's fixture
	// did: one base, no world walk.
	sessionScratchTempDir = func() string { return base }
	sessionScratchUserCacheDir = func() (string, error) { return base, nil }
	defer SetWorldTempBasesForTesting(nil)()
	scratchSweepBeforeRemove = func() {
		if err := os.WriteFile(filepath.Join(signals, "at-window"), []byte("1"), 0600); err != nil {
			t.Errorf("signal the window: %v", err)
			return
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(signals, "proceed")); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Error("the parent never proceeded")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("child sweep: %v", err)
	}
}

// waitForFile polls a positive file condition with a bounded deadline, failing
// the test with desc on timeout. Starvation only delays the appearance, never
// misses it, so the poll never flakes.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestScratchRetentionReportsLeaseAcquisitionFailure is the regression test for
// the swallowed lease error: ReleaseScratchRetention must continue for confirmed
// contention (a genuinely held lease is left for the collector) but must REPORT
// a real acquisition failure (open/stat/chmod), so a release cannot look
// successful when a lease could not be inspected or released.
func TestScratchRetentionReportsLeaseAcquisitionFailure(t *testing.T) {
	t.Run("contention_stays_quiet", func(t *testing.T) {
		base, workspace := scratchRetentionBase(t)
		owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
		scratch, err := NewSessionScratch(base, workspace)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = scratch.Cleanup() }()
		ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
		if err := scratch.Pin(owner, ref); err != nil {
			t.Fatal(err)
		}
		// The live lease is still held (Retain was not called), so the release
		// must skip the directory without reporting a failure.
		if err := ReleaseScratchRetention(owner); err != nil {
			t.Fatalf("a held lease must not be reported as a release failure: %v", err)
		}
	})

	t.Run("non_contention_error_reported", func(t *testing.T) {
		base, workspace := scratchRetentionBase(t)
		owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
		scratch, err := NewSessionScratch(base, workspace)
		if err != nil {
			t.Fatal(err)
		}
		ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
		if err := scratch.Pin(owner, ref); err != nil {
			t.Fatal(err)
		}
		if err := scratch.Retain(); err != nil {
			t.Fatal(err)
		}
		// Replace the lease file with a directory: acquireScratchLease's
		// open(O_RDWR) of a directory fails for a reason that is not contention
		// (EWOULDBLOCK/EAGAIN), so the release must surface it.
		leasePath := filepath.Join(scratch.Dir, sessionScratchLeaseName)
		if err := os.Remove(leasePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(leasePath, 0o700); err != nil {
			t.Fatal(err)
		}
		err = ReleaseScratchRetention(owner)
		if err == nil {
			t.Fatal("non-contention lease failure was silently swallowed as contention")
		}
		if !strings.Contains(err.Error(), scratch.Dir) {
			t.Fatalf("reported error does not identify the affected directory: %v", err)
		}
	})
}

// TestScratchRetentionPinFailurePreventsLeaseRelease proves a failed pin leaves
// the live ownership untouched: the lease stays held and no reference is
// committed, so the allocation cannot be silently collected around failure.
func TestScratchRetentionPinFailurePreventsLeaseRelease(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	// A reference naming a different directory must be refused, not written.
	if err := scratch.Pin(owner, ScratchReference{Dir: filepath.Join(base, "elsewhere"), Kind: "sandbox"}); err == nil {
		t.Fatal("Pin accepted a reference for a foreign directory")
	}
	if scratch.lease == nil {
		t.Fatal("failed Pin released the live scratch lease")
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 0 {
		t.Fatalf("failed Pin committed references: %+v", manifest.References)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("failed Pin wrote a directory pin: %v", err)
	}
}

// TestScratchRetentionRestoreSweepBothOrders proves the retention check and
// removal are ordered safely: sweep-then-open and open-then-sweep both preserve
// the artifact, and open refuses once the owner's manifest is released.
func TestScratchRetentionRestoreSweepBothOrders(t *testing.T) {
	for _, order := range []string{"sweep-open", "open-sweep"} {
		t.Run(order, func(t *testing.T) {
			base, workspace := scratchRetentionBase(t)
			owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
			scratch, err := NewSessionScratch(base, workspace)
			if err != nil {
				t.Fatal(err)
			}
			ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
			artifact := filepath.Join(scratch.Dir, "keep.bin")
			if err := os.WriteFile(artifact, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := scratch.Pin(owner, ref); err != nil {
				t.Fatal(err)
			}
			if err := scratch.Retain(); err != nil {
				t.Fatal(err)
			}
			aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
			if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
				t.Fatal(err)
			}
			if order == "sweep-open" {
				if err := SweepCrashedSessionScratch(workspace); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := OpenRetainedSessionScratch(owner, ref)
			if err != nil {
				t.Fatalf("open retained scratch: %v", err)
			}
			if order == "open-sweep" {
				if err := SweepCrashedSessionScratch(workspace); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := os.ReadFile(artifact); err != nil || string(got) != "keep" {
				t.Fatalf("artifact after %s: %q err=%v", order, got, err)
			}
			if err := restored.Retain(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestScratchRetentionConflictingOrUnreadablePin proves a malformed or
// conflicting pin conservatively retains its directory and surfaces a bounded
// diagnostic instead of being treated as collectible.
func TestScratchRetentionConflictingOrUnreadablePin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	dir := filepath.Join(base, sessionScratchPrefix+"conflict")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, scratchPinName), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	err := SweepCrashedSessionScratch(workspace)
	if err == nil {
		t.Fatal("sweep accepted a malformed pin with no diagnostic")
	}
	if !strings.Contains(err.Error(), "retention pin") {
		t.Fatalf("diagnostic did not name the retention pin: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("malformed pin did not conservatively retain its directory: %v", statErr)
	}
}

// TestScratchRetentionPinBeforeReferenceOrdering proves the directory pin is
// durable before the manifest reference: after Pin, both exist, and each names
// the same immutable identity.
func TestScratchRetentionPinBeforeReferenceOrdering(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	ref := ScratchReference{Dir: scratch.Dir, Kind: "sandbox"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	pinned, err := readScratchDirectoryPin(scratch.Dir)
	if err != nil {
		t.Fatalf("directory pin: %v", err)
	}
	if pinned.Owner != owner || pinned.Dir != filepath.Clean(scratch.Dir) || pinned.Kind != ref.Kind {
		t.Fatalf("pin identity = %+v", pinned)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 1 || filepath.Clean(manifest.References[0].Dir) != filepath.Clean(ref.Dir) {
		t.Fatalf("manifest references = %+v", manifest.References)
	}
}

// TestScratchRetentionFailedManifestPublicationRemovesThePin is the regression
// test for the unreclaimable-pin leak. Pin writes the directory pin FIRST (so a
// concurrent collector cannot collect the directory in the window before the
// manifest reference exists) and publishes the reference second. When that
// publication fails after the pin is durable, the pin used to survive with no
// manifest reference, and ReleaseScratchRetention only removes pins listed in
// manifest.References, so the directory stayed pinned against collection
// forever. A failed publication must leave the allocation exactly as the failed
// call found it: no pin, no reference, and therefore collectible again.
//
// The failure is a real filesystem condition, not a hook: a successful first Pin
// creates the retention directory, lock and manifest, and chmod'ing that
// directory to 0o500 leaves acquireScratchRetentionLock working (the lock file
// already exists and the search bit is intact) and writeScratchDirectoryPin
// working (a pin lives in the scratch directory) while
// atomicWritePrivateFile's os.CreateTemp of the manifest temp file fails EACCES.
func TestScratchRetentionFailedManifestPublicationRemovesThePin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only retention directory cannot be created as root")
	}
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	// A first successful pin creates the retention directory, its lock and the
	// manifest the second call will fail to extend.
	existing := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	failing, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = failing.Cleanup() })
	retentionDir := scratchRetentionDir(owner)
	if err := os.Chmod(retentionDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(retentionDir, 0o700) })

	err = failing.Pin(owner, ScratchReference{Dir: failing.Dir, Kind: ScratchKindUnsandboxed})
	if err == nil {
		t.Fatal("Pin succeeded although the manifest could not be published")
	}
	pinPath := filepath.Join(failing.Dir, scratchPinName)
	if _, statErr := os.Stat(pinPath); !os.IsNotExist(statErr) {
		t.Fatalf("a failed manifest publication left the pin %q behind: the directory is pinned against collection and no manifest reference lets ReleaseScratchRetention reclaim it (stat err = %v)", pinPath, statErr)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 1 || filepath.Clean(manifest.References[0].Dir) != filepath.Clean(existing.Dir) {
		t.Fatalf("failed publication changed the manifest references: %+v", manifest.References)
	}
	// The preexisting pin the failed call was never allowed to touch is intact.
	if pin, pinErr := readScratchDirectoryPin(existing.Dir); pinErr != nil || pin.Owner != owner {
		t.Fatalf("failed publication damaged the preexisting pin: %+v err=%v", pin, pinErr)
	}
	// The consequence: ordinary age-based collection can now reclaim the
	// directory, which it could not while the orphan pin outlived the call.
	if err := failing.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(failing.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep after the failed publication: %v", err)
	}
	if _, statErr := os.Stat(failing.Dir); !os.IsNotExist(statErr) {
		t.Fatalf("the directory orphaned by the failed publication was not reclaimed: %v", statErr)
	}
}

// TestScratchRetentionRollbackKeepsAPublishedPin proves the rollback's
// published-reference guard: atomicWritePrivateFile commits the manifest rename
// before it fsyncs the containing directory, so a writeScratchRetention error
// can arrive after the reference is already durable. Removing the pin then would
// leave a directory collectible while the manifest still claims it, so the
// rollback must re-read the manifest and leave a published pin exactly as it is.
func TestScratchRetentionRollbackKeepsAPublishedPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	dir, err := canonicalScratchPath(scratch.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackUnpublishedScratchPin(owner, dir, ScratchKindSandbox); err != nil {
		t.Fatalf("rollback reported an error for a published pin: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, scratchPinName)); statErr != nil {
		t.Fatalf("rollback removed a pin the manifest still references: %v", statErr)
	}
	retained, retainErr := ScratchDirectoryRetained(dir)
	if retainErr != nil || !retained {
		t.Fatalf("published directory = retained %v err %v, want retained with no diagnostic", retained, retainErr)
	}
}

// TestScratchRetentionPinScratchBindingRollsBackAFailedCallsPins proves the
// atomic writer leaves no durable trace when a later allocation's pin fails: the
// earlier allocation's directory pin, written before the failure, is removed
// again, no reference is appended and the binding is not published, so the
// allocation is exactly as collectible as the failed call found it.
func TestScratchRetentionPinScratchBindingRollsBackAFailedCallsPins(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only allocation directory cannot be created as root")
	}
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	first, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Cleanup() })
	second, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Cleanup() })
	// A read-only allocation directory denies the pin file its write. The kinds
	// are pinned in sorted order, so first is pinned before second fails.
	if err := os.Chmod(second.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(second.Dir, 0o700) })

	err = PinScratchBinding(owner, retentionBinding("E0", owner.RootSessionID, workspace, nil), map[string]*SessionScratch{
		ScratchKindSandbox:     first,
		ScratchKindUnsandboxed: second,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), second.Dir) {
		t.Fatalf("PinScratchBinding error = %v, want the failed pin of %q (the allocation pinned second)", err, second.Dir)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 0 || len(manifest.Bindings) != 0 {
		t.Fatalf("failed call left durable state: references=%+v bindings=%+v", manifest.References, manifest.Bindings)
	}
	if _, statErr := os.Stat(filepath.Join(first.Dir, scratchPinName)); !os.IsNotExist(statErr) {
		t.Fatalf("the pin of the allocation pinned before the failure survived it: %v", statErr)
	}
}

// TestScratchRetentionPinScratchBindingRollsBackARepairedPin proves the atomic
// writer also undoes a pin it created repairing a reference the manifest
// already listed. The repair's listing predates the call, so the pre-call
// state is a listed-but-unpinned — collectible — directory, and a failed
// transaction must leave exactly that behind instead of an orphaned pin no
// rollback or release will ever name (round 35).
func TestScratchRetentionPinScratchBindingRollsBackARepairedPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	dir, err := canonicalScratchPath(scratch.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := PinScratchBinding(owner, retentionBinding("E0", owner.RootSessionID, workspace, nil), map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatal(err)
	}
	// The repair premise: the manifest lists the reference, but its pin is
	// gone, so the directory is collectible exactly as the call finds it.
	if err := os.Remove(filepath.Join(dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	// The binding update fails after the repair: E1 carries a slot for a
	// directory no reference lists, which the update's validation rejects.
	binding := retentionBinding("E1", owner.RootSessionID, workspace, map[string]ScratchSlot{
		ScratchKindUnsandboxed: {Dir: filepath.Join(workspace, "unreferenced"), OwnsLease: true},
	})
	err = PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil)
	if err == nil || !strings.Contains(err.Error(), "unpinned directory") {
		t.Fatalf("PinScratchBinding error = %v, want the binding update's rejection of the unreferenced slot", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 1 || len(manifest.Bindings) != 1 {
		t.Fatalf("failed repair left durable state: references=%+v bindings=%+v", manifest.References, manifest.Bindings)
	}
	if _, statErr := os.Stat(filepath.Join(dir, scratchPinName)); !os.IsNotExist(statErr) {
		t.Fatalf("the failed repair left an orphaned pin over the listed %q: %v", dir, statErr)
	}
}

// TestScratchRetentionKeepsARepairedPinWhenTheWriteCommits pins round 39's
// High: writeScratchRetention can report a failure after the manifest rename
// already committed, and a committed transaction keeps the pin it created
// repairing a listed reference — the manifest's revision advanced, so the
// repair is part of the durable coherent state, and rolling it back would
// leave the committed reference unpinned and its directory collectible. The
// repair rolls back only when the transaction definitely failed before the
// commit.
func TestScratchRetentionKeepsARepairedPinWhenTheWriteCommits(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	dir, err := canonicalScratchPath(scratch.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := PinScratchBinding(owner, retentionBinding("E0", owner.RootSessionID, workspace, nil), map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatal(err)
	}
	pre, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	// The repair premise: the manifest lists the reference, but its pin is
	// gone, so the directory is collectible exactly as the call finds it.
	if err := os.Remove(filepath.Join(dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	// The manifest write commits and then reports a failure — the
	// post-rename fsync class.
	probeErr := errors.New("probe: directory fsync failed")
	scratchManifestWriteProbe = func() error { return probeErr }
	t.Cleanup(func() { scratchManifestWriteProbe = nil })
	err = PinScratchBinding(owner, retentionBinding("E1", owner.RootSessionID, workspace, nil), map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil)
	if !errors.Is(err, probeErr) {
		t.Fatalf("PinScratchBinding error = %v, want the post-commit probe failure", err)
	}
	current, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision == pre.Revision {
		t.Fatalf("fixture: the manifest write did not commit (revision %d unchanged)", current.Revision)
	}
	if _, statErr := os.Stat(filepath.Join(dir, scratchPinName)); statErr != nil {
		t.Fatalf("the rollback removed a repaired pin the committed manifest still lists: %v", statErr)
	}
}

func retentionOwner(t *testing.T) ScratchOwner {
	t.Helper()
	return ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
}

func pinnedScratch(t *testing.T, base, workspace string, owner ScratchOwner, kind string) *SessionScratch {
	t.Helper()
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if err := scratch.Pin(owner, ScratchReference{Dir: scratch.Dir, Kind: kind}); err != nil {
		t.Fatal(err)
	}
	return scratch
}

func retentionBinding(bindingID, ownerSessionID, workingDir string, slots map[string]ScratchSlot) ScratchBinding {
	cloned := make(map[string]ScratchSlot, len(slots))
	maps.Copy(cloned, slots)
	return ScratchBinding{BindingID: bindingID, OwnerSessionID: ownerSessionID, WorkingDir: workingDir, Slots: cloned}
}

func retentionBindingByID(t *testing.T, owner ScratchOwner, bindingID string) ScratchBinding {
	t.Helper()
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range manifest.Bindings {
		if binding.BindingID == bindingID {
			return binding
		}
	}
	t.Fatalf("binding %q missing from manifest: %+v", bindingID, manifest.Bindings)
	return ScratchBinding{}
}

// TestScratchRetentionStaleRetryKeepsConcurrentMint proves plan 648's rebase
// rule: a stale retry that still names only the caller's observed slot must not
// erase a slot a concurrent writer minted into the same binding.
func TestScratchRetentionStaleRetryKeepsConcurrentMint(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	a := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	b := pinnedScratch(t, base, workspace, owner, ScratchKindUnsandboxed)
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	observed := retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true},
	})
	if err := UpsertScratchBinding(owner, observed, consumer); err != nil {
		t.Fatalf("seed E0/A: %v", err)
	}
	// A concurrent writer mints B into E0.
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox:     {Dir: a.Dir, OwnsLease: true},
		ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("concurrent mint B: %v", err)
	}
	// The stale caller retries its observed record, which still names only A.
	if err := UpsertScratchBinding(owner, observed, consumer); err != nil {
		t.Fatalf("stale retry: %v", err)
	}
	e0 := retentionBindingByID(t, owner, "E0")
	slot, ok := e0.Slots[ScratchKindUnsandboxed]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(b.Dir) || !slot.OwnsLease {
		t.Fatalf("stale retry erased the concurrently minted slot: %+v", e0.Slots)
	}
	if slotA := e0.Slots[ScratchKindSandbox]; filepath.Clean(slotA.Dir) != filepath.Clean(a.Dir) {
		t.Fatalf("stale retry changed A's slot: %+v", e0.Slots)
	}
}

// TestScratchRetentionMergedMoveKeepsBothCurrentSlots proves plan 646/648's
// single-transaction move is accepted and preserves both allocations: E0 keeps
// B while E1 takes A's owning slot.
func TestScratchRetentionMergedMoveKeepsBothCurrentSlots(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	a := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	b := pinnedScratch(t, base, workspace, owner, ScratchKindUnsandboxed)
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("seed E0/A: %v", err)
	}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox:     {Dir: a.Dir, OwnsLease: true},
		ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("concurrent mint B: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	err = UpdateScratchBindings(owner, manifest.Revision, []ScratchBinding{
		retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
			ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
		}),
		retentionBinding("E1", "R", workspace, map[string]ScratchSlot{
			ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true},
		}),
	}, []ScratchConsumerBinding{{SessionID: "R", CurrentBindingID: "E1"}})
	if err != nil {
		t.Fatalf("single-transaction move rejected: %v", err)
	}
	e0 := retentionBindingByID(t, owner, "E0")
	e1 := retentionBindingByID(t, owner, "E1")
	if slot := e0.Slots[ScratchKindUnsandboxed]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(b.Dir) {
		t.Fatalf("E0 did not keep B: %+v", e0.Slots)
	}
	if _, stillOwns := e0.Slots[ScratchKindSandbox]; stillOwns {
		t.Fatalf("E0 still owns A after the move: %+v", e0.Slots)
	}
	if slot := e1.Slots[ScratchKindSandbox]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(a.Dir) {
		t.Fatalf("E1 did not take A's owning slot: %+v", e1.Slots)
	}
}

// TestScratchRetentionOpenRevalidatesAfterConcurrentRelease proves Open
// revalidates the tombstone and pin after it acquires the directory lease: a
// release that commits between the initial validation and the lease acquisition
// must not yield a usable handle for an allocation the release already
// tombstoned.
func TestScratchRetentionOpenRevalidatesAfterConcurrentRelease(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	// Simulate a terminal release that interleaved after the initial validation
	// but before the lease acquisition. It bypasses the manifest lock that the
	// real ReleaseScratchRetention holds, which is exactly the stale-validation
	// window the post-lease revalidation must close.
	scratchRetentionOpenBeforeLease = func() {
		manifest, err := LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("load manifest in hook: %v", err)
			return
		}
		manifest.Released = true
		manifest.Revision++
		if err := writeScratchRetention(owner, manifest); err != nil {
			t.Errorf("write tombstone in hook: %v", err)
			return
		}
		if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
			t.Errorf("remove pin in hook: %v", err)
		}
	}
	t.Cleanup(func() { scratchRetentionOpenBeforeLease = nil })
	if _, err := OpenRetainedSessionScratch(owner, ref); err == nil {
		t.Fatal("Open returned a handle after a concurrent release tombstoned the manifest and removed the pin")
	}
}

// TestScratchRetentionOpenRefusesWhileManifestLocked proves opening is
// serialized with a terminal release under the manifest lock: while another
// writer holds that lock (as ReleaseScratchRetention does across its tombstone
// and pin removal), Open must not acquire the directory lease and return a
// handle from a stale validation.
func TestScratchRetentionOpenRefusesWhileManifestLocked(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRetainedSessionScratch(owner, ref); err == nil {
		_ = lock.Release()
		t.Fatal("Open returned a handle while another writer held the manifest lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatalf("open after the lock was released: %v", err)
	}
	if err := restored.Retain(); err != nil {
		t.Fatal(err)
	}
}

// TestScratchMutationsRefuseReleasedManifest proves the round-11 terminal-write
// hole: once ReleaseScratchRetention commits the tombstone, every binding
// mutation must refuse the manifest. A losing writer retrying through a
// lock-contention window must not be able to resurrect bindings or add
// references to an authority that already authorized collection — and the
// bounded retry must treat the refusal as terminal, never as contention.
func TestScratchMutationsRefuseReleasedManifest(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	original, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: original.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: original}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := original.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("fixture expected the Released tombstone")
	}

	fresh, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Cleanup() })
	consumer := ScratchConsumerBinding{SessionID: owner.RootSessionID, CurrentBindingID: binding.BindingID}

	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: fresh}, nil); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("PinScratchBinding on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("UpsertScratchBinding on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}
	if err := UpsertScratchBindingOnly(owner, binding); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("UpsertScratchBindingOnly on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}
	if err := UpdateScratchBindings(owner, manifest.Revision, []ScratchBinding{binding}, nil); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("UpdateScratchBindings on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}

	var retryCalls int
	retryErr := RetryScratchLockContention(func() error {
		retryCalls++
		return ErrScratchRetentionReleased
	})
	if !errors.Is(retryErr, ErrScratchRetentionReleased) || retryCalls != 1 {
		t.Fatalf("the bounded retry treated a released manifest as contention (%d calls, err %v); the refusal is terminal", retryCalls, retryErr)
	}
}

// TestResetReleasedRetriesLockContention pins the round-12 retry gap: the
// released-manifest reset takes the manifest's fail-fast update lock, and an
// in-process writer holding it must refuse the reset only transiently — a
// single attempt would fail the restore the reset is part of.
func TestResetReleasedRetriesLockContention(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	original, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: original.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: original}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := original.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}

	takeLock := make(chan struct{})
	lockTaken := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		<-takeLock
		_ = WithScratchRetentionLock(owner, func() error {
			close(lockTaken)
			// A bounded hold: longer than the reset's first attempt, shorter
			// than the bounded retry's whole span.
			time.Sleep(2 * time.Millisecond)
			return nil
		})
		close(lockReleased)
	}()
	close(takeLock)
	<-lockTaken
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("the reset gave up on a transient lock refusal: %v", err)
	}
	<-lockReleased
	if fresh.Released || len(fresh.References) != 0 {
		t.Fatalf("the reset returned %+v, want a fresh empty manifest", fresh)
	}
}

// TestResetReleasedReconcilesLeftoverPins pins the round-12 collector hazard:
// the terminal release deliberately leaves pins whose leases are contended,
// relying on the tombstone to read them as collectible. A reset that simply
// dropped them would strand each surviving pin against a manifest with no
// matching reference — retained forever with a diagnostic on every sweep. The
// reset must finish the release for pins whose lease is now free, and carry a
// reference for the pins still held.
func TestResetReleasedReconcilesLeftoverPins(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	// The lease stays held through the terminal release, so the pin is left
	// behind on a tombstoned manifest.
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("fixture expected the contended pin to survive the release: %v", err)
	}

	// Settled before the reset: the reset finishes the release and the pin
	// dies with the manifest.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("reset over the settled leftover pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("the reset left a collectible directory's pin in place: %v", err)
	}
	retained, err := ScratchDirectoryRetained(scratch.Dir)
	if err != nil || retained {
		t.Fatalf("the settled dir must read as ordinary collectible after the reset: retained=%v err=%v", retained, err)
	}

	// Still held at the reset: the pin/reference pair must stay coherent —
	// retained silently, never the no-matching-reference diagnostic.
	held, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Cleanup() })
	second := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: held.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, second, map[string]*SessionScratch{ScratchKindSandbox: held}, nil); err != nil {
		t.Fatalf("pin the second pre-release binding: %v", err)
	}
	// The round-16 reset only carries a reference whose graph rows can
	// travel with it: the still-held binding's consumer is what makes the
	// carried pair one the root's own reader accepts.
	if err := UpsertScratchBinding(owner, second, ScratchConsumerBinding{SessionID: "consumer-reconcile", CurrentBindingID: second.BindingID}); err != nil {
		t.Fatalf("publish the second binding's consumer: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("second terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the still-held pin: %v", err)
	}
	carried := false
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(held.Dir) && ref.Kind == ScratchKindSandbox {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the reset dropped the reference for the still-held pin %q; the collector would retain it forever", held.Dir)
	}
	retained, err = ScratchDirectoryRetained(held.Dir)
	if err != nil {
		t.Fatalf("the still-held dir's pin/reference pair reads as incoherent: %v", err)
	}
	if !retained {
		t.Fatal("a pin still held by a live owner must read as retained")
	}
}

// TestScratchPinRejectsReleasedManifest pins the round-13 writer gap: Pin
// still published references and pins onto a terminally released manifest —
// the one writer left outside the ErrScratchRetentionReleased discipline.
func TestScratchPinRejectsReleasedManifest(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind on the tombstone.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	err = scratch.Pin(owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox})
	if !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("pin onto a terminally released manifest: %v, want %v", err, ErrScratchRetentionReleased)
	}
}

// TestResetReleasedTreatsCollectedReferenceAsStale pins the round-13 reset
// gap: a reference whose directory the collector already removed from under
// the tombstone must die with the manifest, not fail the reset on a lease
// that can never be opened again.
func TestResetReleasedTreatsCollectedReferenceAsStale(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("fixture expected the contended pin to survive the release: %v", err)
	}
	// Settle the lease, then let the collector take the directory: the pin
	// dies with it and only the tombstoned reference remains.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(scratch.Dir); err != nil {
		t.Fatal(err)
	}

	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("the reset failed on an already-collected reference: %v", err)
	}
	if fresh.Released {
		t.Fatal("the reset left the manifest tombstoned")
	}
	if len(fresh.References) != 0 {
		t.Fatalf("the reset carried a stale reference for a collected directory: %+v", fresh.References)
	}
}

// TestResetReleasedReleasesLeaseWhenPinRemoveFails pins the round-13 lease
// leak: when the reset cannot remove a settled pin, it must still release the
// per-directory retention lease it took, or the leaked flock holds the
// directory against every later writer.
func TestResetReleasedReleasesLeaseWhenPinRemoveFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the read-only directory fixture cannot make os.Remove fail on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("the read-only directory fixture cannot make os.Remove fail for root")
	}
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	t.Cleanup(func() { _ = os.Chmod(scratch.Dir, 0o700) })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// Settled: the lease is free for the reset to take and the pin is left
	// for it to remove. A read-only directory makes that removal fail while
	// the pin itself still reads back valid.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scratch.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err == nil {
		t.Fatal("expected the pin-remove failure to surface from the reset")
	}
	probe, _, err := acquireScratchLease(filepath.Join(scratch.Dir, sessionScratchLeaseName))
	if err != nil {
		t.Fatalf("the reset leaked the retention lease: %v", err)
	}
	if err := probe.Release(); err != nil {
		t.Fatal(err)
	}
}

// TestScratchLockContentionDelayDoublesToACap pins the documented spacing
// contract: double from 1ms up to the 8ms ceiling and never past it, so a deep
// retry's waits stay bounded.
func TestScratchLockContentionDelayDoublesToACap(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Millisecond},
		{1, 2 * time.Millisecond},
		{2, 4 * time.Millisecond},
		{3, 8 * time.Millisecond},
		{4, 8 * time.Millisecond},
		{10, 8 * time.Millisecond},
	} {
		if got := ScratchLockContentionDelay(tc.attempt); got != tc.want {
			t.Fatalf("ScratchLockContentionDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestScratchOpenReportsReleasedManifestTyped pins the round-14 typing gap:
// OpenRetainedSessionScratch used to report a terminally released manifest
// with an untyped error, so the refresh could not recognize the seal and
// failed the restore racing a terminal close.
func TestScratchOpenReportsReleasedManifestTyped(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// reference and pin behind on the tombstone.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	_, err = OpenRetainedSessionScratch(owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox})
	if !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("open on a terminally released manifest: %v, want the typed %v", err, ErrScratchRetentionReleased)
	}
}

// TestScratchUpsertAtRevisionRefusesMovedManifest pins the round-14 revision
// check's contract: an upsert built from a superseded snapshot must refuse
// with the stale-revision sentinel instead of merging, and one built from the
// manifest as it stands must succeed.
func TestScratchUpsertAtRevisionRefusesMovedManifest(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the binding: %v", err)
	}
	snapshot, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent writer moves the manifest after the snapshot.
	rival, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rival.Cleanup() })
	rivalBinding := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: rival.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, rivalBinding, map[string]*SessionScratch{ScratchKindSandbox: rival}, nil); err != nil {
		t.Fatalf("pin the rival binding: %v", err)
	}
	consumer := ScratchConsumerBinding{SessionID: "consumer-test", CurrentBindingID: binding.BindingID}
	err = UpsertScratchBindingAtRevision(owner, binding, consumer, snapshot.Revision)
	if !errors.Is(err, ErrScratchRetentionStaleRevision) {
		t.Fatalf("upsert from a superseded snapshot: %v, want %v", err, ErrScratchRetentionStaleRevision)
	}
	// The same upsert derived from the manifest as it now stands succeeds.
	current, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpsertScratchBindingAtRevision(owner, binding, consumer, current.Revision); err != nil {
		t.Fatalf("upsert at the current revision: %v", err)
	}
}

// TestScratchUpsertAtRevisionChecksRevisionsAboveMaxInt64 pins round 24's Low:
// the at-revision upsert carried its expected revision through an int64 and
// read a negative narrowing conversion as the "unchecked" sentinel, so
// revisions past MaxInt64 silently bypassed the staleness check the parameter
// exists to enforce. The check must be guarded by an explicit expected/absent
// flag, never by the sign of a conversion.
func TestScratchUpsertAtRevisionChecksRevisionsAboveMaxInt64(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-rev-24", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the binding: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Revision = math.MaxInt64 + 10
	if err := writeScratchRetention(owner, manifest); err != nil {
		t.Fatalf("force the revision past int64: %v", err)
	}
	err = UpsertScratchBindingAtRevision(owner, binding, consumer, math.MaxInt64+11)
	if !errors.Is(err, ErrScratchRetentionStaleRevision) {
		t.Fatalf("upsert from a superseded revision above MaxInt64: %v, want %v", err, ErrScratchRetentionStaleRevision)
	}
	// The same upsert derived from the manifest as it stands still publishes.
	if err := UpsertScratchBindingAtRevision(owner, binding, consumer, math.MaxInt64+10); err != nil {
		t.Fatalf("upsert at the standing revision above MaxInt64: %v", err)
	}
}

// TestScratchRevalidationReportsReleasedTyped pins the round-15 typing gap: the
// post-lease revalidation reported the tombstone with an untyped error, so the
// refresh's errors.Is decline check missed the exact race it was written for —
// a release committing between the initial validation and the lease
// acquisition — and failed the restore instead of declining to fresh scratch.
func TestScratchRevalidationReportsReleasedTyped(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// reference and pin behind on the tombstone.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	err = revalidateRetainedScratchAfterLease(owner, scratch.Dir, ScratchKindSandbox)
	if !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("revalidation over a released manifest: %v, want the typed %v", err, ErrScratchRetentionReleased)
	}
}

// TestResetReleasedCarriesAValidGraph pins the round-16 reset gap: the reset
// carried references for still-held pins into a fresh manifest that never
// gained a single binding row, committing a graph its own restore validator
// rejects — "references but no binding" — that no later reset would repair
// (Released is false again). A carried reference must travel with the binding
// that owns its directory and the consumer role that names it.
func TestResetReleasedCarriesAValidGraph(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-graph", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind and the reset must carry the reference.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held pin: %v", err)
	}
	if len(fresh.References) == 0 {
		t.Fatal("fixture expected the held pin's reference to carry")
	}
	for _, ref := range fresh.References {
		dir := filepath.Clean(ref.Dir)
		ownerID, ok := leaseOwningBinding(fresh, dir)
		if !ok {
			t.Fatalf("carried reference %q is owned by no binding in the reset manifest %+v", ref.Dir, fresh)
		}
		if _, ok := scratchBindingByID(fresh.Bindings, ownerID); !ok {
			t.Fatalf("the carried reference's owning binding %q is absent from the reset manifest", ownerID)
		}
		named := false
		for _, consumer := range fresh.Consumers {
			if consumer.CurrentBindingID == ownerID {
				named = true
			}
		}
		if !named {
			t.Fatalf("the carried binding %q is named by no consumer in the reset manifest %+v", ownerID, fresh.Consumers)
		}
	}
}

// TestResetReportsACommittedWrite pins round 45's Medium: the reset's
// manifest write can report the post-rename failure class — an error for a
// reset the rename already committed — and the reset returned reset=false
// with that error, so the install aborted over a manifest the reset had
// already repaired: Released false with the carried rows at the advanced
// revision, but no restored consumer to re-probe them, pinning the carried
// directories indefinitely. The reset must recognize its committed write —
// the single-writer retention lock means an unreleased manifest at fresh's
// advanced revision is this reset's commit — and report it.
func TestResetReportsACommittedWrite(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-commit-45", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind and the committed reset must carry its reference.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	released, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !released.Released {
		t.Fatal("fixture: the manifest was not released")
	}

	// Fail the reset's manifest write exactly once, after its rename has
	// committed: the post-rename fsync failure class the write probe
	// simulates.
	var fired bool
	old := scratchManifestWriteProbe
	scratchManifestWriteProbe = func() error {
		if fired {
			return nil
		}
		fired = true
		return errors.New("probe: post-rename fsync failure")
	}
	t.Cleanup(func() { scratchManifestWriteProbe = old })

	fresh, reset, err := ResetScratchRetentionIfReleased(owner)

	// The committed-reset evidence first: the durable manifest must be the
	// reset this call wrote — unreleased, at the advanced revision — or the
	// report below would be judging a write that never committed.
	current, cerr := LoadScratchRetention(owner)
	if cerr != nil {
		t.Fatalf("fixture: load the committed manifest: %v", cerr)
	}
	if current.Released {
		t.Fatalf("fixture: the reset write did not commit (still released)")
	}
	if current.Revision != released.Revision+1 {
		t.Fatalf("fixture: the reset write did not commit (revision %d, want %d)", current.Revision, released.Revision+1)
	}
	if err != nil {
		t.Fatalf("the reset reported a committed write as failed: %v", err)
	}
	if !reset {
		t.Fatal("the reset reported a committed write as not-reset")
	}
	if fresh.Revision != current.Revision {
		t.Fatalf("the reported manifest is not the committed one: %d vs %d", fresh.Revision, current.Revision)
	}
	if len(fresh.References) == 0 {
		t.Fatal("the committed reset dropped every carried reference")
	}
}

// TestResetReleasedCarriesWrapperBindingsAndTheirConsumers pins round 24's
// second Medium: the reset's carry narrowed to the lease-owning binding, so a
// wrapper-only binding — a distinct consumer's identity for the same retained
// directory — died with the tombstone together with its consumer role. That
// consumer's next restore then found no current binding and minted fresh
// scratch instead of borrowing the directory it still held. The reference must
// travel with every binding whose slot names its directory, not only the lease
// owner.
func TestResetReleasedCarriesWrapperBindingsAndTheirConsumers(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	ownerBinding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	wrapperBinding := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: false}})
	ownerConsumer := ScratchConsumerBinding{SessionID: "consumer-owner-24", CurrentBindingID: ownerBinding.BindingID}
	wrapperConsumer := ScratchConsumerBinding{SessionID: "consumer-wrapper-24", CurrentBindingID: wrapperBinding.BindingID}
	if err := PinScratchBinding(owner, ownerBinding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the lease-owning binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, ownerBinding, ownerConsumer); err != nil {
		t.Fatalf("publish the owning consumer: %v", err)
	}
	if err := UpsertScratchBindingOnly(owner, wrapperBinding); err != nil {
		t.Fatalf("publish the wrapper binding: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateScratchBindings(owner, manifest.Revision, nil, []ScratchConsumerBinding{wrapperConsumer}); err != nil {
		t.Fatalf("publish the wrapper consumer: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind and the reset must carry the reference with both identities.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held pin: %v", err)
	}
	var wrapperRow ScratchBinding
	for _, binding := range fresh.Bindings {
		if binding.BindingID == wrapperBinding.BindingID {
			wrapperRow = binding
		}
	}
	slot, hasSlot := wrapperRow.Slots[ScratchKindSandbox]
	if !hasSlot {
		t.Fatalf("the reset dropped the wrapper binding %q whose slot borrows the retained %q", wrapperBinding.BindingID, scratch.Dir)
	}
	if filepath.Clean(slot.Dir) != filepath.Clean(scratch.Dir) || slot.OwnsLease {
		t.Fatalf("the carried wrapper slot is %+v; want the borrow of %q", slot, scratch.Dir)
	}
	var wrapperRole ScratchConsumerBinding
	for _, consumer := range fresh.Consumers {
		if consumer.SessionID == wrapperConsumer.SessionID {
			wrapperRole = consumer
		}
	}
	if wrapperRole.CurrentBindingID != wrapperBinding.BindingID {
		t.Fatalf("the reset dropped the wrapper consumer's role: %+v; want it naming %q", wrapperRole, wrapperBinding.BindingID)
	}
	// The lease owner and its consumer carry exactly as before.
	var ownerRow ScratchBinding
	for _, binding := range fresh.Bindings {
		if binding.BindingID == ownerBinding.BindingID {
			ownerRow = binding
		}
	}
	if _, ok := ownerRow.Slots[ScratchKindSandbox]; !ok {
		t.Fatalf("the reset dropped the owning binding %q", ownerBinding.BindingID)
	}
	named := false
	for _, consumer := range fresh.Consumers {
		if consumer.SessionID == ownerConsumer.SessionID && consumer.CurrentBindingID == ownerBinding.BindingID {
			named = true
		}
	}
	if !named {
		t.Fatalf("the reset dropped the owning consumer %q", ownerConsumer.SessionID)
	}
}

// TestResetReleasedDropsSlotsOfAMismatchedKind pins the round-67 kind rule on
// the carry: the slot loop matched bindings by directory alone, so a slot
// claiming the retained directory under the WRONG KIND traveled into the
// reinitialized manifest beside a reference of a different kind — a graph the
// reader fails closed on, wedging every later restore of the root. The slot
// must carry only under the reference's own kind.
func TestResetReleasedDropsSlotsOfAMismatchedKind(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	// The pin matches the manifest's sandbox reference exactly — the contended
	// carry branch's own checks all pass — while the second binding's slot
	// names the same directory under the wrong kind. The live writer refuses
	// that row once kinds are checked, so the released manifest is seeded the
	// way a pre-fix or foreign-written one reads.
	ownerBinding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	mismatched := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: false}})
	manifest := ScratchManifest{
		Owner:      owner,
		References: []ScratchReference{{Dir: scratch.Dir, Kind: ScratchKindSandbox}},
		Bindings:   []ScratchBinding{ownerBinding, mismatched},
		Consumers: []ScratchConsumerBinding{
			{SessionID: "consumer-kind-owner", CurrentBindingID: ownerBinding.BindingID},
			{SessionID: "consumer-kind-mismatch", CurrentBindingID: mismatched.BindingID},
		},
		Released: true,
	}
	if err := writeScratchRetention(owner, manifest); err != nil {
		t.Fatalf("seed the released manifest: %v", err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox}); err != nil {
		t.Fatalf("seed the retained pin: %v", err)
	}
	// The scratch's own lease stays held, so the reset reaches the reference
	// through the contended-carry branch.
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held pin: %v", err)
	}
	for _, binding := range fresh.Bindings {
		for kind, slot := range binding.Slots {
			if filepath.Clean(slot.Dir) != filepath.Clean(scratch.Dir) {
				continue
			}
			if kind != ScratchKindSandbox {
				t.Fatalf("the reset carried binding %q slot %q over the %q reference: a kind-mismatched slot wedges every later restore on the graph reader", binding.BindingID, kind, ScratchKindSandbox)
			}
		}
	}
	// The legitimate pair carries exactly as before.
	ownerCarried, ownerRoleCarried := false, false
	for _, binding := range fresh.Bindings {
		ownerCarried = ownerCarried || binding.BindingID == ownerBinding.BindingID
	}
	for _, consumer := range fresh.Consumers {
		ownerRoleCarried = ownerRoleCarried || consumer.SessionID == "consumer-kind-owner" && consumer.CurrentBindingID == ownerBinding.BindingID
	}
	if !ownerCarried || !ownerRoleCarried {
		t.Fatalf("the kind gate dropped the legitimate pair: bindings %v, consumers %v", fresh.Bindings, fresh.Consumers)
	}
}

// TestResetReleasedDropsAReferenceWhoseOnlyOwnerClaimsWithTheWrongKind pins
// the round-81 inverse of the round-67 shape: there the mismatched slot sat
// BESIDE a correct owner, here it is the ONLY owner. The carry's owning
// lookup matched by directory alone, so a reference whose only lease-owning
// slot claims its directory under the wrong kind counted as owned and
// carried — while the carry's kind gate dropped that very slot, committing a
// rebuilt manifest that held the reference with no binding at all: the graph
// the reader fails closed on, unreleased, with no later reset left to repair
// it. The owning lookup must require the reference's own kind, so the pair
// dies like every other ownerless reference instead.
func TestResetReleasedDropsAReferenceWhoseOnlyOwnerClaimsWithTheWrongKind(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	// The pin matches the sandbox reference exactly — every check the
	// contended-carry branch makes passes — while the manifest's only owning
	// slot claims the directory under the wrong kind. The live writer refuses
	// that row once kinds are checked, so the released manifest is seeded the
	// way a pre-fix or foreign-written one reads.
	onlyOwner := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}})
	manifest := ScratchManifest{
		Owner:      owner,
		References: []ScratchReference{{Dir: scratch.Dir, Kind: ScratchKindSandbox}},
		Bindings:   []ScratchBinding{onlyOwner},
		Consumers: []ScratchConsumerBinding{
			{SessionID: "consumer-kind-wrong", CurrentBindingID: onlyOwner.BindingID},
		},
		Released: true,
	}
	if err := writeScratchRetention(owner, manifest); err != nil {
		t.Fatalf("seed the released manifest: %v", err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox}); err != nil {
		t.Fatalf("seed the retained pin: %v", err)
	}
	// The scratch's own lease stays held, so the reset reaches the reference
	// through the contended-carry branch.
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held pin: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried the %q reference under an owner whose only slot claims the directory as %q: the rebuilt manifest holds the reference with no binding at all, the graph every later restore fails closed on, committed unreleased", ref.Kind, ScratchKindUnsandboxed)
		}
	}
	if fresh.Released {
		t.Fatalf("the reset did not commit: %+v", fresh)
	}
	// The death leaves the pin in place (round 25): the holder keeps its
	// protection, and the next terminal release's tombstone authorizes the
	// collector to remove the pin and its directory.
	if _, pinErr := readScratchDirectoryPin(scratch.Dir); pinErr != nil {
		t.Fatalf("the death disturbed the retained pin: %v", pinErr)
	}
}

// TestValidateResetScratchGraph pins the reset's belt-and-suspenders commit
// gate: the rebuilt manifest must pair every carried reference with a carried
// binding slot of its kind, name only carried bindings from its consumer
// roles, and keep a naming consumer for every lease-owning binding. The
// carry machinery guarantees all of this by construction, so no integration
// path can reach a violation once the kind-aware owning lookup is in — the
// gate exists for the regression that breaks that guarantee, and this table
// pins each failure mode it must refuse before the raw commit.
func TestValidateResetScratchGraph(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	ownerRow := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	wrongKindRow := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}})
	wrapperRow := retentionBinding("E2", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: false}})
	carriedRef := ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox}
	for name, tt := range map[string]struct {
		manifest ScratchManifest
		wantErr  string
	}{
		"a carried pair is valid": {
			manifest: ScratchManifest{References: []ScratchReference{carriedRef},
				Bindings:  []ScratchBinding{ownerRow},
				Consumers: []ScratchConsumerBinding{{SessionID: "consumer-graph", CurrentBindingID: ownerRow.BindingID}}},
		},
		"a wrapper binding beside the owner is valid": {
			manifest: ScratchManifest{References: []ScratchReference{carriedRef},
				Bindings: []ScratchBinding{ownerRow, wrapperRow},
				Consumers: []ScratchConsumerBinding{
					{SessionID: "consumer-graph", CurrentBindingID: ownerRow.BindingID},
					{SessionID: "consumer-wrapper", CurrentBindingID: wrapperRow.BindingID},
				}},
		},
		"an empty reinitialized manifest is valid": {
			manifest: ScratchManifest{},
		},
		"a reference with no binding is rejected": {
			manifest: ScratchManifest{References: []ScratchReference{carriedRef}},
			wantErr:  "is claimed by no carried binding slot",
		},
		"a reference only a wrong-kind slot claims is rejected": {
			manifest: ScratchManifest{References: []ScratchReference{carriedRef},
				Bindings:  []ScratchBinding{wrongKindRow},
				Consumers: []ScratchConsumerBinding{{SessionID: "consumer-graph", CurrentBindingID: wrongKindRow.BindingID}}},
			wantErr: "no reference of that kind pins",
		},
		"a slot naming an unpinned directory is rejected": {
			manifest: ScratchManifest{Bindings: []ScratchBinding{ownerRow},
				Consumers: []ScratchConsumerBinding{{SessionID: "consumer-graph", CurrentBindingID: ownerRow.BindingID}}},
			wantErr: "no reference of that kind pins",
		},
		"a consumer role naming an absent binding is rejected": {
			manifest: ScratchManifest{References: []ScratchReference{carriedRef},
				Bindings:  []ScratchBinding{ownerRow},
				Consumers: []ScratchConsumerBinding{{SessionID: "consumer-graph", CurrentBindingID: "E-missing"}}},
			wantErr: "unknown binding",
		},
		"an owning binding no consumer names is rejected": {
			manifest: ScratchManifest{References: []ScratchReference{carriedRef},
				Bindings: []ScratchBinding{ownerRow}},
			wantErr: "no consumer role names it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateResetScratchGraph(tt.manifest)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateResetScratchGraph rejected a valid rebuilt manifest: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateResetScratchGraph error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestScratchUpsertRejectsASlotOfAMismatchedKind pins the writer's half of the
// round-67 kind rule: validation accepted any slot whose directory was
// pinned, without comparing the slot's kind to the reference's, so a binding
// claiming a retained directory under the wrong kind could be published live —
// a manifest every subsequent graph read fails closed on.
func TestScratchUpsertRejectsASlotOfAMismatchedKind(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	ownerBinding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, ownerBinding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the owning binding: %v", err)
	}
	// The pinned reference for this directory is a sandbox allocation; a slot
	// claiming it as unsandboxed passes the pinned-directory check and poisons
	// the manifest for every reader.
	mismatched := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: false}})
	if err := UpsertScratchBindingOnly(owner, mismatched); err == nil {
		t.Fatalf("the writer accepted a %q slot over the %q reference: the manifest now fails every graph read", ScratchKindUnsandboxed, ScratchKindSandbox)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range manifest.Bindings {
		if binding.BindingID == mismatched.BindingID {
			t.Fatalf("the mismatched row landed in the manifest: %+v", binding)
		}
	}
}

// TestResetReleasedDropsContendedReferenceWithoutPin pins the round-16
// pin-validation gap: the contended-carry path preserved a manifest reference
// without checking its immutable pin still exists, so a crash between pin
// removal and publication left the reset persisting a reference that wedges
// every later restore at its pin verification.
func TestResetReleasedDropsContendedReferenceWithoutPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The lease stays held, so the terminal release leaves the pin — and the
	// crash simulation removes the pin file it would have published.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held, unpinned reference: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried a reference whose pin is gone: %+v", fresh.References)
		}
	}
}

// TestResetReleasedCarriesMultiRoleConsumerGraph pins the round-17 consumer
// overwrite: a consumer routinely names several bindings, and narrowing it once
// per carried reference let the second carry overwrite the first's roles.
// The surviving carried binding was then named by no consumer — the exact
// graph the restore reader fails closed on, with Released false again and no
// later reset to repair it.
func TestResetReleasedCarriesMultiRoleConsumerGraph(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratchA, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratchA.Cleanup() })
	scratchB, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratchB.Cleanup() })
	bindingE0 := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratchA.Dir, OwnsLease: true}})
	bindingE1 := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratchB.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, bindingE0, map[string]*SessionScratch{ScratchKindSandbox: scratchA}, nil); err != nil {
		t.Fatalf("pin the E0 binding: %v", err)
	}
	if err := PinScratchBinding(owner, bindingE1, map[string]*SessionScratch{ScratchKindSandbox: scratchB}, nil); err != nil {
		t.Fatalf("pin the E1 binding: %v", err)
	}
	consumer := ScratchConsumerBinding{
		SessionID:             "consumer-multi",
		CurrentBindingID:      bindingE0.BindingID,
		ParentSharedBindingID: bindingE1.BindingID,
	}
	if err := UpsertScratchBinding(owner, bindingE0, consumer); err != nil {
		t.Fatalf("publish the multi-role consumer: %v", err)
	}
	// Both leases stay held, so the terminal release leaves both pins behind
	// and the reset must carry both references with their rows.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over two held pins: %v", err)
	}
	if len(fresh.References) != 2 {
		t.Fatalf("fixture expected both held references to carry: %+v", fresh.References)
	}
	carried, found := ScratchConsumerBinding{}, false
	for _, row := range fresh.Consumers {
		if row.SessionID == consumer.SessionID {
			carried, found = row, true
		}
	}
	if !found {
		t.Fatalf("the multi-role consumer is absent from the reset manifest: %+v", fresh.Consumers)
	}
	if carried.CurrentBindingID != bindingE0.BindingID || carried.ParentSharedBindingID != bindingE1.BindingID {
		t.Fatalf("the carried consumer lost a role across two carried bindings: %+v", carried)
	}
	for _, binding := range fresh.Bindings {
		if len(consumersNamingScratchBinding(fresh.Consumers, binding.BindingID)) == 0 {
			t.Fatalf("carried binding %q is named by no consumer in the reset manifest %+v", binding.BindingID, fresh.Consumers)
		}
	}
}

// TestResetReleasedDropsContendedReferenceWithForeignPin pins the round-17
// identity gap on the contended carry: the reset validated only the pin's
// existence, so a readable pin belonging to another owner was carried into
// the fresh manifest — wedging every later restore at its pin verification,
// with Released false again and no later reset to repair it.
func TestResetReleasedDropsContendedReferenceWithForeignPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	foreign := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-foreign", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The lease stays held, so the terminal release leaves the pin — and the
	// fixture replaces it with a foreign owner's pin for the same directory.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, foreign, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox}); err != nil {
		t.Fatalf("write the foreign pin: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over a foreign held pin: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried a reference whose pin belongs to another owner: %+v", fresh.References)
		}
	}
	// A foreign pin is not this reset's to remove: the file stays for its own
	// manifest.
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("the reset removed a foreign pin it does not own: %v", err)
	}
}

// TestResetReleasedDropsFreeLeaseReferenceWithMismatchedPin covers the
// free-lease branch of the same round-17 identity gap: a readable pin with
// our own owner but a kind this reference cannot verify fell into the old
// carry-everything default, wedging later restores the same way.
func TestResetReleasedDropsFreeLeaseReferenceWithMismatchedPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-mismatch", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// Free the lease so the reset's free-lease branch takes this reference,
	// then replace the pin with our own pin naming the WRONG kind.
	_ = scratch.Retain()
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindUnsandboxed}); err != nil {
		t.Fatalf("write the kind-mismatched pin: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over a mismatched free-lease pin: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried a reference whose pin fails identity verification: %+v", fresh.References)
		}
	}
	// Round 19 flipped this pin's fate: the lease is ours right now, so the
	// reset finishes the release for the malformed pin it owns instead of
	// stranding an orphan the collector conservatively retains, with a
	// diagnostic, forever. The directory becomes ordinary.
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("the reset left a malformed pin it owns and could remove: stat got %v, want not exist", err)
	}
}

// TestResetReleasedAbortsOnUnreadablePin pins round 19's Medium: a pin the
// reset cannot read might be anyone's protection, so committing past it could
// strand an orphan the collector conservatively retains forever. The reset
// must abort with the tombstone intact, so a later retry heals a transient
// read failure.
func TestResetReleasedAbortsOnUnreadablePin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-unreadable", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// Free the lease so the reset's free-lease branch takes this reference,
	// then corrupt the pin past reading.
	_ = scratch.Retain()
	if err := os.WriteFile(filepath.Join(scratch.Dir, scratchPinName), []byte("not a pin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err == nil {
		t.Fatal("expected the unreadable pin to abort the reset")
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("the aborted reset committed an unreleased manifest past a pin it could not read")
	}
}

// TestResetReleasedAbortsOnContendedMismatchedOwnPin covers the contended arm
// of the same round-19 finding: our own malformed pin under a lease we do not
// hold must abort the reset too — the contention is transient (a live holder
// releases), so the tombstone's retry reaches this reference through the
// free-lease branch, which removes the malformed pin and finishes the
// release.
func TestResetReleasedAbortsOnContendedMismatchedOwnPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-mismatch-c", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The lease stays held (the contended branch) and the pin is replaced with
	// our own naming the wrong kind.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindUnsandboxed}); err != nil {
		t.Fatalf("write the kind-mismatched pin: %v", err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err == nil {
		t.Fatal("expected the contended mismatched own pin to abort the reset")
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("the aborted reset committed an unreleased manifest past its own malformed pin")
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("the aborted reset disturbed the mismatched pin: %v", err)
	}
}

// TestResetReleasedLeavesTheContendedDyingPinInPlace pins round 25's Medium:
// the pair-death path ran lease-less — its only caller is the reset's
// contended branch — so the reset removed a dying pair's own pin while a live
// holder still held the directory's lease, stripping the protection that
// keeps the collector from sweeping the directory out from under the holder.
// The death now leaves a readable contended pin untouched and drops only the
// pair: the holder keeps its protection, and the pin's protection ends with
// the next terminal release's tombstone, which is what authorizes the
// collector to remove the pin and its directory. (Round 18's abort contract
// for this branch — refuse to commit past a removal that fails — retired with
// the removal itself: the death never attempts one under contention, and the
// reinstall that owns this path re-pins the same identity immediately.)
func TestResetReleasedLeavesTheContendedDyingPinInPlace(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	// No consumer row is published, so the reset's carry path lets the pair
	// die — and the lease the scratch handle keeps makes that death
	// contended, the only branch the death can run in.
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// The dying pair's pin is this owner's and its lease is held: the death
	// must leave the pin in place rather than strip it from under the live
	// holder — the reset still completes, so the reinstall that owns this
	// path can republish immediately.
	if _, _, err := ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("the reset must complete past a contended dying pair: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Released {
		t.Fatal("the reset left the manifest tombstoned")
	}
	for _, candidate := range manifest.References {
		if filepath.Clean(candidate.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatal("the dead pair's reference survived the reset")
		}
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("the reset stripped the live holder's protection pin lease-less: %v", err)
	}
	// The left pin is protection, not an orphan the collector may take: the
	// holder still uses the directory, so the sweep conservatively retains it
	// with its diagnostic.
	retained, retErr := ScratchDirectoryRetained(scratch.Dir)
	if !retained || retErr == nil {
		t.Fatalf("the stripped-pin directory read collectible: retained=%v err=%v", retained, retErr)
	}
	// The holder releases its lease without removing the directory, the way a
	// live session's teardown does; the next terminal release tombstones the
	// manifest, which is what authorizes the collector to finally remove the
	// released pin and its directory — the protection the leave extends is
	// bounded by the next terminal cycle.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("next terminal release: %v", err)
	}
	if retained, retErr := ScratchDirectoryRetained(scratch.Dir); retained || retErr != nil {
		t.Fatalf("the freed-pin directory still reads retained after the terminal release: retained=%v err=%v", retained, retErr)
	}
}

// TestVerifyDyingReferencePin pins the death branch's pin contract: a
// reference whose owning pair died with the tombstoned manifest is dropped
// freely when the pin is absent, a pin that cannot be read aborts the death —
// the same contract the contended and free-lease branches enforce on their
// own reads (round 19) — and a readable pin is LEFT UNTOUCHED: the death's
// only caller is the reset's contended branch, so the pin protects a
// directory whose lease a live holder still holds, and stripping it
// lease-less would leave the holder's directory collectible while the holder
// uses it. This owner's own pin is re-pinned by the reinstall's republish
// (same owner, directory, and kind — the pin write is idempotent) or removed
// by the next terminal release once the lease frees; a foreign pin belongs to
// its own manifest (round 25). The death branch's read is the second one the
// reset takes on the directory, so a cross-process writer is what can flip it
// between the two; the contract is pinned here directly.
func TestVerifyDyingReferencePin(t *testing.T) {
	owner := retentionOwner(t)
	other := retentionOwner(t)

	// A pin that is already absent has nothing to protect: the reference
	// dies with the manifest.
	bare := t.TempDir()
	if err := verifyDyingReferencePin(bare); err != nil {
		t.Fatalf("absent pin: %v", err)
	}

	// This owner's pin is left in place: the death never removes it
	// lease-less.
	ours := t.TempDir()
	if err := writeScratchDirectoryPin(ours, owner, ScratchReference{Dir: ours, Kind: ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDyingReferencePin(ours); err != nil {
		t.Fatalf("our own pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ours, scratchPinName)); err != nil {
		t.Fatalf("the death disturbed this owner's pin: %v", err)
	}

	// A foreign pin belongs to its own manifest and is left alone.
	foreign := t.TempDir()
	if err := writeScratchDirectoryPin(foreign, other, ScratchReference{Dir: foreign, Kind: ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDyingReferencePin(foreign); err != nil {
		t.Fatalf("foreign pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(foreign, scratchPinName)); err != nil {
		t.Fatalf("the foreign pin was disturbed: %v", err)
	}

	// A pin that cannot be read aborts the death: committing the reference
	// away past an unreadable pin strands an orphan the collector retains
	// forever under an unreleased manifest, with no later reset left to
	// retry.
	if os.Geteuid() == 0 {
		t.Skip("the unreadable-pin fixture cannot make the read fail for root")
	}
	unreadable := t.TempDir()
	if err := writeScratchDirectoryPin(unreadable, owner, ScratchReference{Dir: unreadable, Kind: ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(unreadable, scratchPinName), 0o600) })
	if err := os.Chmod(filepath.Join(unreadable, scratchPinName), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := verifyDyingReferencePin(unreadable); err == nil {
		t.Fatal("an unreadable pin must abort the death, not let the reference drop silently")
	}
}

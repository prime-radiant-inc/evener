package agent

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// TestScratchRetentionConcurrentAdoption runs several consumers' adoptions of
// one retained pool concurrently. A pool's ownership maps are shared state:
// without synchronization the concurrent handle/adopted mutations are an
// unsynchronized map access (reported by the race detector, and a hard
// concurrent-map fatal in production), so this test is meaningful under -race.
func TestScratchRetentionConcurrentAdoption(t *testing.T) {
	t.Parallel()
	const consumers = 8
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}

	base := t.TempDir()
	bindings := make([]sandbox.ScratchBinding, 0, consumers)
	consumerBindings := make([]sandbox.ScratchConsumerBinding, 0, consumers)
	dirs := make([]string, consumers)
	for i := range consumers {
		scratch, err := sandbox.NewSessionScratch(base, dir)
		if err != nil {
			t.Fatal(err)
		}
		ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
		if err := scratch.Pin(owner, ref); err != nil {
			t.Fatal(err)
		}
		dirs[i] = scratch.Dir
		bindings = append(bindings, sandbox.ScratchBinding{
			BindingID:      fmt.Sprintf("E%d", i),
			OwnerSessionID: owner.RootSessionID,
			WorkingDir:     dir,
			Slots: map[string]sandbox.ScratchSlot{
				sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true},
			},
		})
		consumerBindings = append(consumerBindings, sandbox.ScratchConsumerBinding{
			SessionID:        fmt.Sprintf("consumer-%d", i),
			CurrentBindingID: fmt.Sprintf("E%d", i),
		})
		// Release the live lease so prepareRetainedScratch reacquires a handle for
		// every reference: adoption's owning-slot path is what mutates the pool.
		if err := scratch.Retain(); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision, bindings, consumerBindings); err != nil {
		t.Fatal(err)
	}
	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch: %v", err)
	}
	pool := root.retainedScratch.Load()
	if pool == nil {
		t.Fatal("prepareRetainedScratch published no pool")
	}
	// No adoption is in flight yet, so this read is ordered before the goroutines
	// below by the go statements.
	if pooled := len(pool.handles); pooled != consumers {
		t.Fatalf("pooled handles = %d, want %d", pooled, consumers)
	}

	envs := make([]*execenv.LocalExecutionEnvironment, consumers)
	for i := range envs {
		envs[i] = execenv.NewLocalExecutionEnvironment(dir)
	}
	results := make([]error, consumers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range consumers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := root.adoptConsumerScratch(envs[i], fmt.Sprintf("consumer-%d", i))
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("consumer-%d adoption: %v", i, err)
		}
	}
	for i, env := range envs {
		if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(dirs[i]) {
			t.Fatalf("consumer-%d scratch = %q, want %q", i, got, dirs[i])
		}
		env.RetainSessionScratch()
	}
}

// seedOneRetainedAllocation pins exactly one retained allocation onto root's
// manifest — a pinned reference with its lease-owning binding and consumer row —
// and frees the live lease so prepareRetainedScratch can reacquire it.
func seedOneRetainedAllocation(t *testing.T, root *Session, dir string) (sandbox.ScratchOwner, string) {
	t.Helper()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	base := t.TempDir()
	scratch, err := sandbox.NewSessionScratch(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	binding := sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     dir,
		Slots: map[string]sandbox.ScratchSlot{
			sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true},
		},
	}
	consumer := sandbox.ScratchConsumerBinding{SessionID: "consumer-0", CurrentBindingID: "E0"}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision, []sandbox.ScratchBinding{binding}, []sandbox.ScratchConsumerBinding{consumer}); err != nil {
		t.Fatal(err)
	}
	// Release the live lease so prepareRetainedScratch reacquires the handle.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	return owner, scratch.Dir
}

// TestPrepareRetainedScratchRetriesTransientLockRefusal pins round 50's
// first Medium: the cold-restore open pass treated a fail-fast manifest-lock
// refusal from OpenRetainedSessionScratch as fatal, so any concurrent
// in-process writer holding the lock for the fsync scale of its transaction
// failed an otherwise recoverable restore. The refusal is transient by
// construction — the open must retry it with the shared bounded backoff
// exactly like every other scratch writer.
func TestPrepareRetainedScratchRetriesTransientLockRefusal(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, retainedDir := seedOneRetainedAllocation(t, root, dir)

	// The first open is refused the way a concurrent in-process writer
	// refuses it: the manifest lock is fail-fast and held only for the
	// fsync scale of a transaction. The second attempt finds it released.
	calls := 0
	sandbox.SetScratchOpenProbeForTesting(func() error {
		calls++
		if calls == 1 {
			return sandbox.ErrScratchRetentionLockHeld
		}
		return nil
	})
	t.Cleanup(func() { sandbox.SetScratchOpenProbeForTesting(nil) })

	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch over a transient lock refusal: %v", err)
	}
	pool := root.retainedScratch.Load()
	if pool == nil {
		t.Fatal("prepareRetainedScratch published no pool")
	}
	if _, pooled := pool.handles[canonicalScratchDir(retainedDir)]; !pooled {
		t.Fatal("the retried open never reacquired the retained allocation")
	}
	_ = owner
}

// TestPrepareRetainedScratchDeclinesWhenTheReleaseWinsTheWindow pins round
// 50's second Medium: a terminal release committing between the preparation's
// manifest load and the locked open made ErrScratchRetentionReleased fatal,
// aborting the restore before installScratchRetention could reset the
// tombstone and continue on fresh scratch. The decline must behave exactly
// like the already-released short-circuit at the preparation's head: release
// every handle acquired and publish nothing, so the install path's reset
// runs.
func TestPrepareRetainedScratchDeclinesWhenTheReleaseWinsTheWindow(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, _ := seedOneRetainedAllocation(t, root, dir)

	// The terminal release lands inside the preparation-to-open window: the
	// probe runs at the open's top, before its lock, so the open's in-lock
	// revalidation reads the real tombstone and refuses.
	sandbox.SetScratchOpenProbeForTesting(func() error {
		if err := sandbox.ReleaseScratchRetention(owner); err != nil {
			t.Fatalf("fixture terminal release: %v", err)
		}
		return nil
	})
	t.Cleanup(func() { sandbox.SetScratchOpenProbeForTesting(nil) })

	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch over a mid-window terminal release: %v", err)
	}
	if pool := root.retainedScratch.Load(); pool != nil {
		t.Fatal("the declined retained restore published a pool: the reset path must run instead")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("fixture: the release did not tombstone the manifest inside the window")
	}
}

// TestScratchRetentionAdoptWrapperOnlyBeforeOwner proves a wrapper-only
// consumer binding is restored from the retained directory even when it is
// adopted before the lease-owning binding, whether the owner's handle is still
// pooled or its lease is contended in this process. The wrapper-only slot never
// takes a second lease, so neither condition may skip its restore.
func TestScratchRetentionAdoptWrapperOnlyBeforeOwner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		releaseLease bool
	}{
		{"owner handle still pooled", true},
		{"owner lease contended in process", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root := newQueuePersistTestSession(t, dir)
			defer root.Close()
			owner, ok := root.scratchRetentionOwner()
			if !ok {
				t.Fatal("root had no scratch retention owner")
			}

			base := t.TempDir()
			scratch, err := sandbox.NewSessionScratch(base, dir)
			if err != nil {
				t.Fatal(err)
			}
			ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
			if err := scratch.Pin(owner, ref); err != nil {
				t.Fatal(err)
			}
			manifest, err := sandbox.LoadScratchRetention(owner)
			if err != nil {
				t.Fatal(err)
			}
			// E0 owns the lease; E1 shares the same directory wrapper-only, the
			// shape mergeScratchBindingSlots mints for a second binding that names
			// an allocation another binding owns.
			if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
				[]sandbox.ScratchBinding{
					{
						BindingID:      "E0",
						OwnerSessionID: owner.RootSessionID,
						WorkingDir:     dir,
						Slots: map[string]sandbox.ScratchSlot{
							sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true},
						},
					},
					{
						BindingID:      "E1",
						OwnerSessionID: owner.RootSessionID,
						WorkingDir:     dir,
						Slots: map[string]sandbox.ScratchSlot{
							sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: false},
						},
					},
				},
				[]sandbox.ScratchConsumerBinding{
					{SessionID: "consumer-owner", CurrentBindingID: "E0"},
					{SessionID: "consumer-sharer", CurrentBindingID: "E1"},
				}); err != nil {
				t.Fatal(err)
			}
			if tc.releaseLease {
				if err := scratch.Retain(); err != nil {
					t.Fatal(err)
				}
			}
			if err := root.prepareRetainedScratch(); err != nil {
				t.Fatalf("prepareRetainedScratch: %v", err)
			}

			// Restore the sharing consumer BEFORE its owner's binding.
			sharer := execenv.NewLocalExecutionEnvironment(dir)
			adopted, _, err := root.adoptConsumerScratch(sharer, "consumer-sharer")
			if err != nil {
				t.Fatalf("adopt sharing consumer: %v", err)
			}
			if !adopted {
				t.Fatal("sharing consumer had no current binding")
			}
			if got := sharer.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
				t.Fatalf("sharing consumer scratch = %q, want the retained directory %q", got, scratch.Dir)
			}
			sharer.RetainSessionScratch()

			if !tc.releaseLease {
				// The owner's lease is still held in this process, so the owner
				// cannot take it; only the wrapper-only borrow is asserted above.
				return
			}
			ownerEnv := execenv.NewLocalExecutionEnvironment(dir)
			if _, _, err := root.adoptConsumerScratch(ownerEnv, "consumer-owner"); err != nil {
				t.Fatalf("adopt owner after sharing consumer: %v", err)
			}
			if got := ownerEnv.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
				t.Fatalf("owner scratch = %q, want the retained directory %q", got, scratch.Dir)
			}
			ownerEnv.RetainSessionScratch()
		})
	}
}

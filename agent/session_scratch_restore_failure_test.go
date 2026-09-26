package agent

// Regression tests for the two roborev round 6 scratch-lifetime findings:
//
//   - High 2: a failed delegate restore must never destructively dispose a
//     retained owning slot it adopted onto the fresh environment it created.
//   - Medium 6: a failed root restore must release the retained scratch handles
//     prepareRetainedScratch reacquired but no live environment adopted.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// crashLiveRoot releases a live root's scratch leases and closes the stores a
// process death would close, with no teardown appends and no retention
// tombstone, so a later RestoreSessionFromMetaWithConfig sees the same durable
// state a daemon restart would.
func crashLiveRoot(t *testing.T, root *Session) {
	t.Helper()
	if local, ok := root.env.(*execenv.LocalExecutionEnvironment); ok {
		local.RetainSessionScratch()
	}
	if root.jobManager != nil {
		_ = root.jobManager.closeStoreOnly()
	}
	if err := root.closeAttachedTranscript(); err != nil {
		t.Fatalf("close root transcript: %v", err)
	}
}

// TestDelegateRestoreFailurePreservesAdoptedRetainedScratch is High 2. A fresh
// unsandboxed child environment adopts a retained owning slot before the child
// session is constructed, and a later construction fault fails the restore.
// The adopted allocation is durable state the manifest still references, so the
// failed restore must release its lease and keep it; the pre-fix defer derived
// its destructive teardown from ownsFresh ("this restore created the
// environment") and deleted the directory.
func TestDelegateRestoreFailurePreservesAdoptedRetainedScratch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	owner, ok := root1.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}

	// The child's retained allocation: a pinned, unsandboxed directory owned by
	// the child consumer's current binding.
	childDir := t.TempDir()
	childID := identifier.MustNewSessionID()
	base := t.TempDir()
	scratch, err := sandbox.NewSessionScratch(base, childDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
	ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(scratch.Dir, "durable.bin")
	if err := os.WriteFile(artifact, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{{
			BindingID:      "E-child",
			OwnerSessionID: owner.RootSessionID,
			WorkingDir:     childDir,
			Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
		}},
		[]sandbox.ScratchConsumerBinding{{SessionID: childID, CurrentBindingID: "E-child"}}); err != nil {
		t.Fatal(err)
	}
	// Release the live lease: a cold consumer's allocation restores from the
	// manifest, and adoption must reacquire it from the prepared pool.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}

	meta := root1.Meta()
	crashLiveRoot(t, root1)

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true},
	})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root2.Close()

	childMeta := root2.Meta()
	childMeta.ID = childID
	childMeta.ParentSessionID = root2.id
	childMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(dir, childMeta); err != nil {
		t.Fatal(err)
	}

	// A write-capable ceiling keeps the restore off the read-only floor, so the
	// child gets a plain unsandboxed clone built for this restore.
	childConfig := childMeta.Config.Clone()
	childConfig.AgentName = "subagent"
	boom := errors.New("restored delegate construction failed")
	root2.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		OwnerSessionID:    root2.id,
		VisibleSessionID:  root2.id,
		Task:              "resume adopted retained scratch",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		ToolNameCeiling:   []string{"communicate", "write_file"},
		// A working directory distinct from the root environment's forces the
		// fresh-clone path rather than the shared parent-environment path.
		WorkingDir:     childDir,
		LocalEnvPolicy: "default",
		Config:         childConfig,
		Resumable:      true,
	}
	started := delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: descriptor,
	}
	sub, restored, err := (delegateRuntime{owner: root2}).restoreIdle(started)
	if !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}
	if sub != nil || restored {
		t.Fatalf("failed restore returned sub=%p restored=%v", sub, restored)
	}

	// The adopted retained allocation survives, bytes intact.
	if _, err := os.Stat(scratch.Dir); err != nil {
		t.Fatalf("failed delegate restore destroyed the adopted retained scratch %s: %v", scratch.Dir, err)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || string(got) != "durable" {
		t.Fatalf("adopted retained artifact lost: bytes=%q err=%v", got, err)
	}
	after, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	referenced := false
	for _, candidate := range after.References {
		if filepath.Clean(candidate.Dir) == filepath.Clean(scratch.Dir) {
			referenced = true
		}
	}
	if !referenced {
		t.Fatalf("retained reference for %s was erased: %+v", scratch.Dir, after.References)
	}
	// Its lease is handed back to the pool rather than leaked: the failed
	// restore settles by the manifest (round 30), and the settle's handoff
	// for a transferred handle is the requeue — the next in-process restore
	// of this child re-claims it without flock churn.
	pool := root2.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the restored root has no retained-scratch pool after the failed delegate restore")
	}
	pool.mu.Lock()
	requeued := pool.handles[canonicalScratchDir(scratch.Dir)]
	pool.mu.Unlock()
	if requeued == nil {
		t.Fatalf("adopted retained scratch %q was not requeued into the pool for the next restore", scratch.Dir)
	}
}

// TestRootRestoreFailureReleasesUnadoptedRetainedScratch is Medium 6. A root
// restore reacquires two retained allocations, adopts one onto the launch
// environment it was handed, and then fails. The adopted allocation belongs to
// that live environment and must be preserved; the handle nothing adopted must
// be released, or its lease stays held by a session that was never returned and
// never closed.
func TestRootRestoreFailureReleasesUnadoptedRetainedScratch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	owner, ok := root1.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	env1, ok := root1.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}

	// The root's own allocation, adopted onto the restored root environment.
	if _, err := env1.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("mint root scratch: %v", err)
	}
	residentDir := env1.SessionScratchDir()
	if residentDir == "" {
		t.Fatal("root minted no scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(residentDir) })
	residentRef := sandbox.ScratchReference{Dir: residentDir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := root1.installScratchRetention(env1); err != nil {
		t.Fatalf("install root retention: %v", err)
	}

	// A second pinned allocation no consumer will adopt.
	base := t.TempDir()
	orphan, err := sandbox.NewSessionScratch(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(orphan.Dir) })
	orphanRef := sandbox.ScratchReference{Dir: orphan.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := orphan.Pin(owner, orphanRef); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{{
			BindingID:      "E-orphan",
			OwnerSessionID: owner.RootSessionID,
			WorkingDir:     dir,
			Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: orphan.Dir, OwnsLease: true}},
		}},
		[]sandbox.ScratchConsumerBinding{{SessionID: "absent-child", CurrentBindingID: "E-orphan"}}); err != nil {
		t.Fatal(err)
	}
	if err := orphan.Retain(); err != nil {
		t.Fatal(err)
	}

	meta := root1.Meta()
	crashLiveRoot(t, root1)

	boom := errors.New("root restoration failed after retained scratch was reacquired")
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	launchEnv := execenv.NewLocalExecutionEnvironment(dir)
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), launchEnv, meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sessionInitFault: func(point string) error {
			if point == "builtin_agents" {
				return boom
			}
			return nil
		}},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("root restore error = %v, want %v", err, boom)
	}
	if root2 != nil {
		t.Fatal("failed root restore returned a session")
	}
	// The launch environment is the caller's; keep it alive so the lease it
	// adopted stays held for the assertion below.
	_ = launchEnv

	// Preserved: the allocation the live launch environment adopted is still
	// owned by it, exactly as the caller handed it in.
	if _, err := sandbox.OpenRetainedSessionScratch(owner, residentRef); !errors.Is(err, sandbox.ErrScratchRetentionLeaseHeld) {
		t.Errorf("restored root's adopted scratch lease = %v, want ErrScratchRetentionLeaseHeld", err)
	}
	// Released: the unadopted handle no longer holds its lease, so a later
	// restore can reacquire it.
	handle, err := sandbox.OpenRetainedSessionScratch(owner, orphanRef)
	if err != nil {
		t.Fatalf("unadopted retained handle still held after failed restore: %v", err)
	}
	if err := handle.Retain(); err != nil {
		t.Fatal(err)
	}
}

// --- roborev round 8: adopted retained scratch across restore/re-entry ---

// pinRetainedConsumerSlot creates and pins one durable retained allocation for a
// delegate consumer, publishes the child's binding and current-consumer record,
// and releases the live lease so a cold restore reacquires it from the manifest.
// It returns the declared reference and the directory the allocation owns.
func pinRetainedConsumerSlot(t *testing.T, owner sandbox.ScratchOwner, childID, bindingID, workDir, kind string) sandbox.ScratchReference {
	t.Helper()
	scratch, err := sandbox.NewSessionScratch(t.TempDir(), workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
	ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: kind}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{{
			BindingID:      bindingID,
			OwnerSessionID: owner.RootSessionID,
			WorkingDir:     workDir,
			Slots:          map[string]sandbox.ScratchSlot{kind: {Dir: scratch.Dir, OwnsLease: true}},
		}},
		[]sandbox.ScratchConsumerBinding{{SessionID: childID, CurrentBindingID: bindingID}}); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	return ref
}

// restorableSandboxedDelegate is a cold restored root plus the committed start of
// a read-only scoped delegate whose retained allocation is already published in
// the root's manifest, so restoreIdle exercises the sandboxed cold restore.
type restorableSandboxedDelegate struct {
	root       *Session
	descriptor delegatestore.Descriptor
	started    delegateStartCommit
	ref        sandbox.ScratchReference
}

func newRestorableSandboxedDelegate(t *testing.T, kind string) restorableSandboxedDelegate {
	t.Helper()
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	owner, ok := root1.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no scratch retention owner")
	}
	childID := identifier.MustNewSessionID()
	workDir := t.TempDir()
	ref := pinRetainedConsumerSlot(t, owner, childID, "E-r8-delegate", workDir, kind)
	meta := root1.Meta()
	crashLiveRoot(t, root1)

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       sandbox.FakeProber{Facts: sbxBwrapFacts(t.TempDir())},
		},
	})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(root.Close)

	childMeta := root.Meta()
	childMeta.ID = childID
	childMeta.ParentSessionID = root.id
	childMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(dir, childMeta); err != nil {
		t.Fatal(err)
	}

	childConfig := childMeta.Config.Clone()
	childConfig.AgentName = "subagent"
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		OwnerSessionID:    root.id,
		VisibleSessionID:  root.id,
		Task:              "resume sandboxed delegate with retained scratch",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		// A read-only structured tool scope makes restoreDelegateSandboxFloor
		// derive the read-only box, so the child gets a provisioned sandbox
		// scratch (the shape High 2 and Medium 3 are about).
		ToolNameCeiling: []string{"communicate", "read_file", "shell"},
		WorkingDir:      workDir,
		LocalEnvPolicy:  "default",
		Config:          childConfig,
		Resumable:       true,
	}
	started := delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: descriptor,
	}
	return restorableSandboxedDelegate{root: root, descriptor: descriptor, started: started, ref: ref}
}

// TestDelegateSandboxedRestoreAdoptsRetainedSandboxScratch is round-8 High 2. A
// restored SANDBOXED delegate must run in the sandbox scratch it originally
// worked in. prepareSubagentEnvironment provisions a fresh sandbox scratch via
// EnableSandbox, and adoptRetainedScratchFor's same-kind guard then skips the
// persisted sandbox slot, leaving the delegate in the empty fresh directory with
// its retained handle orphaned and its kernel wrapper pointing at the mint.
func TestDelegateSandboxedRestoreAdoptsRetainedSandboxScratch(t *testing.T) {
	t.Parallel()
	fixture := newRestorableSandboxedDelegate(t, sandbox.ScratchKindSandbox)
	artifact := filepath.Join(fixture.ref.Dir, "durable.bin")
	if err := os.WriteFile(artifact, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}

	sub, restored, err := (delegateRuntime{owner: fixture.root}).restoreIdle(fixture.started)
	if err != nil {
		t.Fatalf("restore sandboxed delegate: %v", err)
	}
	if sub == nil || !restored {
		t.Fatalf("restore returned sub=%v restored=%t", sub, restored)
	}
	defer sub.sess.discardRestoredCandidate()

	local, ok := sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("restored env = %T, want a local environment", sub.sess.currentEnv())
	}
	if got := local.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(fixture.ref.Dir) {
		t.Fatalf("restored sandboxed delegate ran in scratch %q, want its retained %q", got, fixture.ref.Dir)
	}
	wrapper := local.KernelWrapper()
	if wrapper == nil || filepath.Clean(wrapper.SessionTmp()) != filepath.Clean(fixture.ref.Dir) {
		t.Fatalf("kernel wrapper TMPDIR = %v, want the retained scratch %q", wrapper, fixture.ref.Dir)
	}
	if got, err := os.ReadFile(artifact); err != nil || string(got) != "durable" {
		t.Fatalf("restored delegate lost its retained artifact: bytes=%q err=%v", got, err)
	}
}

// TestDelegateSandboxedRestoreFailurePreservesAdoptedUnsandboxedScratch is
// round-8 Medium 3. The restore used to re-derive adoption from a
// SessionScratchDir() before/after comparison; for a sandboxed env that accessor
// reflects only the wrapper tmp, so a transferred unsandboxed retained slot left
// adoptedScratch false and the failure path disposed the durable directory. The
// adopted flag must come from adoption itself.
func TestDelegateSandboxedRestoreFailurePreservesAdoptedUnsandboxedScratch(t *testing.T) {
	t.Parallel()
	fixture := newRestorableSandboxedDelegate(t, sandbox.ScratchKindUnsandboxed)
	artifact := filepath.Join(fixture.ref.Dir, "durable.bin")
	if err := os.WriteFile(artifact, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("restored delegate construction failed")
	fixture.root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	sub, restored, err := (delegateRuntime{owner: fixture.root}).restoreIdle(fixture.started)
	if !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}
	if sub != nil || restored {
		t.Fatalf("failed restore returned sub=%p restored=%v", sub, restored)
	}
	if _, err := os.Stat(fixture.ref.Dir); err != nil {
		t.Fatalf("failed sandboxed restore destroyed the adopted retained scratch %s: %v", fixture.ref.Dir, err)
	}
	if got, err := os.ReadFile(artifact); err != nil || string(got) != "durable" {
		t.Fatalf("adopted retained artifact lost: bytes=%q err=%v", got, err)
	}
	// Its lease is handed back to the pool rather than leaked: the failed
	// restore settles by the manifest (round 30), and the settle's handoff
	// for a transferred handle is the requeue — the next in-process restore
	// of this child re-claims it without flock churn.
	pool := fixture.root.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the restored root has no retained-scratch pool after the failed sandboxed restore")
	}
	pool.mu.Lock()
	requeued := pool.handles[canonicalScratchDir(fixture.ref.Dir)]
	pool.mu.Unlock()
	if requeued == nil {
		t.Fatalf("adopted retained scratch %q was not requeued into the pool for the next restore", fixture.ref.Dir)
	}
}

// TestDiscardRestoredCandidateRetainsAdoptedRetainedScratch is round-8 High 1 on
// the delegate discard path: a candidate whose environment adopted a durable
// retained allocation must release that allocation's lease and keep the
// directory, not os.RemoveAll it out from under the manifest a later resume
// reacquires.
func TestDiscardRestoredCandidateRetainsAdoptedRetainedScratch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	candidate := newSession(t, withDir(dir), withConfig(SessionConfig{
		StateDir: dir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true},
	}))
	owner, ok := candidate.scratchRetentionOwner()
	if !ok {
		t.Fatal("candidate has no scratch retention owner")
	}
	local, ok := candidate.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("candidate env = %T, want a local environment", candidate.currentEnv())
	}
	ref := pinRetainedConsumerSlot(t, owner, candidate.id, "E-r8-discard", dir, sandbox.ScratchKindUnsandboxed)
	artifact := filepath.Join(ref.Dir, "durable.bin")
	if err := os.WriteFile(artifact, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var installed sandbox.ScratchBinding
	for _, candidateBinding := range manifest.Bindings {
		if candidateBinding.BindingID == "E-r8-discard" {
			installed = candidateBinding
		}
	}
	if installed.BindingID == "" {
		t.Fatal("fixture binding E-r8-discard missing from the manifest")
	}
	handle, err := sandbox.OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SetScratchRetentionBinding(owner, installed); err != nil {
		t.Fatal(err)
	}
	if err := local.RestoreSessionScratch(installed.BindingID, ref, handle); err != nil {
		t.Fatal(err)
	}
	candidate.ownsEnv = true

	candidate.discardRestoredCandidate()

	if _, err := os.Stat(ref.Dir); err != nil {
		t.Fatalf("discarding the candidate removed the adopted retained scratch %s: %v", ref.Dir, err)
	}
	if got, err := os.ReadFile(artifact); err != nil || string(got) != "durable" {
		t.Fatalf("discard lost the adopted retained artifact: bytes=%q err=%v", got, err)
	}
	released, err := sandbox.OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatalf("discard did not release the adopted scratch lease: %v", err)
	}
	if err := released.Retain(); err != nil {
		t.Fatal(err)
	}
}

// --- roborev round 18: a partially adopted binding's failed-restore teardown ---

// pinRetainedScratchPair pins one sandbox-kind and one unsandboxed-kind
// allocation for a single child consumer's binding, writes an artifact into
// each, and releases both live leases so a cold restore reacquires them from
// the manifest. The sandbox slot is inserted first on purpose: binding.Slots is
// a map whose iteration order is randomized, and the slot that can be
// transferred has to be attempted before the slot that cannot for the partial
// adoption to happen at all.
func pinRetainedScratchPair(t *testing.T, owner sandbox.ScratchOwner, childID, bindingID, workDir string) (sandbox.ScratchReference, sandbox.ScratchReference) {
	t.Helper()
	pin := func(kind string) sandbox.ScratchReference {
		scratch, err := sandbox.NewSessionScratch(t.TempDir(), workDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
		ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: kind}
		if err := scratch.Pin(owner, ref); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scratch.Dir, "durable.bin"), []byte("durable"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := scratch.Retain(); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	sandboxRef := pin(sandbox.ScratchKindSandbox)
	unsandboxedRef := pin(sandbox.ScratchKindUnsandboxed)
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{{
			BindingID:      bindingID,
			OwnerSessionID: owner.RootSessionID,
			WorkingDir:     workDir,
			Slots: map[string]sandbox.ScratchSlot{
				sandbox.ScratchKindSandbox:     {Dir: sandboxRef.Dir, OwnsLease: true},
				sandbox.ScratchKindUnsandboxed: {Dir: unsandboxedRef.Dir, OwnsLease: true},
			},
		}},
		[]sandbox.ScratchConsumerBinding{{SessionID: childID, CurrentBindingID: bindingID}}); err != nil {
		t.Fatal(err)
	}
	return sandboxRef, unsandboxedRef
}

// TestDelegatePartialAdoptionFailurePreservesRetainedScratch is round 18's
// Medium. A binding's slots are transferred one at a time, so an adoption can
// commit the earlier slot and then fail on a later one; adoptConsumerScratch
// then reports that nothing was adopted, and the failed restore's teardown ran
// DisposeUnadoptedScratch over the directory the earlier slot had already
// transferred — a directory the durable retention manifest still references, so
// the root's retirement preparation would refuse and a cold resume would fail
// on it. The teardown has to settle the environment by what the manifest names:
// keep the referenced allocations, hand a transferred slot back to the pool
// with its claim cleared, and dispose only what no reference names.
//
// binding.Slots iterates in a randomized order, so a single restore may attempt
// the unadoptable slot first and fail before transferring anything; the fixture
// is driven repeatedly, each run with its own fresh environment, until the
// failing order is exercised. Every run has to leave the referenced allocations
// intact and the pool still able to hand the sandbox slot out.
func TestDelegatePartialAdoptionFailurePreservesRetainedScratch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	owner, ok := root1.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no scratch retention owner")
	}
	childID := identifier.MustNewSessionID()
	workDir := t.TempDir()
	const bindingID = "E-r18-partial"
	sandboxRef, unsandboxedRef := pinRetainedScratchPair(t, owner, childID, bindingID, workDir)
	meta := root1.Meta()
	crashLiveRoot(t, root1)

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       sandbox.FakeProber{Facts: sbxBwrapFacts(t.TempDir())},
		},
	})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(root.Close)

	childMeta := root.Meta()
	childMeta.ID = childID
	childMeta.ParentSessionID = root.id
	childMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(dir, childMeta); err != nil {
		t.Fatal(err)
	}
	childConfig := childMeta.Config.Clone()
	childConfig.AgentName = "subagent"
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		OwnerSessionID:    root.id,
		VisibleSessionID:  root.id,
		Task:              "resume a partially adoptable retained binding",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		// A read-only structured tool scope provisions the child its own sandbox
		// scratch, which the adoption's wrapper branch replaces with the
		// retained directory before it transfers it.
		ToolNameCeiling: []string{"communicate", "read_file", "shell"},
		WorkingDir:      workDir,
		LocalEnvPolicy:  "default",
		Config:          childConfig,
		Resumable:       true,
	}
	started := delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: descriptor,
	}

	// The prepared pool, minus the handle prepareRetainedScratch would have
	// reacquired for the unsandboxed slot: exactly the state its liveDirs skip
	// leaves for a reference whose directory the session's own environment
	// already holds, with no handle and no contention recorded.
	pool := root.retainedScratch.Load()
	if pool == nil {
		t.Fatal("restore published no retained scratch pool")
	}
	pool.mu.Lock()
	dropped := pool.handles[filepath.Clean(unsandboxedRef.Dir)]
	delete(pool.handles, filepath.Clean(unsandboxedRef.Dir))
	pool.mu.Unlock()
	if dropped == nil {
		t.Fatal("pool held no handle for the unsandboxed slot to drop")
	}
	_ = dropped.Retain()

	key := filepath.Clean(sandboxRef.Dir)
	for range 16 {
		sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
		if err == nil {
			t.Fatalf("restore of a binding with an unadoptable slot succeeded: sub=%p restored=%t", sub, restored)
		}
		if sub != nil || restored {
			t.Fatalf("failed restore returned sub=%p restored=%t", sub, restored)
		}
		for _, ref := range []sandbox.ScratchReference{sandboxRef, unsandboxedRef} {
			got, err := os.ReadFile(filepath.Join(ref.Dir, "durable.bin"))
			if err != nil || string(got) != "durable" {
				t.Fatalf("failed restore destroyed the manifest-referenced %s scratch %q: bytes=%q err=%v", ref.Kind, ref.Dir, got, err)
			}
		}
		if pool.handles[key] == nil || pool.adopted[key] != "" {
			t.Fatalf("failed restore left the transferred slot unusable: pooled=%v claim=%q", pool.handles[key] != nil, pool.adopted[key])
		}
	}

	// Nothing erased the references either: a later restore still has both
	// allocations to reacquire.
	after, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []sandbox.ScratchReference{sandboxRef, unsandboxedRef} {
		referenced := false
		for _, candidate := range after.References {
			if filepath.Clean(candidate.Dir) == filepath.Clean(ref.Dir) {
				referenced = true
			}
		}
		if !referenced {
			t.Fatalf("retained reference for %s was erased: %+v", ref.Dir, after.References)
		}
	}
}

// --- roborev round 25: the poolless settle's transferred allocation ---

// TestSettleFailedRestoreKeepsTransferredScratchWithoutAPool pins round 25's
// first Medium. A partial adoption commits an earlier slot before failing on a
// later one, leaving the environment holding a manifest-referenced allocation
// the pool already handed over; when the pool then detaches — swept between
// the adoption's legs and the settle — the settle's poolless created-
// environment branch assumed every reference such an environment held was its
// own fresh mint and disposed the transferred directory the manifest still
// references, the exact leak this settle exists to prevent. The classification
// is poolless by construction, so it must run for a detached pool too: the
// transferred allocation is kept with its lease released for a later restore
// to reacquire, and only the unreferenced mint dies with the failed
// construction.
func TestSettleFailedRestoreKeepsTransferredScratchWithoutAPool(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01R25DETACHED1"
	const bindingID = "b-r25-detached"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindUnsandboxed)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindUnsandboxed].Dir
	if err := os.WriteFile(filepath.Join(retainedDir, "durable.bin"), []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The pool detaches after the partial adoption — the state a sweep between
	// the adoption's legs and the settle leaves behind.
	s.retainedScratch.Store(nil)

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}
	fresh := env.SessionScratchDir()
	if fresh == "" || filepath.Clean(fresh) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture fresh scratch %q must exist apart from the retained %q", fresh, retainedDir)
	}
	// The partial adoption's committed leg: the retained unsandboxed
	// allocation is transferred into the environment the failed construction
	// created.
	if err := env.SetScratchRetentionBinding(owner, bindingRow); err != nil {
		t.Fatal(err)
	}
	if err := env.RestoreSessionScratch(bindingID, sandbox.ScratchReference{Dir: retainedDir, Kind: sandbox.ScratchKindUnsandboxed}, slots[sandbox.ScratchKindUnsandboxed]); err != nil {
		t.Fatalf("transfer the retained unsandboxed allocation: %v", err)
	}

	s.settleFailedRestoreScratch(env, consumerID, true)

	// The transferred allocation survives the failed restore's teardown...
	if got, err := os.ReadFile(filepath.Join(retainedDir, "durable.bin")); err != nil || string(got) != "durable" {
		t.Fatalf("the settle destroyed the manifest-referenced transferred allocation %q: bytes=%q err=%v", retainedDir, got, err)
	}
	// ...and stays manifest-referenced for a later restore to reacquire.
	after, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	referenced := false
	for _, candidate := range after.References {
		if filepath.Clean(candidate.Dir) == filepath.Clean(retainedDir) {
			referenced = true
		}
	}
	if !referenced {
		t.Fatalf("the transferred allocation lost its manifest reference: %+v", after.References)
	}
	// The construction's own fresh mint dies with the failure.
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("the settle kept the failed construction's fresh mint %q: %v", fresh, err)
	}
	// The settle released the transferred allocation's lease: a later restore
	// reacquires it from the manifest.
	handle, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: retainedDir, Kind: sandbox.ScratchKindUnsandboxed})
	if err != nil {
		t.Fatalf("the settle did not release the transferred allocation's lease: %v", err)
	}
	if err := handle.Retain(); err != nil {
		t.Fatal(err)
	}
}

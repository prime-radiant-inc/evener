package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// newResumeScratchLane builds a scripted-lane root session with an isolated
// state dir and NO Close registration, so a test can crash it (release the
// scratch lease and close its stores the way process death does) and then
// resume the same id, exactly as a daemon restart does. It mirrors
// newScriptedLaneRepoWithConfig without newSession's t.Cleanup(Close).
func newResumeScratchLane(t *testing.T) (*scriptedLaneRepo, *Session) {
	t.Helper()
	root := scriptedCanonicalDir(t, t.TempDir())
	stateDir := scriptedCanonicalDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".git", "worktrees"), 0o755); err != nil {
		t.Fatalf("create scripted main git dir: %v", err)
	}
	git := newScriptedWorktreeGit(root)
	cfg := worktreeTestSessionConfig()
	cfg.StateDir = stateDir
	cfg.NoProjectPrompts = true
	cfg.MaxSubagentDepth = 1
	cfg.testOnly.skipGitSnapshot = true
	cfg.testOnly.minimalSystemPrompt = true
	cfg.testOnly.noSyncJobStore = true
	cfg.testOnly.environmentInfo = scriptedEnvironmentInfo
	cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
		return git.run
	}
	sess, err := NewSession(w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.stateDir = stateDir
	sess.mu.Lock()
	sess.worktreeGitVersionOK = true
	sess.mu.Unlock()
	return &scriptedLaneRepo{t: t, s: sess, git: git, mainRoot: root, stateDir: stateDir}, sess
}

// crashResumeScratchRoot abandons the live session's runtime the way process
// death does: release the scratch leases (keeping the directories) and close
// the transcript and job store with no teardown appends, so the same id can be
// resumed over the same state dir.
func crashResumeScratchRoot(t *testing.T, sess *Session, envs ...*execenv.LocalExecutionEnvironment) {
	t.Helper()
	for _, env := range envs {
		if env != nil {
			env.RetainSessionScratch()
		}
	}
	if err := sess.closeAttachedTranscript(); err != nil {
		t.Fatalf("close crashed transcript: %v", err)
	}
	if sess.jobManager != nil {
		_ = sess.jobManager.closeStoreOnly()
	}
}

// scratchBindingOwning reports the binding that holds the lease-owning slot for
// dir in the loaded manifest, plus that slot's kind.
func scratchBindingOwning(t *testing.T, manifest sandbox.ScratchManifest, dir string) (sandbox.ScratchBinding, string, bool) {
	t.Helper()
	want := filepath.Clean(dir)
	for _, binding := range manifest.Bindings {
		for kind, slot := range binding.Slots {
			if !slot.OwnsLease {
				continue
			}
			if filepath.Clean(slot.Dir) == want {
				return binding, kind, true
			}
		}
	}
	return sandbox.ScratchBinding{}, "", false
}

func scratchConsumerFor(t *testing.T, manifest sandbox.ScratchManifest, sessionID string) sandbox.ScratchConsumerBinding {
	t.Helper()
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == sessionID {
			return consumer
		}
	}
	t.Fatalf("consumer %q missing: %+v", sessionID, manifest.Consumers)
	return sandbox.ScratchConsumerBinding{}
}

// TestRetirementRootWorktreeMoveKeepsPerEnvironmentBindings is plan 646/648's
// binding-identity case on the real swap path: root R mints A on its launch
// environment E0, enters a real worktree (constructing the clone E1 and adopting
// A), and a shared child on the parked E0 later mints B. The manifest must hold
// two distinct bindings, both owned by R: E1 owns A and E0 keeps its own
// identity and owns B. Collapsing E0 and E1 onto one binding would leave one of
// the two scratches without an owning slot, so it could not restore (plan 654).
func TestRetirementRootWorktreeMoveKeepsPerEnvironmentBindings(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	root := r.s
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	launch, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("launch env = %T, want a local environment", root.currentEnv())
	}
	// E0's first command mints A and pins it under E0's binding.
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	aDir := launch.SessionScratchDir()
	if aDir == "" {
		t.Fatal("launch environment minted no scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(aDir) })
	launchBinding, err := launch.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("launch retention binding: %v", err)
	}
	e0ID := launchBinding.BindingID
	if e0ID == "" {
		t.Fatal("launch environment has no binding id")
	}

	// Real worktree enter: constructs E1 and adopts A off E0.
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	entered, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok || entered == launch {
		t.Fatalf("enter installed env %p, want a distinct clone beside %p", root.currentEnv(), launch)
	}
	if got := entered.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(aDir) {
		t.Fatalf("entered environment scratch = %q, want the adopted %q", got, aDir)
	}

	// A shared child on the parked E0 mints B there.
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("child command on the parked environment: %v", err)
	}
	bDir := launch.SessionScratchDir()
	if bDir == "" || filepath.Clean(bDir) == filepath.Clean(aDir) {
		t.Fatalf("parked environment scratch after the move = %q, want a fresh one beside %q", bDir, aDir)
	}
	t.Cleanup(func() { _ = os.RemoveAll(bDir) })

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if len(manifest.Bindings) != 2 {
		t.Fatalf("bindings = %d, want two distinct owned environments: %+v", len(manifest.Bindings), manifest.Bindings)
	}
	aBinding, _, ok := scratchBindingOwning(t, manifest, aDir)
	if !ok {
		t.Fatalf("no binding owns A at %q: %+v", aDir, manifest.Bindings)
	}
	bBinding, _, ok := scratchBindingOwning(t, manifest, bDir)
	if !ok {
		t.Fatalf("no binding owns B at %q: %+v", bDir, manifest.Bindings)
	}
	if aBinding.BindingID == bBinding.BindingID {
		t.Fatalf("A and B are owned by one collapsed binding %q", aBinding.BindingID)
	}
	if aBinding.OwnerSessionID != root.id || bBinding.OwnerSessionID != root.id {
		t.Fatalf("bindings owned by %q and %q, want both owned by root %q", aBinding.OwnerSessionID, bBinding.OwnerSessionID, root.id)
	}
	if bBinding.BindingID != e0ID {
		t.Fatalf("parked E0 binding = %q, want the id %q it held before the move", bBinding.BindingID, e0ID)
	}
	if aBinding.WorkingDir == bBinding.WorkingDir {
		t.Fatalf("both bindings report working dir %q; the clone kept the parked identity", aBinding.WorkingDir)
	}
	consumer := scratchConsumerFor(t, manifest, root.id)
	if consumer.CurrentBindingID != aBinding.BindingID {
		t.Fatalf("root current binding = %q, want the A-owning clone %q", consumer.CurrentBindingID, aBinding.BindingID)
	}
	// The parked environment and a real child consumer of it must both resolve
	// to E0's persisted identity.
	if consumer.WorktreeRestoreBindingID != e0ID {
		t.Fatalf("worktree-restore role = %q, want the parked E0 %q", consumer.WorktreeRestoreBindingID, e0ID)
	}
	if err := root.installChildScratchRetention(launch, "shared-child"); err != nil {
		t.Fatalf("register shared child: %v", err)
	}
	manifest, err = sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if child := scratchConsumerFor(t, manifest, "shared-child"); child.CurrentBindingID != e0ID {
		t.Fatalf("shared child consumer binding = %q, want the shared E0 %q", child.CurrentBindingID, e0ID)
	}
}

// TestRetirementSharedChildUsesTheSharedEnvironmentsBinding covers plan 646/648's
// shared-child rule directly: a child spawned on an existing environment object
// registers under THAT environment's binding id rather than a fabricated
// child-owned one, and the binding's owner is not renamed to the child.
func TestRetirementSharedChildUsesTheSharedEnvironmentsBinding(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	shared := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(func() { shared.RetainSessionScratch() })
	if err := shared.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "shared-env",
		OwnerSessionID: root.id,
		WorkingDir:     dir,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("mint shared scratch: %v", err)
	}

	if err := root.installChildScratchRetention(shared, "child-session"); err != nil {
		t.Fatalf("install child retention: %v", err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	consumer := scratchConsumerFor(t, manifest, "child-session")
	if consumer.CurrentBindingID != "shared-env" {
		t.Fatalf("child consumer binding = %q, want the shared environment's %q", consumer.CurrentBindingID, "shared-env")
	}
	sharedBinding, ok := findScratchBinding(manifest, "shared-env")
	if !ok {
		t.Fatalf("shared environment's binding missing: %+v", manifest.Bindings)
	}
	if sharedBinding.OwnerSessionID != root.id {
		t.Fatalf("shared binding owner = %q, want root %q (never renamed to the child)", sharedBinding.OwnerSessionID, root.id)
	}
	// No binding was fabricated for the child: every binding belongs to a real
	// owned environment (here the root's own launch env and the shared one).
	for _, binding := range manifest.Bindings {
		if binding.OwnerSessionID == "child-session" {
			t.Fatalf("child-owned binding was fabricated: %+v", binding)
		}
	}
}

// TestRetirementRootWorktreeExitKeepsParkedIdentityAndMintedSlot covers the
// backswap direction: R exits the worktree back to the occupied E0. E0 keeps its
// own binding id and B's owning slot, the incoming A loses only its owning slot
// (staying a pinned reference, reacquirable at its original path), E1's identity
// survives for reuse, and R's current binding resolves back to E0. The swap hook
// runs after AdoptSessionScratch has already released A's lease, so observing the
// manifest already clear of A's owning slot there proves the transition was
// persisted before that release, not after.
func TestRetirementRootWorktreeExitKeepsParkedIdentityAndMintedSlot(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	root := r.s
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	launch, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("launch env = %T, want a local environment", root.currentEnv())
	}
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	aDir := launch.SessionScratchDir()
	if aDir == "" {
		t.Fatal("launch environment minted no scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(aDir) })
	launchBinding, err := launch.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	e0ID := launchBinding.BindingID

	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	entered, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok || entered == launch {
		t.Fatal("enter did not install a distinct clone")
	}
	enteredBinding, err := entered.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	e1ID := enteredBinding.BindingID
	if e1ID == "" || e1ID == e0ID {
		t.Fatalf("clone binding = %q, want a distinct id beside %q", e1ID, e0ID)
	}

	// A shared child on the parked E0 mints B there.
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("child command on the parked environment: %v", err)
	}
	bDir := launch.SessionScratchDir()
	if bDir == "" || filepath.Clean(bDir) == filepath.Clean(aDir) {
		t.Fatalf("parked environment scratch = %q, want a fresh one beside %q", bDir, aDir)
	}
	t.Cleanup(func() { _ = os.RemoveAll(bDir) })

	// The hook runs at step 0b: after AdoptSessionScratch moved the handles (and
	// so after A's lease was released) but before the environment is installed.
	ownedDuringMove := true
	root.cfg.testOnly.swapEnvAfterAdopt = func(context.Context) {
		manifest, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("load manifest during exit: %v", err)
			return
		}
		_, _, ownedDuringMove = scratchBindingOwning(t, manifest, aDir)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if ownedDuringMove {
		t.Fatal("exit released A's lease before the manifest dropped E1's owning slot")
	}
	if got, _ := root.currentEnv().(*execenv.LocalExecutionEnvironment); got != launch {
		t.Fatalf("after exit the session holds %p, want the parked environment %p", root.currentEnv(), launch)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	bBinding, _, ok := scratchBindingOwning(t, manifest, bDir)
	if !ok || bBinding.BindingID != e0ID {
		t.Fatalf("E0 binding = %+v ok=%v, want id %q owning B", bBinding, ok, e0ID)
	}
	if bBinding.OwnerSessionID != root.id {
		t.Fatalf("E0 binding owner = %q, want root %q", bBinding.OwnerSessionID, root.id)
	}
	if _, _, ok := scratchBindingOwning(t, manifest, aDir); ok {
		t.Fatalf("a binding still owns the backswapped A: %+v", manifest.Bindings)
	}
	if consumer := scratchConsumerFor(t, manifest, root.id); consumer.CurrentBindingID != e0ID {
		t.Fatalf("root current binding = %q, want E0 %q", consumer.CurrentBindingID, e0ID)
	}
	if _, ok := findScratchBinding(manifest, e1ID); !ok {
		t.Fatalf("E1's binding identity %q did not survive the backswap", e1ID)
	}
	// A stays a pinned reference: its lease is free and it reacquires at its
	// original path.
	handle, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: aDir, Kind: sandbox.ScratchKindUnsandboxed})
	if err != nil {
		t.Fatalf("A was not retained as a pinned reference: %v", err)
	}
	_ = handle.Retain()
}

// TestRetirementResumedWorktreeKeepsBindingIdentityAcrossBackswap is plan
// 646/648/650/654's cold-resume case: a root crashes while occupied in a real
// worktree and is resumed over the same state dir. The re-entered environment
// must carry the persisted binding identity for the environment it represents
// (E1, the one that owned A), its parked worktreeRestoreEnv must carry the
// parked environment's identity (E0), and a post-resume backswap must persist
// the ownership transition before the move, leaving no stale owning slot on E1.
func TestRetirementResumedWorktreeKeepsBindingIdentityAcrossBackswap(t *testing.T) {
	sr, root := newResumeScratchLane(t)
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	launch, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("launch env = %T, want a local environment", root.currentEnv())
	}
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	aDir := launch.SessionScratchDir()
	if aDir == "" {
		t.Fatal("launch environment minted no scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(aDir) })
	launchBinding, err := launch.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	e0ID := launchBinding.BindingID
	if e0ID == "" {
		t.Fatal("launch environment has no binding id")
	}

	r := sr.wt()
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	entered, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok || entered == launch {
		t.Fatal("enter did not install a distinct clone")
	}
	enteredBinding, err := entered.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	e1ID := enteredBinding.BindingID
	if e1ID == "" || e1ID == e0ID {
		t.Fatalf("clone binding = %q, want a distinct id beside %q", e1ID, e0ID)
	}
	meta := root.Meta()
	if meta.WorktreePath == "" || meta.WorktreeRestoreRoot == "" {
		t.Fatalf("crashed meta records no worktree occupancy: %+v", meta)
	}

	// Crash: release the lease the entered environment holds and abandon the
	// runtime, then resume the same id over the same state dir.
	crashResumeScratchRoot(t, root, entered)
	restored, err := RestoreSessionFromMetaWithConfig(w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(sr.mainRoot), meta, sr.restoreConfig())
	if err != nil {
		t.Fatalf("resume root: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	reentered, ok := restored.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("re-entered env = %T, want a local environment", restored.currentEnv())
	}
	if got := reentered.WorkingDirectory(); got != meta.WorktreePath {
		t.Fatalf("re-entered working dir = %q, want the persisted worktree %q", got, meta.WorktreePath)
	}
	reenteredBinding, err := reentered.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("re-entered env binding: %v (want the persisted E1 %q)", err, e1ID)
	}
	if reenteredBinding.BindingID != e1ID {
		t.Fatalf("re-entered env binding id = %q, want the persisted E1 %q", reenteredBinding.BindingID, e1ID)
	}
	if got := reentered.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(aDir) {
		t.Fatalf("re-entered scratch = %q, want the restored A %q", got, aDir)
	}
	// The resumed environment owns A's lease under its own identity, so
	// PinOwnedScratch (which no-ops only when an env carries no binding) now
	// publishes any later mint durably instead of dropping it.
	if slot := reenteredBinding.Slots[sandbox.ScratchKindUnsandboxed]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(aDir) {
		t.Fatalf("re-entered binding slot = %+v, want it owning A at %q", slot, aDir)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if consumer := scratchConsumerFor(t, manifest, restored.id); consumer.WorktreeRestoreBindingID != e0ID {
		t.Fatalf("resumed worktree-restore role = %q, want the parked E0 %q", consumer.WorktreeRestoreBindingID, e0ID)
	}

	// The hook runs after AdoptSessionScratch (the source E1's handles have
	// already moved) and before the install, so observing E0 owning A there
	// proves the transition was persisted before the lease moved, not after.
	e0OwnedAAtMove := false
	restored.cfg.testOnly.swapEnvAfterAdopt = func(context.Context) {
		current, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("load manifest during post-resume exit: %v", err)
			return
		}
		owning, _, ok := scratchBindingOwning(t, current, aDir)
		e0OwnedAAtMove = ok && owning.BindingID == e0ID
	}
	if _, ok, err := restored.exitWorktree(); err != nil || !ok {
		t.Fatalf("post-resume exit = ok=%v err=%v, want a backswap", ok, err)
	}
	if !e0OwnedAAtMove {
		t.Fatal("post-resume backswap had not persisted E0 owning A before the move completed")
	}

	manifest, err = sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	owning, _, ok := scratchBindingOwning(t, manifest, aDir)
	if !ok || owning.BindingID != e0ID {
		t.Fatalf("A owner after the post-resume backswap = %+v ok=%v, want E0 %q", owning, ok, e0ID)
	}
	for _, binding := range manifest.Bindings {
		if binding.BindingID == e1ID {
			if slot, still := binding.Slots[sandbox.ScratchKindUnsandboxed]; still && slot.OwnsLease {
				t.Fatalf("stale lease-owning A slot survived on the pre-crash binding %q: %+v", e1ID, binding.Slots)
			}
		}
	}
	if consumer := scratchConsumerFor(t, manifest, restored.id); consumer.CurrentBindingID != e0ID {
		t.Fatalf("resumed root current binding = %q, want E0 %q", consumer.CurrentBindingID, e0ID)
	}
}

// TestRetirementSwapStagePreservesRecordedRoles covers plan 650's role
// durability across a swap: the stage transaction may change only the session's
// current binding, so the role ids it already recorded (the parked
// worktree-restore binding here) must survive the seconds-wide window before
// the swap completes and re-registers roles. A crash or refusal in that window
// must not leave the manifest without them.
func TestRetirementSwapStagePreservesRecordedRoles(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	root := r.s
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	launch, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("launch env = %T, want a local environment", root.currentEnv())
	}
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	aDir := launch.SessionScratchDir()
	t.Cleanup(func() { _ = os.RemoveAll(aDir) })
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	parkedID := scratchConsumerFor(t, manifest, root.id).WorktreeRestoreBindingID
	if parkedID == "" {
		t.Fatal("the live enter recorded no worktree-restore role to preserve")
	}

	wiped := false
	root.cfg.testOnly.swapEnvAfterAdopt = func(context.Context) {
		current, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("load manifest during exit: %v", err)
			return
		}
		for _, consumer := range current.Consumers {
			if consumer.SessionID == root.id && consumer.WorktreeRestoreBindingID == "" {
				wiped = true
			}
		}
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if wiped {
		t.Fatal("the swap stage durably wiped the session's recorded worktree-restore role before the swap completed")
	}
}

// TestRetirementColdDelegateScratchManifest proves the root-owned manifest
// round-trips through the agent layer: a pinned allocation referenced only by a
// cold consumer is reacquired by prepareRetainedScratch and adopted by the
// consumer's binding at its original absolute path with its bytes intact.
func TestRetirementColdDelegateScratchManifest(t *testing.T) {
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
	artifact := filepath.Join(scratch.Dir, "cold.bin")
	want := []byte("cold-delegate-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
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
	binding := sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     dir,
		Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{binding},
		[]sandbox.ScratchConsumerBinding{{SessionID: "cold-child", CurrentBindingID: "E0"}}); err != nil {
		t.Fatal(err)
	}
	// Release the live lease: a cold consumer's allocation must restore from the
	// manifest, not from a live handle.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}

	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch: %v", err)
	}
	if id, ok := root.retainedScratchBindingFor("cold-child", "current"); !ok || id != "E0" {
		t.Fatalf("cold consumer binding = %q ok=%v, want E0", id, ok)
	}
	if _, ok := root.retainedScratchBindingFor("missing-child", "current"); ok {
		t.Fatal("unknown consumer resolved a binding")
	}

	env := execenv.NewLocalExecutionEnvironment(dir)
	if err := root.adoptRetainedScratch(env, "E0"); err != nil {
		t.Fatalf("adoptRetainedScratch: %v", err)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
		t.Fatalf("restored scratch dir = %q, want original %q", got, scratch.Dir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("artifact lost at original path: bytes=%q err=%v", got, err)
	}
	// A second adoption of the same binding must fail rather than duplicate the
	// lease onto another environment.
	other := execenv.NewLocalExecutionEnvironment(dir)
	if err := root.adoptRetainedScratch(other, "E0"); err == nil {
		t.Fatal("adopted an already-transferred binding onto a second environment")
	}
	env.RetainSessionScratch()
}

// TestRetirementLiveSessionMintsScratchManifest proves a live root session
// publishes its retention manifest and binding at construction, so the
// primitives are not inert in production.
func TestRetirementLiveSessionMintsScratchManifest(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Revision == 0 || len(manifest.Bindings) != 1 || len(manifest.Consumers) != 1 {
		t.Fatalf("live session did not mint a retention manifest: %+v", manifest)
	}
	binding := manifest.Bindings[0]
	if binding.OwnerSessionID != root.id || binding.WorkingDir != dir || binding.BindingID == "" {
		t.Fatalf("minted binding = %+v, want owner %q working dir %q", binding, root.id, dir)
	}
	if consumer := manifest.Consumers[0]; consumer.SessionID != root.id || consumer.CurrentBindingID != binding.BindingID {
		t.Fatalf("minted consumer = %+v, want current binding %q", consumer, binding.BindingID)
	}
}

// TestRetirementAgedScratchRestoresAtOriginalPath is the plan's end-to-end aged
// proof: a required artifact is created through a real delegate child's
// environment, the daemon "crashes" leaving only durable state, the same root
// is restored, and sending to the SAME cold delegate restores that child's real
// environment at the artifact's original absolute path.
func TestRetirementAgedScratchRestoresAtOriginalPath(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	rootID := root1.ID()
	d := retirementIdleDelegate(t, root1)
	tree1 := root1.delegateController
	child := tree1.residentDelegateRuntime(d.DelegateID)
	if child == nil {
		t.Fatal("original delegate runtime missing")
	}
	childEnv, _ := child.env.(*execenv.LocalExecutionEnvironment)
	scratchDir := ""
	if childEnv != nil {
		scratchDir = childEnv.SessionScratchDir()
	}
	if scratchDir == "" {
		if local, ok := root1.env.(*execenv.LocalExecutionEnvironment); ok {
			scratchDir = local.SessionScratchDir()
		}
	}
	if scratchDir == "" {
		t.Fatal("child environment minted no scratch")
	}
	if childEnv != nil {
		if err := root1.installChildScratchRetention(childEnv, d.ChildSessionID); err != nil {
			t.Fatalf("install child retention: %v", err)
		}
	}
	artifact := filepath.Join(scratchDir, "aged-required.bin")
	want := []byte("aged-cold-delegate-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		t.Fatal(err)
	}
	meta := root1.Meta()

	// Crash: abandon runtime handles and close stores/transcripts with no
	// teardown appends, and release the scratch leases the way process death
	// would, keeping the directories for the handoff.
	if local, ok := root1.env.(*execenv.LocalExecutionEnvironment); ok {
		local.RetainSessionScratch()
	}
	if childEnv != nil {
		childEnv.RetainSessionScratch()
	}
	if child.jobManager != nil {
		_ = child.jobManager.closeStoreOnly()
	}
	_ = root1.jobManager.closeStoreOnly()
	if err := child.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	if err := root1.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	tree1.mu.Lock()
	if live := tree1.live[d.DelegateID]; live != nil {
		live.runtime = nil
	}
	tree1.mu.Unlock()

	// Daemon restart: resume the same root over the same state dir.
	client := llm.NewClient()
	client.Register(&retirementDelegateAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root2.Close()
	if root2.ID() != rootID {
		t.Fatalf("restored another root: %s", root2.ID())
	}
	// Send to the SAME cold delegate: this materializes the child through the
	// real restore path, which must adopt the retained scratch.
	outcome := (delegateRuntime{owner: root2}).send(context.Background(), d.DelegateID, "read-aged-artifact", 0)
	if outcome.result.Err != nil {
		t.Fatalf("send to cold delegate: %v", outcome.result.Err)
	}
	rchild := root2.delegateController.residentDelegateRuntime(d.DelegateID)
	if rchild == nil {
		t.Fatal("cold delegate was not restored")
	}
	rchildEnv, _ := rchild.env.(*execenv.LocalExecutionEnvironment)
	if rchildEnv == nil {
		t.Fatal("restored child has no local environment")
	}
	if got := rchildEnv.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratchDir) {
		t.Fatalf("restored child scratch = %q, want original %q", got, scratchDir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("aged artifact lost at original path %q: bytes=%q err=%v", artifact, got, err)
	}
	// Config and sandbox identity survive the restore.
	if rchild.envInfo.WorkingDir != child.envInfo.WorkingDir {
		t.Fatalf("restored working dir = %q, want %q", rchild.envInfo.WorkingDir, child.envInfo.WorkingDir)
	}
	if got, wantPolicy := localEnvPolicyName(rchild.env), localEnvPolicyName(child.env); got != wantPolicy {
		t.Fatalf("restored sandbox policy = %q, want %q", got, wantPolicy)
	}
	// Task state is reconstructible from the restored child.
	if tasks := rchild.getOrCreateTaskStore().View(); len(tasks) != 0 {
		t.Fatalf("restored child task store = %+v, want empty", tasks)
	}
}

// TestRetirementRootScratchRestoresAtOriginalPath proves the ROOT's own
// retained scratch is adopted on resume: a root mints scratch, crashes, is
// restored, and must find its original directory (not a fresh replacement),
// with the stored slot record preserved.
func TestRetirementRootScratchRestoresAtOriginalPath(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	rootID := root1.ID()
	env1, ok := root1.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}
	scratchDir := env1.SessionScratchDir()
	if scratchDir == "" {
		if _, err := env1.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
			t.Fatalf("mint root scratch: %v", err)
		}
		scratchDir = env1.SessionScratchDir()
	}
	if scratchDir == "" {
		t.Fatal("root minted no scratch")
	}
	artifact := filepath.Join(scratchDir, "root-required.bin")
	want := []byte("root-aged-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root1.installScratchRetention(env1); err != nil {
		t.Fatalf("install root retention: %v", err)
	}
	meta := root1.Meta()

	// Crash: release the scratch lease as process death would, close the store
	// and transcript with no teardown appends.
	env1.RetainSessionScratch()
	_ = root1.jobManager.closeStoreOnly()
	if err := root1.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root2.Close()
	if root2.ID() != rootID {
		t.Fatalf("restored another root: %s", root2.ID())
	}
	env2, ok := root2.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("restored root has no local environment")
	}
	if got := env2.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratchDir) {
		t.Fatalf("resumed root scratch = %q, want original %q", got, scratchDir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("root artifact lost at original path %q: bytes=%q err=%v", artifact, got, err)
	}

	// The stored slot record survives the restore-time republish.
	owner, ok := root2.scratchRetentionOwner()
	if !ok {
		t.Fatal("restored root has no retention owner")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var bindingID string
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == root2.id {
			bindingID = consumer.CurrentBindingID
		}
	}
	if bindingID == "" {
		t.Fatalf("root consumer binding missing: %+v", manifest.Consumers)
	}
	found := false
	for _, binding := range manifest.Bindings {
		if binding.BindingID != bindingID {
			continue
		}
		for _, slot := range binding.Slots {
			if filepath.Clean(slot.Dir) == filepath.Clean(scratchDir) && slot.OwnsLease {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("stored owning slot for %q was erased: %+v", scratchDir, manifest.Bindings)
	}
}

// TestRetirementConsumerRolesRecordEachBinding proves a consumer whose current
// binding is E1 and whose parent-shared environment is E0 records E0 for the
// shared role, so Task 6 can resolve each role to its exact binding.
func TestRetirementConsumerRolesRecordEachBinding(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no retention owner")
	}
	current, ok := root.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}
	// A distinct parent-shared environment owning its own allocation.
	shared := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(func() { shared.RetainSessionScratch() })
	if err := shared.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E0", OwnerSessionID: root.id, WorkingDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("mint shared scratch: %v", err)
	}
	root.mu.Lock()
	root.parentSharedEnv = shared
	root.mu.Unlock()

	if err := root.registerScratchConsumerRoles(current); err != nil {
		t.Fatalf("register roles: %v", err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var consumer sandbox.ScratchConsumerBinding
	found := false
	for _, candidate := range manifest.Consumers {
		if candidate.SessionID == root.id {
			consumer = candidate
			found = true
		}
	}
	if !found {
		t.Fatalf("root consumer missing: %+v", manifest.Consumers)
	}
	if consumer.ParentSharedBindingID != "E0" {
		t.Fatalf("parent-shared role = %q, want E0 (current %q)", consumer.ParentSharedBindingID, consumer.CurrentBindingID)
	}
	if consumer.ParentSharedBindingID == consumer.CurrentBindingID {
		t.Fatalf("shared role collapsed onto the current binding %q", consumer.CurrentBindingID)
	}
}

// TestRetirementRestoreFailsClosedOnReferenceWithoutBinding proves restore
// preparation refuses an incomplete retention manifest: a crash between
// publishing a pinned reference and publishing the binding/consumer that maps
// it leaves references with no binding at all. Restore must fail closed instead
// of preparing a pool that adopts nothing and lets initialization mint a
// replacement scratch directory, silently losing the original durable
// artifacts.
func TestRetirementRestoreFailsClosedOnReferenceWithoutBinding(t *testing.T) {
	stateDir := t.TempDir()
	base := t.TempDir()
	workDir := t.TempDir()
	const rootID = "crashed-root"
	owner := sandbox.ScratchOwner{StateDir: stateDir, RootSessionID: rootID}
	scratch, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
	// The directory pin and manifest reference committed; the binding and
	// consumer publication installScratchRetentionFor performs next never did,
	// exactly as a daemon crash in that window leaves it.
	if err := scratch.Pin(owner, sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) == 0 || len(manifest.Bindings) != 0 {
		t.Fatalf("fixture manifest = %+v, want references and no bindings", manifest)
	}

	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	root.stateDir = stateDir
	root.id = rootID
	root.delegateRootSessionID = ""
	if err := root.prepareRetainedScratch(); err == nil {
		t.Fatal("restore prepared a manifest that holds references but no binding")
	}
}

// TestRetirementResumedRootSandboxScratchRestoresAtOriginalPath is H1: a resumed
// root whose persisted binding owns a sandbox-allocated scratch must resume in
// THAT directory. Resume provisions the sandbox before retained-scratch adoption
// and EnableSandbox always mints a fresh session scratch; without dropping the
// fresh mint, the same-kind guard leaves the session in a new directory, orphans
// the retained sandbox slot, and the wrapper grants the wrong TMPDIR.
func TestRetirementResumedRootSandboxScratchRestoresAtOriginalPath(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	owner, ok := root1.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no scratch retention owner")
	}
	env1, ok := root1.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}
	// Mint a sandbox-kind scratch through the real EnableSandbox path (a
	// write-blocked off policy eagerly provisions one without a kernel wrapper).
	if err := env1.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("provision E0's sandbox scratch: %v", err)
	}
	scratchDir := env1.SessionScratchDir()
	if scratchDir == "" {
		t.Fatal("E0 minted no sandbox scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratchDir) })
	artifact := filepath.Join(scratchDir, "root-sandbox-required.bin")
	want := []byte("root-sandbox-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root1.installScratchRetention(env1); err != nil {
		t.Fatalf("install root retention: %v", err)
	}
	before, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := findScratchBinding(before, scratchConsumerFor(t, before, root1.id).CurrentBindingID)
	if !ok {
		t.Fatal("root has no current binding")
	}
	if slot := binding.Slots[sandbox.ScratchKindSandbox]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(scratchDir) {
		t.Fatalf("fixture binding sandbox slot = %+v, want it owning %q", slot, scratchDir)
	}
	meta := root1.Meta()
	// Persist a non-off mode so the resume re-provisions an enforced sandbox and
	// mints a fresh session scratch before adoption runs.
	meta.Config.Sandbox = sandbox.ModeRestricted.String()

	env1.RetainSessionScratch()
	_ = root1.jobManager.closeStoreOnly()
	if err := root1.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{sandboxProber: bwrapCapableProber(dir), skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root2.Close()
	env2, ok := root2.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("restored root has no local environment")
	}
	if got := env2.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratchDir) {
		t.Fatalf("resumed root sandbox scratch = %q, want the retained %q", got, scratchDir)
	}
	if env2.Wrapper == nil {
		t.Fatal("resumed root lost its kernel wrapper")
	}
	if got := env2.Wrapper.SessionTmp(); filepath.Clean(got) != filepath.Clean(scratchDir) {
		t.Fatalf("resumed wrapper session tmp = %q, want the retained %q", got, scratchDir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("retained sandbox artifact lost at original path %q: bytes=%q err=%v", artifact, got, err)
	}
	// The root's current binding still owns the retained sandbox slot.
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	resumedBinding, ok := findScratchBinding(manifest, scratchConsumerFor(t, manifest, root2.id).CurrentBindingID)
	if !ok {
		t.Fatal("resumed root has no current binding")
	}
	if slot := resumedBinding.Slots[sandbox.ScratchKindSandbox]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(scratchDir) {
		t.Fatalf("resumed binding sandbox slot = %+v, want it owning %q", slot, scratchDir)
	}
}

// TestRetirementResumedWorktreePinsTheActiveClone is M1: worktree re-entry
// replaces s.env with a clone that adopted the caller's scratch, so retention
// must be installed on the active clone. Installing on the caller's now-empty
// environment would publish an empty binding and leave the clone's allocation
// unpinned.
func TestRetirementResumedWorktreePinsTheActiveClone(t *testing.T) {
	sr, root := newResumeScratchLane(t)
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no scratch retention owner")
	}
	r := sr.wt()
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	meta := root.Meta()
	if meta.WorktreePath == "" {
		t.Fatal("crashed meta records no worktree occupancy")
	}
	// Drop the durable retention state so the resumed root starts without an
	// existing binding (the finding's "sessions without an existing binding").
	if err := os.RemoveAll(filepath.Join(sr.stateDir, "scratch-retention")); err != nil {
		t.Fatal(err)
	}
	crashResumeScratchRoot(t, root)

	// A caller environment with a live scratch; re-entry's clone adopts it.
	launchEnv := execenv.NewLocalExecutionEnvironment(sr.mainRoot)
	if _, err := launchEnv.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("mint caller scratch: %v", err)
	}
	claimed := launchEnv.SessionScratchDir()
	if claimed == "" {
		t.Fatal("caller environment minted no scratch")
	}
	restored, err := sr.restoreSessionOn(launchEnv, meta, sr.restoreConfig())
	if err != nil {
		t.Fatalf("resume root: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	active, ok := restored.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok || sameEnvironment(active, launchEnv) {
		t.Fatalf("re-entry did not install a distinct clone (active=%p caller=%p)", active, launchEnv)
	}
	scratch := active.SessionScratchDir()
	if filepath.Clean(scratch) != filepath.Clean(claimed) {
		t.Fatalf("active clone scratch = %q, want the adopted %q", scratch, claimed)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := findScratchBinding(manifest, scratchConsumerFor(t, manifest, restored.id).CurrentBindingID)
	if !ok {
		t.Fatal("resumed root has no current binding")
	}
	slot, ok := binding.Slots[sandbox.ScratchKindUnsandboxed]
	if !ok || !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(scratch) {
		t.Fatalf("resumed binding slots = %+v, want the active clone's scratch %q pinned", binding.Slots, scratch)
	}
}

// TestRetirementSwapPublicationFailureIsSticky is M3: a post-swap
// registerScratchConsumerRoles failure is emitted as a warning but must also be
// recorded sticky, so a later preparation readiness check fails closed with a
// persistence error instead of trusting a manifest that diverged from the live
// environments.
func TestRetirementSwapPublicationFailureIsSticky(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	root := r.s
	defer root.Close()
	launch, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("mint launch scratch: %v", err)
	}
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("enter worktree: %v", err)
	}
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "swap_scratch_consumer_roles" {
			return errors.New("injected scratch retention publication failure")
		}
		return nil
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if err := root.scratchRetentionPersistenceError(); err == nil {
		t.Fatal("post-swap publication failure was not recorded sticky")
	}
	if err := root.validateRetainedScratchPresent(); err == nil {
		t.Fatal("preparation did not fail closed on the sticky publication failure")
	}
}

// TestRetirementStaleSwapRetryKeepsMovedSlots is M4: a stale-revision retry in
// stageScratchSwapBinding when the source binding is absent from the loaded
// manifest must not drop the kinds it is moving. Before the fix the loop deleted
// those kinds from the aliased source map (and so from the moved set), so the
// eventual successful write left the target binding with no owning slots.
func TestRetirementStaleSwapRetryKeepsMovedSlots(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no scratch retention owner")
	}
	base := t.TempDir()
	workDir := t.TempDir()
	sandboxScratch, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatal(err)
	}
	unsandboxedScratch, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sandboxScratch.Retain()
		unsandboxedScratch.Retain()
		_ = os.RemoveAll(sandboxScratch.Dir)
		_ = os.RemoveAll(unsandboxedScratch.Dir)
	})
	if err := sandboxScratch.Pin(owner, sandbox.ScratchReference{Dir: sandboxScratch.Dir, Kind: sandbox.ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := unsandboxedScratch.Pin(owner, sandbox.ScratchReference{Dir: unsandboxedScratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}); err != nil {
		t.Fatal(err)
	}
	// A source environment that names a binding the manifest does NOT hold: the
	// aliasing path the finding describes.
	source := execenv.NewLocalExecutionEnvironment(workDir)
	if err := source.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "missing-source",
		OwnerSessionID: root.id,
		WorkingDir:     workDir,
		Slots: map[string]sandbox.ScratchSlot{
			sandbox.ScratchKindSandbox:     {Dir: sandboxScratch.Dir, OwnsLease: true},
			sandbox.ScratchKindUnsandboxed: {Dir: unsandboxedScratch.Dir, OwnsLease: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	target := execenv.NewLocalExecutionEnvironment(workDir)

	// Force exactly one stale-revision retry: bump the manifest revision between
	// the loop's load and its update on the first attempt.
	flipped := false
	root.cfg.testOnly.scratchSwapBeforeUpdate = func() {
		if flipped {
			return
		}
		flipped = true
		current, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("load manifest in stale hook: %v", err)
			return
		}
		if err := sandbox.UpdateScratchBindings(owner, current.Revision, nil, nil); err != nil {
			t.Errorf("bump manifest revision in stale hook: %v", err)
		}
	}
	if err := root.stageScratchSwapBinding(target, source, root.id); err != nil {
		t.Fatalf("stageScratchSwapBinding: %v", err)
	}
	targetBinding, err := target.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := findScratchBinding(manifest, targetBinding.BindingID)
	if !ok {
		t.Fatalf("target binding %q missing after the retried swap: %+v", targetBinding.BindingID, manifest.Bindings)
	}
	want := map[string]string{
		sandbox.ScratchKindSandbox:     sandboxScratch.Dir,
		sandbox.ScratchKindUnsandboxed: unsandboxedScratch.Dir,
	}
	for kind, wantDir := range want {
		slot, ok := stored.Slots[kind]
		if !ok || !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(wantDir) {
			t.Fatalf("target binding %q slot %q = %+v, want it owning %q; moved kinds were dropped across the stale retry", stored.BindingID, kind, slot, wantDir)
		}
	}
}

// TestRetirementRejectsForeignRetainedScratchPin is L1: validateRetainedScratchPresent
// must verify each reference's ownership/kind identity pin, not just that the
// directory and manifest path exist. A foreign pin means the next restore would
// reject the allocation, so retirement readiness must fail closed.
func TestRetirementRejectsForeignRetainedScratchPin(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()
	base := t.TempDir()
	const rootID = "l1-foreign-pin-root"
	owner := sandbox.ScratchOwner{StateDir: stateDir, RootSessionID: rootID}
	scratch, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		scratch.Retain()
		_ = os.RemoveAll(scratch.Dir)
	})
	if err := scratch.Pin(owner, sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{{
			BindingID:      "E0",
			OwnerSessionID: rootID,
			WorkingDir:     workDir,
			Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
		}}, nil); err != nil {
		t.Fatal(err)
	}

	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	root.stateDir = stateDir
	root.id = rootID
	root.delegateRootSessionID = ""
	// Baseline: the identity-matching pin the fixture wrote must be accepted.
	if err := root.validateRetainedScratchPresent(); err != nil {
		t.Fatalf("validate with the owner's own pin: %v", err)
	}
	// Overwrite the pin with a foreign owner's for the same directory and kind.
	foreignPin := map[string]any{
		"version": 1,
		"owner":   map[string]any{"state_dir": "/foreign-state", "root_session_id": "foreign-root"},
		"dir":     scratch.Dir,
		"kind":    sandbox.ScratchKindUnsandboxed,
	}
	raw, err := json.Marshal(foreignPin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch.Dir, ".evener-retained-session.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.validateRetainedScratchPresent(); err == nil {
		t.Fatal("retirement readiness accepted a foreign ownership pin")
	}
}

// --- roborev round 7 Medium: a resumed root's discarded fresh sandbox scratch ---

// TestRetirementResumedRootScratchAdoptionFailureLeavesUsableScratch covers the
// adoption exit of adoptResumedRootScratch. Disposal of the freshly minted
// sandbox scratch is inherent to the flow — RestoreSessionScratch refuses to
// replace an exposed scratch (execenv/scratch_retention.go), so the mint has to
// go before the retained slot can be restored — which means a FAILED adoption
// must re-provision one rather than leave the environment with no sandbox
// scratch at all.
func TestRetirementResumedRootScratchAdoptionFailureLeavesUsableScratch(t *testing.T) {
	base := t.TempDir()
	workDir := t.TempDir()
	const sessionID = "01RESUMEDROOTADOPTFAIL1"
	const bindingID = "b-adopt-fail"
	retained, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(retained.Dir) })

	env := execenv.NewLocalExecutionEnvironment(workDir)
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	// A write-blocked off policy eagerly provisions a sandbox-kind scratch with no
	// kernel wrapper, which is the shape resume gives a host that cannot wrap.
	if err := env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}
	minted := env.SessionScratchDir()
	if minted == "" {
		t.Fatal("environment minted no fresh sandbox scratch")
	}
	if filepath.Clean(minted) == filepath.Clean(retained.Dir) {
		t.Fatalf("fixture scratch dirs collide at %q", minted)
	}

	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	// The consumer already holds this slot's transfer, so adoption is refused by
	// the pool's own duplicate-transfer guard.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:   owner,
		handles: map[string]*sandbox.SessionScratch{filepath.Clean(retained.Dir): retained},
		bindings: map[string]sandbox.ScratchBinding{bindingID: {
			BindingID: bindingID,
			Slots: map[string]sandbox.ScratchSlot{
				sandbox.ScratchKindSandbox: {Dir: retained.Dir, OwnsLease: true},
			},
		}},
		consumers: map[string]sandbox.ScratchConsumerBinding{
			sessionID: {SessionID: sessionID, CurrentBindingID: bindingID},
		},
		adopted: map[string]string{filepath.Clean(retained.Dir): sessionID},
	})

	if err := s.adoptResumedRootScratch(env, sessionID); err == nil {
		t.Fatal("adoption of an already-transferred slot was expected to fail")
	}
	got := env.SessionScratchDir()
	if got == "" {
		t.Fatal("failed adoption left the environment with no sandbox scratch")
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("failed adoption left the reported sandbox scratch %q unusable: %v", got, err)
	}
}

// TestRetirementResumedRootScratchWrapperFailureRefusesBeforeDisposal covers the
// wrapper-rebuild exit: the replacement kernel wrapper has to be built BEFORE the
// fresh mint is discarded, so a host that cannot wrap the retained directory
// refuses the restore while the environment still owns a live scratch. Rebuilding
// after the disposal would leave the wrapper's TMPDIR pointing at a removed
// directory.
func TestRetirementResumedRootScratchWrapperFailureRefusesBeforeDisposal(t *testing.T) {
	base := t.TempDir()
	workDir := t.TempDir()
	root := t.TempDir()
	const sessionID = "01RESUMEDROOTWRAPFAIL1"
	const bindingID = "b-wrapper-fail"
	retained, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(retained.Dir) })

	env := execenv.NewLocalExecutionEnvironment(root)
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), root, sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision enforced sandbox: %v", err)
	}
	minted := env.SessionScratchDir()
	if minted == "" {
		t.Fatal("environment minted no fresh sandbox scratch")
	}
	if filepath.Clean(minted) == filepath.Clean(retained.Dir) {
		t.Fatalf("fixture scratch dirs collide at %q", minted)
	}
	// A backend with no kernel wrapper: rebuilding the wrapper around the retained
	// directory is refused by sandbox.NewWrapper.
	broken := *policy
	broken.Backend = sandbox.BackendNone
	env.Sandbox = &broken

	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	// A claimable slot: the adoption itself succeeds, so this test isolates the
	// wrapper rebuild that follows it.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:   owner,
		handles: map[string]*sandbox.SessionScratch{filepath.Clean(retained.Dir): retained},
		bindings: map[string]sandbox.ScratchBinding{bindingID: {
			BindingID: bindingID,
			Slots: map[string]sandbox.ScratchSlot{
				sandbox.ScratchKindSandbox: {Dir: retained.Dir, OwnsLease: true},
			},
		}},
		consumers: map[string]sandbox.ScratchConsumerBinding{
			sessionID: {SessionID: sessionID, CurrentBindingID: bindingID},
		},
		adopted: map[string]string{},
	})

	if err := s.adoptResumedRootScratch(env, sessionID); err == nil {
		t.Fatal("rebuilding the wrapper around the retained directory was expected to fail")
	}
	got := env.SessionScratchDir()
	if filepath.Clean(got) != filepath.Clean(minted) {
		t.Fatalf("refused rebuild moved the environment's sandbox scratch: got %q, want the minted %q", got, minted)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("refused rebuild left the reported sandbox scratch %q unusable: %v", got, err)
	}
}

// TestRetirementResumedUnsandboxedRootKeepsRetainedScratch is the regression for
// a resumed unsandboxed root that kept a fresh launch mint instead of its
// retained directory. The launcher environment handed to the restore may already
// own an unsandboxed scratch its own command minted (session_worktree_resume.go).
// adoptResumedRootScratch only ever looked at the retained SANDBOX slot, so for
// an unsandboxed root it fell through to adoptRetainedScratchFor, whose
// same-kind guard skipped the persisted unsandboxed slot because the launcher
// mint already supplied that kind. The resumed root then worked in a brand-new
// empty directory while its retained allocation, artifacts and all, stayed
// unattributed in the pool.
func TestRetirementResumedUnsandboxedRootKeepsRetainedScratch(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	env1, ok := root1.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}
	if _, err := env1.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("mint root scratch: %v", err)
	}
	scratchDir := env1.SessionScratchDir()
	if scratchDir == "" {
		t.Fatal("root minted no scratch")
	}
	artifact := filepath.Join(scratchDir, "root-required.bin")
	want := []byte("root-aged-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root1.installScratchRetention(env1); err != nil {
		t.Fatalf("install root retention: %v", err)
	}
	meta := root1.Meta()

	// Crash: release the scratch lease as process death would, close the store
	// and transcript with no teardown appends.
	env1.RetainSessionScratch()
	_ = root1.jobManager.closeStoreOnly()
	if err := root1.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}

	// A launcher environment whose own command already minted its unsandboxed
	// scratch before the restore, exactly as session_worktree_resume.go describes.
	launch := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("mint launcher scratch: %v", err)
	}
	launchMint := launch.SessionScratchDir()
	if launchMint == "" {
		t.Fatal("launcher minted no scratch")
	}
	if filepath.Clean(launchMint) == filepath.Clean(scratchDir) {
		t.Fatalf("fixture scratch dirs collide at %q", launchMint)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), launch, meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root2.Close()
	env2, ok := root2.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("restored root has no local environment")
	}
	if got := env2.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratchDir) {
		t.Fatalf("resumed root scratch = %q, want retained %q", got, scratchDir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("retained unsandboxed artifact lost at original path %q: bytes=%q err=%v", artifact, got, err)
	}
}

package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

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
	client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
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
	if got, wantPolicy := localEnvPolicyName(rchild.env.(execenv.ExecutionEnvironment)), localEnvPolicyName(child.env.(execenv.ExecutionEnvironment)); got != wantPolicy {
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

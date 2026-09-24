package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func sbxGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func sbxLane(t *testing.T) (lane, home string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	sbxGit(t, main, "init", "-q")
	sbxGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	lane = filepath.Join(base, "lane")
	sbxGit(t, main, "worktree", "add", "-q", lane, "-b", "feat")
	resolved, err := filepath.EvalSymlinks(lane)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, t.TempDir()
}

func sbxBwrapFacts(home string) sandbox.HostFacts {
	return sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}
}

func sbxResolve(t *testing.T, facts sandbox.HostFacts, cwd string, mode sandbox.Mode, add ...string) *sandbox.ResolvedPolicy {
	t.Helper()
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &net, DenylistAdd: add}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return &rp
}

func TestSandboxSnapshotRoundTrip(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	net := true
	inputs := sandbox.SandboxPolicy{
		Mode:               sandbox.ModeRestricted,
		Network:            &net,
		DenylistAdd:        []string{"/opt/tight-secret"},
		DenylistRemove:     []string{"/home/x/.kube"},
		ExtraWritableRoots: []string{"/srv/build"},
		ExtraReadRoots:     []string{"/srv/ro"},
	}
	snapshot := sandboxSnapshotFromInputs(inputs)
	if snapshot == nil {
		t.Fatal("a sandboxed policy must yield a snapshot")
	}
	policy := sandboxPolicyFromStableSnapshot(snapshot)
	if policy == nil || policy.Mode != sandbox.ModeRestricted ||
		!slices.Contains(policy.DenylistAdd, "/opt/tight-secret") ||
		!slices.Contains(policy.DenylistRemove, "/home/x/.kube") ||
		!slices.Contains(policy.ExtraWritableRoots, "/srv/build") ||
		!slices.Contains(policy.ExtraReadRoots, "/srv/ro") {
		t.Fatalf("stable snapshot round trip lost policy inputs: %#v", policy)
	}
	policy.DenylistAdd[0] = "mutated"
	if snapshot.DenylistAdd[0] != "/opt/tight-secret" {
		t.Fatal("stable sandbox policy aliases its durable descriptor")
	}

	env := execenv.NewLocalExecutionEnvironment(lane)
	env.Sandbox = sbxResolve(t, facts, lane, sandbox.ModeRestricted, "/opt/tight-secret")
	if envSnapshot := sandboxSnapshotFromEnv(env); envSnapshot == nil || !slices.Contains(envSnapshot.DenylistAdd, "/opt/tight-secret") {
		t.Fatalf("environment snapshot lost the denylist delta: %#v", envSnapshot)
	}
}

func TestSandboxSnapshotOffIsNil(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if snapshot := sandboxSnapshotFromEnv(env); snapshot != nil {
		t.Fatalf("an off environment must yield a nil snapshot, got %#v", snapshot)
	}
	if policy := sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: "not-a-mode"}); policy != nil {
		t.Fatalf("malformed stable sandbox mode yielded a policy: %#v", policy)
	}
}

func TestDiscardRestoredCandidateDisposesSandboxScratch(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	child := newSession(t, withClient(client), withDir(lane), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	local := child.currentEnv().(*execenv.LocalExecutionEnvironment)
	if err := local.EnableSandbox(sbxResolve(t, facts, lane, sandbox.ModeWorkspaceWrite)); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	tmp := local.Wrapper.SessionTmp()
	child.ownsEnv = true
	child.discardRestoredCandidate()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("discarded restore candidate retained sandbox scratch: %v", err)
	}
}

// The DEFAULT session environment is unsandboxed, and it mints a session scratch
// of its own on its first command rather than at construction. A discarded
// candidate was never adopted, so no Close is ever coming to release that
// directory or the flock lease under it: disposing only the sandbox-owned
// scratch leaves the unsandboxed one, and its lease, for the life of the process.
func TestDiscardRestoredCandidateDisposesUnsandboxedScratch(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	candidate := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	local, ok := candidate.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("restore candidate env = %T, want a local environment", candidate.currentEnv())
	}
	// Running a command is what mints the unsandboxed scratch, exactly as a
	// restore's own first command does.
	if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("an unsandboxed env minted no session scratch, so there is nothing to dispose")
	}

	candidate.ownsEnv = true
	candidate.discardRestoredCandidate()

	// The lease file lives inside the scratch dir, so the directory's removal is
	// the lease's removal too.
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("discarded restore candidate retained unsandboxed scratch %s: stat err = %v", scratch, err)
	}
}

// A candidate does not always OWN what it holds: prepareSubagentEnvironment
// hands back the parent's environment untouched when the delegate needs neither
// a working-dir re-root nor a box of its own. close() already guards its scratch
// handoff on the child's ownsEnv for exactly that reason, and the discard path
// has to make the same distinction — otherwise aborting one candidate deletes the
// scratch dir out from under the live parent still working in it.
func TestDiscardRestoredCandidateLeavesASharedEnvironmentAlone(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	shared, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}
	if _, err := shared.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	sharedScratch := shared.SessionScratchDir()
	if sharedScratch == "" {
		t.Fatal("the parent minted no session scratch, so there is nothing to protect")
	}

	// A candidate on the parent's own environment, exactly as a delegate with no
	// working dir and no per-delegate box gets one.
	candidate, err := NewSession(client, parent.currentProfile(), shared, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the parent's environment: %v", err)
	}

	candidate.discardRestoredCandidate()

	if _, err := os.Stat(sharedScratch); err != nil {
		t.Errorf("discarding a candidate on the parent's shared environment removed its scratch %s: %v", sharedScratch, err)
	}
	if got := shared.SessionScratchDir(); got != sharedScratch {
		t.Errorf("parent scratch = %q after the discard, want the one it is still using %q", got, sharedScratch)
	}
}

func TestDelegateDescriptorJSONRoundTripSnapshot(t *testing.T) {
	net := false
	descriptor := delegatestore.Descriptor{
		ChildSessionID: "child",
		Sandbox: sandboxSnapshotFromInputs(sandbox.SandboxPolicy{
			Mode:        sandbox.ModeWorkspaceWrite,
			Network:     &net,
			DenylistAdd: []string{"/opt/a"},
		}),
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var restored delegatestore.Descriptor
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Sandbox == nil || restored.Sandbox.Mode != "workspace-write" || restored.Sandbox.Network == nil || *restored.Sandbox.Network || !slices.Contains(restored.Sandbox.DenylistAdd, "/opt/a") {
		t.Fatalf("stable descriptor lost sandbox snapshot: %#v", restored.Sandbox)
	}
}

func sbxSandboxedParent(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string) string {
	t.Helper()
	extra := filepath.Join(facts.Home, "parent-extra")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode:               sandbox.ModeWorkspaceWrite,
		Network:            &net,
		ExtraWritableRoots: []string{extra},
	}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve parent: %v", err)
	}
	wrapper, err := sandbox.NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("parent wrapper: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	env.Wrapper = wrapper
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
	return extra
}

func sbxDelegateSession(t *testing.T, facts sandbox.HostFacts) *Session {
	t.Helper()
	return sbxDelegateSessionWithProber(t, sandbox.FakeProber{Facts: facts})
}

func sbxDelegateSessionWithProber(t *testing.T, prober sandbox.Prober) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	return newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-delegate-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       prober,
		},
	}))
}

// scratchDirsIn lists the session scratch directories under base. Tests that
// cannot reach an internally built environment point TMPDIR here instead and
// read the disposal off the filesystem, the way an operator would.
func scratchDirsIn(t *testing.T, base string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(base, "evener-sandbox-*"))
	if err != nil {
		t.Fatalf("glob session scratch dirs: %v", err)
	}
	return found
}

// A committed delegate's restore constructs the session on the child's
// environment — and the construction runs the git snapshot, which is what
// mints an unsandboxed environment's scratch dir. The default child shape
// works in the parent's workspace, so the mint lands on the live parent's
// SHARED environment, and the construction's pin recorded it under the
// parent's inherited binding row: the manifest references it. A restore that
// fails after that point leaves no session to own the scratch, but the
// settlement must still classify by the manifest the way the pooled tail does
// (round 21): the referenced mint is retained with its lease released — the
// handoff a retirement makes — so a later restore of the same child re-probes
// and reacquires the durable directory instead of the state silently
// disappearing. Round 11 disposed it as "a directory nothing will ever
// reacquire"; that premise only ever held for an environment the restore
// itself created, whose binding row died with the failure. The parent's
// world-usable temp container is not the mint and stays (round 20).
func TestRestoreIdleFailureRetainsTheManifestReferencedChildScratch(t *testing.T) {
	// A write-capable ceiling keeps the restore off the read-only floor, so the
	// child gets a plain unsandboxed environment — the default shape, which mints
	// its scratch lazily rather than owning one from EnableSandbox.
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.ToolNameCeiling = []string{"communicate", "write_file"}
	})
	// The snapshot only runs commands in a repo, and running commands is what
	// mints that scratch.
	sbxGit(t, fixture.workspace, "init", "-q")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	scratchBase := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, scratchBase)
	// The child takes production's snapshot path, so its environment mints a
	// scratch before anything can fail; the fault then fails the construction
	// after it, which is the shape this abort has to clean up after.
	boom := errors.New("restored delegate construction failed")
	root.cfg.testOnly.skipGitSnapshot = false
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, _, err := (delegateRuntime{owner: root}).restoreIdle(started); !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}
	_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))

	// The manifest-referenced mint survives, its lease released for the next
	// restore of the same child to reacquire.
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root session has no scratch retention owner")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var minted []sandbox.ScratchReference
	for _, ref := range manifest.References {
		if strings.HasPrefix(filepath.Clean(ref.Dir), filepath.Clean(scratchBase)+string(os.PathSeparator)) {
			minted = append(minted, ref)
		}
	}
	if len(minted) == 0 {
		t.Fatalf("no manifest reference names a scratch under the mint base %q: %+v", scratchBase, manifest.References)
	}
	for _, ref := range minted {
		if _, err := os.Stat(ref.Dir); err != nil {
			t.Fatalf("the manifest-referenced scratch %q did not survive the failed restore: %v", ref.Dir, err)
		}
		handle, err := sandbox.OpenRetainedSessionScratch(owner, ref)
		if err != nil {
			t.Fatalf("the retained scratch %q was not left with its lease released for a later restore: %v", ref.Dir, err)
		}
		_ = handle.Retain()
	}
}

// A created environment's failed restore must route through the manifest
// settlement (mintedScratch starts at ownsFresh), not the adoption retain
// handoff. When the claim flips to contended after the disposal, the heal
// re-provisions a fresh scratch and reports no transfer, and the settlement's
// designed outcome for that fallback is durability: the pending-kind contract
// pins it as a bare manifest reference beside the slot the binding still keeps
// on the retained directory, with its lease released so a later restore of the
// same consumer reacquires it instead of minting a third scratch. The retained
// directory the fixture holds a lease on is never this restore's to touch.
func TestRestoreIdleFailureSettlesTheReprovisionedFreshScratch(t *testing.T) {
	// Isolate the scratch base: every directory this restore mints lands
	// under it, so the settlement's disposals are observable directly, and
	// cleanup removes directory and pin together.
	isolated := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, isolated)
	netDisabled := false
	childWorkspace := t.TempDir()
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		// A workspace of the child's own keeps the restore off the root's
		// shared environment, so it creates a fresh sandboxed one — and a
		// recorded read-only snapshot is the mode this communicate-only
		// structured scope requires. The store's preflight wants the config
		// projection the snapshot agrees with.
		descriptor.WorkingDir = childWorkspace
		descriptor.Sandbox = &delegatestore.SandboxSnapshot{Mode: "read-only", Network: &netDisabled}
		descriptor.Config.Sandbox = "read-only"
		descriptor.Config.SandboxNet = &netDisabled
	})
	sbxGit(t, fixture.workspace, "init", "-q")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root session has no scratch retention owner")
	}
	const bindingID = "b-settle-fresh"
	slots, bindingRow := mintRefreshScratchBinding(t, root, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, root, fixture.childID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	key := canonicalScratchDir(retainedDir)
	root.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{fixture.childID: {SessionID: fixture.childID, CurrentBindingID: bindingID}},
		adopted:   map[string]string{},
		contended: map[string]struct{}{},
	})
	preMinted, err := filepath.Glob(filepath.Join(isolated, "evener-sandbox-*"))
	if err != nil {
		t.Fatal(err)
	}

	// The claim flips to contended between the replacement's guard and its
	// own hold, so the already-disposed mint forces the heal to re-provision.
	root.cfg.testOnly.scratchAdoptionBeforeClaim = func() {
		root.cfg.testOnly.scratchAdoptionBeforeClaim = nil
		pool := root.retainedScratch.Load()
		pool.mu.Lock()
		delete(pool.handles, key)
		pool.contended[key] = struct{}{}
		pool.mu.Unlock()
	}
	boom := errors.New("restored delegate construction failed")
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, _, err := (delegateRuntime{owner: root}).restoreIdle(started); !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}
	_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))

	// The settlement's record: exactly the re-provisioned fallback remains
	// under the base this restore minted in — the original mint the
	// replacement disposed, and nothing else the restore created. The
	// settlement retains the bare-pinned reprovision rather than dropping it
	// (the pending-kind contract below), so the retry the caller is about to
	// run adopts what this failed attempt's reprovision pinned.
	postMinted, err := filepath.Glob(filepath.Join(isolated, "evener-sandbox-*"))
	if err != nil {
		t.Fatal(err)
	}
	var fallback string
	for _, dir := range postMinted {
		if !slices.Contains(preMinted, dir) {
			if fallback != "" {
				t.Fatalf("the failed restore left more than one fresh fallback behind: %q and %q", fallback, dir)
			}
			fallback = dir
		}
	}
	if fallback == "" {
		t.Fatal("the failed restore left no re-provisioned fallback record")
	}

	// The fallback is pinned exactly as the pending-kind contract promises: a
	// bare manifest reference, while the binding's slot keeps naming the
	// retained directory for a later restore to re-probe.
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("the carried binding %q is absent after the failed restore", bindingID)
	}
	slot, hasSlot := row.Slots[sandbox.ScratchKindSandbox]
	if !hasSlot || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the binding's slot must keep naming the retained %q: %+v", retainedDir, row.Slots)
	}
	current := ""
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == fixture.childID {
			current = consumer.CurrentBindingID
		}
	}
	if current != bindingID {
		t.Fatalf("the child's consumer row names %q, want %q", current, bindingID)
	}
	fallbackReferenced := false
	retainedReferenced := false
	for _, ref := range manifest.References {
		switch filepath.Clean(ref.Dir) {
		case filepath.Clean(fallback):
			fallbackReferenced = true
		case filepath.Clean(retainedDir):
			retainedReferenced = true
		}
	}
	if !fallbackReferenced {
		t.Fatalf("the re-provisioned fallback %q lost its bare manifest reference: %+v", fallback, manifest.References)
	}
	if !retainedReferenced {
		t.Fatalf("the retained %q lost its manifest reference: %+v", retainedDir, manifest.References)
	}

	// The settlement released the fallback's lease for the next restore of
	// this consumer to reacquire, and the retained directory survives
	// untouched beside it.
	handle, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: fallback, Kind: sandbox.ScratchKindSandbox})
	if err != nil {
		t.Fatalf("the re-provisioned fallback %q was not left reacquirable: %v", fallback, err)
	}
	_ = handle.Retain()
	if _, err := os.Stat(retainedDir); err != nil {
		t.Fatalf("the retained %q did not survive the failed restore: %v", retainedDir, err)
	}
}

// A created environment whose restore genuinely transferred a retained
// allocation must still settle a failure by the manifest, not retain every
// scratch the environment carries: a later construction step can leave
// further scratch on that environment — here, an unrelated directory the
// manifest does not reference — and a blanket retain hands it a durable
// lease-less leak instead of the settlement's classification. The manifest
// names the adopted allocation, so the settlement keeps exactly that one and
// disposes the unreferenced newcomer (round 30).
func TestRestoreIdleFailureSettlesUnrelatedScratchBesideAnAdoptedTransfer(t *testing.T) {
	// Isolate the scratch base: every directory this restore mints or the
	// test installs lands under it, so the settlement's keep-vs-dispose is
	// observable directly.
	isolated := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, isolated)
	// A workspace of the child's own keeps the restore off the root's shared
	// environment (a fresh plain environment this restore created), and a
	// write-capable ceiling keeps the restore off the read-only floor so the
	// environment mints its scratch lazily — nothing exists for the adoption
	// to skip, so the retained unsandboxed slot genuinely transfers.
	childWorkspace := t.TempDir()
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.WorkingDir = childWorkspace
		descriptor.ToolNameCeiling = []string{"communicate", "write_file"}
	})
	sbxGit(t, fixture.workspace, "init", "-q")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root session has no scratch retention owner")
	}
	const bindingID = "b-adopted-settle"
	slots, bindingRow := mintRefreshScratchBinding(t, root, bindingID, sandbox.ScratchKindUnsandboxed)
	mapRefreshScratchConsumer(t, root, fixture.childID, bindingID)
	retainedDir := slots[sandbox.ScratchKindUnsandboxed].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindUnsandboxed].Retain() })
	key := canonicalScratchDir(retainedDir)
	root.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: slots[sandbox.ScratchKindUnsandboxed]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{fixture.childID: {SessionID: fixture.childID, CurrentBindingID: bindingID}},
		adopted:   map[string]string{},
		contended: map[string]struct{}{},
	})

	// The later construction step: an unrelated scratch directory the
	// manifest does not name, installed on the environment after the
	// adoption — the exact newcomer the settlement must classify away.
	extraDir := t.TempDir()
	if !strings.HasPrefix(filepath.Clean(extraDir), filepath.Clean(isolated)+string(os.PathSeparator)) {
		// t.TempDir roots elsewhere; keep the assertion set honest by placing
		// the newcomer inside the isolated base the settlement reads.
		extraDir = filepath.Join(isolated, "evener-sandbox-unrelated")
		if err := os.MkdirAll(extraDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root.cfg.testOnly.scratchRestoreAfterAdoption = func(env *execenv.LocalExecutionEnvironment) {
		borrowed, err := sandbox.BorrowRetainedSessionScratch(extraDir)
		if err != nil {
			t.Fatalf("borrow the unrelated scratch: %v", err)
		}
		if err := env.RestoreSessionScratch(bindingID, sandbox.ScratchReference{Dir: extraDir, Kind: sandbox.ScratchKindSandbox}, borrowed); err != nil {
			t.Fatalf("install the unrelated scratch: %v", err)
		}
	}
	boom := errors.New("restored delegate construction failed")
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, _, err := (delegateRuntime{owner: root}).restoreIdle(started); !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}
	_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))

	// The unrelated, unreferenced scratch is this restore's to dispose: the
	// settlement must not have retained it beside the adopted allocation.
	if _, err := os.Stat(extraDir); !os.IsNotExist(err) {
		t.Fatalf("the unrelated scratch %q survived the failed restore's settlement beside an adopted transfer: %v", extraDir, err)
	}
	// The adopted allocation is manifest-referenced durable state: it
	// survives, handed back to the pool — the settle releases the
	// environment's hold by requeueing the transferred handle, so the next
	// in-process restore of this child re-claims it without flock churn.
	if _, err := os.Stat(retainedDir); err != nil {
		t.Fatalf("the adopted retained scratch %q did not survive the failed restore: %v", retainedDir, err)
	}
	pool := root.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the root pool is gone after the failed restore")
	}
	pool.mu.Lock()
	requeued := pool.handles[key]
	pool.mu.Unlock()
	if requeued == nil {
		t.Fatalf("the adopted retained scratch %q was not requeued into the pool for the next restore", retainedDir)
	}
}

// saveColdRestorableChild writes a committed child's session meta and an
// empty transcript — the durable state a real committed spawn leaves — so a
// cold restore can reconstruct the child from disk.
func saveColdRestorableChild(t *testing.T, stateDir string, base schema.SessionMeta, childID, parentID, task, workdir string, depth int) {
	t.Helper()
	childMeta := base
	childMeta.ID = childID
	childMeta.ParentSessionID = parentID
	childMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(transcriptPath(stateDir, childID), transcript.Header{
		SessionID:       childID,
		ParentSessionID: parentID,
		Task:            task,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		WorkingDir:      workdir,
		Depth:           depth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// A grandchild restore runs as the CHILD owner, and a child never runs
// prepareRetainedScratch — so the failed restore settles with no pool, the
// exact state the round-11 nil-pool dispose branch was written for. But the
// environment it fails on can be the live parent's shared one, not a fresh
// one this restore created: the caller's mintedScratch gate only proves the
// environment held no SCRATCH at adoption time, while the parent's
// world-usable TMPDIR container — provisioned long before this restore and
// still serving the parent's spawned children — is something
// DisposeUnadoptedScratch also removes, breaking removeUnsandboxedTmpLocked's
// own rule that a live env keeps its container. The settle must leave the
// shared environment alone entirely — its container by that rule, and since
// round 51 its scratch too: the empty-snapshot record cannot attribute what
// stands there, so the construction's own mint now stays with the live parent
// that owns it.
func TestRestoreIdleFailureOnASharedEnvKeepsTheParentTempContainer(t *testing.T) {
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	root, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	// The child owner: its own working directory gives it a private plain
	// unsandboxed environment (the write-capable ceiling keeps the restore off
	// the read-only floor), and being a child it owns no retained-scratch pool.
	childID := identifier.MustNewSessionID()
	childWorkspace := t.TempDir()
	childConfig := meta.Config.Clone()
	childConfig.AgentName = "subagent"
	childDescriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		TranscriptRef:     encodeRef("", childID),
		OwnerSessionID:    meta.ID,
		VisibleSessionID:  meta.ID,
		Task:              "own the grandchild's restore",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		FrozenRolePrompt:  defaultSubagentInstructions,
		ToolNameCeiling:   []string{"communicate", "write_file"},
		WorkingDir:        childWorkspace,
		LocalEnvPolicy:    "default",
		Config:            childConfig,
		Resumable:         true,
	}
	saveColdRestorableChild(t, stateDir, meta, childID, meta.ID, childDescriptor.Task, childWorkspace, 1)
	childSub, _, err := (delegateRuntime{owner: root}).restoreIdle(delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: childDescriptor,
	})
	if err != nil {
		t.Fatalf("restore the child owner: %v", err)
	}
	defer childSub.sess.discardRestoredCandidate()
	parent := childSub.sess
	if parent.retainedScratch.Load() != nil {
		t.Fatal("fixture expected the child owner to hold no retained-scratch pool")
	}
	parentEnv, ok := parent.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("child owner env = %T, want a local environment", parent.env)
	}
	if dir := parentEnv.SessionScratchDir(); dir != "" {
		t.Fatalf("fixture expected the child owner's environment scratchless before the grandchild restore, got %q", dir)
	}

	// The parent's environment has already served commands, so it holds the
	// world-usable TMPDIR container every spawned child receives. The scratch
	// that first command minted is then dropped — DisposeUnsandboxedScratch's
	// own contract keeps the container and its lease — so the grandchild's
	// restore enters against a scratchless environment that nonetheless holds
	// a live container: exactly the live-parent shape.
	probe, err := parentEnv.ExecCommand(context.Background(), `printf %s "$TMPDIR"`, 5000, "", nil)
	if err != nil {
		t.Fatalf("probe the parent's TMPDIR: %v", err)
	}
	containerDir := strings.TrimSpace(probe.Stdout)
	if containerDir == "" {
		t.Fatal("fixture expected the parent environment to hold a TMPDIR container")
	}
	parentEnv.DisposeUnsandboxedScratch()
	if dir := parentEnv.SessionScratchDir(); dir != "" {
		t.Fatalf("fixture expected the parent environment scratchless before the grandchild restore, got %q", dir)
	}

	// The grandchild is committed against the child owner with the SAME
	// working directory, which is what routes its restore onto the parent's
	// live shared environment instead of building a fresh one.
	grandchildID := identifier.MustNewSessionID()
	grandchildConfig := meta.Config.Clone()
	grandchildConfig.AgentName = "subagent"
	grandchildDescriptor := delegatestore.Descriptor{
		ChildSessionID:    grandchildID,
		TranscriptRef:     encodeRef("", grandchildID),
		OwnerSessionID:    childID,
		VisibleSessionID:  childID,
		Task:              "fail construction on the shared environment",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		FrozenRolePrompt:  defaultSubagentInstructions,
		ToolNameCeiling:   []string{"communicate", "write_file"},
		WorkingDir:        childWorkspace,
		LocalEnvPolicy:    "default",
		Config:            grandchildConfig,
		Resumable:         true,
	}
	saveColdRestorableChild(t, stateDir, meta, grandchildID, childID, grandchildDescriptor.Task, childWorkspace, 2)
	// The snapshot only runs commands in a repo, and running commands is what
	// mints the shared environment's scratch.
	sbxGit(t, childWorkspace, "init", "-q")
	scratchBase := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, scratchBase)
	boom := errors.New("grandchild construction failed")
	parent.cfg.testOnly.skipGitSnapshot = false
	parent.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	if _, _, err := (delegateRuntime{owner: parent}).restoreIdle(delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: grandchildDescriptor,
	}); !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}

	// The grandchild's construction minted a fresh scratch on the shared
	// environment — and on a shared environment that mint cannot be told
	// apart from a concurrent actor's, so it stays with the live parent that
	// owns it: the environment reuses it on its next command, and its close
	// releases the lease for the sweeper to collect.
	if dirs := scratchDirsIn(t, scratchBase); len(dirs) != 1 {
		t.Errorf("failed grandchild restore left %v scratch dirs; the shared env must keep exactly the one mint it holds", dirs)
	}
	if dir := parentEnv.SessionScratchDir(); dir == "" {
		t.Fatal("the shared environment lost the minted scratch it owns")
	}
	// The parent's container is NOT this restore's to remove: the live parent
	// and its already-spawned children still point at it as TMPDIR.
	if _, err := os.Stat(containerDir); err != nil {
		t.Errorf("the failed grandchild restore destroyed the live parent's TMPDIR container %q: %v", containerDir, err)
	}
}

// A failed shared-environment restore must never dispose scratch a concurrent
// actor minted on the parent's environment. The mintedScratch gate records
// only that the environment held no scratch when the adoption finished — an
// empty snapshot, not an attribution: the environment holds one scratch per
// kind and the lazy mint reuses whatever is present, so the allocation
// standing there at settlement is the FIRST minter's — this restore's
// construction, or another parent/child's command that ran in the window —
// with nothing on the environment to tell them apart. A settle that disposes
// by that inference deletes the concurrent actor's live allocation.
func TestRestoreIdleFailureKeepsAConcurrentActorsScratchOnASharedEnv(t *testing.T) {
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	root, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	// The child owner: its own working directory gives it a private plain
	// unsandboxed environment, and being a child it owns no retained-scratch
	// pool.
	childID := identifier.MustNewSessionID()
	childWorkspace := t.TempDir()
	childConfig := meta.Config.Clone()
	childConfig.AgentName = "subagent"
	childDescriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		TranscriptRef:     encodeRef("", childID),
		OwnerSessionID:    meta.ID,
		VisibleSessionID:  meta.ID,
		Task:              "own the grandchild's restore",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		FrozenRolePrompt:  defaultSubagentInstructions,
		ToolNameCeiling:   []string{"communicate", "write_file"},
		WorkingDir:        childWorkspace,
		LocalEnvPolicy:    "default",
		Config:            childConfig,
		Resumable:         true,
	}
	saveColdRestorableChild(t, stateDir, meta, childID, meta.ID, childDescriptor.Task, childWorkspace, 1)
	childSub, _, err := (delegateRuntime{owner: root}).restoreIdle(delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: childDescriptor,
	})
	if err != nil {
		t.Fatalf("restore the child owner: %v", err)
	}
	defer childSub.sess.discardRestoredCandidate()
	parent := childSub.sess
	if parent.retainedScratch.Load() != nil {
		t.Fatal("fixture expected the child owner to hold no retained-scratch pool")
	}
	parentEnv, ok := parent.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("child owner env = %T, want a local environment", parent.env)
	}
	if dir := parentEnv.SessionScratchDir(); dir != "" {
		t.Fatalf("fixture expected the child owner's environment scratchless before the grandchild restore, got %q", dir)
	}

	// The grandchild is committed against the child owner with the SAME
	// working directory, which routes its restore onto the parent's live
	// shared environment instead of building a fresh one.
	grandchildID := identifier.MustNewSessionID()
	grandchildConfig := meta.Config.Clone()
	grandchildConfig.AgentName = "subagent"
	grandchildDescriptor := delegatestore.Descriptor{
		ChildSessionID:    grandchildID,
		TranscriptRef:     encodeRef("", grandchildID),
		OwnerSessionID:    childID,
		VisibleSessionID:  childID,
		Task:              "fail construction beside a concurrent actor's scratch",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		FrozenRolePrompt:  defaultSubagentInstructions,
		ToolNameCeiling:   []string{"communicate", "write_file"},
		WorkingDir:        childWorkspace,
		LocalEnvPolicy:    "default",
		Config:            grandchildConfig,
		Resumable:         true,
	}
	saveColdRestorableChild(t, stateDir, meta, grandchildID, childID, grandchildDescriptor.Task, childWorkspace, 2)
	sbxGit(t, childWorkspace, "init", "-q")
	scratchBase := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, scratchBase)
	// The concurrent actor: between the adoption's empty-snapshot check and
	// the construction's own mint, another parent/child sharing this
	// environment runs a command, and its lazy mint lands first. The
	// construction's git snapshot then reuses what is present instead of
	// minting a second allocation.
	parent.cfg.testOnly.scratchRestoreAfterAdoption = func(env *execenv.LocalExecutionEnvironment) {
		parent.cfg.testOnly.scratchRestoreAfterAdoption = nil
		if _, err := env.ExecCommand(context.Background(), `true`, 5000, "", nil); err != nil {
			t.Fatalf("the concurrent actor's command on the shared environment: %v", err)
		}
	}
	boom := errors.New("grandchild construction failed")
	parent.cfg.testOnly.skipGitSnapshot = false
	parent.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	if _, _, err := (delegateRuntime{owner: parent}).restoreIdle(delegateStartCommit{
		lease:      delegateLease{delegateID: identifier.MustNewDelegateID()},
		ctx:        context.Background(),
		descriptor: grandchildDescriptor,
	}); !errors.Is(err, boom) {
		t.Fatalf("restoreIdle error = %v, want %v", err, boom)
	}

	// The concurrent actor's allocation is live and owned by the environment
	// the failed restore merely shared: it must still be on disk and still
	// held, not classified as this restore's own mint and disposed.
	if dirs := scratchDirsIn(t, scratchBase); len(dirs) != 1 {
		t.Errorf("the failed shared-env restore left %v scratch dirs; the concurrent actor's one allocation must survive", dirs)
	}
	if dir := parentEnv.SessionScratchDir(); dir == "" {
		t.Fatal("the shared environment lost the concurrent actor's scratch")
	}
}

// The create path builds a spawning delegate its own environment and then
// constructs the session on it, running the same git snapshot the restore does.
// A NewSession that fails after the snapshot leaves nothing to own the scratch
// that snapshot minted, so the spawn's own rollback has to drop it.
func TestSpawnedSubagentSessionFailureDisposesTheChildScratch(t *testing.T) {
	workspace := t.TempDir()
	// The snapshot only runs commands in a repo, and running commands is what
	// mints the scratch.
	sbxGit(t, workspace, "init", "-q")
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root := newSession(t, withClient(client), withDir(workspace), withoutGitSnapshot())

	scratchBase := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, scratchBase)
	// The child takes production's snapshot path, and the fault then fails its
	// construction after it.
	boom := errors.New("spawned delegate construction failed")
	root.cfg.testOnly.skipGitSnapshot = false
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	// A working dir is what makes the child's environment its own rather than
	// the parent's, which is the case whose scratch nobody else releases.
	if _, err := root.spawnAgent(context.Background(), "child task", "", workspace, 1, "", "", nil, nil); !errors.Is(err, boom) {
		t.Fatalf("spawnAgent error = %v, want %v", err, boom)
	}

	if leaked := scratchDirsIn(t, scratchBase); len(leaked) != 0 {
		t.Errorf("failed subagent spawn left scratch %v, which nothing will ever release", leaked)
	}
}

// disposeUnadoptedSubagentSession is the create-path twin of
// discardRestoredCandidate, and the two have to make the same two decisions:
// drop BOTH scratch dirs of an environment built for the child (Close only
// releases the unsandboxed one's lease and keeps the directory), and leave a
// shared environment alone, since it belongs to the live parent.
func TestDisposeUnadoptedSubagentSessionDisposesEveryScratchItOwns(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	owned := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	ownedEnv, ok := owned.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("child env = %T, want a local environment", owned.currentEnv())
	}
	if _, err := ownedEnv.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	ownedScratch := ownedEnv.SessionScratchDir()
	if ownedScratch == "" {
		t.Fatal("the child env minted no session scratch, so there is nothing to dispose")
	}

	owned.ownsEnv = true
	disposeUnadoptedSubagentSession(owned)

	if got := owned.State(); got != SessionClosed {
		t.Errorf("unadopted child state = %q, want %q", got, SessionClosed)
	}
	if _, err := os.Stat(ownedScratch); !os.IsNotExist(err) {
		t.Errorf("unadopted child retained its scratch %s: stat err = %v", ownedScratch, err)
	}

	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	shared, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}
	if _, err := shared.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	sharedScratch := shared.SessionScratchDir()
	// Counting Cleanup is how the second decision is observable: Cleanup is what
	// releases the environment's live scratch leases and signals the processes it
	// tracks, and on a shared environment both belong to the live parent.
	counted := &cleanupCountingEnv{ExecutionEnvironment: shared}
	sharing, err := NewSession(client, parent.currentProfile(), counted, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the parent's environment: %v", err)
	}

	disposeUnadoptedSubagentSession(sharing)

	if got := sharing.State(); got != SessionClosed {
		t.Errorf("child sharing the parent's environment was left %q, want %q", got, SessionClosed)
	}
	if got := counted.count(); got != 0 {
		t.Errorf("unadopted child ran Cleanup %d time(s) on the parent's environment, releasing the parent's live scratch lease and signalling the processes it tracks", got)
	}
	if _, err := os.Stat(sharedScratch); err != nil {
		t.Errorf("disposing a child on the parent's shared environment removed its scratch %s: %v", sharedScratch, err)
	}
}

// A stable delegate's construction hands prepareSubagentRunFromSelection an
// environment the isolation step already prepared, so the spawn's own rollback
// deliberately leaves that environment alone and delegateIsolation.cleanup is
// what rolls it back. The construction it wraps runs the child's git snapshot, which mints an
// unsandboxed environment's scratch, so this rollback has to drop both dirs too.
func TestDelegateIsolationCleanupDisposesEveryScratchItOwns(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	owner := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	fresh := owner.currentEnv().(*execenv.LocalExecutionEnvironment).WithWorkingDirectory(t.TempDir())
	if _, err := fresh.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	scratch := fresh.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the isolated env minted no session scratch, so there is nothing to dispose")
	}

	// No worktree path: this is the plain re-rooted/boxed lane, whose rollback is
	// only the environment.
	delegateIsolation{env: fresh, ownsFreshEnv: true}.cleanup(owner, "")

	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("delegate isolation rollback retained scratch %s: stat err = %v", scratch, err)
	}
}

// Worktree re-entry (a resume of a session that was working in a worktree)
// REPLACES the session's environment with a clone rooted in that worktree, and
// initSessionState's snapshot then mints THAT clone's scratch. The caller's own
// failure path can only dispose the environment it handed in, so a restore that
// fails after re-entry has to drop what it re-rooted onto itself.
func TestWorktreeReentryRestoreFailureDisposesTheReenteredScratch(t *testing.T) {
	lane, _ := sbxLane(t)
	launchDir := t.TempDir()
	stateDir := t.TempDir()
	meta := artifactRestoreMeta(t)
	meta.WorktreePath = lane
	meta.WorktreeRestoreRoot = launchDir

	scratchBase := t.TempDir()
	t.Setenv(envvars.TmpDir.Name, scratchBase)
	boom := errors.New("restore failed after the snapshot")
	cfg := artifactRestoreConfig(t, stateDir)
	// Production's snapshot path, so the re-entered environment mints its scratch
	// before the fault fails the restore after it.
	cfg.testOnly.skipGitSnapshot = false
	cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	launchEnv := execenv.NewLocalExecutionEnvironment(launchDir)
	if _, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), launchEnv, meta, cfg); !errors.Is(err, boom) {
		t.Fatalf("restore error = %v, want %v", err, boom)
	}
	// The launch environment belongs to the caller, whose own failure path
	// disposes it (run.go, serve.go, restoreIdle); everything left after that is
	// the restore's own.
	launchEnv.DisposeUnadoptedScratch()

	if leaked := scratchDirsIn(t, scratchBase); len(leaked) != 0 {
		t.Errorf("failed worktree re-entry restore left scratch %v, which nothing will ever release", leaked)
	}
}

// A fresh WithWorkingDirectory clone SHARES the parent's process table -- the
// runningPIDs map is copied by pointer -- so Cleanup on a child's OWN clone
// signals the parent's in-flight tools, not just the child's. An unadopted child
// therefore never runs its environment's Cleanup at all: it closes the session
// and disposes the scratch it owns, and every tracked process stays with the
// environment that tracks it.
func TestDisposeUnadoptedSubagentSessionLeavesTheParentsProcessesAlone(t *testing.T) {
	dir := t.TempDir()
	parentEnv := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(parentEnv.Cleanup)
	handle, err := parentEnv.StreamCommand(context.Background(), "sleep 30", dir, nil, io.Discard)
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_, _ = handle.Wait()
		close(exited)
	}()

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	child, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), parentEnv.WithWorkingDirectory(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on a fresh clone: %v", err)
	}

	child.ownsEnv = true
	disposeUnadoptedSubagentSession(child)

	// No bound needed: a Cleanup that reached this process would have signalled
	// it, waited out the termination grace and killed it, all before the dispose
	// call returned — so its exit is already visible here if it happened at all.
	select {
	case <-exited:
		t.Fatalf("disposing an unadopted child ended the parent's in-flight process %d", handle.Pid)
	default:
	}
	// The other half: the process is still the parent's to end, so it never left
	// the environment that tracks it. Awaiting the real exit rather than a bound;
	// a cleanup that never ends it hangs until the package deadline, which is the
	// same failure with a clearer report.
	parentEnv.Cleanup()
	<-exited
}

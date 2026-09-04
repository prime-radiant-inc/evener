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
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
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
	child.discardRestoredCandidate(true)
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

	candidate.discardRestoredCandidate(true)

	// The lease file lives inside the scratch dir, so the directory's removal is
	// the lease's removal too.
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("discarded restore candidate retained unsandboxed scratch %s: stat err = %v", scratch, err)
	}
}

// A candidate does not always OWN what it holds: prepareSubagentEnvironment
// hands back the parent's environment untouched when the delegate needs neither
// a working-dir re-root nor a box of its own. close() already guards its scratch
// handoff on the subagent's ownsEnv for exactly that reason, and the discard path
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

	candidate.discardRestoredCandidate(false)

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

// A committed delegate's restore builds the child its own environment and then
// constructs the session on it — and the construction runs the git snapshot,
// which is what mints an unsandboxed environment's scratch dir. A restore that
// fails after that point (a fault anywhere inside initSessionState) leaves no
// session to own the scratch, so the abort has to drop it and its lease.
func TestRestoreIdleFailureDisposesTheChildScratch(t *testing.T) {
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

	if leaked := scratchDirsIn(t, scratchBase); len(leaked) != 0 {
		t.Errorf("failed delegate restore left scratch %v, which nothing will ever release", leaked)
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

	disposeUnadoptedSubagentSession(owned, true)

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

	disposeUnadoptedSubagentSession(sharing, false)

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

// A stable delegate's construction hands createSubagent an environment the
// isolation step already prepared, so the spawn's own rollback deliberately
// leaves that environment alone and delegateIsolation.cleanup is what rolls it
// back. The construction it wraps runs the child's git snapshot, which mints an
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

	disposeUnadoptedSubagentSession(child, true)

	select {
	case <-exited:
		t.Fatalf("disposing an unadopted child ended the parent's in-flight process %d", handle.Pid)
	case <-time.After(200 * time.Millisecond):
	}
	// The other half: the process is still the parent's to end, so it never left
	// the environment that tracks it.
	parentEnv.Cleanup()
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the parent's own cleanup did not end the process it tracks")
	}
}

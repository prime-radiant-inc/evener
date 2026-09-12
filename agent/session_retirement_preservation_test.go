package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// retirementPrimaryFiles captures the durable primary records of a state dir so
// a before/after comparison can demand exact preservation where no write is
// expected. Lock/socket files are excluded: they are process-local.
type retirementPrimaryFiles map[string][]byte

func readRetirementPrimaryFiles(t *testing.T, stateDir string) retirementPrimaryFiles {
	t.Helper()
	files := retirementPrimaryFiles{}
	err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Capture primary stores and required artifacts, not lock/socket files.
		rel, err := filepath.Rel(stateDir, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".lock") || d.Type()&os.ModeSocket != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestRetirementReleasePreservesRootTranscript is the plan's simple isolation
// case: the non-terminal release appends no terminal transcript evidence and
// the same root id restores from the same state dir.
func TestRetirementReleasePreservesRootTranscript(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	id := root.ID()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	prepared, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := root.ReleaseForRetirement(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("retirement appended terminal transcript evidence")
	}
	// The session is not terminated: its durable state restores the same root,
	// and the controller stays in the retiring phase (no fallback terminal Close).
	if snap := c.Snapshot(); snap.Phase != "retiring" {
		t.Fatalf("phase after release = %q, want retiring", snap.Phase)
	}
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	if restored.ID() != id {
		t.Fatalf("restored another root: %s", restored.ID())
	}
}

// TestRetirementReleaseRefusesUncommittedPreparation proves the exact
// committed preparation is required: an uncommitted claim is refused before any
// side effect, and the claim can still be aborted (admission reopens).
func TestRetirementReleaseRefusesUncommittedPreparation(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	id := root.ID()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	prepared, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	before, err := readFileOrEmpty(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	// Not committed yet: release must refuse.
	if err := root.ReleaseForRetirement(context.Background(), prepared); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("release of an uncommitted preparation = %v, want ErrRetirementUnavailable", err)
	}
	after, err := readFileOrEmpty(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused release still changed the transcript")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatalf("Abort after refused release: %v", err)
	}
	if snap := c.Snapshot(); snap.Phase != "resident" {
		t.Fatalf("phase after Abort = %q, want resident", snap.Phase)
	}
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	if restored.ID() != id {
		t.Fatalf("restored another root: %s", restored.ID())
	}
}

// TestRetirementReleaseFailureAfterCommitStaysRetiring injects a transcript
// close failure after Commit and asserts a failed retiring diagnostic with no
// Abort path and no reopened admission.
func TestRetirementReleaseFailureAfterCommitStaysRetiring(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	prepared, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected transcript close failure")
	retirementReleaseFault = func(point string) error {
		if point == "transcript_close" {
			return injected
		}
		return nil
	}
	defer func() { retirementReleaseFault = nil }()

	if err := root.ReleaseForRetirement(context.Background(), prepared); !errors.Is(err, injected) {
		t.Fatalf("release error = %v, want injected failure", err)
	}
	snap := c.Snapshot()
	if snap.Phase != "retiring" {
		t.Fatalf("phase after failed release = %q, want retiring", snap.Phase)
	}
	if snap.Failure != "release_failed" {
		t.Fatalf("failure diagnostic = %q, want release_failed", snap.Failure)
	}
	// There is no Abort path after Commit.
	if err := c.Abort(claim, "prepare_failed"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("Abort after failed release = %v, want ErrRetirementUnavailable", err)
	}
	// A failed release never reopens admission: reads still refuse.
	if _, err := c.Borrow(); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("Borrow after failed release = %v, want ErrRetirementUnavailable", err)
	}
}

// TestRetirementReleaseRefusesAfterTerminalClose pins the API contract that
// succeeded only when THIS call performed the non-terminal release. If a
// terminal Close already consumed the session's single teardown pass, the
// release must error before any side effect rather than report a vacuous
// success over a terminally stopped tree.
func TestRetirementReleaseRefusesAfterTerminalClose(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	prepared, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A terminal Close consumes the one teardown pass first.
	root.Close()
	before, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.ReleaseForRetirement(context.Background(), prepared); err == nil {
		t.Fatal("ReleaseForRetirement reported success after a terminal Close spent the teardown pass")
	}
	after, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused post-Close release still changed the transcript")
	}
	// The terminal Close did not touch the controller; the release must not
	// reopen admission or run a second teardown.
	if snap := c.Snapshot(); snap.Phase != "retiring" {
		t.Fatalf("phase after refused post-Close release = %q, want retiring", snap.Phase)
	}
}

// TestRetirementDeferredCloseAfterReleaseIsNoOp pins plan 803: closeOnce/terminal
// ownership must not let a later deferred Close execute a destructive second
// pass after a successful non-terminal release.
func TestRetirementDeferredCloseAfterReleaseIsNoOp(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	prepared, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := root.ReleaseForRetirement(context.Background(), prepared); err != nil {
		t.Fatalf("release: %v", err)
	}
	before, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no retention owner")
	}
	manifestBefore, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifestBefore.Released {
		t.Fatal("retirement released the scratch manifest")
	}
	// A deferred Close must be a no-op: no terminal transcript evidence, no
	// phase change, no stop/disposal/unlock (the manifest stays unreleased).
	root.Close()
	after, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("deferred Close after retirement ran a destructive second pass on the transcript")
	}
	if snap := c.Snapshot(); snap.Phase != "retiring" {
		t.Fatalf("phase after deferred Close = %q, want retiring", snap.Phase)
	}
	manifestAfter, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifestAfter.Released {
		t.Fatal("deferred Close after retirement released the retained scratch manifest")
	}
}

// TestRetirementSharedConsumerBorrowUnit is the unit-level half of the Task 5
// re-review N1 carry-forward: two consumers share one binding, prepare
// reacquires the single allocation, the root adopts it, and a DISTINCT child
// consumer resolves the original path through the lease-less borrow branch
// (session_scratch_retention.go adoptRetainedScratchFor). The lease is free
// after the root releases, proving release precedes restore. The real
// root/delegate/worktree end-to-end checkpoint lives in
// TestRetirementSharedChildScratchBindingsRestore.
func TestRetirementSharedConsumerBorrowUnit(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	base := t.TempDir()
	scratch, err := sandbox.NewSessionScratch(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
	artifact := filepath.Join(scratch.Dir, "shared-child.bin")
	want := []byte("shared-child-artifact")
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
		OwnerSessionID: root.id,
		WorkingDir:     dir,
		Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{binding},
		[]sandbox.ScratchConsumerBinding{
			{SessionID: root.id, CurrentBindingID: "E0"},
			{SessionID: "child-consumer", CurrentBindingID: "E0"},
		}); err != nil {
		t.Fatal(err)
	}
	// Release the live lease: a cold consumer's allocation must restore from the
	// committed manifest.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch: %v", err)
	}

	rootEnv := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := root.adoptConsumerScratch(rootEnv, root.id); err != nil {
		t.Fatalf("adopt as root: %v", err)
	}
	if got := rootEnv.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
		t.Fatalf("root adopted scratch = %q, want original %q", got, scratch.Dir)
	}
	// A DISTINCT consumer of the same binding must resolve the original path
	// through the borrow branch without taking a second lease.
	childEnv := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := root.adoptConsumerScratch(childEnv, "child-consumer"); err != nil {
		t.Fatalf("adopt as child: %v", err)
	}
	if got := childEnv.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
		t.Fatalf("child resolved scratch = %q, want original %q", got, scratch.Dir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("shared artifact lost: bytes=%q err=%v", got, err)
	}
	// The distinct consumer resolves the same original allocation idempotently
	// through the lease-less borrow branch, never by duplicating ownership.
	if _, err := root.adoptConsumerScratch(childEnv, "child-consumer"); err != nil {
		t.Fatalf("repeat borrow adoption: %v", err)
	}
	if got := childEnv.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
		t.Fatalf("repeat borrow changed child scratch: %q", got)
	}

	// Release must precede restore: while the root still holds the lease the
	// directory is contended, and only after it releases can a restore reacquire.
	if _, err := sandbox.OpenRetainedSessionScratch(owner, ref); !errors.Is(err, sandbox.ErrScratchRetentionLeaseHeld) {
		t.Fatalf("reacquire while root holds lease = %v, want ErrScratchRetentionLeaseHeld", err)
	}
	rootEnv.RetainSessionScratch()
	restored, err := sandbox.OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatalf("reacquire after root release: %v", err)
	}
	if err := restored.Retain(); err != nil {
		t.Fatal(err)
	}
	// The child's borrow is lease-less: releasing the root's lease must not have
	// required releasing anything the child owned.
	if got := childEnv.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
		t.Fatalf("child scratch changed after root release: %q", got)
	}
}

// TestScratchRetentionTerminalReleaseAllowsCollection proves a terminal root
// close commits the Released tombstone, so an aged required directory is
// collected by the ordinary startup sweep, while an unreleased run is not.
func TestScratchRetentionTerminalReleaseAllowsCollection(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	env, ok := root.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("root has no local environment")
	}
	if env.SessionScratchDir() == "" {
		if _, err := env.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
			t.Fatalf("mint scratch: %v", err)
		}
	}
	scratchDir := env.SessionScratchDir()
	if scratchDir == "" {
		t.Fatal("root minted no scratch")
	}
	artifact := filepath.Join(scratchDir, "terminal-required.bin")
	if err := os.WriteFile(artifact, []byte("terminal"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root has no retention owner")
	}
	// Before the terminal close the manifest is unreleased, so an aged
	// directory must survive the sweep.
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Released {
		t.Fatal("a live root already has a Released tombstone")
	}
	// Negative control: an unreleased sibling in the same base must survive the
	// very same aged sweep, proving the sweep collects on Released authorization
	// rather than deleting every aged directory.
	unreleasedOwner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "unreleased-sibling"}
	unreleased, err := sandbox.NewSessionScratch(filepath.Dir(scratchDir), dir)
	if err != nil {
		t.Fatalf("create unreleased sibling: %v", err)
	}
	if err := unreleased.Pin(unreleasedOwner, sandbox.ScratchReference{Dir: unreleased.Dir, Kind: sandbox.ScratchKindUnsandboxed}); err != nil {
		t.Fatalf("pin unreleased sibling: %v", err)
	}
	if err := unreleased.Retain(); err != nil {
		t.Fatalf("retain unreleased sibling: %v", err)
	}
	unreleasedDir := unreleased.Dir
	t.Cleanup(func() { _ = os.RemoveAll(unreleasedDir) })
	root.Close()
	manifest, err = sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("terminal root close did not commit the Released tombstone")
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(scratchDir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unreleasedDir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SweepCrashedSessionScratch(dir); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(scratchDir); !os.IsNotExist(err) {
		t.Fatalf("released terminal scratch not collected: %v", err)
	}
	if _, err := os.Stat(unreleasedDir); err != nil {
		t.Fatalf("unreleased sibling was collected by the same sweep: %v", err)
	}
}

func readFileOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// retirementPreservationFixture is a real-git root with a genuinely TWO-LEVEL
// delegate chain: the root creates an isolated delegate, and that delegate
// creates a second isolated delegate, each ending through the real
// communicate/result path. It carries one clean and one tracked-dirty occupied
// lane, a required scratch artifact per child environment, and captures the
// original durable records and git occupancy markers so both can be compared
// before and after a non-terminal retirement.
type retirementPreservationFixture struct {
	t              *testing.T
	repo           *wtRepo
	root           *Session
	controller     *RetirementController
	rootID         string
	delegateIDs    []string
	childIDs       []string
	childRuntimes  []*Session
	lanePaths      []string
	artifactPaths  []string
	artifactBytes  [][]byte
	rootTranscript string
	primaryBefore  retirementPrimaryFiles
	laneBlocks     map[string]string
	laneHeads      []string
	laneStatuses   []string
}

func retirementFixtureConfig() SessionConfig {
	cfg := worktreeTestSessionConfig()
	cfg.MaxSubagentDepth = 2
	// The minimal worktree registry removes the `delegate` tool from children
	// (hasConfiguredDelegateCapability), which would make a genuine two-level
	// chain impossible. Use the full registry so the first delegate can create
	// the second through its real turn.
	cfg.testOnly.minimalWorktreeToolRegistry = false
	return cfg
}

// newRetirementWorktreeRepo builds a real one-commit git repo and a session
// rooted at it whose state dir is fixed AT CONSTRUCTION, so the session's
// transcript writer, delegate controller and task store all agree on one
// durable location (the plain wtRepo reassigns stateDir afterward, which leaves
// a delegate's attention path without an attached writer).
func newRetirementWorktreeRepo(t *testing.T) *wtRepo {
	t.Helper()
	root, err := filepath.EvalSymlinks(packageFixtureTempDir(t, "retirement-repo-*"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	copyWorktreeBaseRepo(t, root)
	_, head := worktreeBaseRepo(t)
	stateDir, err := filepath.EvalSymlinks(packageFixtureTempDir(t, "retirement-state-*"))
	if err != nil {
		t.Fatalf("EvalSymlinks state: %v", err)
	}
	cfg := retirementFixtureConfig()
	cfg.StateDir = stateDir
	s := newSession(t, withDir(root), withConfig(cfg))
	s.mu.Lock()
	s.worktreeGitVersionOK = true
	s.mu.Unlock()
	return &wtRepo{s: s, mainRoot: root, stateDir: stateDir, head: head}
}

// newRetirementForeignSession builds an independent root on the same repo and
// state dir, for an intervening residue sweep by another session.
func newRetirementForeignSession(t *testing.T, repo *wtRepo) *Session {
	t.Helper()
	cfg := retirementFixtureConfig()
	cfg.StateDir = repo.stateDir
	s := newSession(t, withDir(repo.mainRoot), withConfig(cfg))
	s.mu.Lock()
	s.worktreeGitVersionOK = true
	s.mu.Unlock()
	return s
}

func newRetirementPreservationFixture(t *testing.T) *retirementPreservationFixture {
	t.Helper()
	repo := newRetirementWorktreeRepo(t)
	root := repo.s
	root.client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	f := &retirementPreservationFixture{t: t, repo: repo, root: root, controller: c, rootID: root.ID(), rootTranscript: root.TranscriptPath()}

	// Level 1: the root creates an isolated delegate.
	f.addIsolatedChild(root, "retirement-lane-one")
	// Level 2: the first delegate creates another isolated delegate from its own
	// real run, driven by a scripted provider that issues a real `delegate` tool
	// call routed by the structural request SessionID. This is what actually
	// reaches depth 2.
	root.client.Register(&nestedDelegateAdapter{
		fakeAdapter:      fakeAdapter{name: "openai"},
		creatorSessionID: f.childIDs[0],
		nestedTask:       "retirement-lane-two",
	})
	outcome := (delegateRuntime{owner: root}).send(context.Background(), f.delegateIDs[0], "create a nested delegate", 0)
	if outcome.result.Err != nil {
		t.Fatalf("send nested-create to first delegate: %v", outcome.result.Err)
	}
	// Settle the first delegate's turn (and, transitively, the nested child's
	// result) through the real result path before restoring the base adapter.
	retirementSettleDelegate(t, root, delegateResult{DelegateID: f.delegateIDs[0], ChildSessionID: f.childIDs[0]})
	// Drain the nested result's notification through one more real root turn so
	// the tree reaches quiescence.
	if _, err := root.ProcessInput(context.Background(), "drain-nested-notification", nil); err != nil {
		t.Fatalf("drain nested notification: %v", err)
	}
	root.client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
	secondID, secondChildID := f.nestedDelegateUnder(f.delegateIDs[0])
	if secondID == "" {
		t.Fatal("first delegate did not create a nested delegate")
	}
	f.recordIsolatedChild(secondID, secondChildID, "retirement-lane-two")
	// One clean and one tracked-dirty occupied lane.
	if err := os.WriteFile(filepath.Join(f.lanePaths[1], "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.laneBlocks = retirementLaneBlocks(t, repo.mainRoot)
	for i := range f.lanePaths {
		f.laneHeads = append(f.laneHeads, strings.TrimSpace(wtGit(t, f.lanePaths[i], "rev-parse", "HEAD")))
		f.laneStatuses = append(f.laneStatuses, wtGit(t, f.lanePaths[i], "status", "--porcelain=v1"))
	}
	f.primaryBefore = readRetirementPrimaryFiles(t, repo.stateDir)
	return f
}

// nestedDelegateUnder returns the delegate whose descriptor names parentID as
// its parent (the real depth-2 identity the controller recorded).
func (f *retirementPreservationFixture) nestedDelegateUnder(parentID string) (delegateID, childSessionID string) {
	f.t.Helper()
	f.root.delegateController.mu.Lock()
	defer f.root.delegateController.mu.Unlock()
	for id, aggregate := range f.root.delegateController.durable {
		if aggregate.Descriptor.ParentDelegateID == parentID {
			return id, aggregate.Descriptor.ChildSessionID
		}
	}
	return "", ""
}

// nestedDelegateAdapter is a scripted provider. For exactly one creator child
// session it issues a real `delegate` tool call (worktree isolation) on that
// session's first turn, then ends; every other session ends through the real
// communicate/result path. Routing is by the structural Request.SessionID, never
// by prompt prose.
type nestedDelegateAdapter struct {
	fakeAdapter
	creatorSessionID string
	nestedTask       string
	mu               sync.Mutex
	issued           bool
}

func (a *nestedDelegateAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if req.SessionID == a.creatorSessionID {
		a.mu.Lock()
		first := !a.issued
		a.issued = true
		a.mu.Unlock()
		if first {
			raw, err := json.Marshal(map[string]any{"prompt": a.nestedTask, "isolation": "worktree"})
			if err != nil {
				return llm.Response{}, err
			}
			return toolCallResponse(llm.ToolCallData{ID: "nested-create-" + a.nestedTask, Name: "delegate", Arguments: raw}), nil
		}
	}
	response := communicateWithDefaultOutput("nested-result")
	response.Provider, response.Model = a.name, req.Model
	return response, nil
}

// addIsolatedChild creates one isolated delegate under owner, settles it through
// the real result path, mints and pins a required scratch artifact through the
// child's own environment, and records the live lane/scratch/bytes.
func (f *retirementPreservationFixture) addIsolatedChild(owner *Session, task string) {
	f.t.Helper()
	result := owner.createDelegate(context.Background(), delegateArgs{Task: task, Isolation: "worktree"})
	if result.Err != nil {
		f.t.Fatalf("create isolated delegate %q: %v", task, result.Err)
	}
	retirementSettleDelegate(f.t, f.root, result)
	f.recordIsolatedChild(result.DelegateID, result.ChildSessionID, task)
}

// recordIsolatedChild records one already-created isolated child: it mints and
// pins a required scratch artifact through the child's own live environment and
// captures the lane/scratch/bytes from the live environment, never the manifest.
func (f *retirementPreservationFixture) recordIsolatedChild(delegateID, childSessionID, task string) {
	f.t.Helper()
	child := f.root.delegateController.residentDelegateRuntime(delegateID)
	if child == nil {
		f.t.Fatalf("delegate %q runtime missing", task)
	}
	childEnv, ok := child.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		f.t.Fatalf("delegate %q has no local environment", task)
	}
	if childEnv.SessionScratchDir() == "" {
		if _, err := childEnv.ExecCommand(context.Background(), "true", 5000, childEnv.WorkingDirectory(), nil); err != nil {
			f.t.Fatalf("mint delegate %q scratch: %v", task, err)
		}
	}
	scratchDir := childEnv.SessionScratchDir()
	if scratchDir == "" {
		f.t.Fatalf("delegate %q minted no scratch", task)
	}
	f.t.Cleanup(func() { _ = os.RemoveAll(scratchDir) })
	if err := f.root.installChildScratchRetention(childEnv, childSessionID); err != nil {
		f.t.Fatalf("install delegate %q retention: %v", task, err)
	}
	lanePath := childEnv.WorkingDirectory()
	artifact := filepath.Join(scratchDir, task+".bin")
	want := []byte("nested-cold-restore-artifact:" + task)
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.delegateIDs = append(f.delegateIDs, delegateID)
	f.childIDs = append(f.childIDs, childSessionID)
	f.childRuntimes = append(f.childRuntimes, child)
	f.lanePaths = append(f.lanePaths, lanePath)
	f.artifactPaths = append(f.artifactPaths, artifact)
	f.artifactBytes = append(f.artifactBytes, want)
}

// assertLanesUnchanged verifies every original occupancy marker, lane and
// tracked bytes is byte-identical to the captured live reference. label names
// the checkpoint in failure messages.
func (f *retirementPreservationFixture) assertLanesUnchanged(label string) {
	f.t.Helper()
	current := retirementLaneBlocks(f.t, f.repo.mainRoot)
	for i, lanePath := range f.lanePaths {
		want, ok := f.laneBlocks[lanePath]
		if !ok {
			f.t.Fatalf("no captured porcelain block for lane %q", lanePath)
		}
		got, present := current[lanePath]
		if !present {
			f.t.Fatalf("%s removed lane %q from the worktree registry", label, lanePath)
		}
		if got != want {
			f.t.Fatalf("%s changed lane %q occupancy marker:\nbefore:\n%s\nafter:\n%s", label, lanePath, want, got)
		}
		if !strings.Contains(got, "locked ") {
			f.t.Fatalf("%s left lane %q unlocked", label, lanePath)
		}
		if !f.repo.lanePresent(lanePath) {
			f.t.Fatalf("%s removed the required lane %q", label, lanePath)
		}
		if got := strings.TrimSpace(wtGit(f.t, lanePath, "rev-parse", "HEAD")); got != f.laneHeads[i] {
			f.t.Fatalf("%s changed lane %q HEAD: %q != %q", label, lanePath, got, f.laneHeads[i])
		}
		if got := wtGit(f.t, lanePath, "status", "--porcelain=v1"); got != f.laneStatuses[i] {
			f.t.Fatalf("%s changed lane %q status: %q != %q", label, lanePath, got, f.laneStatuses[i])
		}
	}
}

func retirementPorcelain(t *testing.T, mainRoot string) string {
	t.Helper()
	return wtGit(t, mainRoot, "worktree", "list", "--porcelain")
}

// retirementLaneBlocks returns each worktree's own porcelain block keyed by its
// exact path, so a comparison targets the original occupancy lanes without being
// perturbed by an unrelated main-checkout HEAD change or a seeded lane.
func retirementLaneBlocks(t *testing.T, mainRoot string) map[string]string {
	t.Helper()
	blocks := map[string]string{}
	for _, chunk := range strings.Split(retirementPorcelain(t, mainRoot), "\n\n") {
		trimmed := strings.TrimSpace(chunk)
		if trimmed == "" {
			continue
		}
		for _, line := range strings.Split(trimmed, "\n") {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				blocks[strings.TrimSpace(path)] = trimmed
			}
		}
	}
	return blocks
}

// retire runs the exact preparation/commit/release sequence and requires the
// non-terminal release to succeed.
func (f *retirementPreservationFixture) retire() *RetirementPreparation {
	f.t.Helper()
	claim, state, err := f.controller.TryClaim(true)
	if err != nil || claim == nil {
		f.t.Fatalf("claim: %+v %v", state, err)
	}
	prepared, err := f.controller.Prepare(context.Background(), claim)
	if err != nil {
		f.t.Fatalf("prepare: %v", err)
	}
	if err := f.controller.Commit(claim); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	if err := f.controller.DrainReaders(context.Background()); err != nil {
		f.t.Fatalf("drain readers: %v", err)
	}
	if err := f.root.ReleaseForRetirement(context.Background(), prepared); err != nil {
		f.t.Fatalf("release: %v", err)
	}
	return prepared
}

// foreignSweep creates an independent root on the same repo and state dir and
// runs the real residue sweep, returning a real seeded foreign lane that the
// same pass must collect.
func (f *retirementPreservationFixture) foreignSweep() {
	f.t.Helper()
	// Seed an unlocked, merged foreign lane that the same pass must collect:
	// collection was not globally disabled.
	seedDelegateID, seedPath, _ := f.repo.seedForeignUnlockedLane(f.t)
	f.repo.commitAndFastForwardMerge(f.t, seedDelegateID, seedPath)
	f.repo.ageBeyondGrace(f.t, seedDelegateID)

	foreign := newRetirementForeignSession(f.t, f.repo)
	foreign.runLaneResidueSweep(context.Background())

	if f.repo.lanePresent(seedPath) {
		f.t.Error("the intervening foreign sweep did not collect a collectible foreign lane")
	}
	// The sweep must not have touched any original occupancy marker or lane.
	f.assertLanesUnchanged("foreign sweep")
}

// assertRestored restores a fresh root from the durable state and cold-sends to
// EVERY recorded delegate id, requiring each original lane, scratch path and
// artifact, and then re-verifies every git occupancy marker/registry entry.
func (f *retirementPreservationFixture) assertRestored() *Session {
	f.t.Helper()
	meta, err := schema.LoadSessionMeta(f.repo.stateDir, f.rootID)
	if err != nil {
		f.t.Fatalf("load root meta: %v", err)
	}
	client := llm.NewClient()
	client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(f.repo.mainRoot), meta, RestoreSessionConfig{StateDir: f.repo.stateDir})
	if err != nil {
		f.t.Fatalf("restore root: %v", err)
	}
	if restored.ID() != f.rootID {
		f.t.Fatalf("restored another root: %s", restored.ID())
	}
	for i, delegateID := range f.delegateIDs {
		if i == 0 {
			// A direct delegate is cold-sent from the restored root.
			outcome := (delegateRuntime{owner: restored}).send(context.Background(), delegateID, "read-artifact", 0)
			if outcome.result.Err != nil {
				f.t.Fatalf("cold send to %s: %v", delegateID, outcome.result.Err)
			}
		} else {
			// A descendant delegate is controllable only by its direct parent, so
			// it is cold-sent from the already-restored parent's own live turn.
			f.coldSendThroughParent(restored, i)
		}
		rchild := restored.delegateController.residentDelegateRuntime(delegateID)
		if rchild == nil {
			f.t.Fatalf("cold delegate %s was not restored", delegateID)
		}
		rchildEnv, ok := rchild.env.(*execenv.LocalExecutionEnvironment)
		if !ok {
			f.t.Fatalf("restored child %s has no local environment", delegateID)
		}
		if got := filepath.Clean(rchildEnv.WorkingDirectory()); got != filepath.Clean(f.lanePaths[i]) {
			f.t.Fatalf("restored child %s lane = %q, want original %q", delegateID, got, f.lanePaths[i])
		}
		// The required artifact was created through the child's environment; it
		// must remain readable at its original absolute path. (Symbolic
		// scratch-path adoption across a worktree move is the shared-child
		// checkpoint's proof; here the lane branch/ownership and the same IDs are
		// the nested-restore oracle.)
		got, err := os.ReadFile(f.artifactPaths[i])
		if err != nil || !bytes.Equal(got, f.artifactBytes[i]) {
			f.t.Fatalf("required artifact lost at %q: bytes=%q err=%v", f.artifactPaths[i], got, err)
		}
	}
	// The restored tree must re-adopt every recorded lane marker unchanged.
	f.assertLanesUnchanged("cold restore")
	return restored
}

// coldSendThroughParent reconstructs the i-th (nested) delegate by driving its
// direct parent's real turn: the scripted adapter has the parent issue a
// delegate_send with a blocking wait, which cold-restores the child through the
// real send path.
func (f *retirementPreservationFixture) coldSendThroughParent(restored *Session, i int) {
	f.t.Helper()
	parentID := f.delegateIDs[i-1]
	senderChildID := f.childIDs[i-1]
	if restored.delegateController.residentDelegateRuntime(parentID) == nil {
		f.t.Fatalf("parent delegate %s was not restored before its child", parentID)
	}
	restored.client.Register(&nestedSendAdapter{
		fakeAdapter:      fakeAdapter{name: "openai"},
		senderSessionID:  senderChildID,
		targetDelegateID: f.delegateIDs[i],
	})
	outcome := (delegateRuntime{owner: restored}).send(context.Background(), parentID, "resolve-nested-child", 0)
	if outcome.result.Err != nil {
		f.t.Fatalf("start parent %s turn: %v", parentID, outcome.result.Err)
	}
	sub := restored.subagents.get(senderChildID)
	if sub == nil {
		f.t.Fatalf("parent subagent %s missing", senderChildID)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(30 * time.Second): // TRIPWIRE: the parent turn's own completion channel is the mechanism; this only bounds a deadlock in the fixture.
		f.t.Fatal("parent delegate turn did not finish")
	}
	if _, err := restored.ProcessInput(context.Background(), "settle-nested-restore", nil); err != nil {
		f.t.Fatalf("settle nested restore: %v", err)
	}
	restored.client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
}

// nestedSendAdapter is a scripted provider that has exactly one sender child
// issue a blocking delegate_send to a target delegate on its first turn, then
// ends. Routing is by the structural Request.SessionID.
type nestedSendAdapter struct {
	fakeAdapter
	senderSessionID  string
	targetDelegateID string
	mu               sync.Mutex
	issued           bool
}

func (a *nestedSendAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if req.SessionID == a.senderSessionID {
		a.mu.Lock()
		first := !a.issued
		a.issued = true
		a.mu.Unlock()
		if first {
			raw, err := json.Marshal(map[string]any{"to": a.targetDelegateID, "message": "read-nested-artifact", "max_wait_ms": 30000})
			if err != nil {
				return llm.Response{}, err
			}
			return toolCallResponse(llm.ToolCallData{ID: "nested-send", Name: "delegate_send", Arguments: raw}), nil
		}
	}
	response := communicateWithDefaultOutput("nested-send-result")
	response.Provider, response.Model = a.name, req.Model
	return response, nil
}

// TestRetirementPreservationNestedColdRestore is the real-git end-to-end proof:
// a root with a genuinely two-level delegate chain (one clean and one
// tracked-dirty occupied lane, a required scratch artifact per child) retires
// non-terminally, an intervening foreign sweep leaves every original occupancy
// marker intact, and a fresh root cold-sends to BOTH delegates which resolve
// their original lanes and artifacts.
func TestRetirementPreservationNestedColdRestore(t *testing.T) {
	f := newRetirementPreservationFixture(t)
	if len(f.delegateIDs) != 2 || len(f.lanePaths) != 2 || len(f.artifactPaths) != 2 {
		t.Fatalf("fixture depth = %d delegates / %d lanes, want a two-level chain", len(f.delegateIDs), len(f.lanePaths))
	}
	if parent := f.root.delegateController.durable[f.delegateIDs[1]].Descriptor.ParentDelegateID; parent != f.delegateIDs[0] {
		t.Fatalf("second delegate parent = %q, want first delegate %q (not a nested chain)", parent, f.delegateIDs[0])
	}
	if f.laneStatuses[1] == "" {
		t.Fatal("second occupied lane must be tracked-dirty")
	}
	if f.laneStatuses[0] != "" {
		t.Fatal("first occupied lane must be clean")
	}
	f.retire()

	// The durable git occupancy markers are unchanged by retirement.
	f.assertLanesUnchanged("retirement")
	// Release appended no terminal transcript evidence.
	if got, err := os.ReadFile(f.rootTranscript); err != nil || len(got) == 0 {
		t.Fatalf("root transcript unreadable: %v", err)
	}
	before := f.primaryBefore
	after := readRetirementPrimaryFiles(t, f.repo.stateDir)
	for name, want := range before {
		if strings.HasSuffix(name, ".lock") {
			continue
		}
		got, ok := after[name]
		if !ok {
			t.Fatalf("primary record %q disappeared across retirement", name)
		}
		// A meta.json may take a legitimate final save (work time, usage); its
		// IDENTITY must be preserved instead of its exact bytes.
		if strings.HasSuffix(name, ".meta.json") {
			continue
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("primary record %q changed across retirement", name)
		}
	}
	// A newly created primary record would be a durable stop/cancel/watch-drop/
	// disposal event written to a new file. Only the tolerated writes the plan
	// sanctions (a final .meta.json save, the scratch manifest revision bump, and
	// process-local lock/temp files) may appear.
	for name := range after {
		if _, existed := before[name]; existed {
			continue
		}
		if toleratedRetirementPrimaryAddition(name) {
			continue
		}
		t.Fatalf("retirement created a new primary record %q", name)
	}
	// No terminal session state may be persisted: the root and every child
	// metadata still names the same session.
	rootMeta, err := schema.LoadSessionMeta(f.repo.stateDir, f.rootID)
	if err != nil {
		t.Fatalf("root meta after retirement: %v", err)
	}
	if rootMeta.ID != f.rootID {
		t.Fatalf("root meta after retirement = %+v", rootMeta)
	}
	for _, childID := range f.childIDs {
		childMeta, err := schema.LoadSessionMeta(f.repo.stateDir, childID)
		if err != nil {
			t.Fatalf("child %s meta after retirement: %v", childID, err)
		}
		if childMeta.ID != childID {
			t.Fatalf("child meta id = %q, want %q", childMeta.ID, childID)
		}
	}

	f.foreignSweep()

	restored := f.assertRestored()
	defer restored.Close()
}

// toleratedRetirementPrimaryAddition reports whether name is a legitimate
// addition the plan sanctions: process-local lock/temp files, the root scratch
// manifest (whose revision bumps on adoption), and executed ATIF output.
func toleratedRetirementPrimaryAddition(name string) bool {
	if strings.HasSuffix(name, ".lock") || strings.Contains(name, ".tmp-") {
		return true
	}
	if strings.HasPrefix(filepath.ToSlash(name), "scratch-retention/") {
		return true
	}
	return false
}

// TestRetirementForeignSweepPreservesOccupiedLanes is the plan's sweep-scoped
// proof on the same fixture: an intervening foreign root's real residue sweep
// preserves the retired root's occupied clean and dirty lanes while still
// collecting an independently seeded collectible foreign lane.
func TestRetirementForeignSweepPreservesOccupiedLanes(t *testing.T) {
	f := newRetirementPreservationFixture(t)
	if f.laneStatuses[1] == "" {
		t.Fatal("second occupied lane must be tracked-dirty")
	}
	f.retire()
	f.assertLanesUnchanged("retirement")

	f.foreignSweep()
	for _, id := range f.delegateIDs {
		if !f.repo.branchExists(t, id) {
			t.Fatalf("foreign sweep deleted the occupied lane branch %s", id)
		}
	}
	restored := f.assertRestored()
	defer restored.Close()
}

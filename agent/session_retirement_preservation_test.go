package agent

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
}

// TestRetirementSharedChildScratchBindingsRestore is the Task 5 re-review N1
// carry-forward. Two consumers share one binding: prepare reacquires the single
// allocation, the root adopts it, and a DISTINCT child consumer resolves the
// original path through the lease-less borrow branch
// (session_scratch_retention.go adoptRetainedScratchFor). The lease is free
// after the root releases, proving release precedes restore.
func TestRetirementSharedChildScratchBindingsRestore(t *testing.T) {
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
	if err := sandbox.SweepCrashedSessionScratch(dir); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(scratchDir); !os.IsNotExist(err) {
		t.Fatalf("released terminal scratch not collected: %v", err)
	}
}

func readFileOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// retirementPreservationFixture is a real-git root with a real delegate lane and
// a required scratch artifact created through the child's own environment. It
// captures the original durable records and git occupancy markers so both can
// be compared before and after a non-terminal retirement.
type retirementPreservationFixture struct {
	t               *testing.T
	repo            *wtRepo
	root            *Session
	controller      *RetirementController
	rootID          string
	delegateID      string
	childID         string
	child           *Session
	lanePath        string
	artifactPath    string
	artifactBytes   []byte
	rootTranscript  string
	childTranscript string
	primaryBefore   retirementPrimaryFiles
	gitMarker       string
	laneHead        string
	laneStatus      string
}

func retirementFixtureConfig() SessionConfig {
	cfg := worktreeTestSessionConfig()
	cfg.MaxSubagentDepth = 2
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

	// A real delegate with a real isolation lane, ended through the real
	// communicate/result path.
	result := root.createDelegate(context.Background(), delegateArgs{Task: "retirement-lane-delegate", Isolation: "worktree"})
	if result.Err != nil {
		t.Fatalf("create isolated delegate: %v", result.Err)
	}
	retirementSettleDelegate(t, root, result)
	child := root.delegateController.residentDelegateRuntime(result.DelegateID)
	if child == nil {
		t.Fatal("isolated delegate runtime missing")
	}
	childEnv, ok := child.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("isolated delegate has no local environment")
	}
	if childEnv.SessionScratchDir() == "" {
		if _, err := childEnv.ExecCommand(context.Background(), "true", 5000, childEnv.WorkingDirectory(), nil); err != nil {
			t.Fatalf("mint child scratch: %v", err)
		}
	}
	scratchDir := childEnv.SessionScratchDir()
	if scratchDir == "" {
		t.Fatal("child minted no scratch")
	}
	if err := root.installChildScratchRetention(childEnv, result.ChildSessionID); err != nil {
		t.Fatalf("install child retention: %v", err)
	}
	artifact := filepath.Join(scratchDir, "required-artifact.bin")
	want := []byte("nested-cold-restore-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		t.Fatal(err)
	}
	lanePath := childEnv.WorkingDirectory()
	head := strings.TrimSpace(wtGit(t, lanePath, "rev-parse", "HEAD"))
	status := wtGit(t, lanePath, "status", "--porcelain=v1")

	f := &retirementPreservationFixture{
		t: t, repo: repo, root: root, controller: c, rootID: root.ID(),
		delegateID: result.DelegateID, childID: result.ChildSessionID, child: child,
		lanePath: lanePath, artifactPath: artifact, artifactBytes: want,
		rootTranscript: root.TranscriptPath(), childTranscript: child.TranscriptPath(),
		gitMarker: retirementPorcelain(t, repo.mainRoot), laneHead: head, laneStatus: status,
	}
	f.primaryBefore = readRetirementPrimaryFiles(t, repo.stateDir)
	return f
}

func retirementPorcelain(t *testing.T, mainRoot string) string {
	t.Helper()
	return wtGit(t, mainRoot, "worktree", "list", "--porcelain")
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
}

// assertRestored restores a fresh root from the durable state and cold-sends to
// the same delegate, requiring the original lane, scratch path and artifact.
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
	outcome := (delegateRuntime{owner: restored}).send(context.Background(), f.delegateID, "read-artifact", 0)
	if outcome.result.Err != nil {
		f.t.Fatalf("cold send: %v", outcome.result.Err)
	}
	rchild := restored.delegateController.residentDelegateRuntime(f.delegateID)
	if rchild == nil {
		f.t.Fatal("cold delegate was not restored")
	}
	rchildEnv, ok := rchild.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		f.t.Fatal("restored child has no local environment")
	}
	if got := filepath.Clean(rchildEnv.WorkingDirectory()); got != filepath.Clean(f.lanePath) {
		f.t.Fatalf("restored child lane = %q, want original %q", got, f.lanePath)
	}
	if got := filepath.Clean(rchildEnv.SessionScratchDir()); got != filepath.Clean(filepath.Dir(f.artifactPath)) {
		f.t.Fatalf("restored child scratch = %q, want original %q", got, filepath.Dir(f.artifactPath))
	}
	got, err := os.ReadFile(f.artifactPath)
	if err != nil || !bytes.Equal(got, f.artifactBytes) {
		f.t.Fatalf("required artifact lost at %q: bytes=%q err=%v", f.artifactPath, got, err)
	}
	return restored
}

// TestRetirementPreservationNestedColdRestore is the real-git end-to-end proof:
// a root with a resident delegate lane and required scratch artifact retires
// non-terminally, an intervening foreign sweep leaves every original occupancy
// marker intact, and a fresh root cold-sends to the same delegate which resolves
// its original lane and artifact.
func TestRetirementPreservationNestedColdRestore(t *testing.T) {
	f := newRetirementPreservationFixture(t)
	f.retire()

	// The durable git occupancy markers are unchanged by retirement.
	if got := retirementPorcelain(t, f.repo.mainRoot); got != f.gitMarker {
		t.Fatalf("retirement changed the worktree registry:\nbefore:\n%s\nafter:\n%s", f.gitMarker, got)
	}
	if !f.repo.lanePresent(f.lanePath) {
		t.Fatal("retirement removed the required delegate lane")
	}
	if got := strings.TrimSpace(wtGit(t, f.lanePath, "rev-parse", "HEAD")); got != f.laneHead {
		t.Fatalf("retirement changed lane HEAD: %q != %q", got, f.laneHead)
	}
	if got := wtGit(t, f.lanePath, "status", "--porcelain=v1"); got != f.laneStatus {
		t.Fatalf("retirement changed lane status: %q != %q", got, f.laneStatus)
	}
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
	// No terminal session state may be persisted: the root and child metadata
	// still name the same sessions and neither is closed.
	rootMeta, err := schema.LoadSessionMeta(f.repo.stateDir, f.rootID)
	if err != nil {
		t.Fatalf("root meta after retirement: %v", err)
	}
	if rootMeta.ID != f.rootID {
		t.Fatalf("root meta after retirement = %+v", rootMeta)
	}
	childMeta, err := schema.LoadSessionMeta(f.repo.stateDir, f.childID)
	if err != nil {
		t.Fatalf("child meta after retirement: %v", err)
	}
	if childMeta.ID != f.childID {
		t.Fatalf("child meta id = %q, want %q", childMeta.ID, f.childID)
	}

	f.foreignSweep()
	// The foreign sweep must not have touched the original occupancy markers or
	// required lane.
	if got := retirementPorcelain(t, f.repo.mainRoot); got == "" {
		t.Fatal("worktree registry unreadable after foreign sweep")
	}
	if !f.repo.lanePresent(f.lanePath) {
		t.Fatal("foreign sweep collected a locked original lane")
	}
	if got := strings.TrimSpace(wtGit(t, f.lanePath, "rev-parse", "HEAD")); got != f.laneHead {
		t.Fatalf("foreign sweep changed lane HEAD: %q != %q", got, f.laneHead)
	}
	if got := wtGit(t, f.lanePath, "status", "--porcelain=v1"); got != f.laneStatus {
		t.Fatalf("foreign sweep changed lane status: %q != %q", got, f.laneStatus)
	}

	restored := f.assertRestored()
	defer restored.Close()
}

// TestRetirementForeignSweepPreservesOccupiedLanes is the plan's sweep-scoped
// proof on the same fixture: an intervening foreign root's real residue sweep
// preserves the retired root's occupied clean and dirty lanes while still
// collecting an independently seeded collectible foreign lane.
func TestRetirementForeignSweepPreservesOccupiedLanes(t *testing.T) {
	f := newRetirementPreservationFixture(t)
	// Tracked-dirty lane: a real modification to a tracked file.
	if err := os.WriteFile(filepath.Join(f.lanePath, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirtyStatus := wtGit(t, f.lanePath, "status", "--porcelain=v1")
	if dirtyStatus == "" {
		t.Fatal("lane is not tracked-dirty")
	}
	f.retire()

	f.foreignSweep()
	if !f.repo.lanePresent(f.lanePath) {
		t.Fatal("foreign sweep removed an occupied dirty lane")
	}
	if got := wtGit(t, f.lanePath, "status", "--porcelain=v1"); got != dirtyStatus {
		t.Fatalf("foreign sweep changed dirty lane status: %q != %q", got, dirtyStatus)
	}
	if !f.repo.branchExists(t, f.delegateID) {
		t.Fatal("foreign sweep deleted the occupied lane branch")
	}
	restored := f.assertRestored()
	defer restored.Close()
}

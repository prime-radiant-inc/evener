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
	"sync/atomic"
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
	t.Parallel()
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
	t.Parallel()
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
	defer setRetirementReleaseFault(func(point string) error {
		if point == "transcript_close" {
			return injected
		}
		return nil
	})()

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
	t.Parallel()
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
	t.Parallel()
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
	root.client.Register(&retirementDelegateAdapter{name: "openai"})
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
		name:             "openai",
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
	root.client.Register(&retirementDelegateAdapter{name: "openai"})
	secondID, secondChildID := f.nestedDelegateUnder(f.delegateIDs[0])
	if secondID == "" {
		t.Fatal("first delegate did not create a nested delegate")
	}
	f.recordIsolatedChild(secondID, secondChildID, "retirement-lane-two")
	// The nested delegate runs asynchronously: its terminal result, and the root
	// attention that result arms, can land AFTER the explicit drain above. Under
	// load that leaves a genuinely undelivered delegate notification pending, and
	// retirement then correctly refuses (a safety property of this branch). Settle
	// the whole tree through the production drain before capturing the live
	// reference, so retirement is attempted only once the tree is quiescent. This
	// is a real readiness condition -- DrainJobTree runs notification turns until
	// every job notification, delegate attention and running child has settled --
	// never a timing guess.
	awaitTreeQuiesced(t, root)
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

// awaitTreeQuiesced settles the whole delegate tree through the production
// drain before any retirement attempt. DrainJobTree is the real readiness
// signal: it runs notification turns until no job notification, delegate
// attention, delegate delivery or running/finalizing/driving child remains, so
// a retirement that follows cannot be refused for work the tree had not yet
// surfaced when an ad-hoc drain returned. The deadline only bounds a genuinely
// wedged tree; a well-formed tree quiesces on its own.
func awaitTreeQuiesced(t *testing.T, root *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // TRIPWIRE: DrainJobTree returns on its real completion signal (no job notification, delegate attention, delivery or running child left); this ceiling sits orders of magnitude above the scripted in-process drain and only fires on a genuinely wedged tree.
	defer cancel()
	if _, err := root.DrainJobTree(ctx); err != nil {
		t.Fatalf("settle delegate tree before retirement: %v", err)
	}
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
	for chunk := range strings.SplitSeq(retirementPorcelain(t, mainRoot), "\n\n") {
		trimmed := strings.TrimSpace(chunk)
		if trimmed == "" {
			continue
		}
		for line := range strings.SplitSeq(trimmed, "\n") {
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
	client.Register(&retirementDelegateAdapter{name: "openai"})
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
			// The send only launches the child's run. The next iteration
			// sends to this delegate again (as the nested child's parent),
			// which a run still completing refuses as target_busy.
			sub := restored.subagents.get(f.childIDs[i])
			if sub == nil {
				f.t.Fatalf("cold-sent delegate %s missing from the restored root", delegateID)
			}
			sub.mu.Lock()
			done := sub.done
			sub.mu.Unlock()
			select {
			case <-done:
			case <-time.After(30 * time.Second): // TRIPWIRE: the run's own completion channel is the mechanism; this only bounds a deadlock in the fixture.
				f.t.Fatalf("cold-sent delegate %s run did not finish", delegateID)
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
		name:             "openai",
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
	restored.client.Register(&retirementDelegateAdapter{name: "openai"})
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
	restored.Close()
}

// TestRetirementTreeSettleDrainsPendingRootAttention is the deterministic
// regression for the load-dependent nested-cold-restore flake. A delegate
// completion arms a durable, undelivered root delegate attention; while it is
// pending the retirement safety property correctly refuses. The fixture must
// therefore settle the whole tree through the production drain before
// retiring. This test builds a real delegate, lets it finish WITHOUT draining
// its result, requires the armed attention to be observable, then requires the
// settle step to drain it and leave retirement claimable. It fails if the
// settle step is absent or a no-op.
func TestRetirementTreeSettleDrainsPendingRootAttention(t *testing.T) {
	repo := newRetirementWorktreeRepo(t)
	root := repo.s
	t.Cleanup(func() { root.Close() })
	root.client.Register(&retirementDelegateAdapter{name: "openai"})
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	result := root.createDelegate(context.Background(), delegateArgs{Task: "late-root-attention"})
	if result.Err != nil {
		t.Fatalf("create delegate: %v", result.Err)
	}
	sub := root.subagents.get(result.ChildSessionID)
	if sub == nil {
		t.Fatal("created delegate missing from the manager")
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	// Wait for the runner only: no ProcessInput drain, so the completion's root
	// attention stays genuinely undelivered -- the state the flake retired in.
	select {
	case <-done:
	case <-time.After(30 * time.Second): // TRIPWIRE: bounds a fixture bug only.
		t.Fatal("delegate runner did not finish")
	}
	// done does not cover the namer the child's first turn launched; the
	// root's emitters stay unjoined so nothing drains the root attention.
	joinRetirementTreeEmitters(sub.sess)
	deadline := time.Now().Add(10 * time.Second)
	for !root.hasPendingRootDelegateAttention() {
		if time.Now().After(deadline) {
			t.Fatal("fixture: the delegate completion armed no pending root attention to drain")
		}
		time.Sleep(time.Millisecond)
	}
	// The safety property holds while the attention is pending: retirement
	// refuses rather than discarding an undelivered delegate notification.
	if _, state, err := c.TryClaim(true); err != nil || !hasRetirementBlocker(state.Blockers, "notification") {
		t.Fatalf("claim with pending root attention = %+v %v, want a notification refusal", state, err)
	}
	// The production readiness path drains the tree, after which retirement is
	// claimable.
	awaitTreeQuiesced(t, root)
	if root.hasPendingRootDelegateAttention() {
		t.Fatal("tree settle left the root delegate attention pending")
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim after tree settle = %+v %v, want a claim", state, err)
	}
	c.Abort(claim, "")
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
	restored.Close()
}

// --- Task 6 fix round 2: plan 776-778, shared-child scratch bindings across a
// worktree move, retirement, sweep, cold restore and the root backswap. ---

// sharedChildScratchMintAdapter is a scripted provider that issues exactly one
// real `shell` tool call on a specific child session's first turn after it is
// installed, so that child's live environment really mints a fresh allocation
// through the normal command path. Every other session ends through the real
// communicate/result path. Routing is by the structural Request.SessionID.
type sharedChildScratchMintAdapter struct {
	fakeAdapter
	childSessionID string
	mu             sync.Mutex
	issued         bool
}

func (a *sharedChildScratchMintAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if req.SessionID == a.childSessionID {
		a.mu.Lock()
		first := !a.issued
		a.issued = true
		a.mu.Unlock()
		if first {
			raw, err := json.Marshal(map[string]any{"command": "true"})
			if err != nil {
				return llm.Response{}, err
			}
			return toolCallResponse(llm.ToolCallData{ID: "shared-child-mint", Name: "shell", Arguments: raw}), nil
		}
	}
	response := communicateWithDefaultOutput("shared-child-result")
	response.Provider, response.Model = a.name, req.Model
	return response, nil
}

// TestRetirementConcurrentReleaseOnlyOneProceeds is the regression test for the
// unsynchronized one-use release guard. Concurrent ReleaseForRetirement calls
// for the same committed preparation must win teardown exactly once; run under
// -race so the detector observes the guard access.
func TestRetirementConcurrentReleaseOnlyOneProceeds(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
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

	// Count every caller that actually enters the teardown path. The fault point
	// runs after the one-use guard is claimed, so it distinguishes "proceeded"
	// from "refused" without changing teardown behavior.
	var entered atomic.Int32
	defer setRetirementReleaseFault(func(point string) error {
		if point == "before_release" {
			entered.Add(1)
		}
		return nil
	})()

	const callers = 8
	results := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = root.ReleaseForRetirement(context.Background(), prepared)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := entered.Load(); got != 1 {
		t.Fatalf("%d callers entered teardown, want exactly 1 (release results=%v)", got, results)
	}
	successes := 0
	for _, result := range results {
		if result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("%d callers reported release success, want exactly 1 (results=%v)", successes, results)
	}
}

// TestRetirementClaimsTeardownBeforeChildRelease proves retirement reserves the
// session's single teardown pass before releasing any child runtime. A terminal
// Close racing the boundary must never find the pass still free after children
// were already released; the reservation point must precede every child_release.
func TestRetirementClaimsTeardownBeforeChildRelease(t *testing.T) {
	f := newRetirementPreservationFixture(t)
	if len(f.childIDs) == 0 {
		t.Fatal("fixture produced no resident children to release")
	}
	claim, state, err := f.controller.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", state, err)
	}
	prepared, err := f.controller.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := f.controller.Commit(claim); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := f.controller.DrainReaders(context.Background()); err != nil {
		t.Fatalf("drain readers: %v", err)
	}

	var order []string
	defer setRetirementReleaseFault(func(point string) error {
		if point == "teardown_claimed" || point == "child_release" {
			order = append(order, point)
		}
		return nil
	})()

	if err := f.root.ReleaseForRetirement(context.Background(), prepared); err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(order) == 0 || order[0] != "teardown_claimed" {
		t.Fatalf("retirement did not claim the teardown pass before releasing children; order=%v", order)
	}
	childReleases := 0
	claimIndex := -1
	for i, point := range order {
		switch point {
		case "teardown_claimed":
			claimIndex = i
		case "child_release":
			childReleases++
		}
	}
	if childReleases == 0 {
		t.Fatalf("fixture released no child runtime; order=%v", order)
	}
	if claimIndex != 0 {
		t.Fatalf("teardown claim was not the first recorded step; order=%v", order)
	}
}

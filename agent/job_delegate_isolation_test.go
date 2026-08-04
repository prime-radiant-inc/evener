package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// These are integration tests for delegate worktree isolation (native worktree
// tools spec §9, Task 21): delegate(isolation:"worktree") gets its own managed
// worktree lane, the child env is rooted there, manage_worktree is denied to the
// child (spawn AND restore, including all-tools agent types), and a revived kept
// lane re-takes its serf:dlg: lock.
//
// This file is MIXED across the two git boundaries; see docs/testing.md for the
// rule. A test whose subject is serf's own decision-making — the tool-deny
// policy, rollback bookkeeping, argument validation, which lane a second job
// runs in, what the notification block carries — runs on the scripted boundary
// (newScriptedWtDlgRepo). These stay on real git because their subject IS git's
// observable behavior:
//
//   - TestDelegateIsolation_SpawnCreatesLockedManagedWorktree — the lane's .git
//     pointer file and a lock that really lands in git's registry
//   - TestDelegateIsolation_WorktreeReportDetectsAheadAndDirty — real ahead-count
//     and real dirty detection over committed work. The model derives dirtiness
//     from untracked files only — it cannot see a modified tracked file.
//   - TestDelegateIsolation_ManageWorktreeDeniedAfterRestoreAllTools — the lane
//     must SURVIVE the parent's close, which needs a real ancestry verdict: the
//     close pass keeps only a lane git judges unmerged, and the scripted model
//     refuses to answer `merge-base --is-ancestor` at all
//   - TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree
//     and TestDelegateIsolation_BackgroundCompletionNotificationCarriesWorktreeReport
//     — both read a worktree report's Ahead field, and the scripted model's
//     `rev-list --count` arm fails loudly (matching every other verdict git
//     alone can answer), so a real ahead=0 is required here too

// wtDlgRepo is a real git repo plus a parent session rooted at it, wired for
// delegate-isolation tests: a real StateDir (so isolation lanes land under
// <stateDir>/worktrees/<Project.ID>/<delegate_id>, mirroring manage_worktree's
// own layout — session_tools_worktree_create_test.go's wtRepo) and a client
// the caller scripts per test.
type wtDlgRepo struct {
	s        *Session
	mainRoot string
	stateDir string
}

func newWtDlgRepo(t *testing.T, c *llm.Client) *wtDlgRepo {
	t.Helper()

	root := packageFixtureTempDir(t, "delegate-repo-*")
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	copyWorktreeBaseRepo(t, root)

	stateDir, err := filepath.EvalSymlinks(packageFixtureTempDir(t, "delegate-state-*"))
	if err != nil {
		t.Fatalf("EvalSymlinks state: %v", err)
	}
	s := newSession(t, withClient(c), withDir(root), withConfig(SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	return &wtDlgRepo{s: s, mainRoot: root, stateDir: stateDir}
}

func (r *wtDlgRepo) metaDir(t *testing.T) string {
	return filepath.Join(r.stateDir, "worktrees", resolvedProjectID(t, r.s.currentEnv(), r.mainRoot), ".meta")
}

func (r *wtDlgRepo) lanePath(t *testing.T, delegateID string) string {
	return filepath.Join(r.stateDir, "worktrees", resolvedProjectID(t, r.s.currentEnv(), r.mainRoot), delegateID)
}

// commitWorkInLane makes a real commit in the lane so it is a CHANGED lane
// (commits beyond its base): the only state close-time disposal (spec §9 step 4)
// KEEPS. A revival test that closes the parent and expects the lane to survive
// must first give the lane genuine work — an unchanged lane is disposed
// (removed) at close, which is correct behavior, not a bug.
func (r *wtDlgRepo) commitWorkInLane(t *testing.T, lane string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(lane, "lane-work.txt"), []byte("delegate work\n"), 0o644); err != nil {
		t.Fatalf("write lane work: %v", err)
	}
	wtGit(t, lane, "add", "lane-work.txt")
	wtGit(t, lane, "commit", "-m", "delegate committed work")
}

// porcelainEntryFor finds the porcelain record for path in the main repo's
// worktree registry, failing the test if absent.
func (r *wtDlgRepo) porcelainEntryFor(t *testing.T, path string) worktree.PorcelainEntry {
	t.Helper()
	out := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	want := filepath.Clean(path)
	for _, e := range worktree.ParsePorcelain(out) {
		if filepath.Clean(e.Path) == want {
			return e
		}
	}
	t.Fatalf("no porcelain entry for %s in:\n%s", path, out)
	return worktree.PorcelainEntry{}
}

// scriptedWtDlgRepo is the scripted-boundary counterpart of wtDlgRepo: the same
// parent session, state dir and real on-disk sidecars and .git pointer files,
// with scriptedWorktreeGit standing in for the git binary. It exposes the
// wtDlgRepo methods unchanged through repo, plus the lane-lock reads a lock
// assertion needs.
type scriptedWtDlgRepo struct {
	*wtDlgRepo
	git *scriptedWorktreeGit
}

// newScriptedWtDlgRepo builds a parent session whose worktree git boundary is
// scripted. The tool registry stays full (not minimalWorktreeToolRegistry) so a
// spawned child really has the tools the isolation deny policy operates on.
func newScriptedWtDlgRepo(t *testing.T, c *llm.Client) *scriptedWtDlgRepo {
	t.Helper()
	root := scriptedCanonicalDir(t, packageFixtureTempDir(t, "delegate-scripted-repo-*"))
	stateDir := scriptedCanonicalDir(t, packageFixtureTempDir(t, "delegate-scripted-state-*"))
	if err := os.MkdirAll(filepath.Join(root, ".git", "worktrees"), 0o755); err != nil {
		t.Fatalf("create scripted main git dir: %v", err)
	}

	git := newScriptedWorktreeGit(root)
	s := newSession(t, withClient(c), withDir(root), withConfig(SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			environmentInfo:     scriptedEnvironmentInfo,
			worktreeGitRunner: func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
				return git.run
			},
		},
	}))
	s.mu.Lock()
	s.worktreeGitVersionOK = true
	s.mu.Unlock()
	return &scriptedWtDlgRepo{wtDlgRepo: &wtDlgRepo{s: s, mainRoot: root, stateDir: stateDir}, git: git}
}

func delegateTestClient(step func(req llm.Request) llm.Response) *llm.Client {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{step}})
	return c
}

// --- Spawn: managed worktree, sidecar, lock, child env rooting, restore descriptor ---

// REAL git: the lane's ".git" must really be a pointer file, and the atomic
// `worktree add --lock` must really have landed a lock in git's registry.
// REAL git: the lane's ".git" must really be a pointer file, and the atomic
// `worktree add --lock` must really have landed a lock in git's registry.
func TestDelegateIsolation_SpawnCreatesLockedManagedWorktree(t *testing.T) {
	t.Parallel()
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") })
	r := newWtDlgRepo(t, c)

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:           "do isolated work",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if res.DelegateID == "" {
		t.Fatal("createDelegate returned no delegate_id")
	}

	lane := r.lanePath(t, res.DelegateID)
	info, err := os.Stat(filepath.Join(lane, ".git"))
	if err != nil {
		t.Fatalf("stat lane .git pointer: %v", err)
	}
	if info.IsDir() {
		t.Error(".git in a linked worktree must be a pointer file, got a directory")
	}

	// Sidecar carries delegate_id and the parent's session id (spec §9 step 1).
	sc, err := worktree.ReadSidecar(r.metaDir(t), res.DelegateID)
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	if sc.DelegateID != res.DelegateID {
		t.Errorf("sidecar DelegateID = %q, want %q", sc.DelegateID, res.DelegateID)
	}
	if sc.CreatorSession != r.s.id {
		t.Errorf("sidecar CreatorSession = %q, want parent session id %q", sc.CreatorSession, r.s.id)
	}

	// Locked atomically at `git worktree add` time with the serf:dlg: marker.
	entry := r.porcelainEntryFor(t, lane)
	if !entry.Locked {
		t.Fatal("lane worktree is not locked")
	}
	wantReason := worktree.FormatDelegateMarker(res.DelegateID, r.s.id)
	if entry.LockReason != wantReason {
		t.Errorf("lock reason = %q, want %q", entry.LockReason, wantReason)
	}
	if _, err := r.s.worktreeSwitchByName(context.Background(), res.DelegateID); err == nil {
		t.Fatal("expected parent switch into a live isolated delegate lane to be refused")
	} else if !strings.Contains(err.Error(), wantReason) {
		t.Errorf("switch refusal error = %q, want it to name the delegate lock reason %q", err.Error(), wantReason)
	}

	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	sub := r.s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("no retained child runtime")
	}
	if got := sub.sess.currentEnv().WorkingDirectory(); got != lane {
		t.Errorf("child env working directory = %q, want lane %q", got, lane)
	}
	if sub.sess.reg.Get("manage_worktree") != nil {
		t.Error("manage_worktree must be denied to an isolation delegate child")
	}
	// A plain, non-worktree tool must still be present: the deny is specific.
	if sub.sess.reg.Get("shell") == nil {
		t.Error("shell must still be available to the child")
	}

	rec, err := findJobRecord(r.s.jobManager, res.StartedJobID)
	if err != nil {
		t.Fatalf("findJobRecord: %v", err)
	}
	if rec.DelegateRestore == nil {
		t.Fatal("job record missing delegate restore descriptor")
	}
	if rec.DelegateRestore.WorkingDir != lane {
		t.Errorf("restore descriptor WorkingDir = %q, want lane %q", rec.DelegateRestore.WorkingDir, lane)
	}
	if rec.DelegateRestore.Isolation != "worktree" {
		t.Errorf("restore descriptor Isolation = %q, want worktree", rec.DelegateRestore.Isolation)
	}
}

func TestDelegateIsolation_NonLocalEnvironmentErrorsClearly(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 1}))
	s.mu.Lock()
	s.env = &timeoutEnv{wd: s.env.WorkingDirectory()}
	s.mu.Unlock()

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "do isolated work",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 1000,
	})
	if res.Err == nil {
		t.Fatal("expected isolation:\"worktree\" to error on a non-local execution environment")
	}
	if !strings.Contains(res.Err.Error(), "local execution environment") {
		t.Errorf("error = %q, want it to mention a local execution environment", res.Err.Error())
	}
}

func TestDelegateIsolation_UnsupportedValueRejected(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 1}))

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:      "do work",
		Isolation: "container",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "invalid_request") {
		t.Fatalf("createDelegate isolation=%q err = %v, want invalid_request", "container", res.Err)
	}
}

func TestDelegateIsolation_TreeAtCapacityAfterWorktreeCreateRollsBackLane(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newScriptedWtDlgRepo(t, c)

	if r.s.treeCounter == nil {
		t.Fatal("root session has no tree counter; cannot saturate")
	}
	reserved := 0
	for r.s.treeCounter.reserve(slotKindJob) {
		reserved++
	}
	t.Cleanup(func() {
		for range reserved {
			r.s.treeCounter.releaseKind(slotKindJob)
		}
	})

	// Model preflight succeeds before the lane is created. Child preparation
	// then fails at the saturated tree counter, after lane creation, so this
	// continues to prove the post-worktree rollback path.
	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:      "do isolated work at capacity",
		Isolation: "worktree",
	})
	if !errors.Is(res.Err, errTreeAtCapacity) {
		t.Fatalf("createDelegate error = %v, want errTreeAtCapacity", res.Err)
	}
	if res.DelegateID == "" {
		t.Fatal("createDelegate did not report the delegate_id it minted before failing")
	}
	if res.JobID == "" {
		t.Fatal("createDelegate did not report the job_id bound before preparation")
	}
	requireNoOutputArtifacts(t, filepath.Join(r.s.jobManager.dir, "jobs", res.JobID+".log"))
	lane := r.lanePath(t, res.DelegateID)
	if _, err := os.Stat(lane); !os.IsNotExist(err) {
		t.Fatalf("stat lane after rollback = (%v), want it removed", err)
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t), res.DelegateID); err == nil {
		t.Fatal("expected the sidecar to be removed by rollback")
	}
}

// TestDelegateIsolation_AttachFailureAfterWorktreeCreateRollsBackLane covers
// createDelegate's SECOND rollback call site: unlike
// TestDelegateIsolation_TreeAtCapacityAfterWorktreeCreateRollsBackLane above
// (which fails at prepareSubagentRun, before a job record exists),
// this fails one step later — prepareSubagentRun SUCCEEDS (the isolation
// lane is created and the child env is rooted at it), but
// attachDelegateJobWithPreparedAndDelegate then fails because its output
// file cannot be created (the jobs dir is made read-only). The lane must
// still be rolled back.
func TestDelegateIsolation_AttachFailureAfterWorktreeCreateRollsBackLane(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permissions do not restrict writes")
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newScriptedWtDlgRepo(t, c)

	jm, err := sessionJobManager(r.s)
	if err != nil {
		t.Fatalf("sessionJobManager: %v", err)
	}
	jobsDir := filepath.Join(jm.dir, "jobs")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatalf("mkdir jobs dir: %v", err)
	}
	if err := os.Chmod(jobsDir, 0o555); err != nil {
		t.Fatalf("chmod jobs dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(jobsDir, 0o755) })

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:      "do isolated work",
		Isolation: "worktree",
	})
	if res.Err == nil {
		t.Fatal("expected createDelegate to fail when the jobs output dir is unwritable")
	}
	if res.DelegateID == "" {
		t.Fatal("createDelegate did not report the delegate_id it minted before failing")
	}
	// Restore write access so t.TempDir()'s cleanup (and the lane-removed
	// assertion below) can proceed.
	if err := os.Chmod(jobsDir, 0o755); err != nil {
		t.Fatalf("restore jobs dir permissions: %v", err)
	}
	lane := r.lanePath(t, res.DelegateID)
	if _, err := os.Stat(lane); !os.IsNotExist(err) {
		t.Fatalf("stat lane after rollback = (%v), want it removed", err)
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t), res.DelegateID); err == nil {
		t.Fatal("expected the sidecar to be removed by rollback")
	}
}

func TestAttachDelegateJobCollisionDoesNotOverwriteOrLeakArtifacts(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newScriptedWtDlgRepo(t, c)
	jm, err := sessionJobManager(r.s)
	if err != nil {
		t.Fatal(err)
	}

	jobID := "job_" + r.s.ID() + "_000000000000"
	jm.newJobID = func(string) (string, error) { return jobID, nil }
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	if err := os.WriteFile(outputPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:       "do isolated work",
		Isolation:  "worktree",
		Background: true,
	})
	if !errors.Is(res.Err, os.ErrExist) {
		t.Fatalf("createDelegate error = %v, want fs.ErrExist", res.Err)
	}
	if got, err := os.ReadFile(outputPath); err != nil || string(got) != "keep me\n" {
		t.Fatalf("occupied output = %q, err=%v", got, err)
	}
	for _, sidecar := range []string{
		outputPath + ".meta.json",
		outputPath + ".meta.json.tmp",
		outputPath + ".meta.json.pending",
		outputPath + ".meta.json.pending.tmp",
	} {
		if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("output sidecar %q leaked: %v", sidecar, err)
		}
	}
	if res.DelegateID == "" {
		t.Fatal("createDelegate did not report the delegate ID bound before preparation")
	}
	lane := r.lanePath(t, res.DelegateID)
	if _, err := os.Stat(lane); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared worktree remains after collision: %v", err)
	}
	if got := r.s.subagents.directSubagents(); len(got) != 0 {
		t.Fatalf("prepared subagent remains tracked after collision: %d", len(got))
	}
}

// --- manage_worktree deny: spawn, all-tools agent type, and after restore ---

// REAL git: the lane must SURVIVE the parent's close for the revival re-lock
// assertion to run at all, which needs a real ancestry verdict — the close pass
// keeps only a lane git judges unmerged, and commitWorkInLane makes it so.
func TestDelegateIsolation_ManageWorktreeDeniedAfterRestoreAllTools(t *testing.T) {
	t.Parallel()
	var request llm.Request
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
		func(req llm.Request) llm.Response {
			request = req
			return communicateWithDefaultOutput("resumed done")
		},
	}})
	r := newWtDlgRepo(t, c)
	r.s.pluginAgents["dlg_alltools"] = plugin.Agent{Name: "dlg_alltools", AllTools: true, PluginName: "test"}

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:           "do isolated work",
		AgentType:      "dlg_alltools",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	spawnedSub := r.s.subagents.get(childID)
	if spawnedSub == nil || spawnedSub.sess == nil {
		t.Fatal("no retained child runtime after spawn")
	}
	if spawnedSub.sess.reg.Get("manage_worktree") != nil {
		t.Error("manage_worktree must be denied even to an all-tools isolation delegate child at spawn")
	}
	// AllTools otherwise means everything: prove the base policy really was
	// all-tools, so the deny is doing real work rather than an accident of a
	// restrictive base policy.
	if spawnedSub.sess.reg.Get("shell") == nil {
		t.Error("all-tools child should still have shell at spawn")
	}

	// Give the lane genuine work so close-time disposal (spec §9 step 4) keeps
	// it — an unchanged lane is removed at close and cannot be revived.
	r.commitWorkInLane(t, r.lanePath(t, res.DelegateID))

	parentMeta := r.s.Meta()
	stateDir := r.s.stateDir
	r.s.Close()

	restoredParentEnv := execenv.NewLocalExecutionEnvironment(r.mainRoot)
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), restoredParentEnv, parentMeta, RestoreSessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restored child runtime before send = %+v, want none", sub)
	}

	sendRes := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         res.DelegateID,
		Message:        "keep going",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if sendRes.Err != nil {
		t.Fatalf("sendDelegateMessage: %v", sendRes.Err)
	}
	if !requestMessagesContain(request, "keep going") {
		t.Fatalf("resumed request did not carry the new message: %+v", request.Messages)
	}

	sub := restored.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("no reconstructed child runtime after restore")
	}
	if sub.sess.reg.Get("manage_worktree") != nil {
		t.Error("manage_worktree must stay denied to an all-tools isolation delegate child across restore")
	}
	if sub.sess.reg.Get("shell") == nil {
		t.Error("all-tools child should still have shell after restore")
	}

	entry := r.porcelainEntryFor(t, r.lanePath(t, res.DelegateID))
	wantReason := worktree.FormatDelegateMarker(res.DelegateID, restored.id)
	if !entry.Locked {
		t.Fatal("revival must re-lock a kept changed lane")
	}
	if entry.LockReason != wantReason {
		t.Errorf("lock reason after revival = %q, want %q", entry.LockReason, wantReason)
	}
}

// --- Second job in the same lane; per-job worktree report ---

// The subject here is that the SECOND job stays in the first job's lane and gets
// its own report, which is serf's own bookkeeping. Runs on real git (rather
// than the scripted boundary) because it reads the report's Ahead field, and
// TestDelegateIsolation_WorktreeReportDetectsAheadAndDirty is this file's sole
// authority for that value over committed work; the fresh-lane ahead=0 here is
// a genuine real-git answer (no commits are made in this test), not a re-proof.
func TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree(t *testing.T) {
	t.Parallel()
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") })
	r := newWtDlgRepo(t, c)

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:           "first job",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	lane := r.lanePath(t, res.DelegateID)
	if res.Worktree == nil {
		t.Fatal("first job result missing worktree report")
	}
	if res.Worktree.Path != lane {
		t.Errorf("first job worktree path = %q, want %q", res.Worktree.Path, lane)
	}
	if res.Worktree.Branch != res.DelegateID {
		t.Errorf("first job worktree branch = %q, want %q", res.Worktree.Branch, res.DelegateID)
	}
	if res.Worktree.Ahead != 0 || res.Worktree.Dirty {
		t.Errorf("first job worktree report = %+v, want a fresh unchanged lane (ahead=0, dirty=false)", res.Worktree)
	}

	sendRes := r.s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         res.DelegateID,
		Message:        "second job",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if sendRes.Err != nil {
		t.Fatalf("sendDelegateMessage: %v", sendRes.Err)
	}
	if sendRes.Worktree == nil {
		t.Fatal("second job result missing worktree report")
	}
	if sendRes.Worktree.Path != lane {
		t.Errorf("second job worktree path = %q, want the SAME lane %q (second job did not stay in the lane)", sendRes.Worktree.Path, lane)
	}

	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	sub := r.s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("no retained child runtime")
	}
	if got := sub.sess.currentEnv().WorkingDirectory(); got != lane {
		t.Errorf("child env after second job = %q, want the same lane %q", got, lane)
	}
}

// The DEFAULT delegate launch is fire-and-forget (delegateTool sets
// Background:true), so an isolated delegate's terminal result reaches the
// parent through the completion NOTIFICATION, not an inline tool response.
// Spec §9 step 3 requires that notification to carry path/branch/ahead/dirty
// so the parent can merge the lane between jobs.
//
// The subject is the notification BLOCK's shape — that the four fields are
// rendered into the model request at all. Runs on real git for the same reason
// as TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree:
// it reads the report's Ahead field, and the fresh-lane ahead=0 rendered here
// is a genuine real-git answer, not a re-proof of
// TestDelegateIsolation_WorktreeReportDetectsAheadAndDirty.
func TestDelegateIsolation_BackgroundCompletionNotificationCarriesWorktreeReport(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}}
	c := llm.NewClient()
	c.Register(adapter)
	r := newWtDlgRepo(t, c)

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:       "do isolated work",
		Isolation:  "worktree",
		Background: true, // the default mode delegateTool uses
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	lane := r.lanePath(t, res.DelegateID)
	head := strings.TrimSpace(wtGit(t, lane, "rev-parse", "HEAD"))

	// The child finishes asynchronously and arms the parent's completion
	// notification; drive the parent's notification turn so the block is
	// rendered into the model request.
	waitForJobNotification(t, r.s)
	if _, err := r.s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	for _, want := range []string{
		`worktree_path="` + lane + `"`,
		`worktree_branch="` + res.DelegateID + `"`,
		`worktree_head_sha="` + head + `"`,
		`worktree_ahead="0"`,
		`worktree_dirty="false"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("background completion notification missing %s:\n%s", want, text)
		}
	}
}

// isolatedDelegateWorktreeReport is exercised directly (not just through a
// scripted no-op turn) so ahead-count and dirty detection are proven against
// real git state, without needing a scripted tool call inside the fake LLM
// turn.
//
// REAL git: this is the file's authority for ahead-count and dirty detection.
// The scripted model refuses to answer `rev-list --count` at all (so the
// ahead=1 assertion would fail loudly there, not prove anything), and it
// derives dirtiness from untracked files only, so the dirty=true assertion on
// a modified tracked file would silently invert against it.
func TestDelegateIsolation_WorktreeReportDetectsAheadAndDirty(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	delegateID := "dlg_01ISOREPORTTESTLANE00001"
	lane, branch, baseSHA, _, _, err := r.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}

	// Clean, at base: ahead=0, dirty=false.
	desc := &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", WorkingDir: lane}
	got := r.s.isolatedDelegateWorktreeReport(desc)
	if got == nil {
		t.Fatal("report is nil on a fresh clean lane")
	}
	if got.Path != lane || got.Branch != branch || got.Ahead != 0 || got.Dirty || got.HeadSHA != baseSHA {
		t.Errorf("fresh lane report = %+v, want path=%s branch=%s head=%s ahead=0 dirty=false", got, lane, branch, baseSHA)
	}

	// One commit ahead, still clean.
	if err := os.WriteFile(filepath.Join(lane, "lane.txt"), []byte("lane work\n"), 0o644); err != nil {
		t.Fatalf("write lane file: %v", err)
	}
	wtGit(t, lane, "add", "lane.txt")
	wtGit(t, lane, "commit", "-m", "lane work")
	tip := strings.TrimSpace(wtGit(t, lane, "rev-parse", "HEAD"))
	var revListRange string
	r.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		next := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) >= 5 && args[2] == "rev-list" && args[3] == "--count" {
				revListRange = args[4]
			}
			return next(args...)
		}
	}
	got = r.s.isolatedDelegateWorktreeReport(desc)
	if got == nil || got.Ahead != 1 || got.Dirty || got.HeadSHA != tip {
		t.Errorf("one-commit-ahead report = %+v, want head=%s ahead=1 dirty=false", got, tip)
	}
	if want := baseSHA + ".." + tip; revListRange != want {
		t.Fatalf("ahead query range = %q, want captured HEAD range %q", revListRange, want)
	}
	if tip == baseSHA {
		t.Fatal("test setup: lane HEAD did not move past base")
	}

	// Now dirty on top.
	if err := os.WriteFile(filepath.Join(lane, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	got = r.s.isolatedDelegateWorktreeReport(desc)
	if got == nil || got.Ahead != 1 || !got.Dirty || got.HeadSHA != tip {
		t.Errorf("dirty report = %+v, want head=%s ahead=1 dirty=true", got, tip)
	}
}

// --- §7 revival re-lock: kept (unlocked) re-locks; foreign-locked refuses ---

func TestDelegateIsolation_RevivalOnForeignLockedLaneRefuses(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	r := newWtDlgRepo(t, c)

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:           "first job",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	lane := r.lanePath(t, res.DelegateID)
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}

	wtGit(t, r.mainRoot, "worktree", "unlock", lane)
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSWITCHEDIN000001")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, lane)

	parentMeta := r.s.Meta()
	stateDir := r.s.stateDir
	r.s.Close()

	restoredParentEnv := execenv.NewLocalExecutionEnvironment(r.mainRoot)
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), restoredParentEnv, parentMeta, RestoreSessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	sendRes := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         res.DelegateID,
		Message:        "revive",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if sendRes.Err == nil {
		t.Fatal("expected revival into a foreign-locked lane to refuse")
	}
	if !strings.Contains(sendRes.Err.Error(), foreignReason) {
		t.Errorf("error = %q, want it to name the foreign lock reason %q", sendRes.Err.Error(), foreignReason)
	}
	if restored.subagents.get(childID) != nil {
		t.Error("a refused revival must not leave a reconstructed child runtime behind")
	}

	// The foreign lock must be left untouched (no co-occupy).
	entry := r.porcelainEntryFor(t, lane)
	if !entry.Locked || entry.LockReason != foreignReason {
		t.Errorf("lane lock after refused revival = (locked=%v reason=%q), want the foreign lock untouched (%q)", entry.Locked, entry.LockReason, foreignReason)
	}
}

// --- §9 Guards: direct not-resumable error messages ---

// TestNotResumableSendError_WorkingDirMissingMessage covers
// notResumableSendError's notResumableWorkingDirMissing arm directly (the
// notResumableWorktreeDisposed arm and the default machine-readable-code
// fallback are already exercised via
// TestResumability_RefusesDisposedDelegate in session_worktree_close_test.go
// and the general delegate_send error-catalog tests respectively).
func TestNotResumableSendError_WorkingDirMissingMessage(t *testing.T) {
	err := notResumableSendError(notResumableWorkingDirMissing)
	if err == nil || !strings.Contains(err.Error(), "working directory no longer exists") {
		t.Fatalf("notResumableSendError(%q) = %v, want a clear working-directory-missing message", notResumableWorkingDirMissing, err)
	}
}

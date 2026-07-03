package agent

import (
	"context"
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

// These are REAL-git integration tests for delegate worktree isolation (native
// worktree tools spec §9, Task 21): delegate(isolation:"worktree") gets its
// own managed worktree lane, the child env is rooted there, manage_worktree is
// denied to the child (spawn AND restore, including all-tools agent types),
// and a revived kept lane re-takes its serf:dlg: lock.

// wtDlgRepo is a real git repo plus a parent session rooted at it, wired for
// delegate-isolation tests: a real StateDir (so isolation lanes land under
// <stateDir>/worktrees/<projectid>/<delegate_id>, mirroring manage_worktree's
// own layout — session_tools_worktree_create_test.go's wtRepo) and a client
// the caller scripts per test.
type wtDlgRepo struct {
	s        *Session
	mainRoot string
	stateDir string
}

func newWtDlgRepo(t *testing.T, c *llm.Client) *wtDlgRepo {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	wtGit(t, root, "init", "-b", "main")
	wtGit(t, root, "config", "user.email", "test@example.com")
	wtGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("main-checkout\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	wtGit(t, root, "add", "README")
	wtGit(t, root, "commit", "-m", "initial")

	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks state: %v", err)
	}
	s := newSession(t, withClient(c), withDir(root), withConfig(SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	}))
	return &wtDlgRepo{s: s, mainRoot: root, stateDir: stateDir}
}

func (r *wtDlgRepo) metaDir() string {
	return filepath.Join(r.stateDir, "worktrees", worktree.ProjectID(r.mainRoot), ".meta")
}

func (r *wtDlgRepo) lanePath(delegateID string) string {
	return filepath.Join(r.stateDir, "worktrees", worktree.ProjectID(r.mainRoot), delegateID)
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

func delegateTestClient(step func(req llm.Request) llm.Response) *llm.Client {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{step}})
	return c
}

// --- Spawn: managed worktree, sidecar, lock, child env rooting, restore descriptor ---

func TestDelegateIsolation_SpawnCreatesLockedManagedWorktree(t *testing.T) {
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

	lane := r.lanePath(res.DelegateID)
	info, err := os.Stat(filepath.Join(lane, ".git"))
	if err != nil {
		t.Fatalf("stat lane .git pointer: %v", err)
	}
	if info.IsDir() {
		t.Error(".git in a linked worktree must be a pointer file, got a directory")
	}

	// Sidecar carries delegate_id and the parent's session id (spec §9 step 1).
	sc, err := worktree.ReadSidecar(r.metaDir(), res.DelegateID)
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
}

func TestDelegateIsolation_ChildEnvRootedAtLaneAndRestoreDescriptorFields(t *testing.T) {
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
	lane := r.lanePath(res.DelegateID)

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

func TestDelegateIsolation_SpawnFailureAfterWorktreeCreateRollsBackLane(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	// prepareSubagentRun fails fast on an unknown agent_type, AFTER
	// createDelegate has already created the isolation lane (spec §9 step 1
	// happens before the child spawns) — the lane must not leak.
	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:      "do isolated work",
		AgentType: "no_such_agent_type_zzz",
		Isolation: "worktree",
	})
	if res.Err == nil {
		t.Fatal("expected createDelegate to fail on an unknown agent_type")
	}
	if res.DelegateID == "" {
		t.Fatal("createDelegate did not report the delegate_id it minted before failing")
	}
	lane := r.lanePath(res.DelegateID)
	if _, err := os.Stat(lane); !os.IsNotExist(err) {
		t.Fatalf("stat lane after rollback = (%v), want it removed", err)
	}
	if _, err := worktree.ReadSidecar(r.metaDir(), res.DelegateID); err == nil {
		t.Fatal("expected the sidecar to be removed by rollback")
	}
}

// --- manage_worktree deny: spawn, all-tools agent type, and after restore ---

func TestDelegateIsolation_ManageWorktreeDeniedAtSpawn(t *testing.T) {
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
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	sub := r.s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("no retained child runtime")
	}
	if sub.sess.reg.Get("manage_worktree") != nil {
		t.Error("manage_worktree must be denied to an isolation delegate child")
	}
	// A plain, non-worktree tool must still be present — the deny is specific.
	if sub.sess.reg.Get("shell") == nil {
		t.Error("shell must still be available to the child")
	}
}

func TestDelegateIsolation_ManageWorktreeDeniedForAllToolsAgentType(t *testing.T) {
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") })
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
	sub := r.s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("no retained child runtime")
	}
	if sub.sess.reg.Get("manage_worktree") != nil {
		t.Error("manage_worktree must be denied even to an all-tools isolation delegate child")
	}
	// AllTools otherwise means everything: prove the base policy really was
	// all-tools, so the deny is doing real work rather than an accident of a
	// restrictive base policy.
	if sub.sess.reg.Get("shell") == nil {
		t.Error("all-tools child should still have shell")
	}
}

func TestDelegateIsolation_ManageWorktreeDeniedAfterRestoreAllTools(t *testing.T) {
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

	// Give the lane genuine work so close-time disposal (spec §9 step 4) keeps
	// it — an unchanged lane is removed at close and cannot be revived.
	r.commitWorkInLane(t, r.lanePath(res.DelegateID))

	parentMeta := r.s.Meta()
	stateDir := r.s.stateDir
	r.s.Close()

	restoredParentEnv := execenv.NewLocalExecutionEnvironment(r.mainRoot)
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), restoredParentEnv, parentMeta, RestoreSessionConfig{
		StateDir: stateDir,
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
		OnIdle:         "start",
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
}

// --- Second job in the same lane; per-job worktree report ---

func TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree(t *testing.T) {
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
	lane := r.lanePath(res.DelegateID)
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
		OnIdle:         "start",
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
func TestDelegateIsolation_BackgroundCompletionNotificationCarriesWorktreeReport(t *testing.T) {
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
	lane := r.lanePath(res.DelegateID)

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
func TestDelegateIsolation_WorktreeReportDetectsAheadAndDirty(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	delegateID := "dlg_01ISOREPORTTESTLANE00001"
	lane, branch, baseSHA, _, err := r.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}

	// Clean, at base: ahead=0, dirty=false.
	desc := &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", WorkingDir: lane}
	got := r.s.isolatedDelegateWorktreeReport(desc)
	if got == nil {
		t.Fatal("report is nil on a fresh clean lane")
	}
	if got.Path != lane || got.Branch != branch || got.Ahead != 0 || got.Dirty {
		t.Errorf("fresh lane report = %+v, want path=%s branch=%s ahead=0 dirty=false", got, lane, branch)
	}

	// One commit ahead, still clean.
	if err := os.WriteFile(filepath.Join(lane, "lane.txt"), []byte("lane work\n"), 0o644); err != nil {
		t.Fatalf("write lane file: %v", err)
	}
	wtGit(t, lane, "add", "lane.txt")
	wtGit(t, lane, "commit", "-m", "lane work")
	got = r.s.isolatedDelegateWorktreeReport(desc)
	if got == nil || got.Ahead != 1 || got.Dirty {
		t.Errorf("one-commit-ahead report = %+v, want ahead=1 dirty=false", got)
	}
	tip := strings.TrimSpace(wtGit(t, lane, "rev-parse", "HEAD"))
	if tip == baseSHA {
		t.Fatal("test setup: lane HEAD did not move past base")
	}

	// Now dirty on top.
	if err := os.WriteFile(filepath.Join(lane, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	got = r.s.isolatedDelegateWorktreeReport(desc)
	if got == nil || got.Ahead != 1 || !got.Dirty {
		t.Errorf("dirty report = %+v, want ahead=1 dirty=true", got)
	}
}

// --- §7 revival re-lock: kept (unlocked) re-locks; foreign-locked refuses ---

func TestDelegateIsolation_RevivalOnKeptUnlockedLaneReLocks(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("revived") },
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
	lane := r.lanePath(res.DelegateID)
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}

	// A CHANGED lane (real commits) is what close-time disposal (spec §9
	// step 4) keeps — it unlocks and preserves it, leaving it resumable. Give
	// the lane genuine work, then close the parent: disposal keeps + unlocks it,
	// reaching exactly the kept-unlocked state a revival must re-lock.
	r.commitWorkInLane(t, lane)

	parentMeta := r.s.Meta()
	stateDir := r.s.stateDir
	r.s.Close()

	// Disposal kept the changed lane unlocked.
	if r.porcelainEntryFor(t, lane).Locked {
		t.Fatal("disposal must leave a kept changed lane unlocked")
	}

	restoredParentEnv := execenv.NewLocalExecutionEnvironment(r.mainRoot)
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), restoredParentEnv, parentMeta, RestoreSessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	sendRes := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         res.DelegateID,
		Message:        "revive",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if sendRes.Err != nil {
		t.Fatalf("sendDelegateMessage on a kept unlocked lane: %v", sendRes.Err)
	}
	if restored.subagents.get(childID) == nil {
		t.Fatal("no reconstructed child runtime after revival")
	}

	entry := r.porcelainEntryFor(t, lane)
	if !entry.Locked {
		t.Fatal("revival must re-lock the lane")
	}
	wantReason := worktree.FormatDelegateMarker(res.DelegateID, restored.id)
	if entry.LockReason != wantReason {
		t.Errorf("lock reason after revival = %q, want %q", entry.LockReason, wantReason)
	}
}

func TestDelegateIsolation_RevivalOnForeignLockedLaneRefuses(t *testing.T) {
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
	lane := r.lanePath(res.DelegateID)
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
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	sendRes := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         res.DelegateID,
		Message:        "revive",
		OnIdle:         "start",
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

// --- §4 step 2 / §9 Guards: parent switch into a live isolated lane is refused ---

func TestDelegateIsolation_ParentSwitchIntoLiveLaneRefused(t *testing.T) {
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

	_, err := r.s.worktreeSwitchByName(context.Background(), res.DelegateID)
	if err == nil {
		t.Fatal("expected switch into a live isolated delegate's lane to be refused")
	}
	wantReason := worktree.FormatDelegateMarker(res.DelegateID, r.s.id)
	if !strings.Contains(err.Error(), wantReason) {
		t.Errorf("error = %q, want it to name the delegate lock reason %q", err.Error(), wantReason)
	}
}

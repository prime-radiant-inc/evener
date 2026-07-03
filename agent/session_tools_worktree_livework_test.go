package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// Task 20: real liveWorkUnder plumbing (spec §5 remove step 4, §7
// liveWorkUnder). These tests exercise the production scan directly — real
// background shell jobs, a real live subagent env, and a synthetic-but-real
// jm.running delegate record — never the worktreeLiveWorkStub test seam
// (session_tools_worktree_remove_test.go's stub tests cover the guard CALL
// site only). They reuse wtRepo/newWorktreeRepo from
// session_tools_worktree_create_test.go.

func TestPathEqualOrUnder(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		target    string
		want      bool
	}{
		{"equal", "/repo/wt/lane", "/repo/wt/lane", true},
		{"nested one level", "/repo/wt/lane/sub", "/repo/wt/lane", true},
		{"nested deep", "/repo/wt/lane/a/b/c", "/repo/wt/lane", true},
		{"sibling", "/repo/wt/lane2", "/repo/wt/lane", false},
		{"prefix-collision sibling", "/repo/wt/lane-other", "/repo/wt/lane", false},
		{"parent", "/repo/wt", "/repo/wt/lane", false},
		{"unrelated", "/other/place", "/repo/wt/lane", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathEqualOrUnder(tt.candidate, tt.target); got != tt.want {
				t.Errorf("pathEqualOrUnder(%q, %q) = %v, want %v", tt.candidate, tt.target, got, tt.want)
			}
		})
	}
}

// --- real background shell job ---

// TestWorktreeRemove_LiveWorkGuardRefusesRealBackgroundShellJob covers the
// brief's core failing-test list: a background shell job launched with its
// working dir rooted exactly at the worktree records that launch workdir,
// liveWorkUnder finds it, and remove refuses even after the session has
// since switched elsewhere (spec §5 remove step 4: "A child may have been
// started with a working dir under a worktree while the parent has already
// switched elsewhere").
func TestWorktreeRemove_LiveWorkGuardRefusesRealBackgroundShellJob(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	// Launch the background shell job exactly the way the registered shell
	// tool does (session_tools_shell.go): WorkingDir comes from the executing
	// env, not the model. The session is currently inside the lane, so this
	// is the "equal to path" case.
	env := r.s.currentEnv()
	se, ok := env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("session env does not support streaming")
	}
	shellRes := runShell(context.Background(), r.s.jobManager, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
		WorkingDir: env.WorkingDirectory(),
	})
	if shellRes.JobID == "" || !shellRes.RunningInBackground {
		t.Fatalf("shell result = %+v, want a running background job", shellRes)
	}

	// Switch elsewhere: the parent leaves the lane, but the background job's
	// recorded launch workdir still points at it.
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	if _, err := r.removeOp(t, map[string]any{"name": "lane", "force": true}); err == nil {
		t.Fatal("expected remove to be refused by the live shell job")
	} else if !strings.Contains(err.Error(), shellRes.JobID) {
		t.Errorf("error should surface the live job id, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the live shell job: %v", statErr)
	}

	// Stop the job; once it has left jm.running, remove proceeds.
	if _, err := r.s.jobManager.stop(shellRes.JobID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitForShellDone(t, r.s.jobManager, shellRes.JobID)
	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove after the shell job stopped: %v", err)
	}
}

// --- real live subagent env ---

// TestWorktreeRemove_LiveWorkGuardRefusesLiveSubagentEnv covers the brief's
// "subagent envs enumerated too": a live subagent (no delegate job record —
// plain subagents mint none, subagents.go) rooted under (not at) the
// worktree still refuses removal, and it does so via the child's CURRENT
// env, not a launch-time snapshot.
func TestWorktreeRemove_LiveWorkGuardRefusesLiveSubagentEnv(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// sub.id mirrors production (subagents.go: "id: subSess.id") — liveWorkUnder
	// reports the live CHILD SESSION's id (child.id), the identifier that
	// actually appears in job records and tool-visible ids, not an arbitrary
	// subagent-local label.
	child := newSession(t, withDir(nested))
	sub := &subagent{id: child.id, sess: child}
	r.s.subagents.track(sub)

	if _, err := r.removeOp(t, map[string]any{"name": "lane", "force": true}); err == nil {
		t.Fatal("expected remove to be refused by the live subagent env")
	} else if !strings.Contains(err.Error(), child.id) {
		t.Errorf("error should surface the live subagent id %q, got: %v", child.id, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the live subagent: %v", statErr)
	}

	r.s.subagents.remove(child.id)
	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove after untracking the subagent: %v", err)
	}
}

// --- real delegate job record ---

// TestWorktreeRemove_LiveWorkGuardRefusesLiveDelegateJobRecord covers the
// brief's "delegate WorkingDir ... enumerated": a running delegate job
// record (DelegateRestore.WorkingDir), inserted directly into jm.running the
// way session_jobtree_drain_test.go's synthetic entries are (real recorded
// state, not the worktreeLiveWorkStub seam), refuses removal even with no
// subagent tracked for it — the job-record source is independent of the
// subagent-env source.
func TestWorktreeRemove_LiveWorkGuardRefusesLiveDelegateJobRecord(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	jm := r.s.jobManager
	jm.mu.Lock()
	jm.running["job_dlg1"] = &runningJob{rec: &jobstore.JobRecord{
		JobID:           "job_dlg1",
		Type:            jobstore.JobDelegate,
		Status:          jobstore.StatusRunning,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{WorkingDir: nested},
	}}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, "job_dlg1")
		jm.mu.Unlock()
	})

	if _, err := r.removeOp(t, map[string]any{"name": "lane", "force": true}); err == nil {
		t.Fatal("expected remove to be refused by the live delegate job record")
	} else if !strings.Contains(err.Error(), "job_dlg1") {
		t.Errorf("error should surface the live delegate job id, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the live delegate job record: %v", statErr)
	}

	jm.mu.Lock()
	delete(jm.running, "job_dlg1")
	jm.mu.Unlock()
	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove after the delegate job record cleared: %v", err)
	}
}

// --- negative: unrelated live work must not false-positive ---

// TestWorktreeRemove_LiveWorkGuardIgnoresUnrelatedWork checks a live shell
// job rooted in a SIBLING worktree lane (a different worktree under the same
// projectid), plus a delegate record with no recorded working dir at all,
// neither block removing the target lane.
func TestWorktreeRemove_LiveWorkGuardIgnoresUnrelatedWork(t *testing.T) {
	r := newWorktreeRepo(t)
	target, err := r.create(t, map[string]any{"name": "target-lane"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	targetPath := target["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	sibling, err := r.create(t, map[string]any{"name": "sibling-lane"})
	if err != nil {
		t.Fatalf("create sibling: %v", err)
	}
	siblingPath := sibling["path"].(string)

	env := r.s.currentEnv()
	se, ok := env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("session env does not support streaming")
	}
	shellRes := runShell(context.Background(), r.s.jobManager, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
		WorkingDir: env.WorkingDirectory(),
	})
	if shellRes.JobID == "" || !shellRes.RunningInBackground {
		t.Fatalf("shell result = %+v, want a running background job", shellRes)
	}
	t.Cleanup(func() {
		_, _ = r.s.jobManager.stop(shellRes.JobID)
	})
	if siblingPath != env.WorkingDirectory() {
		t.Fatalf("sibling lane path mismatch: %q vs %q", siblingPath, env.WorkingDirectory())
	}

	jm := r.s.jobManager
	jm.mu.Lock()
	jm.running["job_dlg_nowd"] = &runningJob{rec: &jobstore.JobRecord{
		JobID:           "job_dlg_nowd",
		Type:            jobstore.JobDelegate,
		Status:          jobstore.StatusRunning,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{},
	}}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, "job_dlg_nowd")
		jm.mu.Unlock()
	})

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit sibling: %v", err)
	}
	if _, err := r.removeOp(t, map[string]any{"name": "target-lane"}); err != nil {
		t.Fatalf("remove target-lane refused by unrelated live work: %v", err)
	}
	if _, statErr := os.Stat(targetPath); statErr == nil {
		t.Errorf("target-lane still present after a clean remove")
	}
}

// TestWorktreeLiveWorkUnder_SkipsSubagentEmptyWorkingDirectory covers
// liveWorkUnder's defensive skip for a tracked subagent whose env reports an
// empty WorkingDirectory() — a state a real LocalExecutionEnvironment never
// produces (RootDir is always non-empty), but reachable via any other
// ExecutionEnvironment implementation, so the scan must not treat "" as a
// (false) match against every target path via pathEqualOrUnder's "under root"
// arithmetic. A SECOND, genuinely live subagent rooted under the target lane
// is tracked alongside it to prove the skip doesn't swallow real live work.
//
// The sibling branch (a tracked subagent whose env is nil) is not exercised
// here: forcing that state by directly nilling a live *Session's env field
// breaks the session's OTHER invariants (session_lifecycle.go's close path
// dereferences the env unconditionally and panics), so it is not a state a
// real *Session can safely reach — see the doc comment on that branch in
// liveWorkUnder itself.
func TestWorktreeLiveWorkUnder_SkipsSubagentEmptyWorkingDirectory(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	emptyWDChild := newSession(t, withDir(r.mainRoot))
	emptyWDChild.mu.Lock()
	emptyWDChild.env = &timeoutEnv{wd: ""}
	emptyWDChild.mu.Unlock()
	r.s.subagents.track(&subagent{id: emptyWDChild.id, sess: emptyWDChild})
	t.Cleanup(func() { r.s.subagents.remove(emptyWDChild.id) })
	// Restore a real env before the session's own Close() cleanup runs.
	t.Cleanup(func() {
		emptyWDChild.mu.Lock()
		emptyWDChild.env = execenv.NewLocalExecutionEnvironment(r.mainRoot)
		emptyWDChild.mu.Unlock()
	})

	liveChild := newSession(t, withDir(nested))
	r.s.subagents.track(&subagent{id: liveChild.id, sess: liveChild})
	t.Cleanup(func() { r.s.subagents.remove(liveChild.id) })

	live := r.s.liveWorkUnder(path)
	found := false
	for _, l := range live {
		if strings.Contains(l, liveChild.id) {
			found = true
		}
		if strings.Contains(l, emptyWDChild.id) {
			t.Errorf("liveWorkUnder reported the empty-WorkingDirectory subagent: %v", live)
		}
	}
	if !found {
		t.Errorf("liveWorkUnder = %v, want it to still find the genuinely live subagent %s", live, liveChild.id)
	}
}

// --- real prune plumbing (not the stub) ---

// TestWorktreePrune_Sweep1_SkipsLiveRealBackgroundShellJob mirrors
// TestWorktreePrune_Sweep1_SkipsLiveViaStub but drives a real background
// shell job through the same liveWorkUnder scan remove uses, confirming
// prune's call site (session_tools_worktree.go's sweep 1) sees the real
// plumbing too, not just the stub seam.
func TestWorktreePrune_Sweep1_SkipsLiveRealBackgroundShellJob(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "live-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	env := r.s.currentEnv()
	se, ok := env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("session env does not support streaming")
	}
	shellRes := runShell(context.Background(), r.s.jobManager, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
		WorkingDir: env.WorkingDirectory(),
	})
	if shellRes.JobID == "" || !shellRes.RunningInBackground {
		t.Fatalf("shell result = %+v, want a running background job", shellRes)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "live-lane")
	if e == nil {
		t.Fatal("live-lane not reported skipped")
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, shellRes.JobID) {
		t.Errorf("reason = %q, want it to surface the live job id", reason)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("live-work worktree removed by prune: %v", statErr)
	}

	if _, err := r.s.jobManager.stop(shellRes.JobID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitForShellDone(t, r.s.jobManager, shellRes.JobID)

	out2, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune after the shell job stopped: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out2, "removed"), "live-lane") == nil {
		t.Fatal("live-lane not collected once the live shell job stopped")
	}
}

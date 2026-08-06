package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// These are the delegate-lane disposal end-to-end scenarios (auto-delegate-lane-
// disposal spec §Testing "E2E"). They tie the whole feature together at the
// session level through the real surfaces — createDelegate, the registered
// manage_worktree op=dispose tool, full Session.Close(), the armed P3 open-pass
// timer (driven by a fake clock, never wall-clock), a real cross-session repo
// share, and sendDelegateMessage refusals — rather than any single mechanic in
// isolation. The lower-level mechanics each have their own unit coverage; these
// prove the pieces compose into the three flows the spec calls out.

// disposeViaTool drives op=dispose through the registered manage_worktree tool
// surface (the exact path a model's tool call takes), returning the structured
// result map.
func disposeViaTool(t *testing.T, s *Session, id string, force, forceDirty bool) map[string]any {
	t.Helper()
	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("session is missing manage_worktree")
	}
	out, err := rt.Exec(t.Context(), s.currentEnv(), map[string]any{
		"operation":   "dispose",
		"id":          id,
		"force":       force,
		"force_dirty": forceDirty,
	})
	if err != nil {
		t.Fatalf("manage_worktree op=dispose: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("dispose result is %T, want map[string]any", out)
	}
	return m
}

// restoreParent reopens a top-level session from meta against the same state dir
// and repo root — the "resume session A" step of the (b) and (c) scenarios.
func restoreParent(t *testing.T, meta schema.SessionMeta, stateDir, launchDir string) *Session {
	t.Helper()
	if meta.ProfileID == "" {
		meta.ProfileID = "openai"
	}
	if meta.Model == "" {
		meta.Model = "gpt-5.2"
	}
	meta.Config.NoProjectPrompts = true
	sess, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(launchDir), meta,
		RestoreSessionConfig{StateDir: stateDir, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// ffMergeLane fast-forwards the repo's main branch to the delegate lane's tip so
// the lane's commits become ancestry-reachable from refs/heads/main (a real
// merge, the D0-auto collectible arm).
func ffMergeLane(t *testing.T, mainRoot, delegateID string) {
	t.Helper()
	wtGit(t, mainRoot, "merge", "--ff-only", delegateID)
}

// TestE2E_ScriptedDisposeFlow is spec §Testing E2E (a): a session spawns an
// isolated delegate through the real createDelegate path; the completion result
// carries the §P2 DisposalHint; the delegate's branch is merged; a model-tier
// manage_worktree op=dispose tool call collects lane+branch+sidecar; and a later
// delegate_send hits the disposed refusal.
func TestE2E_ScriptedDisposeFlow(t *testing.T) {
	t.Parallel()
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("delegate done") })
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
	id := res.DelegateID
	lane := r.lanePath(t, id)

	// (1) The completion result carries the spec §P2 disposal nudge naming this
	// delegate's own dispose invocation.
	if res.Worktree == nil {
		t.Fatal("completion result carried no worktree report")
	}
	if want := "op=dispose id=" + id; !strings.Contains(res.Worktree.DisposalHint, want) {
		t.Fatalf("completion DisposalHint = %q, want it to contain %q", res.Worktree.DisposalHint, want)
	}

	// (2) The delegate's work is committed and merged into main.
	laneCommit(t, lane)
	ffMergeLane(t, r.mainRoot, id)

	// (3) The model disposes via the real tool surface: lane, branch, and sidecar
	// are all collected and the delegate is marked disposed.
	out := disposeViaTool(t, r.s, id, false, false)
	if out["status"] != "disposed" {
		t.Fatalf("dispose status = %v, want disposed", out["status"])
	}
	if laneWorktreePresent(lane) {
		t.Error("lane still present after op=dispose")
	}
	if branchExistsAt(t, r.mainRoot, id) {
		t.Error("lane branch survived op=dispose")
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t), id); err == nil {
		t.Error("lane sidecar survived op=dispose")
	}
	if !disposedRawStoreMentions(t, r.s, id) {
		t.Error("delegate not marked disposed after op=dispose")
	}

	// (4) delegate_send afterward hits the disposed refusal.
	send := r.s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         id,
		Message:        "more work",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if send.Err == nil || !containsAll(send.Err.Error(), "disposed", "start a new delegate") {
		t.Fatalf("post-dispose send = %+v, want disposed refusal", send)
	}
}

// TestE2E_CloseCollectsMergedLaneThenResumeDisposed is spec §Testing E2E (b): a
// session closes with an ancestry-merged owned lane; P0 collects it at close and
// records the disposed mark in its OWN store; resuming that session and sending
// to the delegate hits the disposed refusal (own-store mark).
func TestE2E_CloseCollectsMergedLaneThenResumeDisposed(t *testing.T) {
	t.Parallel()
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("delegate done") })
	r := newWtDlgRepo(t, c)

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task:           "isolated work",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	id := res.DelegateID
	lane := r.lanePath(t, id)

	// Commit and merge the lane so it is ancestry-merged (P0-collectible at close).
	laneCommit(t, lane)
	ffMergeLane(t, r.mainRoot, id)

	meta := r.s.Meta()

	// Full session close runs P0 close-time disposal.
	r.s.Close()

	if laneWorktreePresent(lane) {
		t.Error("merged lane not collected by session close (P0)")
	}
	if !disposedRawStoreMentions(t, r.s, id) {
		t.Error("disposed mark absent in the closing session's own store")
	}

	// Resume the session and send to the delegate → disposed refusal.
	resumed := restoreParent(t, meta, r.stateDir, r.mainRoot)
	send := resumed.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         id,
		Message:        "resume please",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if send.Err == nil || !containsAll(send.Err.Error(), "disposed", "start a new delegate") {
		t.Fatalf("resume send = %+v, want disposed refusal", send)
	}
}

// TestE2E_KeptLaneSweptByForeignSessionThenResumeStatNet is spec §Testing E2E
// (c): session A closes KEEPING an unmerged lane; the branch is merged
// afterward; a foreign top-level session B opens on the SAME project dir and, at
// its armed open-pass timer (driven by a fake clock, injected tiny grace),
// collects the now-merged residue. Because B does not own A's job record, no
// disposed mark is written — so resuming A and sending to the delegate hits the
// WorkingDir-stat crash net, NOT the disposed refusal.
//
// Not parallel: it overrides the package-var laneGrace (parallel tests are
// paused while non-parallel tests run, so the shared var is safe to mutate).
func TestE2E_KeptLaneSweptByForeignSessionThenResumeStatNet(t *testing.T) {
	savedGrace := laneGrace
	laneGrace = time.Millisecond
	defer func() { laneGrace = savedGrace }()

	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("delegate done") })
	a := newWtDlgRepo(t, c)

	res := a.s.createDelegate(context.Background(), delegateArgs{
		Task:           "isolated work",
		Isolation:      "worktree",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	id := res.DelegateID
	lane := a.lanePath(t, id)

	// The lane has committed but UNMERGED work: A's close keeps it (unlocked,
	// resumable), it is not disposed.
	laneCommit(t, lane)

	metaA := a.s.Meta()
	a.s.Close()

	if !laneWorktreePresent(lane) {
		t.Fatal("A's close wrongly collected the unmerged lane; it must be KEPT")
	}
	if disposedRawStoreMentions(t, a.s, id) {
		t.Fatal("A's close wrongly marked the kept lane disposed")
	}

	// The branch is merged after A closes, turning the kept lane into collectible
	// residue.
	ffMergeLane(t, a.mainRoot, id)
	// Age the sidecar past the injected tiny grace so B's sweep will collect it.
	ageSidecar(t, a.metaDir(t), id, time.Second)

	// A foreign top-level session B opens on the SAME repo dir + state dir, with a
	// fake clock so its armed open-pass P3 timer fires on virtual time only.
	clk := agenttest.NewFakeClock()
	b := newSession(t, withDir(a.mainRoot), withConfig(SessionConfig{
		StateDir:         a.stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		clock:            clk,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	t.Cleanup(func() { b.Close() })

	b.mu.Lock()
	armed := b.laneSweepTimer != nil
	b.mu.Unlock()
	if !armed {
		t.Fatal("foreign top-level session B did not arm the P3 open-pass timer")
	}

	// Cross the open-pass delay on the fake clock: B's timer collects the residue.
	// The sidecar is deleted last in the collection sequence (worktree remove →
	// branch delete → sidecar delete), so waiting on its absence proves the whole
	// lane was reclaimed without racing the still-running sweep.
	clk.Advance(laneSweepDelay + time.Second)
	waitForCondition(t, 3*time.Second, "B's open-pass sweep collects the foreign residue lane", func() bool {
		_, err := worktree.ReadSidecar(a.metaDir(t), id)
		return err != nil
	})
	if laneWorktreePresent(lane) {
		t.Error("lane worktree survived B's residue sweep")
	}
	if branchExistsAt(t, a.mainRoot, id) {
		t.Error("lane branch survived B's residue sweep")
	}
	// B collected a FOREIGN lane, so it wrote no disposed mark to A's record.
	if disposedRawStoreMentions(t, a.s, id) {
		t.Error("foreign sweep wrote a disposed mark for a lane it does not own")
	}

	// Resume A and send to the delegate → the WorkingDir crash net refuses (the
	// mark is foreign, so this is NOT the disposed reason).
	resumed := restoreParent(t, metaA, a.stateDir, a.mainRoot)
	send := resumed.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         id,
		Message:        "resume please",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if send.Err == nil {
		t.Fatalf("resume send unexpectedly succeeded: %+v", send)
	}
	if !strings.Contains(send.Err.Error(), "working directory no longer exists") {
		t.Fatalf("resume send = %v, want the WorkingDir-stat refusal", send.Err)
	}
	if strings.Contains(send.Err.Error(), "disposed") {
		t.Fatalf("resume send = %v, must NOT be the disposed reason (foreign mark)", send.Err)
	}
}

// branchExistsAt reports whether a local branch named `name` exists in the repo
// at mainRoot.
func branchExistsAt(t *testing.T, mainRoot, name string) bool {
	t.Helper()
	out := wtGit(t, mainRoot, "branch", "--list", name)
	return len(strings.TrimSpace(out)) > 0
}

// disposedRawStoreMentions reports whether the delegate id carries a durable
// Disposed mark in the session's own jobs.jsonl.
func disposedRawStoreMentions(t *testing.T, s *Session, delegateID string) bool {
	t.Helper()
	store, err := jobstore.OpenNoSync(filepath.Join(s.jobManager.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	for _, rec := range recs {
		if rec.DelegateID == delegateID && rec.Disposed {
			return true
		}
	}
	return false
}

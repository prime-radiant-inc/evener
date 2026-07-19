//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/sandbox"
	taskpkg "primeradiant.com/serf/agent/task"
)

// FuzzSubagentsSeed100Exact replays the deterministic subagent contract cases
// through a fuzz target so the tagged fuzz union owns the orchestration surface.
// The byte is only a stable case selector; no case uses network, host processes,
// provider credentials, or wall-clock sleeps.
func FuzzSubagentsSeed100Exact(f *testing.F) {
	cases := []func(*testing.T){
		TestSubagentFollowUpProvenanceUnionsLaunchActiveAndCompleted,
		TestSubagentRunSnapshotsFinalProvenance,
		TestResultSnapshot_CurrentShape,
		TestResultSnapshot_CarriesAgentIDAndStatus,
		TestCancelAgent_RunningChildBecomesCancelledAndResumable,
		TestCancelAgent_GenuineFailureRacingCancelStaysFailed,
		TestCancelAgent_NotRunning,
		TestSubagentTimestamps_ResetOnResume,
		TestSubagentCannotCallRootOnlyControlTools,
		TestWatchParentChildGetsJobWatchButNotDelegate,
		TestWatchParentGrantIsNotInheritedByGrandchild,
		TestPrepareSubagentRunAllowsRecursionWithAllowance,
		TestPrepareSubagentRunRejectsZeroAllowance,
		TestBaseSubagentPolicyAllowsDelegateWithAllowance,
		TestGrantRejectionAllowanceTruthful,
		TestGrantToolsCannotRegrantAskUser,
		TestGrantToolsAskUserAliasNeverSilentlyGranted,
		TestStopGatingNoResurrection,
		TestDriveWakeDuringInflightDriveReDrives,
		TestJobWatchParentSourceInstallsOnParentWithChildReceiver,
		TestJobWatchParentSourceReceiverScopedClearLeavesOtherReceivers,
		TestW3Init_PrepareSubagentRun_ChildSessionError,
		TestW3Init_PrepareSubagentRun_SkillResolveSkipped,
		TestW3Init_RunSubagentStopHook_BlockedWithContext,
		TestW3Init_RunSubagentStopHook_BlockedDefaultReason,
		TestS5Cov_CloneMaps,
		TestS5Cov_LocalEnvPolicyName,
		TestS5Cov_LocalEnvPolicyFromName,
		TestS5Cov_FrozenSubagentToolNames,
		TestS5Cov_RestoreFrozenSkillBodies,
		TestS5Cov_SubagentNeedsCommunicateNudge,
		seed100SubagentExactEdges,
	}
	for i := range cases {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		t.Run("case", cases[int(selector)%len(cases)])
	})
}

func seed100SubagentExactEdges(t *testing.T) {
	t.Run("helpers", func(t *testing.T) {
		if got := removeStrings([]string{"keep", "drop"}, []string{"drop"}); len(got) != 1 || got[0] != "keep" {
			t.Fatalf("removeStrings = %v", got)
		}
		if got := removeRootOnlySubagentTools(append([]string{"keep"}, rootOnlySubagentTools()...)); len(got) != 1 || got[0] != "keep" {
			t.Fatalf("removeRootOnlySubagentTools = %v", got)
		}
		in := map[string]any{"unmarshalable": func() {}}
		if got := cloneMap(in); got["unmarshalable"] == nil {
			t.Fatal("cloneMap lost shallow fallback value")
		}
		if got := communicateNudge("communicate"); got == "" {
			t.Fatal("communicateNudge returned empty text")
		}
	})

	t.Run("nil-and-closed-guards", func(t *testing.T) {
		s := newSession(t)
		if _, err := s.installParentSourceWatchForChild("", "", watchArgs{}); err == nil {
			t.Fatal("empty watch observer should fail")
		}
		installed, err := s.installParentSourceWatchForChild("observer", "delegate", watchArgs{})
		if err != nil || installed.WatchID == "" {
			t.Fatalf("install parent watch = %+v, %v", installed, err)
		}
		if _, err := s.clearParentSourceWatchForChild("", "", "watch"); err == nil {
			t.Fatal("empty clear observer should fail")
		}
		if _, err := s.clearParentSourceWatchForChild("observer", "delegate", installed.WatchID); err != nil {
			t.Fatalf("clear parent watch: %v", err)
		}
		savedJM := s.jobManager
		s.jobManager = nil
		if _, err := s.installParentSourceWatchForChild("observer", "delegate", watchArgs{}); err == nil {
			t.Fatal("watch without job manager should fail")
		}
		if _, err := s.clearParentSourceWatchForChild("observer", "delegate", "watch"); err == nil {
			t.Fatal("clear without job manager should fail")
		}
		s.jobManager = savedJM
		if err := s.trackAndLaunchPreparedSubagent(nil); err == nil {
			t.Fatal("nil prepared run should fail")
		}
		if _, err := s.sendInput(context.Background(), "missing", "input"); err == nil {
			t.Fatal("unknown subagent should fail")
		}
		if _, err := s.cancelAgent("missing"); err == nil {
			t.Fatal("unknown cancellation should fail")
		}
		if s.driveSubagentNotificationTurn(nil) {
			t.Fatal("nil drive unexpectedly launched")
		}
		_ = (&subagent{}).followUpProvenance(&provenance.Causal{})
		if got, err := (&subagent{}).runSubagentStopHook(context.Background(), "done", errors.New("x"), nil); got != "done" || err == nil {
			t.Fatalf("nil hook guard = %q, %v", got, err)
		}

		prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		if err := s.trackAndLaunchPreparedSubagent(prepared); err == nil {
			t.Fatal("closed session launch should fail")
		}
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	})

	t.Run("parent-activity", func(t *testing.T) {
		var jobID, phase string
		s := newSession(t)
		s.cfg.spawn.parentJobID = "job"
		s.cfg.spawn.parentJobActivity = func(gotJobID, gotPhase string) {
			jobID, phase = gotJobID, gotPhase
		}
		s.noteParentJobActivity("working")
		if jobID != "job" || phase != "working" {
			t.Fatalf("activity callback = %q, %q", jobID, phase)
		}
	})

	t.Run("steer-running-and-driving", func(t *testing.T) {
		s := newSession(t)
		child := newSession(t)
		sub := &subagent{sess: child, running: true}
		if launched, err := s.startOrSteerSubagentRun(sub, "running"); err != nil || launched {
			t.Fatalf("running steer = %v, %v", launched, err)
		}
		sub.mu.Lock()
		sub.running = false
		sub.driving = true
		sub.mu.Unlock()
		if launched, err := s.startOrSteerSubagentRun(sub, "driving"); err != nil || launched {
			t.Fatalf("driving steer = %v, %v", launched, err)
		}
		child.Close()
	})

	t.Run("closed-resume", func(t *testing.T) {
		s := newSession(t)
		child := newSession(t)
		sub := &subagent{id: "idle", sess: child}
		s.subagents.track(sub)
		s.Close()
		if launched, err := s.startOrSteerSubagentRun(sub, "resume"); err == nil || launched {
			t.Fatalf("closed resume = %v, %v", launched, err)
		}
		// Close drains the manager; re-register the idle record to isolate
		// sendInput's error-propagation branch from its unknown-id guard.
		s.subagents.track(sub)
		if _, err := s.sendInput(context.Background(), sub.id, "resume"); err == nil {
			t.Fatal("sendInput should propagate the closed-session error")
		}
		child.Close()
	})

	t.Run("cancel-timeout", func(t *testing.T) {
		clk := agenttest.NewFakeClock()
		s := newSession(t, withConfig(SessionConfig{clock: clk}))
		sub := &subagent{id: "waiting", sess: newSession(t), running: true, done: make(chan struct{})}
		s.subagents.track(sub)
		done := make(chan error, 1)
		// The session arms its own one-shot P3 lane-residue sweep timer
		// (laneSweepDelay, 10m) on this clock at open, so BlockUntil must wait
		// for cancelAgent's After(5s) waiter ON TOP of the waiters the session
		// already owns — a bare BlockUntil(1) is satisfied by the sweep timer
		// alone and Advance can then run before cancelAgent ever parks.
		baseline := clk.BlockedCount()
		go func() {
			_, err := s.cancelAgent(sub.id)
			done <- err
		}()
		clk.BlockUntil(baseline + 1)
		clk.Advance(5 * time.Second)
		if err := <-done; err == nil {
			t.Fatal("cancel timeout should fail")
		}
		sub.sess.Close()
	})

	t.Run("result-error-fallback", func(t *testing.T) {
		s := newSession(t)
		a := &subagent{id: "a", sess: s, status: SubagentFailed, err: errors.New("failure"), startedAt: time.Unix(1, 0)}
		if got := a.resultSnapshotLocked(); got.Output != "failure" || got.Success {
			t.Fatalf("result snapshot = %+v", got)
		}
	})

	t.Run("stop-hook-unblocked-error", func(t *testing.T) {
		a := w3init_stopHookSession(t, `{"decision":"allow"}`)
		want := errors.New("original")
		if got, err := a.runSubagentStopHook(context.Background(), "result", want, nil); got != "result" || !errors.Is(err, want) {
			t.Fatalf("unblocked hook = %q, %v", got, err)
		}
	})

	t.Run("watch-entry-run", func(t *testing.T) {
		child := newTestSession(t)
		sub := &subagent{
			id:           child.ID(),
			sess:         child,
			done:         make(chan struct{}),
			running:      true,
			runFromWatch: true,
		}
		sub.run(context.Background(), "watch delivery", nil)
		if sub.running {
			t.Fatal("watch-entry run did not finalize")
		}
	})

	t.Run("prepare-fault-boundaries", seed100SubagentPrepareFaults)

	t.Run("spawn-close-race", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentAfterPrepare = func(s *Session) { s.Close() }
		s := newSession(t, withConfig(cfg))
		if _, err := s.spawnAgent(context.Background(), "task", "", "", 0, "", "", nil, nil); err == nil {
			t.Fatal("spawn should reject a parent closed after prepare")
		}
	})

	t.Run("drive-stop-gate", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentStopGated = func(*Session, string) (bool, bool) { return true, true }
		parent := newSession(t, withConfig(cfg))
		child := newTestSession(t)
		sub := &subagent{id: child.ID(), sess: child}
		if !parent.driveSubagentNotificationTurn(sub) {
			t.Fatal("drive did not launch")
		}
		parent.sendersWG.Wait()
	})

}

func seed100SubagentPrepareFaults(t *testing.T) {
	prepare := func(t *testing.T, point string, mutate func(*Session, *context.Context)) (*preparedSubagentRun, error) {
		t.Helper()
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentPrepareFault = func(got string) error {
			if got == point {
				return fmt.Errorf("injected %s", point)
			}
			return nil
		}
		s := newSession(t, withConfig(cfg))
		ctx := context.Background()
		if mutate != nil {
			mutate(s, &ctx)
		}
		return s.prepareSubagentRun(ctx, "task", "", t.TempDir(), 0, "", "", nil, nil)
	}

	failing := []struct {
		point  string
		mutate func(*Session, *context.Context)
	}{
		{"working_dir_env", nil},
		{"sandbox_reroot", nil},
		{"sandbox_env", func(_ *Session, ctx *context.Context) {
			*ctx = context.WithValue(*ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{})
		}},
		{"sandbox_resolve", func(_ *Session, ctx *context.Context) {
			*ctx = context.WithValue(*ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{})
		}},
		{"sandbox_enable", func(_ *Session, ctx *context.Context) {
			*ctx = context.WithValue(*ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{})
		}},
		{"new_session", func(_ *Session, ctx *context.Context) {
			*ctx = context.WithValue(*ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{})
		}},
	}
	for _, tc := range failing {
		t.Run(tc.point, func(t *testing.T) {
			prepared, err := prepare(t, tc.point, tc.mutate)
			if err == nil || prepared != nil {
				t.Fatalf("prepare fault %q = %#v, %v", tc.point, prepared, err)
			}
		})
	}

	t.Run("skill-resolve", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentPrepareFault = func(point string) error {
			if point == "skill_resolve" {
				return errors.New("injected skill read")
			}
			return nil
		}
		s := newSession(t, withConfig(cfg))
		s.pluginAgents["fault-skill"] = plugin.Agent{Name: "fault-skill", Skills: []string{"missing"}}
		prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "fault-skill", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		safzCleanupPrepared(prepared)
	})

	t.Run("task-populate-warning", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentPrepareFault = func(point string) error {
			if point == "task_populate" {
				return errors.New("injected task write")
			}
			return nil
		}
		s := newSession(t, withConfig(cfg))
		s.pluginAgents["fault-task"] = plugin.Agent{Name: "fault-task", Tasks: []taskpkg.TaskTemplate{{Title: "one", Prompt: "do one"}}}
		prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "fault-task", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		safzCleanupPrepared(prepared)
	})

	t.Run("retention-error", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentReserveSlot = func(*Session) ([]*subagent, error) { return nil, errors.New("full") }
		s := newSession(t, withConfig(cfg))
		if prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "", "", nil, nil); err == nil || prepared != nil {
			t.Fatalf("retention fault = %#v, %v", prepared, err)
		}
	})

	t.Run("retention-eviction", func(t *testing.T) {
		evictedSession := newSession(t)
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentReserveSlot = func(*Session) ([]*subagent, error) {
			return []*subagent{{id: evictedSession.ID(), sess: evictedSession}}, nil
		}
		s := newSession(t, withConfig(cfg))
		prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		safzCleanupPrepared(prepared)
	})

	t.Run("tree-capacity", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1}
		cfg.testOnly.subagentReserveTreeSlot = func(*Session) (*treeReservation, bool) { return nil, false }
		s := newSession(t, withConfig(cfg))
		if prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "", "", nil, nil); !errors.Is(err, errTreeAtCapacity) || prepared != nil {
			t.Fatalf("tree capacity = %#v, %v", prepared, err)
		}
	})

	t.Run("shared-isolated-sandbox-success", func(t *testing.T) {
		cfg := SessionConfig{MaxSubagentDepth: 1, ShareTasksWithChildren: true}
		s := newSession(t, withConfig(cfg))
		ctx := context.WithValue(context.Background(), ctxIsolation, "worktree")
		ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{})
		prepared, err := s.prepareSubagentRun(ctx, "task", "", "", 0, "", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.isolation != "worktree" {
			t.Fatalf("isolation = %q", prepared.isolation)
		}
		safzCleanupPrepared(prepared)
	})
}

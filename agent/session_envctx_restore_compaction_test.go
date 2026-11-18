package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/llm"
)

func TestRestoreEnvironmentReconcilesCompactionWithStaleMetadata(t *testing.T) {
	t.Run("checkout in the launch prompt", testCompactedLaunchBranch)
	t.Run("inconclusive checkout after fold", testCompactedGitObservation)
	t.Run("branch retained from a removed worktree", func(t *testing.T) {
		const launchBranch = "launch-unborn"
		const linkedBranch = "linked-update"
		root, linked := gittest.NewLinkedWorktree(t)
		root, linked = evalSyms(t, root), evalSyms(t, linked)
		gittest.Run(t, root, "checkout", "--orphan", launchBranch)
		s := newScriptedSummaryCompactSession(t, "fold-worktree", func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nSaved work summary\n[END SUMMARY]")}
		}, withDir(root), withStateDir(t.TempDir()), withEnvironmentSnapshot())
		prompt := s.transcript.Header().SystemPrompt
		if err := s.enterWorktree(linked, false); err != nil {
			t.Fatal(err)
		}
		if err := s.maybeAppendEnvironmentContext(); err != nil {
			t.Fatal(err)
		}
		seedNumberedSessionHistory(t, s, 8)
		gittest.Run(t, linked, "checkout", "-b", linkedBranch)
		if err := s.maybeAppendEnvironmentContext(); err != nil {
			t.Fatal(err)
		}
		changedID := s.history[len(s.history)-1].StableTurnID
		s.contextMgr.PreserveRecentTurns = 2
		if err := s.Compact(t.Context()); err != nil {
			t.Fatal(err)
		}
		if retained := historyEnvironmentTurnIDs(s); !slices.Equal(retained, []string{changedID}) {
			t.Fatalf("fold retained environment turns %v, want only branch update %s", retained, changedID)
		}
		for _, turn := range s.history {
			if turn.StableTurnID == changedID {
				var update envctx.Tracker
				update.ReplayBlock(turn.Message.Text())
				if got := update.State().Last; got.Cwd != linked || got.GitBranch != linkedBranch {
					t.Errorf("retained update identifies cwd %q and branch %q, want %q on %q", got.Cwd, got.GitBranch, linked, linkedBranch)
				}
			}
		}
		s.Close()
		meta := loadMetaForTest(t, s)
		if meta.WorktreePath != linked || meta.WorktreeRestoreRoot != root || meta.PromptLaunch.GitBranch != nil {
			t.Fatalf("persisted checkout %q, restore root %q, launch branch %v", meta.WorktreePath, meta.WorktreeRestoreRoot, meta.PromptLaunch.GitBranch)
		}
		gittest.Run(t, root, "worktree", "remove", linked)
		restored, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(root), meta, RestoreSessionConfig{StateDir: s.stateDir})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restored.Close)
		adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("ack") }}
		restored.client.Register(adapter)
		for round, want := range []string{"", launchBranch} {
			if round == 1 {
				gittest.Run(t, root, "commit", "--allow-empty", "-m", "first")
			}
			enqueueCompletedJobNotification(t, restored, fmt.Sprintf("job_checkout_%d", round))
			if _, err := restored.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			req := adapter.Requests()[round]
			state, _ := requestEnvironmentState(req)
			if state.Last.Cwd != root || state.Last.GitBranch != want || state.GitBranchUnknown != (round == 0) || req.Messages[0].Text() != prompt {
				t.Errorf("restored request %d saw cwd %q and branch %q, unknown %t, original prompt %t", round, state.Last.Cwd, state.Last.GitBranch, state.GitBranchUnknown, req.Messages[0].Text() == prompt)
			}
		}
	})
	for _, retained := range []string{"none", "full", "diff"} {
		t.Run(retained, func(t *testing.T) {
			now := envctxFixedTime
			branch, pressure := "work", "memory pressure: warn level"
			probes := &envctx.Probes{
				Now:       func() time.Time { return now },
				GitBranch: func(string) (string, bool) { return branch, true },
				Memory:    func() string { return pressure },
			}
			dir := t.TempDir()
			compactStarted, releaseCompact := make(chan struct{}), make(chan struct{})
			var releaseOnce, startedOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseCompact) }) }
			t.Cleanup(release)
			s := newScriptedSummaryCompactSession(t, "env-restore-fold", func(llm.Request) llm.Response {
				if retained == "diff" {
					startedOnce.Do(func() { close(compactStarted) })
					<-releaseCompact
				}
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nSaved work summary\n[END SUMMARY]")}
			}, withDir(dir), withConfig(SessionConfig{
				StateDir: dir,
				testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, envProbes: probes},
			}))
			if err := s.maybeAppendEnvironmentContext(); err != nil {
				t.Fatal(err)
			}
			stale := *loadMetaForTest(t, s).EnvContext
			seedNumberedSessionHistory(t, s, 8)
			s.contextMgr.PreserveRecentTurns = 1
			if retained == "diff" {
				// The old full context is in the folded prefix. A changed block
				// appended while summarization runs survives in the merged tail.
				done := make(chan error, 1)
				go func() { done <- s.Compact(context.Background()) }()
				select {
				case <-compactStarted:
				case err := <-done:
					t.Fatalf("compaction ended before summary boundary: %v", err)
				}
				now = now.Add(time.Hour)
				if err := s.maybeAppendEnvironmentContext(); err != nil {
					release()
					<-done
					t.Fatal(err)
				}
				release()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.Compact(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if retained == "full" {
				// The new full snapshot omits the now nominal optional fields.
				// Stale metadata still contains their old warning and branch.
				now = now.Add(time.Hour)
				branch, pressure = "", ""
				if err := s.maybeAppendEnvironmentContext(); err != nil {
					t.Fatal(err)
				}
			}
			before := countEnvironmentTurns(s)
			wantBefore := 1
			if retained == "none" {
				wantBefore = 0
			}
			if before != wantBefore {
				t.Fatalf("compaction retained %d environment turns, want %d", before, wantBefore)
			}
			stateDir, cwd := s.stateDir, s.currentEnv().WorkingDirectory()
			s.Close()
			if retained == "none" {
				cwd = t.TempDir()
			}
			meta := loadMetaForTest(t, s)
			meta.EnvContext = &stale
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
			client := llm.NewClient()
			client.Register(newFakeAdapter("openai", repeatFinalResponse(1, "resumed")...))
			restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(cwd), meta, RestoreSessionConfig{
				StateDir: stateDir,
				testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, envProbes: probes},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if got, want := restored.envTracker.State().HasSent, retained == "full"; got != want {
				t.Fatalf("restored complete environment = %v, want %v for retained %s", got, want, retained)
			}
			if retained == "none" {
				_, _, _, req, _, _, err := restored.prepareModelRequestWithError(t.Context(), 1, &events.RoundTimings{})
				if err != nil {
					t.Fatal(err)
				}
				var projected envctx.Tracker
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleUser {
						projected.ReplayBlock(msg.Text())
					}
				}
				if projected.State().Last.Cwd != cwd || req.Messages[0].Text() != s.cachedSystemPrompt {
					t.Errorf("first resumed request reports cwd %q, want %q with the original prompt", projected.State().Last.Cwd, cwd)
				}
			}
			sendOneUserInput(t, restored, "resume")
			wantCount := before
			if retained != "full" {
				wantCount++
			}
			if got := countEnvironmentTurns(restored); got != wantCount {
				t.Fatalf("resumed environment turns = %d, want %d", got, wantCount)
			}
			want := envctx.NewCollector(*probes).Collect(envctx.Inputs{Cwd: cwd, Sandbox: restored.cfg.Sandbox})
			want.ShellNetwork, want.DetachedShell = meta.PromptLaunch.ShellNetwork, meta.PromptLaunch.DetachedShell
			if got := restored.envTracker.State(); !got.HasSent || got.Last != want {
				t.Fatalf("restored environment state = %+v, want complete %+v", got, want)
			}
		})
	}
}

func testCompactedGitObservation(t *testing.T) {
	const launch, feature, recovery, installed = "launch-79", "feature-41", "recovered-23", "installed-17"
	for _, tc := range []struct {
		name      string
		known     bool
		reinstall bool
		probes    []int
		seen      []string
	}{
		{"confirmed admission", true, false, []int{1, 0, 0}, []string{feature, feature, feature}},
		{"inconclusive admission", false, false, []int{1, 1, 1}, []string{launch, launch, recovery}},
		{"workspace changed after admission", false, true, []int{2, 0, 0}, []string{installed, installed, installed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := evalSyms(t, t.TempDir())
			base := execenv.NewLocalExecutionEnvironment(cwd)
			requestCwd := cwd
			if tc.reinstall {
				requestCwd = evalSyms(t, t.TempDir())
			}
			branch, known := launch, true
			var probes int
			s := newScriptedSummaryCompactSession(t, "admission-fold", func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nWork retained\n[END SUMMARY]")}
			}, withEnv(base), withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{envProbes: &envctx.Probes{
				Now: func() time.Time { return envctxFixedTime },
				GitBranch: func(dir string) (string, bool) {
					probes++
					if dir != cwd {
						return installed, true
					}
					if !known {
						return "", false
					}
					return branch, true
				},
			}}}))
			prompt := s.cachedSystemPrompt
			if err := s.maybeAppendEnvironmentContext(); err != nil {
				t.Fatal(err)
			}
			forceRealRoundSummaryFold(t, s, 0.5, 0.85)
			if tc.reinstall {
				var once sync.Once
				updateSessionTestConfig(s, func(cfg *testConfig) {
					cfg.afterFoldSideEffectsFlush = func() {
						once.Do(func() {
							if err := s.swapEnvAndRefresh(base.WithWorkingDirectory(requestCwd), nil); err != nil {
								t.Errorf("install next workspace: %v", err)
							}
						})
					}
				})
			}
			probes, branch, known = 0, feature, tc.known
			var perRequest []int
			var seen []string
			s.client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
				perRequest = append(perRequest, probes)
				probes = 0
				snapshot, blocks := requestEnvironment(req)
				seen = append(seen, snapshot.GitBranch)
				if blocks == 0 || snapshot.Cwd != requestCwd || req.Messages[0].Text() != prompt {
					t.Errorf("request %d has %d environment updates at %q or changed its prompt", len(seen), blocks, snapshot.Cwd)
				}
				if len(seen) == 1 {
					if !slices.ContainsFunc(currentHistory(t, s), func(turn schema.Turn) bool { return turn.Kind == schema.TurnSummary }) {
						t.Error("the first provider request arrived without a published summary fold")
					}
				}
				if len(seen) == 2 {
					branch, known = recovery, true
				}
				if len(seen) < 3 {
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: fmt.Sprintf("tasks-%d", len(seen)), Name: "task_list", Arguments: []byte(`{}`)})
				}
				return agenttest.FinalResponse("done")
			}})
			sendOneUserInput(t, s, "continue after folding")
			if !slices.Equal(perRequest, tc.probes) || !slices.Equal(seen, tc.seen) {
				t.Errorf("Git probes per request = %v, branches = %v; want %v and %v", perRequest, seen, tc.probes, tc.seen)
			}
		})
	}
	for _, mode := range []string{"warm", "cold", "cleared then cold", "return to launch then cold", "restore at launch then cold"} {
		t.Run(mode, func(t *testing.T) {
			cwd, stateDir := evalSyms(t, t.TempDir()), t.TempDir()
			copyWorktreeBaseRepo(t, cwd)
			gittest.Run(t, cwd, "checkout", "-b", launch)
			s := newScriptedSummaryCompactSession(t, "env-fold-observation", func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nWork retained\n[END SUMMARY]")}
			}, withDir(cwd), withStateDir(stateDir), withEnvironmentSnapshot())
			prompt := s.cachedSystemPrompt
			gittest.Run(t, cwd, "checkout", "-b", feature)
			want := feature
			gitDir, parked := filepath.Join(cwd, ".git"), filepath.Join(t.TempDir(), "repo")
			if mode == "cleared then cold" {
				want = ""
				if err := os.Rename(gitDir, parked); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.maybeAppendEnvironmentContext(); err != nil {
				t.Fatal(err)
			}
			seedNumberedSessionHistory(t, s, 8)
			s.contextMgr.PreserveRecentTurns = 1
			if err := s.Compact(t.Context()); err != nil || countEnvironmentTurns(s) != 0 {
				t.Fatalf("fold retained %d environment turns, err %v", countEnvironmentTurns(s), err)
			}
			if want == "" {
				if err := os.Rename(parked, gitDir); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "return to launch then cold" {
				want = launch
				gittest.Run(t, cwd, "checkout", launch)
				failure := errors.New("transcript sync unavailable")
				attachEnvironmentSyncFailure(t, s, failure, nil)
				if err := s.maybeAppendEnvironmentContext(); !errors.Is(err, failure) || countEnvironmentTurns(s) != 0 {
					t.Fatalf("failed branch publication retained %d turns, err %v", countEnvironmentTurns(s), err)
				}
				for range 2 {
					if err := s.maybeAppendEnvironmentContext(); err != nil {
						t.Fatal(err)
					}
				}
				if count := countEnvironmentTurns(s); count != 1 {
					t.Fatalf("two confirmations after rollback recorded %d environment turns, want one", count)
				}
			}
			restore := func() {
				t.Helper()
				s.Close()
				restored, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(cwd), loadMetaForTest(t, s), RestoreSessionConfig{StateDir: stateDir})
				if err != nil {
					t.Fatal(err)
				}
				s = restored
				t.Cleanup(s.Close)
			}
			adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("ack") }}
			s.client.Register(adapter)
			notify := func(id string) llm.Request {
				t.Helper()
				before := len(adapter.Requests())
				enqueueCompletedJobNotification(t, s, id)
				if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				return adapter.Requests()[before]
			}
			if mode == "restore at launch then cold" {
				want = launch
				gittest.Run(t, cwd, "checkout", launch)
				restore()
				req := notify("job_restored_launch")
				if snapshot, turns := requestEnvironment(req); turns != 1 || snapshot.GitBranch != launch || req.Messages[0].Text() != prompt {
					t.Errorf("first restore reported %d updates on %q, original prompt %t", turns, snapshot.GitBranch, req.Messages[0].Text() == prompt)
				}
			}
			gittest.Run(t, cwd, "checkout", "--orphan", recovery)
			if mode != "warm" {
				restore()
			}
			for round, branch := range []string{want, recovery, recovery} {
				if round == 1 {
					gittest.Run(t, cwd, "commit", "--allow-empty", "-m", "first")
				}
				req := notify(fmt.Sprintf("job_folded_checkout_%d", round))
				model := envctx.NewTracker(envctx.State{Last: envctx.Snapshot{GitBranch: launch}})
				for _, message := range req.Messages {
					if message.Role == llm.RoleUser {
						model.ReplayBlock(message.Text())
					}
				}
				if got := model.State().Last.GitBranch; got != branch || req.Messages[0].Text() != prompt {
					t.Errorf("request %d presents branch %q, want %q, prompt identical %t", round, got, branch, req.Messages[0].Text() == prompt)
				}
				if turns := countEnvironmentTurns(s); turns != min(round+1, 2) {
					t.Errorf("request %d has %d environment updates", round, turns)
				}
			}
		})
	}
}

func testCompactedLaunchBranch(t *testing.T) {
	const branch = "launch-branch-19"
	type notification struct {
		restore, leaveRepo bool
		wantBranch         string
		wantTurns          int
	}
	coldOutside := notification{restore: true, wantTurns: 1}
	for _, tc := range []struct {
		name               string
		unborn, unrecorded bool
		notifications      []notification
	}{
		{"warm", false, false, []notification{{wantTurns: 1}, coldOutside}},
		{"cold", false, false, []notification{coldOutside, coldOutside}},
		{"cold unrecorded", false, true, []notification{coldOutside, coldOutside}},
		{"cold unborn", true, false, []notification{{restore: true, leaveRepo: true, wantTurns: 1}, coldOutside}},
		{"cold unrecorded unborn", true, true, []notification{
			{restore: true, wantBranch: branch, wantTurns: 1},
			{wantBranch: branch, wantTurns: 1},
			{leaveRepo: true, wantTurns: 2},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, stateDir := evalSyms(t, t.TempDir()), t.TempDir()
			copyWorktreeBaseRepo(t, cwd)
			gittest.Run(t, cwd, "checkout", "-b", branch)
			s := newScriptedSummaryCompactSession(t, "env-fold-git", func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nSaved work summary\n[END SUMMARY]")}
			}, withDir(cwd), withStateDir(stateDir), withEnvironmentSnapshot())
			prompt := s.transcript.Header().SystemPrompt
			if !strings.Contains(prompt, branch) {
				t.Fatal("launch branch missing from provider prompt")
			}
			if err := s.maybeAppendEnvironmentContext(); err != nil {
				t.Fatal(err)
			}
			seedNumberedSessionHistory(t, s, 8)
			s.contextMgr.PreserveRecentTurns = 1
			if err := s.Compact(t.Context()); err != nil {
				t.Fatal(err)
			}
			if turns := countEnvironmentTurns(s); turns != 0 {
				t.Fatalf("fold retained %d environment turns", turns)
			}
			leaveRepo := func() {
				t.Helper()
				if err := os.Rename(filepath.Join(cwd, ".git"), filepath.Join(t.TempDir(), "repo")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unborn {
				gittest.Run(t, cwd, "checkout", "--orphan", "unborn")
			} else {
				leaveRepo()
			}
			adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("ack") }}
			s.client.Register(adapter)
			for round, wake := range tc.notifications {
				if wake.restore {
					s.Close()
					meta := loadMetaForTest(t, s)
					if tc.unrecorded && round == 0 {
						meta.PromptLaunch.GitBranch = nil
						if err := schema.SaveSessionMetaForNewPrompt(nil, stateDir, meta); err != nil {
							t.Fatal(err)
						}
					}
					restored, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(cwd), meta, RestoreSessionConfig{StateDir: stateDir})
					if err != nil {
						t.Fatal(err)
					}
					s = restored
					t.Cleanup(s.Close)
				}
				if wake.leaveRepo {
					leaveRepo()
				}
				enqueueCompletedJobNotification(t, s, fmt.Sprintf("job_launch_branch_%d", round))
				if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				req := adapter.Requests()[round]
				model := envctx.NewTracker(envctx.State{Last: envctx.Snapshot{Cwd: cwd, GitBranch: branch}})
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleUser {
						model.ReplayBlock(msg.Text())
					}
				}
				state := model.State()
				if req.Messages[0].Text() != prompt || state.Last.GitBranch != wake.wantBranch || state.GitBranchUnknown || countEnvironmentTurns(s) != wake.wantTurns {
					t.Errorf("round %d presents branch %q (unknown %t), want %q, environment turns %d, want %d, prompt identical %t", round, state.Last.GitBranch, state.GitBranchUnknown, wake.wantBranch, countEnvironmentTurns(s), wake.wantTurns, req.Messages[0].Text() == prompt)
				}
			}
		})
	}
}

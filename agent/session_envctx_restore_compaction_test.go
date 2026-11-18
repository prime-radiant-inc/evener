package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/llm"
)

func TestRestoreEnvironmentReconcilesCompactionWithStaleMetadata(t *testing.T) {
	t.Run("checkout in the launch prompt", testCompactedLaunchBranch)
	t.Run("inconclusive checkout after fold", testCompactedGitObservation)
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
			if got := restored.envTracker.State(); !got.HasSent || got.Last != want {
				t.Fatalf("restored environment state = %+v, want complete %+v", got, want)
			}
		})
	}
}

func testCompactedGitObservation(t *testing.T) {
	const launch, feature, recovery = "launch-79", "feature-41", "recovered-23"
	for _, mode := range []string{"warm", "cold", "cleared then cold"} {
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
			gittest.Run(t, cwd, "checkout", "--orphan", recovery)
			if mode != "warm" {
				s.Close()
				restored, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(cwd), loadMetaForTest(t, s), RestoreSessionConfig{StateDir: stateDir})
				if err != nil {
					t.Fatal(err)
				}
				s = restored
				t.Cleanup(s.Close)
			}
			for round, branch := range []string{want, recovery, recovery} {
				if round == 1 {
					gittest.Run(t, cwd, "commit", "--allow-empty", "-m", "first")
				}
				_, _, _, req, _, _, err := s.prepareModelRequestWithError(t.Context(), round, &events.RoundTimings{})
				if err != nil {
					t.Fatal(err)
				}
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
	for _, mode := range []string{"warm", "cold", "cold unrecorded", "cold unborn"} {
		t.Run(mode, func(t *testing.T) {
			const branch = "launch-branch-19"
			cwd, stateDir := evalSyms(t, t.TempDir()), t.TempDir()
			copyWorktreeBaseRepo(t, cwd)
			gittest.Run(t, cwd, "checkout", "-b", branch)
			s := newScriptedSummaryCompactSession(t, "env-fold-git", func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nSaved work summary\n[END SUMMARY]")}
			}, withDir(cwd), withStateDir(stateDir), withEnvironmentSnapshot())
			prompt := s.cachedSystemPrompt
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
			if mode == "cold unborn" {
				gittest.Run(t, cwd, "checkout", "--orphan", "unborn")
			} else {
				leaveRepo()
			}
			for round := range 2 {
				if mode != "warm" || round > 0 {
					s.Close()
					meta := loadMetaForTest(t, s)
					if mode == "cold unrecorded" && round == 0 {
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
				if mode == "cold unborn" && round == 0 {
					leaveRepo()
				}
				_, _, _, req, _, _, err := s.prepareModelRequestWithError(t.Context(), 1, &events.RoundTimings{})
				if err != nil {
					t.Fatal(err)
				}
				model := envctx.NewTracker(envctx.State{Last: envctx.Snapshot{GitBranch: branch}})
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleUser {
						model.ReplayBlock(msg.Text())
					}
				}
				if req.Messages[0].Text() != prompt || model.State().Last.GitBranch != "" || countEnvironmentTurns(s) != 1 {
					t.Errorf("round %d retained branch %q after leaving the worktree, environment turns %d, prompt identical %t", round, model.State().Last.GitBranch, countEnvironmentTurns(s), req.Messages[0].Text() == prompt)
				}
			}
		})
	}
}

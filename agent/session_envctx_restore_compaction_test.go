package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestRestoreEnvironmentReconcilesCompactionWithStaleMetadata(t *testing.T) {
	for _, retained := range []string{"none", "full", "diff"} {
		t.Run(retained, func(t *testing.T) {
			now := envctxFixedTime
			branch, pressure := "work", "memory pressure: warn level"
			probes := &envctx.Probes{
				Now:       func() time.Time { return now },
				GitBranch: func(string) string { return branch },
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
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSaved work summary\n[END SUMMARY]")}
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
			meta := loadMetaForTest(t, s)
			meta.EnvContext = &stale
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(1, "resumed")})
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

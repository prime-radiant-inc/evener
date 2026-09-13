package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestRestoreEnvironmentWithNoRetainedEntriesReemitsFullBlock(t *testing.T) {
	for _, transcriptState := range []string{"missing", "header-only"} {
		t.Run(transcriptState, func(t *testing.T) {
			dir := t.TempDir()
			probes := &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}
			testCfg := testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				envProbes:           probes,
			}
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(1, "ok")})
			sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
				StateDir: dir,
				testOnly: testCfg,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sess.ProcessInput(context.Background(), "hello", nil); err != nil {
				t.Fatalf("ProcessInput setup: %v", err)
			}
			if got := countEnvironmentTurns(sess); got != 1 {
				t.Fatalf("setup environment turns = %d, want 1", got)
			}
			sess.Close()

			meta := loadMetaForTest(t, sess)
			if err := schema.SaveSessionMeta(dir, meta); err != nil {
				t.Fatalf("save stale metadata: %v", err)
			}
			transcriptPath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
			writer, entries, err := transcript.OpenWriterForSession(transcriptPath, sess.ID())
			if err != nil {
				t.Fatalf("open setup transcript: %v", err)
			}
			header := writer.Header()
			if err := writer.Close(); err != nil {
				t.Fatalf("close setup transcript: %v", err)
			}
			if len(entries) == 0 {
				t.Fatal("setup transcript unexpectedly has no durable entries")
			}
			switch transcriptState {
			case "missing":
				if err := os.Remove(transcriptPath); err != nil {
					t.Fatalf("remove transcript fixture: %v", err)
				}
			case "header-only":
				body, err := json.Marshal(header)
				if err != nil {
					t.Fatalf("marshal transcript header: %v", err)
				}
				body = append(body, '\n')
				if err := os.WriteFile(transcriptPath, body, 0o644); err != nil {
					t.Fatalf("write header-only transcript fixture: %v", err)
				}
			}

			restoreClient := llm.NewClient()
			restoreClient.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(1, "restored")})
			restored, err := RestoreSessionFromMetaWithConfig(
				restoreClient,
				NewOpenAIProfile("gpt-5.2"),
				execenv.NewLocalExecutionEnvironment(dir),
				meta,
				RestoreSessionConfig{StateDir: dir, testOnly: testCfg},
			)
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer restored.Close()
			if _, err := restored.ProcessInput(context.Background(), "again", nil); err != nil {
				t.Fatalf("ProcessInput after %s restore: %v", transcriptState, err)
			}
			if got := countEnvironmentTurns(restored); got != 1 {
				t.Fatalf("%s restore environment turns = %d, want one newly emitted full block", transcriptState, got)
			}
			state := restored.envTracker.State()
			if !state.HasSent {
				t.Fatalf("%s restore did not persist an environment report", transcriptState)
			}
			restoredMeta := loadMetaForTest(t, restored)
			if restoredMeta.EnvContext == nil || !restoredMeta.EnvContext.HasSent {
				t.Fatalf("%s restore did not checkpoint the emitted environment report: %+v", transcriptState, restoredMeta.EnvContext)
			}
		})
	}
}

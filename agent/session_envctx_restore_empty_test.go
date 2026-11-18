package agent

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type recordingCapabilityEnv struct {
	agenttest.FakeEnv
	git, tools atomic.Int32
}

func (e *recordingCapabilityEnv) ExecCommand(ctx context.Context, command string, timeout int, dir string, vars map[string]string) (execenv.ExecResult, error) {
	switch command {
	case gitProbeScript():
		e.git.Add(1)
		return execenv.ExecResult{Stdout: "git=ok\n"}, nil
	case toolProbeScript():
		e.tools.Add(1)
		return execenv.ExecResult{Stdout: "go=no\nnode=no\nrg=yes\n"}, nil
	default:
		return e.FakeEnv.ExecCommand(ctx, command, timeout, dir, vars)
	}
}

func TestRestoreEnvironmentWithNoRetainedEntriesReemitsFullBlock(t *testing.T) {
	for _, transcriptState := range []string{"missing", "header-only", "empty saved prompt"} {
		t.Run(transcriptState, func(t *testing.T) {
			dir := t.TempDir()
			probes := &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}
			testCfg := testConfig{
				noSyncJobStore: true,
				envProbes:      probes,
			}
			client := llm.NewClient()
			client.Register(newFakeAdapter("openai", repeatFinalResponse(1, "ok")...))
			launchEnv := &recordingCapabilityEnv{FakeEnv: agenttest.FakeEnv{WorkDir: dir}}
			sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), launchEnv, SessionConfig{
				StateDir: dir,
				testOnly: testCfg,
			})
			if err != nil {
				t.Fatal(err)
			}
			if git, tools := launchEnv.git.Load(), launchEnv.tools.Load(); git != 1 || tools != 1 {
				t.Fatalf("fresh launch ran %d Git and %d tool probes, want one each", git, tools)
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
			transcriptPath := schema.SessionTranscriptPath(dir, sess.ID())
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
			case "header-only", "empty saved prompt":
				if transcriptState == "empty saved prompt" {
					header.SystemPrompt = ""
				}
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
			restoreClient.Register(newFakeAdapter("openai", repeatFinalResponse(1, "restored")...))
			restoreEnv := &recordingCapabilityEnv{FakeEnv: agenttest.FakeEnv{WorkDir: dir}}
			restored, err := RestoreSessionFromMetaWithConfig(
				restoreClient,
				NewOpenAIProfile("gpt-5.2"),
				restoreEnv,
				meta,
				RestoreSessionConfig{StateDir: dir, testOnly: testCfg},
			)
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer restored.Close()
			needsPrompt := transcriptState == "missing"
			var wantProbes int32
			if needsPrompt {
				wantProbes = 1
			}
			if git, tools := restoreEnv.git.Load(), restoreEnv.tools.Load(); git != wantProbes || tools != wantProbes {
				t.Errorf("restore ran %d Git and %d tool probes, want %d each", git, tools, wantProbes)
			}
			if !needsPrompt && restored.cachedSystemPrompt != header.SystemPrompt {
				t.Error("restore changed the saved prompt bytes")
			}
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

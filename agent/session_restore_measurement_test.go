package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestRestoreMeasurementSeedsOnlyForTheSavedTarget drives the resume path the
// contextmgr tests only split up: a restored session seeds the saved input count
// when it resumes on the target that produced it, and drops it when the resume
// switches models.
func TestRestoreMeasurementSeedsOnlyForTheSavedTarget(t *testing.T) {
	const saved = 5000
	for _, tc := range []struct {
		name       string
		resume     string
		wantSeeded bool
	}{
		{name: "same target", resume: "gpt-5.2", wantSeeded: true},
		{name: "different model", resume: "gpt-5.2-mini", wantSeeded: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			sess := newSession(t, withStateDir(stateDir), withSteps(func(llm.Request) llm.Response {
				resp := agenttest.FinalResponse("done")
				resp.Usage.InputTokens = saved
				return resp
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter; only fires on a genuine hang.
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			meta := sess.Meta()
			if meta.LastInputTokens != saved {
				t.Fatalf("meta LastInputTokens = %d, want %d", meta.LastInputTokens, saved)
			}
			if meta.LastInputMeasuredLen == nil || meta.LastInputAnchor == "" || meta.LastInputTarget == "" {
				t.Fatalf("meta did not persist the measurement prefix: len=%v anchor=%q target=%q", meta.LastInputMeasuredLen, meta.LastInputAnchor, meta.LastInputTarget)
			}
			sess.Close()

			client := llm.NewClient()
			client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("again") }))
			restored, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile(tc.resume)), execenv.NewLocalExecutionEnvironment(stateDir), meta, RestoreSessionConfig{
				StateDir: stateDir,
				testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer restored.Close()

			metrics, _ := restored.ContextStatus()
			if tc.wantSeeded {
				if metrics.Used <= saved {
					t.Fatalf("ContextStatus after restore = %d, want more than the saved %d: the reply appended after the measured prefix was not priced on top of it", metrics.Used, saved)
				}
				if metrics.Used > saved+64 {
					t.Fatalf("ContextStatus after restore = %d, want at most %d: the restored history was re-estimated on top of the saved count", metrics.Used, saved+64)
				}
				return
			}
			if metrics.Used >= saved {
				t.Fatalf("ContextStatus after a model switch = %d, want less than the old target's saved %d: a count from another model was inherited", metrics.Used, saved)
			}
		})
	}
}

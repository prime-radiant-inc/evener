package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A restored meta.json is untrusted input: a pre-normalization file may carry
// a mixed-case level or disable alias (canonicalized), and a corrupted or
// retired level falls back to unset — resuming with the default beats
// bricking the session or letting garbage reach a provider.
func TestRestoreSession_SanitizesReasoningEffort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		persisted  string
		wantEffort string // "" means no reasoning control reaches the request
	}{
		// A disable alias canonicalizes to the off level; a level survives
		// its casing; garbage is dropped and the session takes the default
		// every unconfigured session gets (medium, on gpt-5.2's ladder).
		{name: "disable alias canonicalizes to the off level", persisted: "OFF", wantEffort: "none"},
		{name: "mixed-case level canonicalizes", persisted: "High", wantEffort: "high"},
		{name: "garbage falls back to the default", persisted: "ultra", wantEffort: "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c := llm.NewClient()
			f := newFakeAdapter("openai", func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
			c.Register(f)

			meta := schema.SessionMeta{
				ID:        "01RESTOREEFFORT000000001",
				ProfileID: "openai",
				Model:     "gpt-5.2",
				Config:    schema.ConfigSnapshot{ReasoningEffort: tc.persisted},
			}
			sess, err := RestoreSessionFromMetaWithConfig(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
				t.Fatal(err)
			}
			sess.Close()

			reqs := f.Requests()
			if len(reqs) == 0 {
				t.Fatal("no requests recorded")
			}
			switch {
			case tc.wantEffort == "" && reqs[0].ReasoningEffort != nil:
				t.Fatalf("ReasoningEffort = %q, want no reasoning control on the request", *reqs[0].ReasoningEffort)
			case tc.wantEffort != "" && (reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != tc.wantEffort):
				t.Fatalf("ReasoningEffort = %v, want %q", reqs[0].ReasoningEffort, tc.wantEffort)
			}
		})
	}
}

// TestRestoreSessionWithModelChange_RevalidatesFallbacksPerEntry pins the
// resume-with-a-different-model fallback contract: the saved list was
// validated against a model the session no longer runs, so the same
// per-entry revalidation a mid-session switch applies runs before the
// all-or-nothing startup check — compatible entries stay, incompatible ones
// drop, and a valid pick no longer strands the session. An ordinary resume
// (the persisted model unchanged) keeps the strict all-or-nothing validation.
func TestRestoreSessionWithModelChange_RevalidatesFallbacksPerEntry(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()

	changed := schema.SessionMeta{
		ID:        "01RESTOREMODELFB0000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: schema.ConfigSnapshot{
			ModelFallbacks: []string{"openai/gpt-4o-mini", "anthropic/claude-haiku-test"},
		},
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, withTestSessionNamer(c, newAnthropicProfile("claude-pick")), execenv.NewLocalExecutionEnvironment(t.TempDir()), changed, RestoreSessionConfig{})
	if err != nil {
		t.Fatalf("restore with an explicit model change: %v", err)
	}
	defer restored.Close()
	if got := restored.cfg.ModelFallbacks; len(got) != 1 || got[0] != "anthropic/claude-haiku-test" {
		t.Fatalf("restored model_fallbacks = %v, want only the compatible anthropic/claude-haiku-test", got)
	}

	ordinary := schema.SessionMeta{
		ID:        "01RESTOREMODELFB0000002",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: schema.ConfigSnapshot{
			ModelFallbacks: []string{"anthropic/claude-haiku-test"},
		},
	}
	if _, err := RestoreSessionFromMetaWithConfig(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), ordinary, RestoreSessionConfig{}); err == nil {
		t.Fatal("ordinary restore accepted a cross-surface fallback, want the strict all-or-nothing validation")
	}
}

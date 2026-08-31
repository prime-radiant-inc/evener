package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
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
		wantEffort string // "" means the default applies (medium on gpt-5.2's ladder)
	}{
		{name: "disable alias canonicalizes to the off level", persisted: "OFF", wantEffort: "none"},
		{name: "mixed-case level canonicalizes", persisted: "High", wantEffort: "high"},
		{name: "garbage falls back to the default", persisted: "ultra", wantEffort: "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c := llm.NewClient()
			f := &fakeAdapter{
				name: "openai",
				steps: []func(req llm.Request) llm.Response{
					func(req llm.Request) llm.Response { return finalResponse("done") },
				},
			}
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
			if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != tc.wantEffort {
				t.Fatalf("ReasoningEffort = %v, want %q", reqs[0].ReasoningEffort, tc.wantEffort)
			}
		})
	}
}

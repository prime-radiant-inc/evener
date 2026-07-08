package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// restoreWithSandboxMode restores a session from a persisted meta whose
// ConfigSnapshot carries the given sandbox mode name, returning the restore
// error (nil on success). It exercises the SAME entry point a real resume uses.
func restoreWithSandboxMode(t *testing.T, mode string) error {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	meta := schema.SessionMeta{
		ID:        "restored-sandbox-session",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{Sandbox: mode, NoProjectPrompts: true}).toSnapshot(),
	}
	sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{})
	if sess != nil {
		t.Cleanup(func() { sess.Close() })
	}
	return err
}

// TestRestoreAppliesSandboxFeatureGate is the finding-#1 regression: the restore
// path applied a persisted ConfigSnapshot carrying a non-off sandbox mode without
// re-applying the M1 feature gate that session start enforces at the flag layer.
// A persisted or hand-edited meta.json with "sandbox":"restricted" would resume
// claiming sandboxing with nothing enforced. Restore must fail with the same
// in-development refusal, routed through the single shared gate.
func TestRestoreAppliesSandboxFeatureGate(t *testing.T) {
	for _, mode := range []string{"read-only", "workspace-write", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			err := restoreWithSandboxMode(t, mode)
			if err == nil {
				t.Fatalf("restore with persisted sandbox=%q must be refused by the feature gate", mode)
			}
			if !strings.Contains(err.Error(), "in development and not yet enabled") {
				t.Errorf("restore sandbox=%q: want feature-gate error, got %v", mode, err)
			}
		})
	}
}

// TestRestoreOffSandboxUnchanged proves the gate does not disturb the common
// path: an off or empty persisted mode restores exactly as before.
func TestRestoreOffSandboxUnchanged(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		if err := restoreWithSandboxMode(t, mode); err != nil {
			t.Errorf("restore with sandbox=%q must succeed, got %v", mode, err)
		}
	}
}

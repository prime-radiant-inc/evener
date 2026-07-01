package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// w3init_restoreClient builds a client with the default openai fake adapter.
func w3init_restoreClient() *llm.Client {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return c
}

// w3init_writeJobsJSONL plants raw bytes at the session's jobs.jsonl so restore's
// job-manager construction reads them.
func w3init_writeJobsJSONL(t *testing.T, stateDir, sessionID string, body []byte) {
	t.Helper()
	dir := filepath.Join(stateDir, sessionsSubdir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), body, 0o644); err != nil {
		t.Fatalf("write jobs.jsonl: %v", err)
	}
}

// TestW3Init_Restore_EnvInitializeError covers the env.Initialize failure arm of
// RestoreSessionFromMetaWithConfig.
func TestW3Init_Restore_EnvInitializeError(t *testing.T) {
	t.Parallel()
	env := w3init_failInitEnv{execenv.NewLocalExecutionEnvironment(t.TempDir())}
	meta := schema.SessionMeta{ID: "01W3RESTOREENVINIT0000001", ProfileID: "openai", Model: "gpt-5.2"}

	_, err := RestoreSessionFromMetaWithConfig(w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"), env, meta, RestoreSessionConfig{})
	if err == nil || !strings.Contains(err.Error(), "env initialize") {
		t.Fatalf("err = %v, want env initialize error", err)
	}
}

// TestW3Init_Restore_JobManagerError covers the job-manager construction failure
// arm: a corrupt jobs.jsonl makes newJobManager's pending-watch restore fail.
func TestW3Init_Restore_JobManagerError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3RESTOREJOBMGR00000001"
	w3init_writeJobsJSONL(t, stateDir, id, []byte("{not valid json\n"))
	meta := schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-5.2"}

	_, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err == nil || !strings.Contains(err.Error(), "job manager") {
		t.Fatalf("err = %v, want job manager error", err)
	}
}

// TestW3Init_Restore_InitSessionStateError covers the initSessionState failure arm
// via a persisted system-prompt override pointing at a missing file.
func TestW3Init_Restore_InitSessionStateError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3RESTOREINITSTATE00001"
	missing := filepath.Join(t.TempDir(), "missing-prompt.md")
	snap := SessionConfig{SystemPromptFile: missing}.toSnapshot()
	meta := schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-5.2", Config: snap}

	_, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err == nil || !strings.Contains(err.Error(), "reading system prompt override") {
		t.Fatalf("err = %v, want system prompt override read error", err)
	}
}

// TestW3Init_Restore_SelectStrategyError covers the strategy-selection failure arm
// via an unknown persisted context strategy.
func TestW3Init_Restore_SelectStrategyError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3RESTORESTRATEGY000001"
	snap := SessionConfig{ContextStrategy: "w3init-bogus-strategy"}.toSnapshot()
	meta := schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-5.2", Config: snap}

	_, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err == nil || !strings.Contains(err.Error(), "unknown context strategy") {
		t.Fatalf("err = %v, want unknown context strategy error", err)
	}
}

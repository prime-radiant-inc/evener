package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
)

// TestSessionOriginFromEnv proves a fresh session captures SERF_SESSION_ORIGIN
// at creation time, so the hub can later classify an all-"test" project into
// the "Test runs" group (Task 15).
func TestSessionOriginFromEnv(t *testing.T) {
	t.Setenv(envvars.SERFSessionOrigin.Name, "test")
	sess := newTestSession(t)
	if got := sess.Meta().Origin; got != "test" {
		t.Fatalf("Origin should come from SERF_SESSION_ORIGIN, got %q", got)
	}
}

// restoreSessionWithOrigin restores a session from a persisted meta whose
// Origin field is set to origin. It follows the minimal
// RestoreSessionFromMetaWithConfig harness used by
// TestAskUser_RestoredSubagentStaysInvisible (session_ask_test.go): no
// StateDir is needed since RestoreSessionFromMetaWithConfig takes the meta
// value directly rather than reading it from disk.
func restoreSessionWithOrigin(t *testing.T, origin string) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	meta := schema.SessionMeta{
		ID:        "restored-origin-session",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Origin:    origin,
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	return restored
}

// TestSessionOriginResumeNotClobberedByEnv pins the Task 15 resume-preservation
// invariant: NewSession reads SERF_SESSION_ORIGIN itself, at a fresh-only call
// site, rather than inside the shared initSessionState — so restoring a
// session whose persisted meta.Origin is empty must NOT pick up whatever
// SERF_SESSION_ORIGIN happens to be set in the ambient environment at restore
// time (see the comment above `s.origin = envvars.SERFSessionOrigin.Getenv()`
// in NewSession and `s.origin = meta.Origin` in RestoreSessionFromMetaWithConfig,
// both in session_init.go).
func TestSessionOriginResumeNotClobberedByEnv(t *testing.T) {
	t.Setenv(envvars.SERFSessionOrigin.Name, "test")
	restored := restoreSessionWithOrigin(t, "")
	if got := restored.Meta().Origin; got != "" {
		t.Fatalf("resumed Origin = %q, want \"\" (persisted empty Origin must not be clobbered by ambient SERF_SESSION_ORIGIN)", got)
	}
}

// TestSessionOriginResumePreservesPersistedValue is the symmetric positive
// case: a persisted meta.Origin of "test" survives resume.
func TestSessionOriginResumePreservesPersistedValue(t *testing.T) {
	t.Parallel()
	restored := restoreSessionWithOrigin(t, "test")
	if got := restored.Meta().Origin; got != "test" {
		t.Fatalf("resumed Origin = %q, want \"test\" (persisted Origin must survive resume)", got)
	}
}

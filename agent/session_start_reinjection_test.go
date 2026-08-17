package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// sessionStartHookDispatchEntry returns the single session_start_hooks advisory
// written by a session's SessionStart dispatch.
func sessionStartHookDispatchEntry(t *testing.T, stateDir, sessionID string) sessionlog.SessionLogEntry {
	t.Helper()
	log := mustNewSessionLog(t, filepath.Join(stateDir, sessionsSubdir, sessionID+".log.jsonl"))
	var found []sessionlog.SessionLogEntry
	for _, e := range log.Entries() {
		if e.Action == "session_start_hooks" {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("session_start_hooks entries = %d, want 1; log: %+v", len(found), log.Entries())
	}
	return found[0]
}

// The re-injection detector must not count the HOOK_COMPLETED turns produced by
// the very dispatch it is judging. Those turns land in s.history through the
// runner's HookEnd callback before RunSessionStartFor returns, so a detector
// that counts raw history sees prior history on a session that has none — and
// tags outcome=failure on every fresh session that runs a SessionStart hook,
// which is most of them. That dilution is the whole complaint (kata 6p54): a
// failure tag that fires on the ordinary case cannot grep out of the noise.
func TestSessionStartDispatchOnFreshSessionIsNotFlaggedAsReinjection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:   dir,
		PluginDirs: []string{hookPluginDir(t, "echo FRESH_SESSION_CONTEXT")},
		testOnly:   testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	entry := sessionStartHookDispatchEntry(t, dir, sess.ID())
	// Guard against a vacuous pass: a dispatch that delivered nothing could
	// never trip the detector, so the silence would prove nothing.
	if !strings.Contains(entry.Summary, "delivered=1 ") {
		t.Fatalf("summary = %q, want delivered=1 followed by a delimiter — the hook must actually deliver context for this test to mean anything", entry.Summary)
	}
	if !strings.Contains(entry.Summary, "historyTurns=0 ") {
		t.Errorf("summary = %q, want historyTurns=0 followed by a delimiter — the dispatch's own HOOK_COMPLETED turns are not prior history", entry.Summary)
	}
	if entry.Outcome != "success" {
		t.Errorf("outcome = %q, want success on a fresh session; failures = %v", entry.Outcome, entry.Failures)
	}
	if len(entry.Failures) != 0 {
		t.Errorf("failures = %v, want none on a fresh session", entry.Failures)
	}
}

// The other half of the detector's contract: it must still fire on the anomaly
// it exists to catch — a dispatch delivering context to a session that already
// carries real conversation. Restoring with prior history and a resume-matched
// SessionStart hook reproduces exactly that.
func TestSessionStartDispatchIntoRestoredHistoryIsFlaggedAsReinjection(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)

	meta := schema.SessionMeta{
		ID:        "01KREINJECTIONDETECTOR00000",
		ProfileID: "test",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{PluginDirs: []string{newResumeHookPluginDir(t)}},
		TurnCount: 1,
	}
	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{
			StateDir: stateDir,
			resumeHistory: []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("prior user task")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("prior assistant answer")),
			},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	// Resume SessionStart hooks are deferred until the first user turn.
	if _, err := sess.ProcessInput(t.Context(), "current user task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	entry := sessionStartHookDispatchEntry(t, stateDir, sess.ID())
	if entry.Outcome != "failure" {
		t.Fatalf("outcome = %q, want failure — context delivered into a session with prior history is the anomaly; summary = %q", entry.Outcome, entry.Summary)
	}
	if !strings.Contains(entry.Summary, "historyTurns=2 ") {
		t.Errorf("summary = %q, want historyTurns=2 followed by a delimiter — the two restored conversation turns", entry.Summary)
	}
	if len(entry.Failures) != 1 || !strings.Contains(entry.Failures[0], "re-injected") {
		t.Errorf("failures = %v, want one re-injection diagnostic", entry.Failures)
	}
}

package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
		withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")),
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

// Restoring a session with no conversation still persists the current
// environment before the deferred resume hook runs. That environment record is
// setup context, not prior conversation, so it must not make the hook dispatch
// look like a reinjection.
func TestSessionStartDispatchAfterEmptyRestoreIsNotFlaggedAsReinjection(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)

	meta := schema.SessionMeta{
		ID:        "01KREINJECTIONEMPTYRESTORE0",
		ProfileID: "test",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{PluginDirs: []string{newResumeHookPluginDir(t)}},
	}
	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	if _, err := sess.ProcessInput(t.Context(), "first user task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	data, err := readTranscriptFull(sess.TranscriptPath(), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	haveEnvironment := false
	haveResumeHookContext := false
	for _, item := range data.Entries {
		if item.Turn.Kind == schema.TurnEnvironment {
			haveEnvironment = true
		}
		if strings.Contains(item.Turn.Message.Text(), "RESUME_HOOK_CONTEXT") {
			haveResumeHookContext = true
		}
	}
	if !haveEnvironment {
		t.Fatal("restored empty session did not durably record its environment context")
	}
	if !haveResumeHookContext {
		t.Fatal("resume hook context was not durably delivered")
	}

	entry := sessionStartHookDispatchEntry(t, stateDir, sess.ID())
	if entry.Outcome != "success" {
		t.Fatalf("outcome = %q, want success on an empty restored session; summary = %q; failures = %v", entry.Outcome, entry.Summary, entry.Failures)
	}
	if len(entry.Failures) != 0 {
		t.Fatalf("failures = %v, want none on an empty restored session", entry.Failures)
	}
	if !strings.Contains(entry.Summary, "historyTurns=0 ") {
		t.Fatalf("summary = %q, want historyTurns=0 followed by a delimiter", entry.Summary)
	}
}

// A restored session carrying shared notes projects them as a NOTES_CONTEXT
// turn at the start of its first user turn — before the deferred resume hook
// runs. That snapshot is harness context, not something the user or the model
// said, so it must not make the hook's dispatch look like a reinjection into a
// session with prior history.
func TestSessionStartDispatchOnNotesContextOnlyHistoryIsNotFlaggedAsReinjection(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)

	meta := schema.SessionMeta{
		ID:        "01KREINJECTIONNOTESONLY00",
		ProfileID: "test",
		Model:     "gpt-5.2",
		// A restored note makes the first user turn project a NOTES_CONTEXT
		// turn before the deferred resume hook is drained.
		AgentNote: "restored shared note",
		Config:    schema.ConfigSnapshot{PluginDirs: []string{newResumeHookPluginDir(t)}},
	}
	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir},
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
	if _, err := sess.ProcessInput(t.Context(), "first user task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	entry := sessionStartHookDispatchEntry(t, stateDir, sess.ID())
	// Guard against a vacuous pass: a dispatch that delivered nothing could
	// never trip the detector, so the silence would prove nothing.
	if strings.Contains(entry.Summary, "delivered=0 ") {
		t.Fatalf("summary = %q — the resume hook delivered nothing, so this dispatch could not trip the detector", entry.Summary)
	}
	if !strings.Contains(entry.Summary, "historyTurns=0 ") {
		t.Fatalf("summary = %q, want historyTurns=0 followed by a delimiter — the projected notes snapshot is not prior conversation", entry.Summary)
	}
	if entry.Outcome != "success" {
		t.Fatalf("outcome = %q, want success — a projected NOTES_CONTEXT turn is not prior conversation; summary = %q; failures = %v", entry.Outcome, entry.Summary, entry.Failures)
	}
	if len(entry.Failures) != 0 {
		t.Fatalf("failures = %v, want none for a notes-context-only session", entry.Failures)
	}
}

// A restored session whose only history is a harness memory projection must not
// look like prior conversation to the re-injection detector. The projected
// MEMORY_CONTEXT turn is harness news, not something the user or the model said,
// so a resume dispatch delivering hook context into a memory-only history is not
// a re-injection.
func TestSessionStartDispatchOnMemoryContextOnlyHistoryIsNotFlaggedAsReinjection(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	// The real producer appends the boundary's memory news as one MEMORY_CONTEXT
	// turn. Guard against a vacuous pass: a session with no projection could
	// never trip the detector, so its silence would prove nothing.
	before := memoryContextCount(s)
	s.appendMemoryContext([]memoryContextSection{func() string { return "memory news" }})
	if got := memoryContextCount(s); got != before+1 {
		t.Fatalf("memory contexts = %d, want %d — the projection must land for this test to mean anything", got, before+1)
	}

	s.logSessionStartHookDispatch(plugin.SessionStartKindStartup, 1)
	entry := sessionStartHookDispatchEntry(t, s.stateDir, s.ID())
	if entry.Outcome != "success" {
		t.Fatalf("outcome = %q, want success — a harness memory projection is not prior conversation; failures = %v", entry.Outcome, entry.Failures)
	}
	if len(entry.Failures) != 0 {
		t.Fatalf("failures = %v, want none for a memory-context-only session", entry.Failures)
	}
	if turns, _ := s.conversationSignals(); turns != 0 {
		t.Fatalf("conversation turns = %d, want 0 — the memory projection must not count as conversation", turns)
	}
}

// The other half of the detector's contract: a real user/assistant exchange
// still counts, so excluding memory context cannot silently widen into excluding
// conversation.
func TestSessionStartDispatchOnRealHistoryStillFlaggedAsReinjection(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()
	s.appendTurn(schema.TurnUserInput, llm.User("prior user task"))
	s.appendTurn(schema.TurnAssistant, llm.Assistant("prior assistant answer"))

	s.logSessionStartHookDispatch(plugin.SessionStartKindStartup, 1)
	entry := sessionStartHookDispatchEntry(t, s.stateDir, s.ID())
	if entry.Outcome != "failure" {
		t.Fatalf("outcome = %q, want failure — a real user/assistant exchange is prior conversation; failures = %v", entry.Outcome, entry.Failures)
	}
	if turns, _ := s.conversationSignals(); turns != 2 {
		t.Fatalf("conversation turns = %d, want 2", turns)
	}
	if len(entry.Failures) != 1 {
		t.Fatalf("failures = %v, want one re-injection diagnostic", entry.Failures)
	}
}

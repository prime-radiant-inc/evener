package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func newResumeHookPluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "resume-hook-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	manifest := `{
  "name": "resume-hook-plugin",
  "version": "0.0.1",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "resume",
        "hooks": [
          {
            "type": "command",
            "command": "printf '{\"hookSpecificOutput\":{\"additionalContext\":\"RESUME_HOOK_CONTEXT\"}}\\n'; printf 'RESUME_HOOK_USER_MESSAGE\\n'",
            "timeout": 5
          }
        ]
      }
    ]
  }
}`
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	return pluginDir
}

func restoredSessionWithResumeHook(t *testing.T, adapter *fakeAdapter) *Session {
	t.Helper()
	stateDir := t.TempDir()
	meta := schema.SessionMeta{
		ID:        "01KRESUMEHOOKTEST0000000000",
		ProfileID: "test",
		Model:     "gpt-5.2",
		Config: schema.ConfigSnapshot{
			PluginDirs: []string{newResumeHookPluginDir(t)},
		},
		TurnCount: 1,
	}
	client := llm.NewClient()
	client.Register(adapter)
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
	t.Cleanup(sess.Close)
	return sess
}

func requestText(req llm.Request) string {
	parts := make([]string, 0, len(req.Messages))
	for _, msg := range req.Messages {
		parts = append(parts, msg.Text())
	}
	return strings.Join(parts, "\n---\n")
}

func sessionHistoryText(sess *Session) string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	parts := make([]string, 0, len(sess.history))
	for _, turn := range sess.history {
		parts = append(parts, turn.Message.Text())
	}
	return strings.Join(parts, "\n---\n")
}

func requireRequestContainsInOrder(t *testing.T, req llm.Request, needles ...string) {
	t.Helper()
	text := requestText(req)
	pos := 0
	for _, needle := range needles {
		i := strings.Index(text[pos:], needle)
		if i < 0 {
			t.Fatalf("request text missing %q after offset %d; text:\n%s", needle, pos, text)
		}
		pos += i + len(needle)
	}
}

func closeAndCountSessionStartHooks(t *testing.T, sess *Session, eventsPtr *[]events.SessionEvent, mu *sync.Mutex, doneCh <-chan struct{}) int {
	t.Helper()
	sess.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	return countHookStarts(*eventsPtr, plugin.HookSessionStart)
}

func TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req,
				"prior user task",
				"prior assistant answer",
				"RESUME_HOOK_CONTEXT",
				"current user task",
			)
			return finalResponse("done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)

	preHistory := sessionHistoryText(sess)
	if strings.Contains(preHistory, "RESUME_HOOK_CONTEXT") || strings.Contains(preHistory, "RESUME_HOOK_USER_MESSAGE") {
		t.Fatalf("resume hook output was injected during restore; history:\n%s", preHistory)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "current user task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("adapter requests = %d, want 1", got)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts after first user input = %d, want 1", got)
	}
}

func TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			text := requestText(req)
			if strings.Contains(text, "RESUME_HOOK_CONTEXT") || strings.Contains(text, "RESUME_HOOK_USER_MESSAGE") {
				t.Fatalf("notification request drained resume hooks; text:\n%s", text)
			}
			if !strings.Contains(text, "notification steering without resume hooks") {
				t.Fatalf("notification request missing steering text; text:\n%s", text)
			}
			return finalResponse("notification done")
		},
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req, "RESUME_HOOK_CONTEXT", "first real user")
			return finalResponse("user done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)
	sess.Steer("notification steering without resume hooks")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("adapter requests after notification = %d, want 1", got)
	}
	if _, err := sess.ProcessInput(ctx, "first real user", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("adapter requests after user = %d, want 2", got)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts after notification plus user input = %d, want 1", got)
	}
}

func TestRestoreSessionStartHooksDrainOnlyOnce(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req, "RESUME_HOOK_CONTEXT", "first user")
			return finalResponse("first done")
		},
		func(req llm.Request) llm.Response {
			text := requestText(req)
			if strings.Count(text, "RESUME_HOOK_CONTEXT") != 1 {
				t.Fatalf("resume hook context count across second request = %d, want 1; text:\n%s", strings.Count(text, "RESUME_HOOK_CONTEXT"), text)
			}
			if !strings.Contains(text, "second user") {
				t.Fatalf("second request missing second user prompt; text:\n%s", text)
			}
			return finalResponse("second done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first user", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	if _, err := sess.ProcessInput(ctx, "second user", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts across two user inputs = %d, want 1", got)
	}
}

func TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			requireRequestContainsInOrder(t, req, "RESUME_HOOK_CONTEXT", "accepted user")
			return finalResponse("done")
		},
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	eventsPtr, mu, doneCh := collectEvents(sess)

	if err := sess.Enqueue(context.Background(), ""); err == nil {
		t.Fatal("empty enqueue succeeded, want rejection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "accepted user", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("adapter requests = %d, want 1", got)
	}
	if got := closeAndCountSessionStartHooks(t, sess, eventsPtr, mu, doneCh); got != 1 {
		t.Fatalf("SessionStart hook starts after rejected enqueue plus accepted input = %d, want 1", got)
	}
}

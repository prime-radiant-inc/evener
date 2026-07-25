package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// hookPluginDir builds a minimal plugin whose SessionStart hook runs command
// and returns its directory. The command decides the hook's exit code, which
// is the value the transcript must carry.
func hookPluginDir(t *testing.T, command string) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude-plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "hook-turn-plugin", "version": "1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hooksJSON := `{"hooks": {"SessionStart": [{"matcher": "*", "hooks": [{"type": "command", "command": "` + command + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	return dir
}

// transcriptHookTurns returns every HOOK_COMPLETED entry in a transcript.
func transcriptHookTurns(t *testing.T, path string) []schema.Turn {
	t.Helper()
	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var out []schema.Turn
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnHookCompleted {
			out = append(out, entry.Turn)
		}
	}
	return out
}

// sessionRunningHook starts a session whose only plugin hook runs command,
// waits for the hook to be recorded, and returns the transcript path.
func sessionRunningHook(t *testing.T, command string) string {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:   dir,
		PluginDirs: []string{hookPluginDir(t, command)},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	tpath := sess.TranscriptPath()
	sess.Close()
	return tpath
}

// kata qm9y: a hook's completion must be written down, not merely broadcast
// live. Before this, hook_completed items were produced only by the live
// projector, so Settings -> Transcript's two hook-exit toggles governed
// nothing at all once a session was reloaded.
func TestHookEndPersistsHookCompletedTurn(t *testing.T) {
	t.Parallel()
	hooks := transcriptHookTurns(t, sessionRunningHook(t, "exit 0"))
	if len(hooks) != 1 {
		t.Fatalf("HOOK_COMPLETED entries: got %d, want 1", len(hooks))
	}
	got := hooks[0]
	if got.Hook == nil {
		t.Fatal("HOOK_COMPLETED entry carries no Hook detail")
	}
	if got.Hook.ExitCode != 0 {
		t.Errorf("Hook.ExitCode = %d, want 0", got.Hook.ExitCode)
	}
	if got.Hook.Event != "SessionStart" {
		t.Errorf("Hook.Event = %q, want SessionStart", got.Hook.Event)
	}
	if got.Hook.PluginName != "hook-turn-plugin" {
		t.Errorf("Hook.PluginName = %q, want hook-turn-plugin", got.Hook.PluginName)
	}
	// The announcement also rides the turn's own text so every renderer that
	// reads only turn text still shows the hook line.
	if got.Message.Text() == "" {
		t.Error("HOOK_COMPLETED turn has empty text; renderers that read only turn text would show nothing")
	}
}

// The crux of the toggle split: "Hook exits (normal only)" must be able to
// tell a clean exit from a broken one after a reload, so the real code has to
// survive the write. A persisted zero for a hook that exited 3 would silently
// show a failed hook under the clean-exits-only setting.
func TestNonZeroHookExitIsPersistedVerbatim(t *testing.T) {
	t.Parallel()
	hooks := transcriptHookTurns(t, sessionRunningHook(t, "exit 3"))
	if len(hooks) != 1 {
		t.Fatalf("HOOK_COMPLETED entries: got %d, want 1", len(hooks))
	}
	if got := hooks[0].Hook; got == nil || got.ExitCode != 3 {
		t.Fatalf("Hook = %+v, want ExitCode 3", got)
	}
}

// A hook that fires mid-session takes the direct write path rather than the
// construction-time buffer, and must be recorded just the same. UserPromptSubmit
// is the ordinary case the two toggles were built for.
func TestMidSessionHookPersistsHookCompletedTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pluginDir := t.TempDir()
	pluginDir, err := filepath.EvalSymlinks(pluginDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir .claude-plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name": "mid-session-plugin", "version": "1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hooksJSON := `{"hooks": {"UserPromptSubmit": [{"matcher": "*", "hooks": [{"type": "command", "command": "exit 5"}]}]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks", "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{func(req llm.Request) llm.Response { return finalResponse("ok") }},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:   dir,
		PluginDirs: []string{pluginDir},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	tpath := sess.TranscriptPath()
	sess.Close()

	hooks := transcriptHookTurns(t, tpath)
	if len(hooks) != 1 {
		t.Fatalf("HOOK_COMPLETED entries: got %d, want 1", len(hooks))
	}
	if got := hooks[0].Hook; got == nil || got.ExitCode != 5 || got.Event != "UserPromptSubmit" {
		t.Fatalf("Hook = %+v, want UserPromptSubmit with ExitCode 5", got)
	}
}

// HOOK_COMPLETED is presentational only. Like TurnModelSwitch and TurnFailure
// it must never be replayed to the model: a hook's own bookkeeping is not
// conversation.
func TestHookCompletedTurnIsNeverSentToModel(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		schema.NewTurn(schema.TurnHookCompleted, llm.System("SessionStart hook exit 0")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("hello")),
	}
	msgs := expandHistory(history, replayScope{})
	for _, m := range msgs {
		if m.Text() == "SessionStart hook exit 0" {
			t.Fatalf("hook announcement reached the model: %+v", msgs)
		}
	}
	if len(msgs) != 2 {
		t.Fatalf("history messages = %d, want 2 (user + assistant)", len(msgs))
	}
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func hookPluginDirForEvent(t *testing.T, event, command string) string {
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
	hooksJSON := `{"hooks":{"` + event + `":[{"matcher":"*","hooks":[{"type":"command","command":"` + command + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	return dir
}

// hookPluginDir builds a minimal plugin whose SessionStart hook runs command
// and returns its directory. The command decides the hook's exit code, which
// is the value the transcript must carry.
func hookPluginDir(t *testing.T, command string) string {
	t.Helper()
	return hookPluginDirForEvent(t, "SessionStart", command)
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

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:   dir,
		PluginDirs: []string{hookPluginDir(t, command)},
		testOnly:   testConfig{metaFS: afero.NewMemMapFs()},
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
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:   dir,
		PluginDirs: []string{pluginDir},
		testOnly:   testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	// TRIPWIRE: the adapter is scripted in-process, but the UserPromptSubmit
	// hook is a real `exit 5` subprocess run through exec.CommandContext;
	// only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

func TestCompactedHookToolExchangeProjectsValidProviderMessages(t *testing.T) {
	const callID = "call_with_hook"
	tests := []struct {
		name           string
		preserveRecent int
		steering       bool
	}{
		{name: "cutoff on tool results", preserveRecent: 2},
		{name: "cutoff on hook marker", preserveRecent: 3},
		{name: "cutoff on hook marker before steering", preserveRecent: 4, steering: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			history := []schema.Turn{
				{Kind: schema.TurnUserInput, Message: llm.User("task")},
				{Kind: schema.TurnAssistant, Message: llm.Assistant("earlier answer")},
				schema.NewTurn(schema.TurnAssistant, llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{{
						Kind: llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{
							ID:        callID,
							Name:      "probe",
							Type:      "function",
							Arguments: json.RawMessage(`{}`),
						},
					}},
				}),
			}
			history = append(history, schema.NewTurn(schema.TurnHookCompleted, llm.System("PreToolUse hook exit 0")))
			if tc.steering {
				history = append(history, schema.NewTurn(schema.TurnSteering, llm.User("continue after the hook")))
			}
			history = append(history,
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "ok", false)),
				schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
			)
			cm := contextmgr.NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
			cm.PreserveRecentTurns = tc.preserveRecent
			cm.ForceCompact(context.Background(), &history, "", func(events.EventKind, events.EventData) {})

			// expandHistory is the production projection that creates the
			// per-message slice sent to the provider.
			messages := expandHistory(history, replayScope{})
			callIndex, resultIndex := -1, -1
			for i, message := range messages {
				for _, part := range message.Content {
					if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
						callIndex = i
					}
					if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
						resultIndex = i
						if message.Role != llm.RoleTool || message.ToolCallID != callID {
							t.Fatalf("projected result message = %+v, want tool role linked to %q", message, callID)
						}
					}
				}
			}
			if callIndex < 0 || resultIndex < 0 || callIndex >= resultIndex {
				t.Fatalf("projected tool exchange order call=%d result=%d; messages=%+v", callIndex, resultIndex, messages)
			}
		})
	}
}

func TestPreToolUseHookDoesNotDuplicateResultInNextModelRequest(t *testing.T) {
	t.Parallel()
	const callID = "call_with_pre_tool_hook"
	dir := t.TempDir()
	var requestErr error
	adapter := &fakeAdapter{
		name: "anthropic",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        callID,
					Name:      "hook_probe",
					Type:      "function",
					Arguments: json.RawMessage(`{}`),
				})
			},
			func(req llm.Request) llm.Response {
				requestErr = validateSingleSuccessfulToolResult(req.Messages, callID)
				return finalResponse("done")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(
		client,
		withTestSessionNamer(client, newAnthropicProfile("k3")),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{
			StateDir:   dir,
			PluginDirs: []string{hookPluginDirForEvent(t, "PreToolUse", "exit 0")},
			testOnly:   testConfig{metaFS: afero.NewMemMapFs()},
		},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	go func() {
		for range sess.Events() {
		}
	}()
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "hook_probe",
			Description: "return a deterministic successful result",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return "probe succeeded", nil
		},
	}); err != nil {
		t.Fatalf("register hook_probe: %v", err)
	}

	// TRIPWIRE: the adapter is scripted in-process, but the PreToolUse hook
	// is a real `exit 0` subprocess run through exec.CommandContext; only
	// fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "run the probe", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "done" {
		t.Fatalf("ProcessInput output = %q, want done", out)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}

	transcriptPath := sess.TranscriptPath()
	sess.Close()
	data, err := readTranscriptFull(transcriptPath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	assistantIdx, hookIdx, resultIdx := -1, -1, -1
	for i, entry := range data.Entries {
		switch entry.Turn.Kind {
		case schema.TurnAssistant:
			for _, call := range assistantToolCalls(entry.Turn.Message) {
				if call.ID == callID {
					assistantIdx = i
				}
			}
		case schema.TurnHookCompleted:
			if hookIdx < 0 && entry.Turn.Hook != nil && entry.Turn.Hook.Event == "PreToolUse" {
				hookIdx = i
			}
		case schema.TurnToolResults:
			if countToolResultsInHistory([]schema.Turn{entry.Turn}, callID) == 1 {
				resultIdx = i
			}
		}
	}
	if assistantIdx < 0 || assistantIdx >= hookIdx || hookIdx >= resultIdx {
		t.Fatalf("transcript order assistant=%d hook=%d result=%d", assistantIdx, hookIdx, resultIdx)
	}
}

func validateSingleSuccessfulToolResult(messages []llm.Message, callID string) error {
	results := 0
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.ToolCallID != callID {
				continue
			}
			results++
			if part.ToolResult.IsError {
				return fmt.Errorf("tool result %s is synthetic error: %v", callID, part.ToolResult.Content)
			}
		}
	}
	if results != 1 {
		return fmt.Errorf("tool results for %s = %d, want exactly 1", callID, results)
	}
	return nil
}

// A hook completion is published twice — the durable entry and the live event
// — and the two are not atomic, so whichever goes second can be overtaken by a
// concurrently recorded round and the live and durable projections then order
// the hook differently inside the same turn. The entry goes first, and this is
// where that order is observable rather than assumed: an authoritative
// consumer that is not draining holds the announce at the saturated event
// channel, and the transcript is read from outside the emitter while it waits.
func TestHookEndWritesTheEntryBeforeAnnouncingIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hook-order.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{})
	if err != nil {
		t.Fatalf("NewWriterNoSync: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	s := &Session{
		id:              "hook-order",
		transcript:      writer,
		transcriptReady: true,
		events:          make(chan events.SessionEvent, 1),
		// Nothing drains this session, so an ordinary emitter would drop the
		// event and never park. The mark is what makes the send wait.
		authoritativeConsumer: true,
	}
	s.events <- events.SessionEvent{Kind: events.EventWarning, Data: events.WarningData{Message: "fills the buffer"}}

	blocked := make(chan struct{})
	var blockedOnce sync.Once
	s.testOnlyBlockedSendEntered = func() { blockedOnce.Do(func() { close(blocked) }) }

	announced := make(chan struct{})
	go func() {
		defer close(announced)
		s.emitHookCompleted(events.HookEndData{Event: "PreCompact", HookType: "command", PluginName: "hook-turn-plugin"})
	}()

	select {
	case <-blocked:
	// TRIPWIRE: the callback fires as the emitter reaches the saturated
	// channel, which is immediate; 10s only fires if it never gets there.
	case <-time.After(10 * time.Second):
		t.Fatal("the hook completion's event never reached the saturated channel")
	}

	if hooks := transcriptHookTurns(t, path); len(hooks) != 1 {
		t.Fatalf("HOOK_COMPLETED entries while the event is still held at the channel: got %d, want 1; an announce that precedes its own entry lets a concurrently recorded round land between them and order the hook differently in the live and durable projections", len(hooks))
	}

	<-s.events // releases the parked announce
	select {
	case <-announced:
	// TRIPWIRE: the send completes as soon as the buffer has room; 10s only
	// fires if the emitter stays parked after the drain.
	case <-time.After(10 * time.Second):
		t.Fatal("the hook completion's event stayed parked after the channel drained")
	}
	if got := len(s.events); got != 1 {
		t.Fatalf("events on the channel after the release = %d, want the announced hook end", got)
	}
}

// Writing before announcing is only half the rule: a write that FAILS must
// announce nothing at all. The live event is the only copy a watching client
// gets, so a hook completion published on a failed write is one a reload
// cannot reproduce — and a live history entry for it is the same divergence
// inside the session's own model history.
func TestHookEndAnnouncesNothingWhenTheTranscriptWriteFails(t *testing.T) {
	t.Parallel()
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/hook-write-failure.jsonl", transcript.Header{SessionID: "hook-write-failure"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	s := &Session{
		id:              "hook-write-failure",
		transcript:      writer,
		transcriptReady: true,
		events:          make(chan events.SessionEvent, 8),
	}
	fs.fail = true

	s.emitHookCompleted(events.HookEndData{Event: "PreCompact", HookType: "command", PluginName: "hook-turn-plugin"})

	close(s.events)
	var ends int
	var warnings int
	for event := range s.events {
		switch event.Kind {
		case events.EventHookEnd:
			ends++
		case events.EventWarning:
			warnings++
		}
	}
	if ends != 0 {
		t.Fatalf("HOOK_END events after a failed transcript write = %d, want 0: a hook completion announced live but absent from the transcript disappears on reload", ends)
	}
	if warnings != 1 {
		t.Fatalf("warnings after a failed transcript write = %d, want the write failure reported exactly once", warnings)
	}
	if got := len(s.history); got != 0 {
		t.Fatalf("live history turns after a failed transcript write = %d, want 0: a turn that is not durable must not stay in the model history either", got)
	}
	if got := len(s.persistedAppendLog); got != 0 {
		t.Fatalf("persisted append log entries after a failed transcript write = %d, want 0; a fold would re-append a turn the transcript never held", got)
	}
}

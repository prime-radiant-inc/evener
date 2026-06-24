package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// These tests drive a real *Session and exercise the context manager through the
// session wiring (the manager itself is unit-tested in agent/internal/contextmgr).

func TestSession_ContextManager_Created(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok")})
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.contextMgr == nil {
		t.Fatal("expected contextMgr to be created")
	}
}

func TestSession_ContextManager_AccumulatesUsage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	reasoningTokens := 10
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Assistant("ok"),
					Usage: llm.Usage{
						InputTokens:     100,
						OutputTokens:    50,
						TotalTokens:     150,
						ReasoningTokens: &reasoningTokens,
					},
				})
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	usage := sess.contextMgr.CumulativeUsage()
	if usage.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Fatalf("OutputTokens = %d, want 50", usage.OutputTokens)
	}
}

func TestSession_ContextManager_CompactsWhenNeeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	// Create an adapter that returns a tool call first, then "ok".
	callCount := 0
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// First round: return a read_file tool call with a big result.
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			// Second round: just return text.
			func(req llm.Request) llm.Response {
				callCount++
				return finalResponse("done")
			},
		},
	})

	// Write a big file that will fill a small context window.
	bigContent := strings.Repeat("line of content\n", 200)
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Use a very small context window to force compaction.
	profile := WithContextWindow(NewOpenAIProfile("gpt-5.2"), 500)

	sess, err := NewSession(c, profile, env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Verify the session is configured with the small window.
	if sess.Profile().ContextWindowSize() != 500 {
		t.Fatalf("expected context window 500, got %d", sess.Profile().ContextWindowSize())
	}

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "read the file", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", callCount)
	}
}

func TestSession_ContextManager_EmitsEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	// Return a tool call that produces big output, then "done".
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	})

	bigContent := strings.Repeat("line of content\n", 300)
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	profile := WithContextWindow(NewOpenAIProfile("gpt-5.2"), 500) // Tiny window to force compaction.

	sess, err := NewSession(c, profile, env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "read the file", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	foundCompaction := false
	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			foundCompaction = true
		}
	}
	if !foundCompaction {
		t.Fatal("expected CONTEXT_COMPACTION event when using small context window")
	}
}

// TestBuildCompactionMeta_ExcludesTaskState is the agent-side half of the
// formerly-combined "checkpoint does not freeze stale task state" test: it pins
// buildCompactionMeta's contract that live task descriptions are NOT pulled into
// the CompactionMeta. The companion contextmgr test
// (TestCheckpoint_RendersOnlyFromMetaAndHistory) covers the rendering side.
func TestBuildCompactionMeta_ExcludesTaskState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	store := sess.getOrCreateTaskStore()
	if _, err := store.Append([]task.TaskInput{{Description: "Frobnicate the gizmo"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Update([]task.TaskUpdate{{ID: 1, Status: task.TaskInProgress}}); err != nil {
		t.Fatalf("Update in_progress: %v", err)
	}

	meta := sess.buildCompactionMeta()
	if strings.Contains(meta.SessionID, "Frobnicate") {
		t.Fatalf("task description leaked into SessionID: %q", meta.SessionID)
	}
	for _, s := range meta.ActivatedSkills {
		if strings.Contains(s, "Frobnicate") {
			t.Fatalf("task description leaked into ActivatedSkills: %v", meta.ActivatedSkills)
		}
	}
}

// TestBuildCompactionMeta_SessionID verifies that buildCompactionMeta sets
// SessionID when the session has a stateDir, and leaves it empty otherwise.
func TestBuildCompactionMeta_SessionID(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	t.Run("with stateDir", func(t *testing.T) {
		dir := t.TempDir()
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()

		meta := sess.buildCompactionMeta()
		if meta.SessionID == "" {
			t.Fatal("expected SessionID to be set when stateDir is configured")
		}
		if meta.SessionID != sess.id {
			t.Fatalf("expected SessionID %q, got %q", sess.id, meta.SessionID)
		}
	})

	t.Run("without stateDir", func(t *testing.T) {
		dir := t.TempDir()
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()

		meta := sess.buildCompactionMeta()
		if meta.SessionID != "" {
			t.Fatalf("expected SessionID to be empty without stateDir, got %q", meta.SessionID)
		}
	})
}

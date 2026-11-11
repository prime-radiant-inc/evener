package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
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

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))
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

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))
	defer sess.Close()

	ctx := context.Background()
	_, err := sess.ProcessInput(ctx, "hi", nil)
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
	// The window is sized from this session's own standing request and the
	// first round reports that request as its usage, two thirds of the window
	// and below the 90% checkpoint trigger. The big read alone must lift
	// pressure over the trigger; a one-line read must not, so the tool result
	// is what this test proves drives the compaction.
	standing, window, bigContent := contextWindowForRead(t, dir, "read the file")
	for _, tc := range []struct {
		name           string
		content        string
		wantCompaction bool
	}{
		{name: "read crosses the trigger", content: bigContent, wantCompaction: true},
		{name: "short read stays below", content: "one line\n", wantCompaction: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := llm.NewClient()

			// Create an adapter that returns a tool call first, then "ok".
			callCount := 0
			c.Register(&fakeAdapter{
				name: "openai",
				steps: []func(req llm.Request) llm.Response{
					// First round: return a read_file tool call with the case's result.
					func(req llm.Request) llm.Response {
						callCount++
						return llm.Response{
							Usage: llm.Usage{InputTokens: standing},
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
						return agenttest.FinalResponse("done")
					},
				},
			})

			env := execenv.NewLocalExecutionEnvironment(dir)
			if _, err := env.WriteFile("big.txt", tc.content); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			profile := withContextWindow(NewOpenAIProfile("gpt-5.2"), window)

			sess := newSession(t, withClient(c), withProfile(profile), withEnv(env), withConfig(SessionConfig{}))

			// Collect events to verify compaction actually ran (not just that the adapter
			// was called twice). Without this, commenting out MaybeCompact still passes.
			var evs []events.SessionEvent
			done := make(chan struct{})
			go func() {
				defer close(done)
				for ev := range sess.Events() {
					evs = append(evs, ev)
				}
			}()

			// Verify the session is configured with the computed window.
			if sess.Profile().ContextWindowSize() != window {
				t.Fatalf("expected context window %d, got %d", window, sess.Profile().ContextWindowSize())
			}

			ctx := context.Background()
			_, err := sess.ProcessInput(ctx, "read the file", nil)
			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}

			if callCount != 2 {
				t.Fatalf("expected 2 LLM calls, got %d", callCount)
			}

			sess.Close()
			<-done

			foundCompaction := false
			for _, e := range evs {
				if e.Kind == events.EventContextCompaction {
					foundCompaction = true
				}
			}
			if foundCompaction != tc.wantCompaction {
				t.Fatalf("CONTEXT_COMPACTION emitted = %t, want %t: the read is what crosses the 90%% trigger", foundCompaction, tc.wantCompaction)
			}
		})
	}
}

func TestSession_ContextManager_EmitsEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	standing, window, bigContent := contextWindowForRead(t, dir, "read the file")
	c := llm.NewClient()

	// Return a tool call that produces big output, then "done".
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Usage: llm.Usage{InputTokens: standing},
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
				return agenttest.FinalResponse("done")
			},
		},
	})

	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	profile := withContextWindow(NewOpenAIProfile("gpt-5.2"), window)

	sess := newSession(t, withClient(c), withProfile(profile), withEnv(env), withConfig(SessionConfig{}))

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx := context.Background()
	_, err := sess.ProcessInput(ctx, "read the file", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	var compaction *events.ContextCompactionData
	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			data, ok := e.Data.(events.ContextCompactionData)
			if !ok {
				t.Fatalf("compaction event data = %T, want events.ContextCompactionData", e.Data)
			}
			compaction = &data
		}
	}
	if compaction == nil {
		t.Fatal("expected CONTEXT_COMPACTION event when using small context window")
	}
	if compaction.Layer != "checkpoint" {
		t.Fatalf("compaction layer = %q, want checkpoint", compaction.Layer)
	}
	if compaction.EstTokensAfter > compaction.EstTokensBefore {
		t.Fatalf("compaction raised estimated tokens: before %d, after %d", compaction.EstTokensBefore, compaction.EstTokensAfter)
	}
}

// contextWindowForRead sizes a context window from dir's own standing request
// and returns a big.txt body whose tool result lifts pressure from that
// standing request to about 92% of the window. The window is 1.5x the standing
// request: the first round reports the standing request as usage (two thirds of
// the window, below the 90% checkpoint trigger) and the read adds the last
// quarter, so only the read can cross the trigger while admission stays under
// the window. Sizing from the measured request keeps the bands from moving with
// host-dependent prompt size (#6090).
func contextWindowForRead(t *testing.T, dir, input string) (standing, window int, bigContent string) {
	t.Helper()
	profile := NewOpenAIProfile("gpt-5.2")
	standing = standingRequestTokens(t, dir, profile, SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}, input)
	if standing <= 0 {
		t.Fatalf("standingRequestTokens = %d, want a positive measurement", standing)
	}
	window = standing * 3 / 2
	bigTokens := window*92/100 - standing
	if bigTokens <= 0 {
		t.Fatalf("window %d leaves no room for a read above standing %d", window, standing)
	}
	// 16 bytes per line is four tokens at the estimator's four bytes per token.
	return standing, window, strings.Repeat("line of content\n", bigTokens/4)
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
	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}), withStateDir(dir))
	defer sess.Close()

	store := sess.getOrCreateTaskStore()
	if _, err := store.Append([]task.TaskInput{{Description: "Frobnicate the gizmo"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Update([]task.TaskUpdate{{ID: 1, Status: task.TaskInProgress}}); err != nil {
		t.Fatalf("Update in_progress: %v", err)
	}

	meta := sess.buildCompactionMeta()
	// SessionID should be exactly sess.id — task descriptions must not appear in it.
	if meta.SessionID != sess.id {
		t.Fatalf("expected SessionID %q, got %q", sess.id, meta.SessionID)
	}
	// ActivatedSkills is never populated by buildCompactionMeta; assert it remains nil.
	if len(meta.ActivatedSkills) != 0 {
		t.Fatalf("task state leaked into ActivatedSkills: %v", meta.ActivatedSkills)
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
		sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}), withStateDir(dir))
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
		sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))
		defer sess.Close()

		meta := sess.buildCompactionMeta()
		if meta.SessionID != "" {
			t.Fatalf("expected SessionID to be empty without stateDir, got %q", meta.SessionID)
		}
	})
}

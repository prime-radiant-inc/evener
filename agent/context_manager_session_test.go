package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
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
	// The window is sized from this session fixture's standing request and the
	// first round reports that request as its usage, below the 90% checkpoint
	// trigger. The big read alone must lift pressure over the trigger; a
	// one-line read must not, so the tool result is what this test proves
	// drives the compaction.
	profile, standing, bigContent := contextWindowForRead(t, dir, "read the file")
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
			readArgs, err := json.Marshal(map[string]any{"file_path": "big.txt", "limit": strings.Count(tc.content, "\n")})
			if err != nil {
				t.Fatalf("encode read_file arguments: %v", err)
			}

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
										Arguments: readArgs,
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

			sess := newSession(t, withClient(c), withProfile(profile), withEnv(env), withConfig(SessionConfig{}))

			// Collect events to verify compaction actually ran (not just that the adapter
			// was called twice). Without this, commenting out MaybeCompact still passes.
			eventsDone := captureSessionEvents(sess)

			// Verify the session is configured with the computed window.
			if sess.Profile().ContextWindowSize() != profile.ContextWindowSize() {
				t.Fatalf("expected context window %d, got %d", profile.ContextWindowSize(), sess.Profile().ContextWindowSize())
			}

			ctx := context.Background()
			_, err = sess.ProcessInput(ctx, "read the file", nil)
			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}

			if callCount != 2 {
				t.Fatalf("expected 2 LLM calls, got %d", callCount)
			}

			sess.Close()
			evs := <-eventsDone

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
	profile, standing, bigContent := contextWindowForRead(t, dir, "read the file")
	c := llm.NewClient()
	readArgs, err := json.Marshal(map[string]any{"file_path": "big.txt", "limit": strings.Count(bigContent, "\n")})
	if err != nil {
		t.Fatalf("encode read_file arguments: %v", err)
	}

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
								Arguments: readArgs,
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

	sess := newSession(t, withClient(c), withProfile(profile), withEnv(env), withConfig(SessionConfig{}))

	eventsDone := captureSessionEvents(sess)

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "read the file", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	evs := <-eventsDone

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

// contextWindowForRead returns the fixture's profile, its measured standing
// request, and a big.txt body whose read_file result lifts pressure from that
// standing request to about 93% of the profile's window. The first round
// reports the standing request as usage, so the read is the only thing that can
// cross the 90% checkpoint trigger, while the window keeps the post-read
// request admitted. The body is sized through the estimator's 21 output bytes
// per numbered line (5.25 tokens), and capped under read_file's 50,000-character
// output limit, so neither a larger host prompt nor truncation moves the band.
// Sizing from the measured request keeps it from moving with host-dependent
// prompt size (#6090).
func contextWindowForRead(t *testing.T, dir, input string) (profile *provider.Profile, standing int, bigContent string) {
	t.Helper()
	profile = NewOpenAIProfile("gpt-5.2")
	standing = standingRequestTokens(t, dir, profile, SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}, input)
	if standing <= 0 {
		t.Fatalf("standingRequestTokens = %d, want a positive measurement", standing)
	}
	// read_file renders each line as "%4d\t" + content + newline: 21 bytes for
	// the 16-byte fixture line, which the estimator prices at 5.25 tokens.
	// Cap the body at 2,300 lines so the rendered result stays under the
	// 50,000-character output limit instead of being truncated.
	lines := standing * 38 / 100 * 4 / 21
	if maxLines := 2_300; lines > maxLines {
		lines = maxLines
	}
	toolTokens := lines * 21 / 4
	profile = withContextWindow(profile, standing+toolTokens+standing/10)
	return profile, standing, strings.Repeat("line of content\n", lines)
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

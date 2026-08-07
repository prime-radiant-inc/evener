//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

type toolRoundTailStrategy struct {
	after func(context.Context) error
}

func (toolRoundTailStrategy) Name() string                 { return "tool-round-tail" }
func (toolRoundTailStrategy) Tools() []tool.RegisteredTool { return nil }
func (toolRoundTailStrategy) ManageContext(context.Context, *[]schema.Turn, int, func(events.EventKind, events.EventData)) error {
	return nil
}
func (s toolRoundTailStrategy) AfterAction(ctx context.Context, _ []schema.Turn, _ *llm.Client) error {
	return s.after(ctx)
}

var _ contextmgr.Strategy = toolRoundTailStrategy{}

func toolRoundTailSession(t *testing.T, adapter *agenttest.FakeAdapter) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	s, err := NewSession(client, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s.clock = agenttest.NewFakeClock()
	t.Cleanup(s.Close)
	return s
}

func registerToolRoundTailTool(t *testing.T, s *Session, name string, readOnly bool, exec func(context.Context) (any, error)) {
	t.Helper()
	err := s.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name: name, Description: "deterministic coverage tool",
			Parameters: map[string]any{"type": "object", "additionalProperties": false},
		}, ReadOnly: readOnly},
		OmitPurpose: true,
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, _ map[string]any) (any, error) {
			return exec(ctx)
		},
	})
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func FuzzSessionToolRoundTailCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		adapter := &agenttest.FakeAdapter{Provider: "openai"}
		s := toolRoundTailSession(t, adapter)

		t.Run("defensive decision and panic tails", func(t *testing.T) {
			if retry, err := s.applyNoToolCallsDecision(noToolCallsDecision{}); retry || err != nil {
				t.Fatalf("empty decision = retry %v err %v", retry, err)
			}
			if err := toolBatchPanicError(errors.New("scripted panic error")); err == nil {
				t.Fatal("error panic value was not returned")
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != "scripted non-error panic" {
						t.Fatalf("recovered panic = %#v", recovered)
					}
				}()
				_ = toolBatchPanicError("scripted non-error panic")
			}()
		})

		t.Run("canceled parallel tail", func(t *testing.T) {
			registerToolRoundTailTool(t, s, "tail_read", true, func(context.Context) (any, error) { return "ok", nil })
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			calls := []llm.ToolCallData{{ID: "r1", Name: "tail_read", Arguments: []byte(`{}`)}, {ID: "r2", Name: "tail_read", Arguments: []byte(`{}`)}}
			if _, err := s.execToolBatch(ctx, calls, NewOpenAIProfile("gpt-5"), ""); !errors.Is(err, context.Canceled) {
				t.Fatalf("parallel cancellation = %v", err)
			}
		})

		t.Run("cancel after serial execution", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			registerToolRoundTailTool(t, s, "tail_cancel", false, func(context.Context) (any, error) {
				cancel()
				return "done", nil
			})
			calls := []llm.ToolCallData{{ID: "w1", Name: "tail_cancel", Arguments: []byte(`{}`)}}
			if _, err := s.execToolBatch(ctx, calls, NewOpenAIProfile("gpt-5"), ""); !errors.Is(err, context.Canceled) {
				t.Fatalf("serial tail cancellation = %v", err)
			}
		})

		t.Run("persist cancellation", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := s.persistToolResults(ctx, []llm.ToolCallData{{ID: "p1", Name: "tail_read"}}, []tool.ExecResult{{CallID: "p1", ToolName: "tail_read"}}); !errors.Is(err, context.Canceled) {
				t.Fatalf("persist cancellation = %v", err)
			}
		})

		t.Run("cancel after image description", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			visionAdapter := &agenttest.FakeAdapter{Provider: "openai", Steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					cancel()
					return llm.Response{Message: llm.Assistant("scripted description")}
				},
			}}
			visionSession := toolRoundTailSession(t, visionAdapter)
			calls := []llm.ToolCallData{{ID: "image", Name: "read_image", Arguments: []byte(`{"file_path":"fixture.png"}`)}}
			results := []tool.ExecResult{{CallID: "image", ToolName: "read_image", ImageData: []byte("fixture"), ImageMediaType: "image/png"}}
			if err := visionSession.persistToolResults(ctx, calls, results); !errors.Is(err, context.Canceled) {
				t.Fatalf("post-description cancellation = %v", err)
			}
		})

		t.Run("strategy tails", func(t *testing.T) {
			if err := (&Session{}).notifyStrategyAfterAction(context.Background()); err != nil {
				t.Fatalf("nil strategy = %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s.strategy = toolRoundTailStrategy{after: func(context.Context) error { return nil }}
			if err := s.notifyStrategyAfterAction(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("pre-canceled strategy = %v", err)
			}

			ctx, cancel = context.WithCancel(context.Background())
			s.strategy = toolRoundTailStrategy{after: func(context.Context) error { cancel(); return nil }}
			if err := s.notifyStrategyAfterAction(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("post-action cancellation = %v", err)
			}

			ctx, cancel = context.WithCancel(context.Background())
			s.strategy = toolRoundTailStrategy{after: func(context.Context) error {
				cancel()
				return errors.New("scripted strategy failure")
			}}
			if err := s.notifyStrategyAfterAction(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("error-warning cancellation = %v", err)
			}
		})

		t.Run("loop steering", func(t *testing.T) {
			enabled := true
			s.cfg.EnableLoopDetection = &enabled
			s.cfg.LoopDetectionWindow = 2
			calls := []llm.ToolCallData{{ID: "loop", Name: "tail_read", Arguments: []byte(`{}`)}}
			sigs := []string{"tail_read:" + shortHash([]byte(`{}`))}
			var sigFailed []bool
			if _, err := s.injectPostToolSteering(context.Background(), calls, nil, &sigs, &sigFailed); err != nil {
				t.Fatalf("loop steering: %v", err)
			}
			if s.loopDetectionCount == 0 {
				t.Fatal("loop detection did not fire")
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// Both accumulators are reset together: injectPostToolSteering reads
			// them as one window, so letting them drift here would mask exactly
			// the misalignment the failure-loop rule depends on.
			sigs = []string{"tail_read:" + shortHash([]byte(`{}`))}
			sigFailed = []bool{false}
			if _, err := s.injectPostToolSteering(ctx, calls, nil, &sigs, &sigFailed); !errors.Is(err, context.Canceled) {
				t.Fatalf("loop warning cancellation = %v", err)
			}

			watchErr := errors.New("scripted watch drain failure")
			ctx = context.WithValue(context.Background(), sessionToolRoundHooksKey{}, sessionToolRoundHooks{drainErr: watchErr})
			if _, err := s.injectPostToolSteering(ctx, nil, nil, &sigs, &sigFailed); !errors.Is(err, watchErr) {
				t.Fatalf("watch drain error = %v", err)
			}

			ctx, cancel = context.WithCancel(context.Background())
			ctx = context.WithValue(ctx, sessionToolRoundHooksKey{}, sessionToolRoundHooks{beforeSteering: cancel})
			if _, err := s.injectPostToolSteering(ctx, nil, nil, &sigs, &sigFailed); !errors.Is(err, context.Canceled) {
				t.Fatalf("steering checkpoint cancellation = %v", err)
			}

			ctx, cancel = context.WithCancel(context.Background())
			ctx = context.WithValue(ctx, sessionToolRoundHooksKey{}, sessionToolRoundHooks{beforeTaskReminder: cancel})
			if _, err := s.injectPostToolSteering(ctx, nil, nil, &sigs, &sigFailed); !errors.Is(err, context.Canceled) {
				t.Fatalf("task reminder checkpoint cancellation = %v", err)
			}
		})

		t.Run("delivery boundaries and stop hook", func(t *testing.T) {
			s.mu.Lock()
			s.state = SessionProcessing
			s.comm = communicateResult{}
			s.mu.Unlock()
			done, _ := s.deliverIfCommunicated(context.Background(), true)
			if !done || s.State() != SessionAwaiting {
				t.Fatalf("ask boundary = done %v state %v", done, s.State())
			}

			hookAdapter := &agenttest.FakeAdapter{Provider: "openai", Steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant(`{"decision":"block","reason":"keep working","hookSpecificOutput":{"additionalContext":"model note"},"systemMessage":"user note"}`)}
				},
			}}
			hookClient := llm.NewClient()
			hookClient.Register(hookAdapter)
			runner := hooks.NewRunner(hookClient, "gpt-5")
			runner.Add(plugin.HookStop, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "decide"})
			s.hookRunner = runner
			s.mu.Lock()
			s.state = SessionProcessing
			s.comm = communicateResult{called: true, reply: "reply"}
			s.mu.Unlock()
			done, _ = s.deliverIfCommunicated(context.Background(), false)
			if done || len(hookAdapter.Requests()) != 1 {
				t.Fatalf("blocked stop hook = done %v requests %d", done, len(hookAdapter.Requests()))
			}
		})
	})
}

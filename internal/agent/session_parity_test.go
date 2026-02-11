package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
)

// providerCase defines a profile + adapter name pair for cross-provider parity tests.
type providerCase struct {
	name        string // human-readable
	adapterName string // fakeAdapter.Name()
	profile     func(model string) ProviderProfile
}

var providerCases = []providerCase{
	{"openai", "openai", NewOpenAIProfile},
	{"anthropic", "anthropic", NewAnthropicProfile},
	{"gemini", "google", NewGeminiProfile},
}

// newParitySession creates a session with the given provider and fakeAdapter steps.
func newParitySession(t *testing.T, pc providerCase, steps []func(llm.Request) llm.Response) (*Session, *fakeAdapter) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: pc.adapterName, steps: steps}
	c.Register(f)
	sess, err := NewSession(c, pc.profile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession(%s): %v", pc.name, err)
	}
	return sess, f
}

// collectEvents drains Events() channel into a slice.
// Returns a pointer to the slice so the goroutine's appends are visible to the caller.
func collectEvents(sess *Session) (*[]SessionEvent, *sync.Mutex, <-chan struct{}) {
	events := &[]SessionEvent{}
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			*events = append(*events, ev)
			mu.Unlock()
		}
		close(done)
	}()
	return events, &mu, done
}

func TestParity_SimpleFileCreation(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalWriteFile(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"file_path":"test.txt","content":"hello world"}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "create test.txt")
			sess.Close()

			dir := sess.env.WorkingDirectory()
			data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
			if err != nil {
				t.Fatalf("file not created: %v", err)
			}
			if string(data) != "hello world" {
				t.Fatalf("content: got %q", string(data))
			}
		})
	}
}

func TestParity_ReadFileThenEdit(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalReadFile(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"file_path":"target.txt"}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								makeEditCall(pc.name, "c2", "target.txt", "foo", "bar"),
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			// Pre-create the file.
			dir := sess.env.WorkingDirectory()
			os.WriteFile(filepath.Join(dir, "target.txt"), []byte("foo"), 0644)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "edit target.txt")
			sess.Close()

			data, _ := os.ReadFile(filepath.Join(dir, "target.txt"))
			if string(data) != "bar" {
				t.Fatalf("edit failed: got %q", string(data))
			}
		})
	}
}

func TestParity_ShellCommandExecution(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalShell(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"command":"echo parity_ok"}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, f := newParitySession(t, pc, steps)
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "run echo")
			sess.Close()

			// Tool result should contain shell output.
			reqs := f.Requests()
			found := false
			for _, r := range reqs {
				for _, m := range r.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentToolResult && strings.Contains(fmt.Sprint(p.ToolResult.Content), "parity_ok") {
							found = true
						}
					}
				}
			}
			if !found {
				t.Fatal("shell output not found in tool results")
			}
		})
	}
}

func TestParity_ShellCommandTimeout(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalShell(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"command":"sleep 30","timeout_ms":50}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("timed out")}
				},
			}
			sess, f := newParitySession(t, pc, steps)
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "run slow")
			sess.Close()

			// Tool result should mention timeout.
			reqs := f.Requests()
			found := false
			for _, r := range reqs {
				for _, m := range r.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentToolResult && strings.Contains(fmt.Sprint(p.ToolResult.Content), "timed_out") {
							found = true
						}
					}
				}
			}
			if !found {
				t.Fatal("timeout not reflected in tool results")
			}
		})
	}
}

func TestParity_GrepAndGlob(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalGlob(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"pattern":"*.txt"}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c2", Name: canonicalGrep(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"pattern":"needle","path":"."}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			// Create files to find.
			dir := sess.env.WorkingDirectory()
			os.WriteFile(filepath.Join(dir, "haystack.txt"), []byte("needle in here"), 0644)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "search")
			sess.Close()
		})
	}
}

func TestParity_MultiStepTask(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				// Step 1: Read file.
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalReadFile(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"file_path":"config.txt"}`),
								}},
							},
						},
					}
				},
				// Step 2: Analyze and edit.
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								makeEditCall(pc.name, "c2", "config.txt", "debug=false", "debug=true"),
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			dir := sess.env.WorkingDirectory()
			os.WriteFile(filepath.Join(dir, "config.txt"), []byte("debug=false"), 0644)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "update config")
			sess.Close()

			data, _ := os.ReadFile(filepath.Join(dir, "config.txt"))
			if string(data) != "debug=true" {
				t.Fatalf("multi-step edit failed: got %q", string(data))
			}
		})
	}
}

func TestParity_ParallelToolCalls(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalShell(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"command":"echo first"}`),
								}},
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c2", Name: canonicalShell(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"command":"echo second"}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, f := newParitySession(t, pc, steps)
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "run both")
			sess.Close()

			// Both tool results should be in the second request.
			reqs := f.Requests()
			if len(reqs) < 2 {
				t.Fatalf("expected at least 2 requests, got %d", len(reqs))
			}
			toolResults := 0
			for _, m := range reqs[1].Messages {
				for _, p := range m.Content {
					if p.Kind == llm.ContentToolResult {
						toolResults++
					}
				}
			}
			if toolResults < 2 {
				t.Fatalf("expected 2 tool results in second request, got %d", toolResults)
			}
		})
	}
}

func TestParity_ErrorRecovery(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			step := 0
			steps := []func(llm.Request) llm.Response{
				// First call: read a nonexistent file (will error).
				func(req llm.Request) llm.Response {
					step++
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: canonicalReadFile(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"file_path":"nonexistent.txt"}`),
								}},
							},
						},
					}
				},
				// Second call: model should see the error and try something else.
				func(req llm.Request) llm.Response {
					step++
					// Verify the error was received.
					for _, m := range req.Messages {
						for _, p := range m.Content {
							if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.IsError {
								return llm.Response{Message: llm.Assistant("handled error")}
							}
						}
					}
					return llm.Response{Message: llm.Assistant("no error seen")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := sess.ProcessInput(ctx, "try reading")
			sess.Close()

			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if !strings.Contains(result, "handled error") {
				t.Fatalf("error recovery failed: got %q", result)
			}
		})
	}
}

func TestParity_LoopDetectionWarning(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			// Deliberately create a repeating pattern to trigger loop detection.
			callNum := 0
			steps := []func(llm.Request) llm.Response{
				// Repeat the same tool call many times.
				func(req llm.Request) llm.Response {
					callNum++
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: fmt.Sprintf("c%d", callNum), Name: canonicalShell(pc.name), Type: "function",
									Arguments: json.RawMessage(`{"command":"echo loop"}`),
								}},
							},
						},
					}
				},
			}
			// Extend the steps to repeat many times for loop detection.
			base := steps[0]
			for i := 0; i < 19; i++ {
				steps = append(steps, base)
			}
			// Final response to end the session.
			steps = append(steps, func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			})

			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			eventsPtr, mu, doneCh := collectEvents(sess)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "do something")
			sess.Close()
			<-doneCh

			mu.Lock()
			defer mu.Unlock()
			loopDetected := false
			for _, ev := range *eventsPtr {
				if ev.Kind == EventLoopDetection {
					loopDetected = true
				}
			}
			if !loopDetected {
				t.Fatal("expected LOOP_DETECTION event")
			}
		})
	}
}

func TestParity_SteeringMidTask(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})

			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c1", Name: "slow_tool", Type: "function",
									Arguments: json.RawMessage(`{}`),
								}},
							},
						},
					}
				},
				func(req llm.Request) llm.Response {
					// Check that steering message is injected.
					for _, m := range req.Messages {
						if m.Role == llm.RoleUser {
							for _, p := range m.Content {
								if p.Kind == llm.ContentText && strings.Contains(p.Text, "steer me") {
									return llm.Response{Message: llm.Assistant("steered")}
								}
							}
						}
					}
					return llm.Response{Message: llm.Assistant("not steered")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()
			_ = sess.reg.Register(RegisteredTool{
				Definition: llm.ToolDefinition{Name: "slow_tool"},
				Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
					started <- struct{}{}
					<-release
					return "ok", nil
				},
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan string, 1)
			go func() {
				result, _ := sess.ProcessInput(ctx, "start")
				done <- result
			}()

			<-started
			sess.Steer("steer me")
			close(release)

			result := <-done
			sess.Close()
			if !strings.Contains(result, "steered") {
				t.Fatalf("steering not applied: got %q", result)
			}
		})
	}
}

func TestParity_MultiFileEdit(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				// Round 1: Read both files.
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID: "r1", Name: canonicalReadFile(pc.name), Type: "function",
								Arguments: json.RawMessage(`{"file_path":"a.txt"}`),
							}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID: "r2", Name: canonicalReadFile(pc.name), Type: "function",
								Arguments: json.RawMessage(`{"file_path":"b.txt"}`),
							}},
						},
					}}
				},
				// Round 2: Edit both files.
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							makeEditCall(pc.name, "e1", "a.txt", "alpha", "ALPHA"),
							makeEditCall(pc.name, "e2", "b.txt", "beta", "BETA"),
						},
					}}
				},
				// Round 3: Done.
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}

			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			// Pre-create both files.
			dir := sess.env.WorkingDirectory()
			os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0644)
			os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta"), 0644)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "edit both files")
			sess.Close()

			// Verify both files were edited.
			dataA, err := os.ReadFile(filepath.Join(dir, "a.txt"))
			if err != nil {
				t.Fatalf("a.txt: %v", err)
			}
			if string(dataA) != "ALPHA" {
				t.Errorf("a.txt: got %q, want %q", string(dataA), "ALPHA")
			}

			dataB, err := os.ReadFile(filepath.Join(dir, "b.txt"))
			if err != nil {
				t.Fatalf("b.txt: %v", err)
			}
			if string(dataB) != "BETA" {
				t.Errorf("b.txt: got %q, want %q", string(dataB), "BETA")
			}
		})
	}
}

func TestParity_ToolOutputTruncation(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "c1", Name: canonicalReadFile(pc.name), Type: "function",
							Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
						}}},
					}}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, f := newParitySession(t, pc, steps)
			defer sess.Close()

			// Create a file larger than the read_file limit (50,000 chars).
			dir := sess.env.WorkingDirectory()
			big := strings.Repeat("x", 60_000)
			os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0644)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "read big file")
			sess.Close()

			// Verify the tool result sent to the model was truncated.
			reqs := f.Requests()
			if len(reqs) < 2 {
				t.Fatalf("expected at least 2 requests, got %d", len(reqs))
			}
			found := false
			for _, m := range reqs[1].Messages {
				for _, p := range m.Content {
					if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
						if s, ok := p.ToolResult.Content.(string); ok {
							if strings.Contains(s, "Tool output was truncated") {
								found = true
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("expected truncation marker in tool result for oversized read_file output")
			}
		})
	}
}

func TestParity_ReasoningEffort(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("first")}
				},
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("second")}
				},
			}
			sess, f := newParitySession(t, pc, steps)
			sess.cfg.ReasoningEffort = "low"
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "first")

			sess.SetReasoningEffort("high")
			sess.ProcessInput(ctx, "second")
			sess.Close()

			reqs := f.Requests()
			if len(reqs) < 2 {
				t.Fatalf("expected at least 2 requests, got %d", len(reqs))
			}
			if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != "low" {
				t.Errorf("first call: got %v, want 'low'", reqs[0].ReasoningEffort)
			}
			if reqs[1].ReasoningEffort == nil || *reqs[1].ReasoningEffort != "high" {
				t.Errorf("second call: got %v, want 'high'", reqs[1].ReasoningEffort)
			}
		})
	}
}

func TestParity_SubagentSpawnAndWait(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			// Use reg.ExecuteCall to drive subagent lifecycle deterministically,
			// avoiding races from interleaved parent/subagent LLM calls.
			steps := []func(llm.Request) llm.Response{
				// Subagent's single LLM call — returns text.
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("subagent completed task")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()
			go func() { for range sess.Events() {} }()

			// Spawn agent via registry.
			spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
				ID: "c1", Name: "spawn_agent", Type: "function",
				Arguments: json.RawMessage(`{"task":"hello from subagent"}`),
			})
			if spawnRes.IsError {
				t.Fatalf("spawn_agent error: %s", spawnRes.Output)
			}
			var spawned map[string]any
			if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
				t.Fatalf("unmarshal spawn output: %v (out=%q)", err, spawnRes.Output)
			}
			agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))
			if agentID == "" {
				t.Fatalf("missing agent_id in spawn output: %v", spawned)
			}

			// Wait for the subagent to finish.
			waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
				ID: "c2", Name: "wait", Type: "function",
				Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
			})
			if waitRes.IsError {
				t.Fatalf("wait error: %s", waitRes.Output)
			}

			var result SubAgentResult
			if err := json.Unmarshal([]byte(waitRes.Output), &result); err != nil {
				t.Fatalf("unmarshal wait result: %v (out=%q)", err, waitRes.Output)
			}
			if !result.Success {
				t.Fatalf("expected subagent success, got failure: %+v", result)
			}
			if !strings.Contains(result.Output, "subagent completed task") {
				t.Errorf("expected subagent output in result, got: %q", result.Output)
			}
		})
	}
}

// canonicalXxx returns the wire-name for a tool given the provider name.
// This mirrors the ToolNameMap applied by each profile.
func canonicalWriteFile(provider string) string { return "write_file" }
func canonicalReadFile(provider string) string  { return "read_file" }
func canonicalEditFile(provider string) string {
	if provider == "openai" {
		return "apply_patch"
	}
	return "edit_file"
}

func canonicalShell(provider string) string {
	switch provider {
	case "openai":
		return "exec_command"
	case "gemini":
		return "run_shell_command"
	default:
		return "shell"
	}
}

func canonicalGlob(provider string) string {
	switch provider {
	case "openai":
		return "list_dir"
	case "gemini":
		return "list_directory"
	default:
		return "glob"
	}
}

func canonicalGrep(provider string) string {
	switch provider {
	case "openai":
		return "grep_files"
	case "gemini":
		return "grep_search"
	default:
		return "grep"
	}
}

// makeEditCall constructs a tool call for editing a file, using the provider-aligned tool.
// OpenAI uses apply_patch with v4a format; Anthropic/Gemini use edit_file.
func makeEditCall(provider, id, file, old, new_ string) llm.ContentPart {
	if provider == "openai" {
		patch := fmt.Sprintf("*** Begin Patch\n*** Update File: %s\n@@\n-%s\n+%s\n*** End Patch", file, old, new_)
		return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: id, Name: "apply_patch", Type: "function",
			Arguments: json.RawMessage(fmt.Sprintf(`{"patch":%q}`, patch)),
		}}
	}
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: id, Name: "edit_file", Type: "function",
		Arguments: json.RawMessage(fmt.Sprintf(`{"file_path":%q,"old_string":%q,"new_string":%q}`, file, old, new_)),
	}}
}

package llm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/llm/registry"
)

// toolStampRegistry is the smallest registry that makes a Generate request
// resolve, so the dispatch seam prices the request with ApplyTokenBudget —
// the site that estimates once per dispatched attempt (#6437).
func toolStampRegistry(t *testing.T, instance string) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true),
		registry.WithoutCache(),
		registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			instance: {Base: "openai", APIKey: "unused", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9"}},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

// TestGenerateStampsTheToolCatalogLength pins that the high-level generation
// path measures its tool list once and stamps every request it builds with the
// length, so no per-step or per-dispatch token estimate re-serializes the
// catalog (#6437, the shape #6406 fixed for sessions).
//
// The generation's tool list is built once in prepareGeneration and never
// reassigned, so the stamp describes exactly the slice every request carries.
func TestGenerateStampsTheToolCatalogLength(t *testing.T) {
	// Not parallel: testing.AllocsPerRun reads the process-wide allocator and
	// panics when called from a parallel test.
	const instance = "stamped"
	c := NewClient(WithRegistry(toolStampRegistry(t, instance)))
	a := &scriptedAdapter{name: instance}
	c.Register(a)

	prompt := "hello"
	// Passive tools: no Execute handler, so the first response ends the loop and
	// the generation dispatches exactly one request. The catalog is shaped like
	// an advertised session set, which is what makes its marshal dominate the
	// estimate.
	tools := make([]Tool, 0, 32)
	for _, def := range benchmarkToolDefs(32) {
		tools = append(tools, Tool{Definition: def})
	}
	if _, err := Generate(context.Background(), GenerateOptions{
		Client:   c,
		Model:    "tool-stamp-probe",
		Provider: instance,
		Prompt:   &prompt,
		Tools:    tools,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got, want := len(a.reqs), 1; got != want {
		t.Fatalf("dispatched %d requests, want %d", got, want)
	}

	req := a.reqs[0]
	if len(req.Tools) == 0 {
		t.Fatal("dispatched request carried no tools")
	}
	res, err := c.Resolve(instance + "/tool-stamp-probe")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Subtests so a red run reports every claim, not only the first: the
	// allocation count is the cost witness and the length is the correctness
	// witness, and a fix that satisfies one but not the other is still broken.
	t.Run("costs no marshal", func(t *testing.T) {
		if allocs := testing.AllocsPerRun(20, func() {
			_ = EstimateInputTokensForResolved(res, req)
		}); allocs != 0 {
			t.Fatalf("estimate allocates %.0f times per call, want 0: the tool catalog is re-marshalled", allocs)
		}
	})
	t.Run("stamp describes the list", func(t *testing.T) {
		if got, want := req.ToolsJSONBytes, ToolsJSONBytes(req.Tools); got != want {
			t.Fatalf("dispatched request ToolsJSONBytes = %d, want the catalog's serialized length %d", got, want)
		}
	})
	t.Run("estimate is unchanged", func(t *testing.T) {
		// A faster estimate that returns a different number is a bug.
		unstamped := req
		unstamped.ToolsJSONBytes = 0
		if got, want := EstimateInputTokensForResolved(res, req).Tokens, EstimateInputTokensForResolved(res, unstamped).Tokens; got != want {
			t.Fatalf("stamped estimate = %d, want the re-marshalled %d", got, want)
		}
	})
}

// TestGenerateStampsEveryStepOfAToolLoop pins that the stamp survives the tool
// loop: each step builds a new request from the same generation state, and a
// request rebuilt without the stamp would pay the catalog marshal again on the
// second dispatch — the round that actually follows a tool result (#6437).
func TestGenerateStampsEveryStepOfAToolLoop(t *testing.T) {
	const instance = "loopstamped"
	c := NewClient(WithRegistry(toolStampRegistry(t, instance)))
	call := ToolCallData{ID: "call1", Name: "probe", Arguments: json.RawMessage(`{}`), Type: "function"}
	a := &scriptedAdapter{
		name: instance,
		steps: []func(req Request) (Response, error){
			func(Request) (Response, error) {
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(Request) (Response, error) {
				return Response{Message: Assistant("done")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "go"
	oneRound := 1
	if _, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "tool-stamp-probe",
		Provider:      instance,
		Prompt:        &prompt,
		MaxToolRounds: &oneRound,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "probe", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute:    func(context.Context, any) (any, error) { return "ok", nil },
		}},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got, want := len(a.reqs), 2; got != want {
		t.Fatalf("dispatched %d requests, want %d (one per tool round)", got, want)
	}
	for i, req := range a.reqs {
		if got, want := req.ToolsJSONBytes, ToolsJSONBytes(req.Tools); got != want {
			t.Fatalf("step %d ToolsJSONBytes = %d, want the catalog's %d", i, got, want)
		}
	}
}

// TestStreamGenerateStampsTheToolCatalogLength covers the streaming twin: it
// shares prepareGeneration, and its loop builds requests the same way, so a
// stamp applied on only one of the two entry points would leave a streamed
// round paying the marshal (#6437).
func TestStreamGenerateStampsTheToolCatalogLength(t *testing.T) {
	const instance = "streamstamped"
	finish := Response{Message: Assistant("ok"), Finish: FinishReason{Reason: "stop"}}
	a := &scriptedStreamAdapter{name: instance, scripts: []func(ctx context.Context, req Request) (Stream, error){replayEvents(
		StreamEvent{Type: StreamEventStreamStart},
		StreamEvent{Type: StreamEventTextStart, TextID: "t1"},
		StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "ok"},
		StreamEvent{Type: StreamEventTextEnd, TextID: "t1"},
		finishEvent(finish),
	)}}
	c := NewClient(WithRegistry(toolStampRegistry(t, instance)))
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "tool-stamp-probe",
		Provider: instance,
		Prompt:   &prompt,
		Tools:    []Tool{{Definition: benchmarkToolDefs(4)[0]}},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer out.Close() //nolint:errcheck
	for range out.Events() {
	}
	reqs := a.Requests()
	if len(reqs) != 1 {
		t.Fatalf("dispatched %d requests, want 1", len(reqs))
	}
	if got, want := reqs[0].ToolsJSONBytes, ToolsJSONBytes(reqs[0].Tools); got != want {
		t.Fatalf("streamed request ToolsJSONBytes = %d, want the catalog's %d", got, want)
	}
}

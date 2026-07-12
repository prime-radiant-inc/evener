package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func FuzzGenerateEdges(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		ctx := context.Background()
		if _, ok := ToolCallContextFromCtx(ctx); ok {
			t.Fatal("unexpected tool context")
		}

		defaultClientMu.Lock()
		savedClient, savedErr, savedInit := defaultClient, errDefaultClient, defaultClientInit
		defaultClient, errDefaultClient, defaultClientInit = nil, errors.New("default unavailable"), true
		defaultClientMu.Unlock()
		if _, err := prepareGeneration(GenerateOptions{Model: "m", Prompt: ptrString("p")}); err == nil {
			t.Fatal("expected default client error")
		}
		defaultClientMu.Lock()
		defaultClient, errDefaultClient, defaultClientInit = savedClient, savedErr, savedInit
		defaultClientMu.Unlock()

		client := NewClient()
		client.Register(&scriptedFuzzAdapter{script: []scriptStep{{finish: FinishReasonStop}}})
		neg := -1
		system := "system"
		if gs, err := prepareGeneration(GenerateOptions{Client: client, Model: "m", Messages: []Message{User("u")}, System: &system, MaxToolRounds: &neg}); err != nil || gs.maxToolRounds != 0 {
			t.Fatalf("negative rounds: %+v %v", gs, err)
		}
		if _, err := Generate(ctx, GenerateOptions{Client: client, Model: "", Prompt: ptrString("p")}); err == nil {
			t.Fatal("expected validation error")
		}

		if _, _, err := prepareTools([]Tool{{Definition: ToolDefinition{Name: "bad-name", Parameters: map[string]any{"type": "object"}}}}); err == nil {
			t.Fatal("expected bad name")
		}
		if _, _, err := prepareTools([]Tool{{Definition: ToolDefinition{Name: "x"}}}); err != nil {
			t.Fatalf("default params: %v", err)
		}
		if _, _, err := prepareTools([]Tool{{Definition: ToolDefinition{Name: "x", Parameters: map[string]any{"type": "array"}}}}); err == nil {
			t.Fatal("expected bad root")
		}
		if _, _, err := prepareTools([]Tool{{Definition: ToolDefinition{Name: "x", Parameters: map[string]any{"type": "object", "patternProperties": map[string]any{"[": map[string]any{}}}}}}); err == nil {
			t.Fatal("expected schema compile error")
		}
		if _, err := compileSchema(map[string]any{"type": make(chan int)}); err == nil {
			t.Fatal("expected marshal error")
		}
		t.Run("add-resource-error", func(t *testing.T) {
			compileSchemaHookMu.Lock()
			originalAddResource := compileSchemaAddResource
			compileSchemaAddResource = func(*jsonschema.Compiler, string, io.Reader) error { return errors.New("add resource") }
			t.Cleanup(func() {
				compileSchemaAddResource = originalAddResource
				compileSchemaHookMu.Unlock()
			})
			if _, err := compileSchemaUnlocked(map[string]any{"type": "object"}); err == nil {
				t.Fatal("expected add-resource error")
			}
		})

		results := make([]ToolResultData, 1)
		executeSingleToolCall(ctx, nil, ToolCallData{ID: "unknown", Name: "missing"}, nil, nil, results, 0)
		if !results[0].IsError {
			t.Fatal("unknown tool accepted")
		}

		schema, err := compileSchema(map[string]any{"type": "object", "required": []any{"x"}})
		if err != nil {
			t.Fatal(err)
		}
		seenContext := false
		tools := map[string]Tool{"x": {schema: schema, Execute: func(callCtx context.Context, _ any) (any, error) {
			v, ok := ToolCallContextFromCtx(callCtx)
			seenContext = ok && v.ToolCallID == "call"
			return nil, errors.New("execute failed")
		}}}
		executeSingleToolCall(ctx, tools, ToolCallData{ID: "call", Name: "x", Arguments: json.RawMessage(`{}`)}, nil,
			func(context.Context, ToolCallData, error) (json.RawMessage, error) {
				return nil, errors.New("repair failed")
			}, results, 0)
		if !results[0].IsError {
			t.Fatal("invalid args accepted")
		}
		executeSingleToolCall(ctx, tools, ToolCallData{ID: "call", Name: "x", Arguments: json.RawMessage(`{}`)}, nil,
			func(context.Context, ToolCallData, error) (json.RawMessage, error) {
				return json.RawMessage(`{"x":1}`), nil
			}, results, 0)
		if !results[0].IsError || !seenContext {
			t.Fatalf("execute failure/context: %+v %v", results[0], seenContext)
		}

		calls := []ToolCallData{{ID: "a", Name: "missing"}, {ID: "b", Name: "x", Arguments: json.RawMessage(`{"x":1}`)}}
		if got := executeToolCalls(ctx, tools, calls, nil, nil); len(got) != 2 {
			t.Fatalf("tool result count %d", len(got))
		}
		tools["ro"] = Tool{ReadOnly: true, schema: schema, Execute: func(context.Context, any) (any, error) { return "ok", nil }}
		calls = []ToolCallData{{ID: "ro", Name: "ro", Arguments: json.RawMessage(`{"x":1}`)}, {ID: "b", Name: "x", Arguments: json.RawMessage(`{"x":1}`)}}
		_ = executeToolCalls(ctx, tools, calls, nil, nil)
		if _, err := parseAndValidateArgs(nil, json.RawMessage(`{"x":1}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := parseAndValidateArgs(nil, json.RawMessage(`{`)); err == nil {
			t.Fatal("expected JSON error")
		}
		_, cancel := WithTimeout(ctx, time.Nanosecond)
		cancel()

		active := Tool{Definition: ToolDefinition{Name: "active", Parameters: map[string]any{"type": "object"}}, Execute: func(context.Context, any) (any, error) { return "ok", nil }}
		passive := Tool{Definition: ToolDefinition{Name: "passive", Parameters: map[string]any{"type": "object"}}}
		call := ToolCallData{ID: "p", Name: "passive", Arguments: json.RawMessage(`{}`)}
		loopClient := NewClient()
		loopClient.Register(&scriptedFuzzAdapter{script: []scriptStep{{calls: []ToolCallData{call}, finish: FinishReasonToolCalls}}})
		rounds := 2
		if _, err := Generate(ctx, GenerateOptions{Client: loopClient, Model: "m", Provider: "stub", Prompt: ptrString("p"), Tools: []Tool{active, passive}, MaxToolRounds: &rounds}); err != nil {
			t.Fatal(err)
		}

		call = ToolCallData{ID: "a", Name: "active", Arguments: json.RawMessage(`{}`)}
		loopClient = NewClient()
		loopClient.Register(&scriptedFuzzAdapter{script: []scriptStep{{calls: []ToolCallData{call}, finish: FinishReasonToolCalls}}})
		if _, err := Generate(ctx, GenerateOptions{Client: loopClient, Model: "m", Provider: "stub", Prompt: ptrString("p"), Tools: []Tool{active}, MaxToolRounds: &rounds, StopWhen: func([]StepResult) bool { return true }}); err != nil {
			t.Fatal(err)
		}
	})
}

func ptrString(s string) *string { return &s }

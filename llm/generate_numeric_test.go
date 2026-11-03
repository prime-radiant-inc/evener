package llm

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm/internal/numerictest"
)

func TestGenerateNumericValidationJourney(t *testing.T) {
	for _, tc := range []struct {
		name, invalid, valid string
		numberSchema         map[string]any
		want                 float64
	}{
		{"huge_bounds_rounded_token", `1.5`, `9007199254740993`, map[string]any{"type": "integer", "minimum": json.Number("-1e1000000000"), "maximum": json.Number("1e1000000000")}, 9007199254740992},
		{"underflow_stays_zero", `2`, `1e-1000000000`, map[string]any{"type": "number", "minimum": 0, "maximum": 1, "const": 0}, 0},
		{"tiny_bound_rejects_one", `1`, `1e-1000000000`, map[string]any{"type": "number", "minimum": 0, "maximum": json.Number("1e-1000000000")}, 0},
		{"overflow_stays_rejected", `1e400`, `1`, map[string]any{"type": "number", "minimum": 0, "maximum": 1}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			numerictest.Run(t, "tool-loop", func(t *testing.T) {
				params := map[string]any{"type": "object", "properties": map[string]any{"n": tc.numberSchema}, "required": []string{"n"}}
				t.Log("compiling nonnil tool schema through llm compileSchema")
				schema, err := compileSchema(params)
				if err != nil || schema == nil {
					t.Fatalf("compile: schema=%v err=%v", schema, err)
				}
				if err := schema.Validate(map[string]any{"n": "not-a-number"}); err == nil {
					t.Fatal("schema did not reject control")
				}
				calls := 0
				var received []float64
				toolCall := func(id, token string) Response {
					call := ToolCallData{ID: id, Name: "number", Type: "function", Arguments: json.RawMessage(`{"n":` + token + `}`)}
					return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}, Finish: FinishReason{Reason: FinishReasonToolCalls}}
				}
				checkContinuation := func(req Request, id string, wantError bool) {
					t.Helper()
					found := 0
					for _, m := range req.Messages {
						if m.Role != RoleTool {
							continue
						}
						for _, p := range m.Content {
							if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == id {
								found++
								if p.ToolResult.IsError != wantError {
									t.Fatalf("continuation %s: %+v", id, p.ToolResult)
								}
							}
						}
					}
					if found != 1 {
						t.Fatalf("continuation %s results=%d", id, found)
					}
				}
				a := &scriptedAdapter{name: "openai", steps: []func(Request) (Response, error){
					func(req Request) (Response, error) {
						if len(req.Tools) != 1 || req.Tools[0].Parameters == nil {
							t.Fatal("missing tool schema")
						}
						return toolCall("invalid", tc.invalid), nil
					},
					func(req Request) (Response, error) {
						if calls != 0 {
							t.Fatalf("invalid executed %d times", calls)
						}
						checkContinuation(req, "invalid", true)
						return toolCall("valid", tc.valid), nil
					},
					func(req Request) (Response, error) {
						checkContinuation(req, "valid", false)
						if calls != 1 {
							t.Fatalf("valid executions=%d", calls)
						}
						return Response{Message: Assistant("finished")}, nil
					},
				}}
				c := NewClient()
				c.Register(a)
				prompt, rounds := "validate numbers", 2
				res, err := Generate(context.Background(), GenerateOptions{Client: c, Model: "m", Prompt: &prompt, MaxToolRounds: &rounds, Tools: []Tool{{
					Definition: ToolDefinition{Name: "number", Parameters: params},
					Execute: func(_ context.Context, args any) (any, error) {
						calls++
						n, ok := args.(map[string]any)["n"].(float64)
						if !ok {
							t.Fatalf("ordinary Unmarshal: %T %+v", args, args)
						}
						received = append(received, n)
						return n, nil
					},
				}}})
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || a.i != 3 || len(res.Steps) != 3 {
					t.Fatalf("calls=%d scripts=%d steps=%d", calls, a.i, len(res.Steps))
				}
				if len(received) != 1 || received[0] != tc.want {
					t.Fatalf("decoded values=%v want [%g]", received, tc.want)
				}
				t.Logf("invalid never executed, valid executed once as float64=%g, both continuations observed", tc.want)
			})
		})
	}
}

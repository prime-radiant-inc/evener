package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/internal/numerictest"
)

func TestGenerateObjectNumericValidationJourney(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		constraint  map[string]any
		valid       bool
	}{
		{"exact_integer", "9007199254740993", map[string]any{"type": "integer", "const": json.Number("9007199254740993")}, true},
		{"neighbor_not_const", "9007199254740992", map[string]any{"type": "integer", "const": json.Number("9007199254740993")}, false},
		{"beyond_float_range", "1e400", map[string]any{"type": "integer", "minimum": json.Number("1e400"), "maximum": json.Number("2e400"), "const": json.Number("1e400")}, true},
		{"outside_range", "3e400", map[string]any{"type": "number", "minimum": json.Number("1e400"), "maximum": json.Number("2e400")}, false},
		{"tiny_exact", "1e-1000000000", map[string]any{"type": "number", "minimum": 0, "maximum": 1, "const": json.Number("1e-1000000000")}, true},
		{"tiny_not_zero", "1e-1000000000", map[string]any{"type": "number", "const": 0}, false},
		{"fractional_integer", "1e-1000000000", map[string]any{"type": "integer"}, false},
		{"huge_zero_exponent", "0e99999999999999999999999999999999", map[string]any{"type": "integer", "const": 0, "minimum": 0, "maximum": 0}, true},
		{"outside_huge_bound", "2e1000000000", map[string]any{"type": "number", "maximum": json.Number("1e1000000000")}, false},
	} {
		for _, streaming := range []bool{false, true} {
			mode := "complete"
			if streaming {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				numerictest.Run(t, "object", func(t *testing.T) {
					payload := `{"n":` + tc.token + `}`
					params := map[string]any{"type": "object", "properties": map[string]any{"n": tc.constraint}, "required": []string{"n"}}
					t.Log("compiling nonnil object schema through llm compileSchema")
					schema, err := compileSchema(params)
					if err != nil || schema == nil {
						t.Fatalf("compile: schema=%v err=%v", schema, err)
					}
					if err := schema.Validate(map[string]any{"n": "not-a-number"}); err == nil {
						t.Fatal("schema did not reject control")
					}
					prompt := "return the number"
					opts := GenerateObjectOptions{GenerateOptions: GenerateOptions{Model: "m", Prompt: &prompt}, Schema: params}
					checkRequest := func(req Request) {
						t.Helper()
						if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_schema" || req.ResponseFormat.JSONSchema == nil {
							t.Fatal("missing response schema")
						}
					}
					var output any
					requests := 0
					if streaming {
						c, adapter := newScriptedStreamClient(func(ctx context.Context, req Request) (Stream, error) {
							checkRequest(req)
							return replayEvents(
								StreamEvent{Type: StreamEventTextDelta, Delta: payload[:5]},
								StreamEvent{Type: StreamEventTextDelta, Delta: payload[5:]},
								finishEvent(Response{Message: Assistant(payload), Finish: FinishReason{Reason: FinishReasonStop}}),
							)(ctx, req)
						})
						opts.Client = c
						var result *StreamObjectResult
						result, err = StreamGenerateObject(context.Background(), opts)
						if err != nil {
							t.Fatal(err)
						}
						defer result.Close() //nolint:errcheck
						text := ""
						deltas := 0
						for ev := range result.Events() {
							if ev.Type == StreamEventTextDelta {
								text += ev.Delta
								deltas++
							}
						}
						if text != payload || deltas != 2 {
							t.Fatalf("stream text=%q deltas=%d", text, deltas)
						}
						_, err = result.Response()
						output = result.Output()
						requests = len(adapter.Requests())
					} else {
						a := &scriptedAdapter{name: "openai", steps: []func(Request) (Response, error){func(req Request) (Response, error) {
							checkRequest(req)
							return Response{Message: Assistant(payload)}, nil
						}}}
						c := NewClient()
						c.Register(a)
						opts.Client = c
						var result *GenerateResult
						result, err = GenerateObject(context.Background(), opts)
						if result != nil {
							output = result.Output
						}
						requests = a.i
					}
					if requests != 1 {
						t.Fatalf("script requests=%d", requests)
					}
					if !tc.valid {
						var invalid *NoObjectGeneratedError
						if !errors.As(err, &invalid) || invalid.RawText != payload || errors.Unwrap(invalid) == nil || output != nil {
							t.Fatalf("rejection: output=%v err=%v", output, err)
						}
						t.Logf("schema rejected %s, typed error retains raw output", tc.token)
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					n, ok := output.(map[string]any)["n"].(json.Number)
					if !ok || string(n) != tc.token {
						t.Fatalf("exact output: %+v", output)
					}
					t.Logf("validated json.Number token=%s", n)
				})
			})
		}
	}
}

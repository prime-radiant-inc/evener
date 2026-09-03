package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func invalidUTF8LossyEquivalentCalls(t *testing.T, name string) ([2]llm.ToolCallData, llm.ToolCallData) {
	t.Helper()
	makeInvalid := func(id string, invalid byte) llm.ToolCallData {
		raw := append([]byte(`{"value":"`), invalid)
		raw = append(raw, []byte(`"}`)...)
		return llm.ToolCallData{ID: id, Name: name, Arguments: raw}
	}
	invalid := [2]llm.ToolCallData{makeInvalid("invalid-ff", 0xff), makeInvalid("invalid-fe", 0xfe)}
	valid := llm.ToolCallData{ID: "valid-replacement", Name: name, Arguments: json.RawMessage(`{"value":"�"}`)}

	var want map[string]any
	if err := json.Unmarshal(valid.Arguments, &want); err != nil {
		t.Fatalf("decode valid replacement fixture: %v", err)
	}
	for _, call := range invalid {
		if utf8.Valid(call.Arguments) {
			t.Fatalf("invalid fixture %q is valid UTF-8: %q", call.ID, call.Arguments)
		}
		if got := bytes.ToValidUTF8(call.Arguments, []byte("\ufffd")); !bytes.Equal(got, valid.Arguments) {
			t.Fatalf("lossy decode for %q = %q, want valid replacement payload %q", call.ID, got, valid.Arguments)
		}
		var decoded map[string]any
		if err := json.Unmarshal(call.Arguments, &decoded); err != nil {
			t.Fatalf("encoding/json did not demonstrate the lossy collision for %q: %v", call.ID, err)
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("lossy decode for %q = %#v, want %#v", call.ID, decoded, want)
		}
	}
	return invalid, valid
}

func registerUTF8CollisionTool(t *testing.T, r *Registry, name string, calls *int) {
	t.Helper()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        name,
			Description: "exercise raw UTF-8 validation before registry dispatch",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
				"required": []any{"value"},
			},
		},
		OmitIntent: true,
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			(*calls)++
			if args["value"] != "\ufffd" {
				return nil, errors.New("unexpected dispatched value")
			}
			return "dispatched", nil
		},
	}); err != nil {
		t.Fatalf("register UTF-8 collision tool: %v", err)
	}
}

func TestSemanticFailureBreaker_InvalidUTF8CannotPoisonLossyEquivalentValidCall(t *testing.T) {
	const name = "utf8_collision"
	invalid, valid := invalidUTF8LossyEquivalentCalls(t, name)
	r := NewRegistry()
	calls := 0
	registerUTF8CollisionTool(t, r, name, &calls)

	var invalidResults [2]ExecResult
	for i, call := range invalid {
		invalidResults[i] = r.ExecuteCall(context.Background(), breakerEnv(t), call)
		if !invalidResults[i].IsError || !strings.Contains(invalidResults[i].FullOutput, "not valid UTF-8") {
			t.Fatalf("invalid call %d result = %#v, want raw UTF-8 validation failure", i+1, invalidResults[i])
		}
		if strings.Contains(invalidResults[i].Output, "semantic failure loop") {
			t.Fatalf("invalid call %d parked early: %#v", i+1, invalidResults[i])
		}
		if calls != 0 {
			t.Fatalf("invalid call %d dispatched executor; calls=%d", i+1, calls)
		}
	}
	if invalidResults[0].BreakerExactSignature == invalidResults[1].BreakerExactSignature {
		t.Fatalf("distinct invalid raw bytes shared exact identity %q", invalidResults[0].BreakerExactSignature)
	}
	if invalidResults[0].BreakerSemanticSignature != invalidResults[1].BreakerSemanticSignature {
		t.Fatalf("invalid UTF-8 marker was not stable in one registry: %q != %q", invalidResults[0].BreakerSemanticSignature, invalidResults[1].BreakerSemanticSignature)
	}

	validResult := r.ExecuteCall(context.Background(), breakerEnv(t), valid)
	if validResult.IsError || validResult.FullOutput != "dispatched" || calls != 1 {
		t.Fatalf("valid U+FFFD call was poisoned by lossy-equivalent invalid bytes: calls=%d result=%#v", calls, validResult)
	}
	if validResult.BreakerSemanticSignature == invalidResults[0].BreakerSemanticSignature {
		t.Fatalf("valid U+FFFD payload shared invalid-byte semantic identity %q", validResult.BreakerSemanticSignature)
	}
	for _, signature := range []string{
		invalidResults[0].BreakerExactSignature,
		invalidResults[0].BreakerSemanticSignature,
		invalidResults[1].BreakerExactSignature,
		invalidResults[1].BreakerSemanticSignature,
		validResult.BreakerExactSignature,
		validResult.BreakerSemanticSignature,
	} {
		if signature == "" || len(signature) > 96 {
			t.Fatalf("missing or unbounded breaker identity %q", signature)
		}
	}

	other := NewRegistry()
	otherCalls := 0
	registerUTF8CollisionTool(t, other, name, &otherCalls)
	otherResult := other.ExecuteCall(context.Background(), breakerEnv(t), invalid[0])
	if otherResult.BreakerSemanticSignature == invalidResults[0].BreakerSemanticSignature || otherResult.BreakerExactSignature == invalidResults[0].BreakerExactSignature {
		t.Fatalf("breaker identities are not registry/session keyed: first=%#v other=%#v", invalidResults[0], otherResult)
	}
	if otherCalls != 0 {
		t.Fatalf("invalid call dispatched in second registry; calls=%d", otherCalls)
	}
}

func TestFinalizePrevalidationFailure_NormalizedAskFormsShareSemanticIdentity(t *testing.T) {
	r := NewRegistry()
	dispatches := 0
	if err := r.Register(RegisteredTool{
		Definition: DefAskUser(),
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			dispatches++
			return "unexpected dispatch", nil
		},
	}); err != nil {
		t.Fatalf("register ask_user: %v", err)
	}
	r.MarkRegisteredToolsCoreSemanticMetadata()

	raw := []json.RawMessage{
		json.RawMessage(`{"question":"Which?","options":[{"label":"Only","detail":"one"}]}`),
		json.RawMessage(`{"questions":[{"options":[{"detail":"one","label":"Only"}],"question":"Which?"}]}`),
		json.RawMessage(`{ "options" : [ { "detail" : "one", "label" : "Only" } ], "multi_select" : false, "question" : "Which?" }`),
	}
	semantic := json.RawMessage(`{"questions":[{"options":[{"detail":"one","label":"Only"}],"question":"Which?"}]}`)
	results := make([]ExecResult, 0, len(raw))
	for i, arguments := range raw {
		call := llm.ToolCallData{ID: "ask-finalizer-" + string(rune('1'+i)), Name: "ask_user", Arguments: arguments}
		_, snapshot := r.SnapshotPrevalidation(call.Name)
		res := r.FinalizePrevalidationFailure(context.Background(), snapshot, call, semantic, "schema validation failed", "schema_validation", errors.New("schema validation failed"))
		results = append(results, res)
		if !res.IsError || !res.PrevalOnly {
			t.Fatalf("failure %d = %#v, want prevalidation error", i+1, res)
		}
		if i < 2 && strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("failure %d parked early: %#v", i+1, res)
		}
	}
	if !strings.Contains(results[2].Output, "semantic failure loop") {
		t.Fatalf("third normalized-equivalent failure was not parked: %#v", results[2])
	}
	if dispatches != 0 {
		t.Fatalf("prevalidation failures dispatched ask_user %d times", dispatches)
	}
	exact := map[string]bool{}
	for i, res := range results {
		exact[res.BreakerExactSignature] = true
		if res.BreakerSemanticSignature != results[0].BreakerSemanticSignature {
			t.Fatalf("normalized failure %d semantic identity = %q, want %q", i+1, res.BreakerSemanticSignature, results[0].BreakerSemanticSignature)
		}
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("failure %d has missing or unbounded breaker identity: %#v", i+1, res)
		}
	}
	if len(exact) != len(raw) {
		t.Fatalf("distinct raw ask_user forms collapsed to %d exact identities, want %d", len(exact), len(raw))
	}
}

func TestFinalizePrevalidationFailure_OversizeTrustedNormalizedArgsRetainCanonicalIdentity(t *testing.T) {
	newAskRegistry := func(t *testing.T) *Registry {
		t.Helper()
		r := NewRegistry()
		if err := r.Register(RegisteredTool{
			Definition: DefAskUser(),
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				return "unexpected dispatch", nil
			},
		}); err != nil {
			t.Fatalf("register ask_user: %v", err)
		}
		r.MarkRegisteredToolsCoreSemanticMetadata()
		return r
	}
	normalized := func(t *testing.T, question string) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"questions": []any{map[string]any{
				"question": question,
				"options":  []any{map[string]any{"label": "Only", "detail": "one"}},
			}},
		})
		if err != nil {
			t.Fatalf("marshal trusted normalized arguments: %v", err)
		}
		if len(raw) <= maxToolArgumentBytes {
			t.Fatalf("normalized fixture size = %d, want over %d", len(raw), maxToolArgumentBytes)
		}
		return raw
	}
	finalize := func(t *testing.T, r *Registry, id string, raw, semantic json.RawMessage) ExecResult {
		t.Helper()
		call := llm.ToolCallData{ID: id, Name: "ask_user", Arguments: raw}
		_, snapshot := r.SnapshotPrevalidation(call.Name)
		return r.FinalizePrevalidationFailure(context.Background(), snapshot, call, semantic, "schema validation failed", "schema_validation", errors.New("schema validation failed"))
	}

	t.Run("distinct normalized values do not use raw oversize marker", func(t *testing.T) {
		r := newAskRegistry(t)
		untrustedA := bytes.Repeat([]byte("a"), maxToolArgumentBytes+1)
		untrustedB := bytes.Repeat([]byte("b"), maxToolArgumentBytes+1)
		if one, two := r.semanticSignatureFromRaw("ask_user", untrustedA), r.semanticSignatureFromRaw("ask_user", untrustedB); one != two {
			t.Fatalf("actual untrusted oversize calls lost their common sentinel: %q != %q", one, two)
		}

		seen := map[string]bool{}
		for _, value := range []string{"A", "B", "C"} {
			semantic := normalized(t, strings.Repeat(value, maxToolArgumentBytes))
			res := finalize(t, r, "distinct-"+value, json.RawMessage(`{"question":"`+value+`"}`), semantic)
			if strings.Contains(res.Output, "semantic failure loop") {
				t.Fatalf("distinct normalized value %q falsely parked: %#v", value, res)
			}
			if res.BreakerSemanticSignature == "" || len(res.BreakerSemanticSignature) > 96 {
				t.Fatalf("normalized value %q has unbounded identity: %q", value, res.BreakerSemanticSignature)
			}
			seen[res.BreakerSemanticSignature] = true
		}
		if len(seen) != 3 {
			t.Fatalf("distinct trusted normalized values collapsed to %d identities, want 3", len(seen))
		}
	})

	t.Run("equivalent normalized values still group", func(t *testing.T) {
		r := newAskRegistry(t)
		semantic := normalized(t, strings.Repeat("S", maxToolArgumentBytes))
		raw := []json.RawMessage{
			json.RawMessage(`{"question":"same"}`),
			json.RawMessage(`{ "question":"same"}`),
			json.RawMessage(`{"question": "same"}`),
		}
		results := make([]ExecResult, 0, len(raw))
		for i, arguments := range raw {
			res := finalize(t, r, "equivalent-"+string(rune('1'+i)), arguments, semantic)
			results = append(results, res)
			if i < 2 && strings.Contains(res.Output, "semantic failure loop") {
				t.Fatalf("equivalent normalized failure %d parked early: %#v", i+1, res)
			}
		}
		if !strings.Contains(results[2].Output, "semantic failure loop") {
			t.Fatalf("third equivalent oversize normalized failure was not parked: %#v", results[2])
		}
		exact := map[string]bool{}
		for i, res := range results {
			exact[res.BreakerExactSignature] = true
			if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || res.BreakerSemanticSignature != results[0].BreakerSemanticSignature {
				t.Fatalf("equivalent failure %d identity = %q, want %q", i+1, res.BreakerSemanticSignature, results[0].BreakerSemanticSignature)
			}
		}
		if len(exact) != len(raw) {
			t.Fatalf("distinct raw calls collapsed to %d exact identities, want %d", len(exact), len(raw))
		}
	})
}

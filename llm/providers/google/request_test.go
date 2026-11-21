package google

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestBuildBodyProviderOptionsCycleAndSharedSubMap(t *testing.T) {
	t.Parallel()

	t.Run("self-referential map cycle returns ConfigurationError", func(t *testing.T) {
		cyclic := map[string]any{}
		cyclic["self"] = cyclic

		req := protoReq("")
		req.ProviderOptions = map[string]any{
			"google": map[string]any{
				"custom": cyclic,
			},
		}
		_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("expected *llm.ConfigurationError for cyclic map, got %T %v", err, err)
		}
	})

	t.Run("top-level google options self-cycle returns ConfigurationError", func(t *testing.T) {
		gOpts := map[string]any{}
		gOpts["self"] = gOpts

		req := protoReq("")
		req.ProviderOptions = map[string]any{
			"google": gOpts,
		}
		_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("expected *llm.ConfigurationError for top-level cyclic map, got %T %v", err, err)
		}
	})

	t.Run("self-referential slice cycle returns ConfigurationError", func(t *testing.T) {
		s := make([]any, 1)
		s[0] = s

		req := protoReq("")
		req.ProviderOptions = map[string]any{
			"google": map[string]any{
				"arr": s,
			},
		}
		_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("expected *llm.ConfigurationError for cyclic slice, got %T %v", err, err)
		}
	})

	t.Run("non-cyclic shared sub-map succeeds and deep-clones independently", func(t *testing.T) {
		shared := map[string]any{"nested": "orig"}

		req := protoReq("")
		req.ProviderOptions = map[string]any{
			"google": map[string]any{
				"a": shared,
				"b": shared,
				"generation_config": map[string]any{
					"sub": shared,
				},
			},
		}
		body, err := (&Protocol{}).BuildBody(req, protoRes(nil))
		if err != nil {
			t.Fatalf("shared non-cyclic sub-map unexpectedly failed: %v", err)
		}
		aMap, okA := body["a"].(map[string]any)
		bMap, okB := body["b"].(map[string]any)
		genCfg, okG := body["generation_config"].(map[string]any)
		subMap, okS := genCfg["sub"].(map[string]any)
		if !okA || !okB || !okG || !okS {
			t.Fatalf("unexpected cloned map types: a=%T b=%T gen=%T sub=%T", body["a"], body["b"], body["generation_config"], genCfg["sub"])
		}
		want := map[string]any{"nested": "orig"}
		if !reflect.DeepEqual(aMap, want) || !reflect.DeepEqual(bMap, want) || !reflect.DeepEqual(subMap, want) {
			t.Fatalf("cloned values mismatch: a=%v b=%v sub=%v", aMap, bMap, subMap)
		}

		// Mutating one clone or the source must not affect the others.
		shared["nested"] = "source-mutated"
		aMap["nested"] = "a-mutated"
		if bMap["nested"] != "orig" || subMap["nested"] != "orig" {
			t.Fatalf("clones shared mutable state: b=%v sub=%v", bMap, subMap)
		}
	})
}

func TestBuildBodyProviderOptionsTypedSliceAndMapDeepCloned(t *testing.T) {
	t.Parallel()

	safetySettings := []map[string]any{
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
	}
	tagsMap := map[string][]string{
		"env": {"prod"},
	}
	rawTools := json.RawMessage(`[{"type":"function","name":"raw_fn","description":"d","parameters":{"type":"object"}}]`)
	rawSafety := json.RawMessage(`[{"type":"harassment","threshold":"block_only_high"}]`)
	req := protoReq("")
	req.ProviderOptions = map[string]any{
		"google": map[string]any{
			"safety_settings": safetySettings,
			"tags_map":        tagsMap,
			"tools":           rawTools,
		},
	}
	res := protoRes(nil)
	res.Caps.Fields = map[string]bool{"safety_settings": true, "store": true}

	body1, err := (&Protocol{}).BuildBody(req, res)
	if err != nil {
		t.Fatalf("BuildBody #1: %v", err)
	}
	body2, err := (&Protocol{}).BuildBody(req, res)
	if err != nil {
		t.Fatalf("BuildBody #2: %v", err)
	}

	// Mutate body1's cloned safety_settings and tags_map.
	if ss1, ok := body1["safety_settings"].([]any); ok {
		if elem, ok := ss1[0].(map[string]any); ok {
			elem["threshold"] = "BLOCK_LOW_AND_ABOVE"
		}
		ss1[0] = map[string]any{"mutated": true}
	} else if ss1, ok := body1["safety_settings"].([]map[string]any); ok {
		ss1[0]["threshold"] = "BLOCK_LOW_AND_ABOVE"
		ss1[0] = map[string]any{"mutated": true}
	}
	if tm1, ok := body1["tags_map"].(map[string]any); ok {
		if s, ok := tm1["env"].([]any); ok {
			s[0] = "mutated"
		} else if s, ok := tm1["env"].([]string); ok {
			s[0] = "mutated"
		}
	} else if tm1, ok := body1["tags_map"].(map[string][]string); ok {
		tm1["env"][0] = "mutated"
	}

	if safetySettings[0]["threshold"] != "BLOCK_NONE" {
		t.Fatalf("caller safetySettings mutated: %+v", safetySettings)
	}
	if tagsMap["env"][0] != "prod" {
		t.Fatalf("caller tagsMap mutated: %+v", tagsMap)
	}
	wantSS := []any{map[string]any{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"}}
	if !reflect.DeepEqual(body2["safety_settings"], wantSS) {
		t.Fatalf("body2 safety_settings = %#v, want %#v", body2["safety_settings"], wantSS)
	}

	rawReq := protoReq("")
	rawReq.ProviderOptions = map[string]any{
		"google": map[string]any{
			"tools":           rawTools,
			"safety_settings": rawSafety,
		},
	}
	rawBody1, err := (&Protocol{}).BuildBody(rawReq, res)
	if err != nil {
		t.Fatalf("BuildBody raw #1: %v", err)
	}
	rt1, okRT := rawBody1["tools"].(json.RawMessage)
	rs1, okRS := rawBody1["safety_settings"].(json.RawMessage)
	if !okRT || !okRS {
		t.Fatalf("expected json.RawMessage preserved, got tools=%T safety_settings=%T", rawBody1["tools"], rawBody1["safety_settings"])
	}
	rt1[0] = 'X'
	rs1[0] = 'X'
	if rawTools[0] != '[' || rawSafety[0] != '[' {
		t.Fatalf("caller json.RawMessage mutated: tools=%q safety=%q", rawTools, rawSafety)
	}

	srv, got := protoServer(t, http.StatusOK, interactionsJSON)
	liveRes := protoLive(srv)
	liveRes.Caps.Fields = map[string]bool{"safety_settings": true, "store": true}
	if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), rawReq, liveRes); err != nil {
		t.Fatalf("Complete with json.RawMessage provider options: %v", err)
	}
	if gotTools, ok := got.body["tools"].([]any); !ok || len(gotTools) != 1 {
		t.Fatalf("wire tools = %T %#v, want 1-element JSON array", got.body["tools"], got.body["tools"])
	}
	if gotSafety, ok := got.body["safety_settings"].([]any); !ok || len(gotSafety) != 1 {
		t.Fatalf("wire safety_settings = %T %#v, want 1-element JSON array", got.body["safety_settings"], got.body["safety_settings"])
	}
}

func TestBuildBodyConsecutiveAssistantMessagesFlushGoogleStep(t *testing.T) {
	t.Parallel()

	req := llm.Request{
		Model: "gemini-3.8-flash",
		Messages: []llm.Message{
			llm.User("u1"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "m1", GoogleStep: 1}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "m2", GoogleStep: 1}}},
			llm.User("u2"),
			// Assistant message emitting zero steps must still coalesce adjacent user inputs.
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				GoogleInteraction: true, GoogleInstance: "other", GoogleModel: "gemini-3.8-flash", Signature: "sig_foreign",
			}}}},
			llm.User("u3"),
		},
	}
	body, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	steps, _ := body["input"].([]any)
	want := []any{
		map[string]any{"type": "user_input", "content": []any{map[string]any{"type": "text", "text": "u1"}}},
		map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": "m1"}}},
		map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": "m2"}}},
		map[string]any{"type": "user_input", "content": []any{map[string]any{"type": "text", "text": "u2"}, map[string]any{"type": "text", "text": "u3"}}},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %#v\nwant: %#v", steps, want)
	}
}

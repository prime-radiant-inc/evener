package google

import (
	"errors"
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

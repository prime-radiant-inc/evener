package agent

import (
	"context"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// The registry's map order used to leak into cachedToolDefs, which is both the
// provider's tool list and the system prompt's "Currently callable tools", so a
// session rebuilt from the same configuration advertised the registry-only
// tools in a different order. Rebuilding the cache re-ranges the registry map,
// so an unsorted Definitions yields a different name order on the next build.
func TestSessionToolDefinitionsStayOrderedAcrossRebuilds(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	for _, name := range []string{"apricot_probe", "banana_probe", "walnut_probe", "zebra_probe"} {
		if err := s.reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{
				Name:        name,
				Description: "a registry-only tool",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				return "ok", nil
			},
		}); err != nil {
			t.Fatalf("Register(%s): %v", name, err)
		}
	}
	s.rebuildToolDefsCache()
	first := toolNamesFromDefinitions(s.cachedToolDefs)
	// Each rebuild re-ranges the registry map, so five comparisons make an
	// accidental match between two random orders negligible.
	for i := range 5 {
		s.rebuildToolDefsCache()
		if got := toolNamesFromDefinitions(s.cachedToolDefs); !slices.Equal(got, first) {
			t.Fatalf("rebuild %d advertised a different tool order:\nfirst = %v\n  got = %v", i, first, got)
		}
	}
}

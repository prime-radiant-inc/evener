package provider

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestWithResolvedOverlaysLiveFacts(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.5")
	res := registry.Resolved{Caps: registry.Caps{ContextWindow: new(272000), EffortValues: []string{"low", "high"}, Reasoning: new(true), ThinkingAlwaysOn: new(true), WebSearch: new(false)}}
	q := p.WithResolved(res)
	if q.ContextWindowSize() != 272000 || strings.Join(q.ReasoningEffortLevels(), ",") != "low,high" || !q.ThinkingAlwaysOn() || q.SupportsWebSearch() {
		t.Fatalf("overlay: %d %v %v %v", q.ContextWindowSize(), q.ReasoningEffortLevels(), q.ThinkingAlwaysOn(), q.SupportsWebSearch())
	}
	if p.ContextWindowSize() == 272000 {
		t.Fatal("WithResolved clones")
	}
	for _, td := range q.ToolDefinitions() {
		if td.Name == "task_list" && !strings.Contains(fmt.Sprint(td.Parameters), "high") {
			t.Fatal("task_list effort enum follows the ladder")
		}
	}
	if r := p.WithResolved(registry.Resolved{Caps: registry.Caps{Reasoning: new(false)}}); r.SupportsReasoning() || len(r.ReasoningEffortLevels()) != 0 {
		t.Fatal("reasoning = false clears the ladder")
	}
}

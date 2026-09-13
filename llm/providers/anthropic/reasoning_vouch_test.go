package anthropic

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The adaptive shape writes the effort NAME to output_config.effort. A row
// with no ladder vouches for no name, so only the adaptive body survives — the
// level is dropped rather than forced.
func TestApplyThinkingShape_AdaptiveEmptyLadderDropsEffortName(t *testing.T) {
	high := "high"
	res := protoRes(func(c *registry.Caps) {
		c.EffortValues = nil
		c.ThinkingShape = new("adaptive")
		c.ThinkingAlwaysOn = new(true)
	})
	body := map[string]any{}
	applyThinkingShape(body, llm.Request{ReasoningEffort: &high}, res, false)
	if _, has := body["output_config"]; has {
		t.Fatalf("empty-ladder adaptive row wrote output_config: %v", body)
	}
	if body["thinking"] == nil {
		t.Fatalf("adaptive body must survive the dropped effort: %v", body)
	}
}

// A vouched adaptive effort is still written, clamped into the ladder.
func TestApplyThinkingShape_AdaptiveVouchedEffortClamps(t *testing.T) {
	xhigh := "xhigh"
	res := protoRes(func(c *registry.Caps) {
		c.EffortValues = []string{"low", "high"}
		c.ThinkingShape = new("adaptive")
		c.ThinkingAlwaysOn = new(true)
	})
	body := map[string]any{}
	applyThinkingShape(body, llm.Request{ReasoningEffort: &xhigh}, res, false)
	cfg, _ := body["output_config"].(map[string]any)
	if cfg == nil || cfg["effort"] != "high" {
		t.Fatalf("output_config = %v, want effort high", body["output_config"])
	}
}

// budget+effort still sizes its numeric budget from the effort, but only
// writes the effort name when the row vouches for it.
func TestApplyThinkingShape_BudgetPlusEffortEmptyLadderKeepsBudget(t *testing.T) {
	high := "high"
	res := protoRes(func(c *registry.Caps) {
		c.EffortValues = nil
		c.ThinkingShape = new("budget+effort")
	})
	body := map[string]any{}
	applyThinkingShape(body, llm.Request{ReasoningEffort: &high}, res, false)
	if _, has := body["output_config"]; has {
		t.Fatalf("empty-ladder budget+effort row wrote an effort name: %v", body)
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "enabled" {
		t.Fatalf("budget body must survive the dropped effort name: %v", body)
	}
}

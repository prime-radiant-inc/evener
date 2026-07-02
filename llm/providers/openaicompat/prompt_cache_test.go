package openaicompat

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// longCacheQuirks returns quirks with the long-retention flag enabled.
func longCacheQuirks() ProviderQuirks {
	q := ProviderQuirks{}
	q.SupportsLongCacheRetention = true
	return q
}

func TestPromptCacheKey_ExplicitKey(t *testing.T) {
	req := plainReq("m")
	req.PromptCacheKey = "my-key"
	req.SessionID = "sess-1"
	body := requestBody(t, req, false, ModelCompat{Quirks: longCacheQuirks()})
	if body["prompt_cache_key"] != "my-key" {
		t.Errorf("prompt_cache_key = %v, want my-key (explicit wins)", body["prompt_cache_key"])
	}
	if body["prompt_cache_retention"] != "24h" {
		t.Errorf("prompt_cache_retention = %v, want 24h", body["prompt_cache_retention"])
	}
}

func TestPromptCacheKey_DerivedFromSessionID(t *testing.T) {
	req := plainReq("m")
	req.SessionID = "sess-1"
	body := requestBody(t, req, false, ModelCompat{Quirks: longCacheQuirks()})
	if body["prompt_cache_key"] != "serf-session-sess-1" {
		t.Errorf("prompt_cache_key = %v, want serf-session-sess-1", body["prompt_cache_key"])
	}
	if body["prompt_cache_retention"] != "24h" {
		t.Errorf("prompt_cache_retention = %v, want 24h", body["prompt_cache_retention"])
	}
}

func TestPromptCacheKey_AbsentWhenFlagOff(t *testing.T) {
	req := plainReq("m")
	req.PromptCacheKey = "my-key"
	req.SessionID = "sess-1"
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{}})
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("prompt_cache_key present with flag off: %v", body["prompt_cache_key"])
	}
	if _, ok := body["prompt_cache_retention"]; ok {
		t.Errorf("prompt_cache_retention present with flag off: %v", body["prompt_cache_retention"])
	}
}

func TestPromptCacheKey_AbsentWhenNoKeyMaterial(t *testing.T) {
	req := plainReq("m") // no PromptCacheKey, no SessionID
	body := requestBody(t, req, false, ModelCompat{Quirks: longCacheQuirks()})
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("prompt_cache_key present without key material: %v", body["prompt_cache_key"])
	}
	if _, ok := body["prompt_cache_retention"]; ok {
		t.Errorf("prompt_cache_retention present without key material: %v", body["prompt_cache_retention"])
	}
}

func TestAnthropicCacheControl_TTLWithLongRetention(t *testing.T) {
	req := llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "one"}}},
		},
		Tools: []llm.ToolDefinition{{Name: "a"}},
	}
	q := longCacheQuirks()
	q.CacheControlFormat = "anthropic"
	body := requestBody(t, req, false, ModelCompat{Quirks: q})
	wantCC := map[string]any{"type": "ephemeral", "ttl": "1h"}

	msgs := body["messages"].([]map[string]any)
	sysParts := msgs[0]["content"].([]map[string]any)
	if !reflect.DeepEqual(sysParts[0]["cache_control"], wantCC) {
		t.Errorf("system cache_control = %v, want %v", sysParts[0]["cache_control"], wantCC)
	}
	tools := body["tools"].([]map[string]any)
	if !reflect.DeepEqual(tools[len(tools)-1]["cache_control"], wantCC) {
		t.Errorf("last tool cache_control = %v, want %v", tools[len(tools)-1]["cache_control"], wantCC)
	}
}

func TestAnthropicCacheControl_PlainEphemeralWithoutLongRetention(t *testing.T) {
	req := llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		},
	}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{CacheControlFormat: "anthropic"}})
	wantCC := map[string]any{"type": "ephemeral"}
	msgs := body["messages"].([]map[string]any)
	sysParts := msgs[0]["content"].([]map[string]any)
	if !reflect.DeepEqual(sysParts[0]["cache_control"], wantCC) {
		t.Errorf("system cache_control = %v, want plain ephemeral %v", sysParts[0]["cache_control"], wantCC)
	}
}

// TestApplyCompatConfig_CacheRetentionAndAffinity confirms the two new compat
// flags overlay onto quirks.
func TestApplyCompatConfig_CacheRetentionAndAffinity(t *testing.T) {
	tru := true
	q := ApplyCompatConfig(ProviderQuirks{}, &providercfg.CompatConfig{
		SupportsLongCacheRetention: &tru,
		SendSessionAffinityHeaders: &tru,
	})
	if !q.SupportsLongCacheRetention {
		t.Errorf("SupportsLongCacheRetention not applied")
	}
	if !q.SendSessionAffinityHeaders {
		t.Errorf("SendSessionAffinityHeaders not applied")
	}
}

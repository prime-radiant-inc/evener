package openaicompat

import (
	"strings"

	"primeradiant.com/serf/llm/providercfg"
)

// ModelCompat is the fully-resolved per-model wire behavior: the effective
// quirks (preset → instance compat → model compat), the thinking-level
// translation map, and the default output cap. The adapter resolves one per
// request via compatFor.
type ModelCompat struct {
	Quirks ProviderQuirks
	// ThinkingLevels maps serf effort levels (canonical keys, "xhigh" for the
	// top tier) to the wire string the provider wants. Empty means no
	// translation — levels pass through by name.
	ThinkingLevels map[string]string
	// DefaultMaxTokens is sent as the output cap when the request sets none.
	DefaultMaxTokens int
}

// wireEffort translates a serf effort level to the provider's wire value.
// A model-level map wins; without one, the TranslateMaxToXHigh quirk still
// applies (OpenRouter vocabulary). Unmapped levels pass through by name —
// the session's clamp keeps requests inside the supported set, and the
// adapter stays permissive for anything it doesn't recognize.
func (mc ModelCompat) wireEffort(effort string) string {
	norm := strings.ToLower(strings.TrimSpace(effort))
	if len(mc.ThinkingLevels) > 0 {
		key := norm
		if key == "max" {
			// serf's rank table treats max and xhigh as one tier; the map is
			// keyed on the canonical xhigh.
			key = "xhigh"
		}
		if v, ok := mc.ThinkingLevels[key]; ok {
			return v
		}
		return effort
	}
	if mc.Quirks.TranslateMaxToXHigh && norm == "max" {
		return "xhigh"
	}
	return effort
}

// supportsEffort resolves the SupportsReasoningEffort tri-state against the
// active thinking format's default.
func (q ProviderQuirks) supportsEffort(formatDefault bool) bool {
	if q.SupportsReasoningEffort != nil {
		return *q.SupportsReasoningEffort
	}
	return formatDefault
}

// ApplyCompatConfig overlays a providers.toml compat table onto base quirks,
// field by field: only fields the user set override; everything else inherits.
// FinishReasonMap replaces wholesale rather than merging.
func ApplyCompatConfig(base ProviderQuirks, c *providercfg.CompatConfig) ProviderQuirks {
	if c == nil {
		return base
	}
	q := base
	if c.ThinkingFormat != "" {
		q.ThinkingFormat = c.ThinkingFormat
	}
	if c.SupportsReasoningEffort != nil {
		v := *c.SupportsReasoningEffort
		q.SupportsReasoningEffort = &v
	}
	if c.MaxTokensField != "" {
		q.MaxTokensField = c.MaxTokensField
	}
	if c.ToolStream != nil {
		q.ToolStream = *c.ToolStream
	}
	if c.SupportsStore != nil {
		q.SendStoreFalse = *c.SupportsStore
	}
	if c.SupportsDeveloperRole != nil {
		q.UseDeveloperRole = *c.SupportsDeveloperRole
	}
	if c.SupportsUsageInStreaming != nil {
		q.OmitStreamUsage = !*c.SupportsUsageInStreaming
	}
	if c.RequiresToolResultName != nil {
		q.RequireToolResultName = *c.RequiresToolResultName
	}
	if c.RequiresAssistantAfterToolResult != nil {
		q.RequireAssistantAfterToolResult = *c.RequiresAssistantAfterToolResult
	}
	if c.RequiresThinkingAsText != nil {
		q.ThinkingAsText = *c.RequiresThinkingAsText
	}
	if c.RequiresReasoningContentOnAssistant != nil {
		q.EmptyReasoningContentOnAssistant = *c.RequiresReasoningContentOnAssistant
	}
	if c.CacheControlFormat != "" {
		q.CacheControlFormat = c.CacheControlFormat
	}
	if c.SupportsStrictMode != nil {
		v := *c.SupportsStrictMode
		q.SupportsStrictMode = &v
	}
	if len(c.ChatTemplateKwargs) > 0 {
		q.ChatTemplateKwargs = make(map[string]any, len(c.ChatTemplateKwargs))
		for k, v := range c.ChatTemplateKwargs {
			q.ChatTemplateKwargs[k] = v
		}
	}
	if c.LockTemperature != nil {
		q.LockTemperature = *c.LockTemperature
	}
	if c.LockTopP != nil {
		q.LockTopP = *c.LockTopP
	}
	if c.LockFrequencyPenalty != nil {
		q.LockFrequencyPenalty = *c.LockFrequencyPenalty
	}
	if c.LockPresencePenalty != nil {
		q.LockPresencePenalty = *c.LockPresencePenalty
	}
	if c.ToolChoiceAutoOnly != nil {
		q.ToolChoiceAutoOnly = *c.ToolChoiceAutoOnly
	}
	if c.MaxStopSequences != nil {
		q.MaxStopSequences = *c.MaxStopSequences
	}
	if c.StripEmptyContent != nil {
		q.StripEmptyContent = *c.StripEmptyContent
	}
	if c.NoJSONSchema != nil {
		q.NoJSONSchema = *c.NoJSONSchema
	}
	if len(c.FinishReasonMap) > 0 {
		q.FinishReasonMap = make(map[string]string, len(c.FinishReasonMap))
		for k, v := range c.FinishReasonMap {
			q.FinishReasonMap[k] = v
		}
	}
	if c.TranslateMaxToXHigh != nil {
		q.TranslateMaxToXHigh = *c.TranslateMaxToXHigh
	}
	return q
}

// resolveModelCompat builds the per-model table from instance config: each
// model's quirks start from the instance's resolved quirks and overlay the
// model's own compat.
func resolveModelCompat(instanceQuirks ProviderQuirks, models map[string]providercfg.ModelConfig) map[string]ModelCompat {
	if len(models) == 0 {
		return nil
	}
	out := make(map[string]ModelCompat, len(models))
	for id, mc := range models {
		entry := ModelCompat{
			Quirks:           ApplyCompatConfig(instanceQuirks, mc.Compat),
			DefaultMaxTokens: mc.MaxOutputTokens,
		}
		if len(mc.ThinkingLevels) > 0 {
			entry.ThinkingLevels = make(map[string]string, len(mc.ThinkingLevels))
			for k, v := range mc.ThinkingLevels {
				entry.ThinkingLevels[k] = v
			}
		}
		out[id] = entry
	}
	return out
}

// compatFor returns the resolved wire behavior for one model: the model's
// entry when the instance declared it, else the instance-wide quirks.
func (a *Adapter) compatFor(model string) ModelCompat {
	if mc, ok := a.Models[model]; ok {
		return mc
	}
	return ModelCompat{Quirks: a.Quirks}
}

// anthropicCacheControl marks the request for Anthropic-style prompt caching
// through gateways that forward cache_control: the system prompt, the last
// tool definition, and the last conversation message each get an ephemeral
// marker (mirrors the placement Anthropic documents for messages API callers).
func anthropicCacheControl(body map[string]any) {
	cc := map[string]any{"type": "ephemeral"}
	msgs, _ := body["messages"].([]map[string]any)
	for _, m := range msgs {
		if m["role"] == "system" || m["role"] == "developer" {
			addCacheControlToTextContent(m, cc)
			break
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if r := msgs[i]["role"]; r == "user" || r == "assistant" {
			if addCacheControlToTextContent(msgs[i], cc) {
				break
			}
		}
	}
	if tools, ok := body["tools"].([]map[string]any); ok && len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = cc
	}
}

// addCacheControlToTextContent attaches cc to the message's last text part,
// promoting a plain string content to a one-part array. Returns false when
// the message has no text to mark (empty string or partless content).
func addCacheControlToTextContent(msg map[string]any, cc map[string]any) bool {
	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return false
		}
		msg["content"] = []map[string]any{{"type": "text", "text": content, "cache_control": cc}}
		return true
	case []map[string]any:
		for i := len(content) - 1; i >= 0; i-- {
			if content[i]["type"] == "text" {
				content[i]["cache_control"] = cc
				return true
			}
		}
	}
	return false
}

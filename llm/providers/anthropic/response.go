package anthropic

import (
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
)

func fromAnthropicResponse(raw map[string]any, requestedModel string) llm.Response {
	r := llm.Response{
		Provider: "anthropic",
		Model:    requestedModel,
		Raw:      raw,
	}
	if id, _ := raw["id"].(string); id != "" {
		r.ID = id
	}
	if m, _ := raw["model"].(string); m != "" {
		r.Model = m
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	if content, ok := raw["content"].([]any); ok {
		for _, itAny := range content {
			it, ok := itAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := it["type"].(string)
			switch typ {
			case "text":
				if t, _ := it["text"].(string); t != "" {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: t})
				}
			case "tool_use":
				id, _ := it["id"].(string)
				name, _ := it["name"].(string)
				argsAny := it["input"]
				argsRaw, _ := json.Marshal(argsAny)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        id,
						Name:      name,
						Arguments: argsRaw,
						Type:      "function",
					},
				})
			case "thinking":
				t, _ := it["text"].(string)
				if t == "" {
					t, _ = it["thinking"].(string)
				}
				if t != "" {
					sig, _ := it["signature"].(string)
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentThinking,
						Thinking: &llm.ThinkingData{
							Text:      t,
							Signature: sig,
							Redacted:  false,
						},
					})
				}
			case "redacted_thinking":
				if d, _ := it["data"].(string); d != "" {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentRedThinking,
						Thinking: &llm.ThinkingData{
							Text:     d,
							Redacted: true,
						},
					})
				}
			case "server_tool_use":
				query := ""
				if input, _ := it["input"].(map[string]any); input != nil {
					query, _ = input["query"].(string)
				}
				raw, _ := json.Marshal(it)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentWebSearch,
					WebSearch: &llm.WebSearchData{
						Query: query,
						Raw:   raw,
					},
				})
			case "web_search_tool_result":
				raw, _ := json.Marshal(it)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentWebSearch,
					WebSearch: &llm.WebSearchData{
						Raw: raw,
					},
				})
			default:
				// ignore
			}
		}
	}

	r.Message = msg
	if len(r.ToolCalls()) > 0 {
		r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: "tool_use"}
	} else {
		sr, _ := raw["stop_reason"].(string)
		r.Finish = llm.NormalizeFinishReason("anthropic", sr)
	}

	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseUsage(u)
	}

	if r.Usage.ReasoningTokens == nil && r.Usage.ReasoningTokensEstimated == nil {
		if est := estimateThinkingTokens(msg.Content); est > 0 {
			e := est
			r.Usage.ReasoningTokensEstimated = &e
		}
	}

	return r
}

func parseUsage(u map[string]any) llm.Usage {
	// Anthropic already separates new input, cache reads, and cache creations,
	// matching llm.Usage's invariant.
	input := llm.IntFromAny(u["input_tokens"])
	output := llm.IntFromAny(u["output_tokens"])
	usage := llm.Usage{
		InputTokens:  input,
		OutputTokens: output,
		Raw:          u,
	}
	total := input + output
	if vAny, ok := u["cache_read_input_tokens"]; ok {
		v := llm.IntFromAny(vAny)
		usage.CacheReadTokens = &v
		total += v
	}
	// When extended-cache-ttl is requested, Anthropic reports a
	// `cache_creation` breakdown alongside the aggregate
	// `cache_creation_input_tokens`. Prefer the breakdown so 5m and 1h
	// writes are priced at their correct rates downstream.
	if breakdown, ok := u["cache_creation"].(map[string]any); ok {
		if vAny, ok := breakdown["ephemeral_5m_input_tokens"]; ok {
			v := llm.IntFromAny(vAny)
			usage.CacheWriteTokens = &v
			total += v
		}
		if vAny, ok := breakdown["ephemeral_1h_input_tokens"]; ok {
			v := llm.IntFromAny(vAny)
			usage.CacheWrite1hTokens = &v
			total += v
		}
	} else if vAny, ok := u["cache_creation_input_tokens"]; ok {
		// Fallback: no breakdown, assume default 5m TTL.
		v := llm.IntFromAny(vAny)
		usage.CacheWriteTokens = &v
		total += v
	}
	usage.TotalTokens = total
	return usage
}

// clampEffort ensures the requested effort level is within the model's supported
// range. If the requested level isn't in supportedLevels, clamp down to the
// highest supported level. If supportedLevels is empty, return the input unchanged.
func clampEffort(requested string, supportedLevels []string) string {
	if len(supportedLevels) == 0 {
		return requested
	}
	requested = strings.ToLower(strings.TrimSpace(requested))

	// Check if requested is directly supported.
	for _, lvl := range supportedLevels {
		if strings.EqualFold(lvl, requested) {
			return requested
		}
	}

	// Effort hierarchy for comparison: low < medium < high < max.
	hierarchy := []string{"low", "medium", "high", "max"}
	reqIdx := -1
	for i, h := range hierarchy {
		if h == requested {
			reqIdx = i
			break
		}
	}
	if reqIdx < 0 {
		// Unknown level; return as-is and let the API handle it.
		return requested
	}

	// Find highest supported level that's at or below requested.
	highestIdx := -1
	for _, lvl := range supportedLevels {
		lvlLower := strings.ToLower(lvl)
		for i, h := range hierarchy {
			if h == lvlLower && i <= reqIdx && i > highestIdx {
				highestIdx = i
			}
		}
	}
	if highestIdx >= 0 {
		return hierarchy[highestIdx]
	}
	// No lower level available; return the lowest supported level.
	for _, h := range hierarchy {
		for _, lvl := range supportedLevels {
			if strings.EqualFold(lvl, h) {
				return h
			}
		}
	}
	return requested
}

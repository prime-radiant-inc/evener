// Package openaichat holds conversion helpers shared by the Chat Completions
// request/response paths of the openai and openaicompat adapters. These keep a
// single source of truth for the OpenAI Chat Completions wire format so the two
// adapters cannot drift.
package openaichat

import (
	"bytes"
	"encoding/json"

	"primeradiant.com/serf/invariant"
	"primeradiant.com/serf/llm"
)

// ToChatResponseFormat maps an llm.ResponseFormat onto the Chat Completions
// "response_format" object.
func ToChatResponseFormat(rf llm.ResponseFormat) map[string]any {
	switch rf.Type {
	case "json", "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		out := map[string]any{"type": "json_schema"}
		if rf.JSONSchema != nil {
			schema := map[string]any{
				"name":   "response",
				"schema": rf.JSONSchema,
			}
			if rf.Strict {
				schema["strict"] = true
			}
			out["json_schema"] = schema
		}
		return out
	default:
		return map[string]any{"type": "text"}
	}
}

// ToChatTools converts tool definitions into the Chat Completions tools array
// ({type: function, function: {name, description?, parameters?}}).
func ToChatTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		fn := map[string]any{
			"name": t.Name,
		}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if t.Parameters != nil {
			fn["parameters"] = t.Parameters
		}
		out[i] = map[string]any{
			"type":     "function",
			"function": fn,
		}
	}
	return out
}

// ToolArgumentsString returns a provider-safe function arguments string.
// OpenAI-family APIs carry arguments as a string, but strict compatibles still
// validate that string as a JSON object when replaying tool-call history.
func ToolArgumentsString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "{}"
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return "{}"
	}
	return string(trimmed)
}

// ParseChatUsage maps a Chat Completions "usage" object onto llm.Usage. OpenAI
// and OpenAI-compatible endpoints report prompt_tokens as total-including-cached,
// so cached_tokens is subtracted to honor llm.Usage's "InputTokens means new
// uncached input" invariant.
func ParseChatUsage(raw map[string]any) llm.Usage {
	rawPrompt := llm.IntFromAny(raw["prompt_tokens"])
	output := llm.IntFromAny(raw["completion_tokens"])
	var cachedRead int
	if details, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		cachedRead = llm.IntFromAny(details["cached_tokens"])
	}
	uncachedInput := rawPrompt - cachedRead
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	usage := llm.Usage{
		InputTokens:  uncachedInput,
		OutputTokens: output,
		Raw:          raw,
	}
	usage.TotalTokens = rawPrompt + output
	if v := llm.IntFromAny(raw["total_tokens"]); v > 0 {
		usage.TotalTokens = v
	}
	if details, ok := raw["completion_tokens_details"].(map[string]any); ok {
		if rt := llm.IntFromAny(details["reasoning_tokens"]); rt > 0 {
			usage.ReasoningTokens = &rt
		}
	}
	if cachedRead > 0 {
		usage.CacheReadTokens = &cachedRead
	}
	// InputTokens is new uncached input; the prompt-minus-cached subtraction above
	// is clamped at zero, so a negative value would mean that clamp regressed.
	invariant.Hold(usage.InputTokens >= 0, "ParseChatUsage produced negative InputTokens: %d", usage.InputTokens)
	return usage
}

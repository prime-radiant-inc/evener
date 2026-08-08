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
	sr, _ := raw["stop_reason"].(string)
	r.Finish = llm.NormalizeFinishReason("anthropic", sr)
	// A response can carry a tool_use content block even when stop_reason
	// isn't "tool_use" — e.g. it can be missing/empty on some non-Anthropic
	// wire-compatible backends. Only fill in "tool_calls" as a fallback when
	// the real stop_reason normalized to nothing more specific than "stop";
	// a genuine stop_reason like "max_tokens" (a tool call truncated by the
	// output-token cap) must not be masked by the mere presence of a tool
	// call (kata mmr2).
	if len(r.ToolCalls()) > 0 && r.Finish.Reason == llm.FinishReasonStop {
		r.Finish = llm.FinishReason{Reason: llm.FinishReasonToolCalls, Raw: "tool_use"}
	}

	// Claude 5+ attaches a stop_details object to a "refusal" stop reason
	// (null for other stop reasons). Surface its category/explanation as a
	// warning so the agent layer can show why generation stopped.
	if details, ok := raw["stop_details"].(map[string]any); ok {
		if w := refusalWarning(details); w != nil {
			r.Warnings = append(r.Warnings, *w)
		}
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

// refusalWarning converts an Anthropic stop_details object (Claude 5+
// "refusal" stop reason) into a human-readable warning. Returns nil when the
// details describe no refusal.
func refusalWarning(details map[string]any) *llm.Warning {
	if typ, _ := details["type"].(string); typ != "refusal" {
		return nil
	}
	msg := "model refused to continue"
	if cat, _ := details["category"].(string); cat != "" {
		msg += " (category: " + cat + ")"
	}
	if expl, _ := details["explanation"].(string); expl != "" {
		msg += ": " + expl
	}
	return &llm.Warning{Code: "refusal", Message: msg}
}

// inbandStreamError decodes an SSE error event payload
// ({"type":"error","error":{"type":...,"message":...}}) into the typed error
// hierarchy. Anthropic delivers these on an HTTP 200 stream, so the error type
// is the only signal of what went wrong.
func inbandStreamError(payload map[string]any) error {
	errObj, _ := payload["error"].(map[string]any)
	rawMsg, _ := errObj["message"].(string)
	msg := strings.TrimSpace(rawMsg)
	if msg == "" {
		msg = "provider reported an in-band stream error"
	}
	typ, _ := errObj["type"].(string)
	return llm.ErrorFromHTTPStatus("anthropic", inbandErrorStatus(typ),
		"messages.create(stream): "+msg, payload, nil)
}

// inbandErrorStatus maps an Anthropic API error type to the HTTP status the
// same condition carries when the API reports it as a response status, so an
// in-band stream failure classifies identically to its out-of-band twin.
// Undocumented types return 0, which lands in the retryable-unknown class with
// the provider's error type preserved as the error code.
func inbandErrorStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "api_error":
		return 500
	case "overloaded_error":
		return 529
	}
	return 0
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
// range. It delegates to llm.ClampReasoningEffort so the anthropic provider
// shares the same full effort vocabulary (minimal/low/medium/high/xhigh/max,
// ranked ascending with max above xhigh) as the rest of serf instead
// of maintaining its own narrower hierarchy that can drift out of sync.
func clampEffort(requested string, supportedLevels []string) string {
	return llm.ClampReasoningEffort(requested, supportedLevels)
}

package google

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"primeradiant.com/serf/llm"
)

func geminiThoughtSignature(part map[string]any, fc map[string]any) string {
	if v, _ := part["thoughtSignature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, _ := part["thought_signature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	// Defensive fallback for non-conformant payloads.
	if v, _ := fc["thoughtSignature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, _ := fc["thought_signature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func normalizeJSONNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalizeJSONNumbers(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalizeJSONNumbers(x[i])
		}
		return out
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		return v
	}
}

func fromGeminiResponse(raw map[string]any, requestedModel string) llm.Response {
	r := llm.Response{
		Provider: "google",
		Model:    requestedModel,
		Raw:      raw,
	}

	msg := llm.Message{Role: llm.RoleAssistant}

	// candidates[0].content.parts
	if cands, ok := raw["candidates"].([]any); ok && len(cands) > 0 {
		if c0, ok := cands[0].(map[string]any); ok {
			if content, ok := c0["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, pAny := range parts {
						p, ok := pAny.(map[string]any)
						if !ok {
							continue
						}
						// Thought parts must be checked BEFORE text parts
						// because thought parts also have a "text" field.
						if thought, _ := p["thought"].(bool); thought {
							text, _ := p["text"].(string)
							if text != "" {
								msg.Content = append(msg.Content, llm.ContentPart{
									Kind: llm.ContentThinking,
									Thinking: &llm.ThinkingData{
										Text: text,
									},
								})
							}
							continue
						}
						if t, _ := p["text"].(string); t != "" {
							msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: t})
							continue
						}
						if fc, ok := p["functionCall"].(map[string]any); ok {
							name, _ := fc["name"].(string)
							argsAny := fc["args"]
							argsRaw, _ := json.Marshal(argsAny)
							thoughtSig := geminiThoughtSignature(p, fc)
							msg.Content = append(msg.Content, llm.ContentPart{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:               "call_" + ulid.Make().String(), // synthetic per spec
									Name:             name,
									Arguments:        argsRaw,
									Type:             "function",
									ThoughtSignature: thoughtSig,
								},
							})
						}
					}
				}
			}
			if fr, _ := c0["finishReason"].(string); fr != "" {
				r.Finish = llm.NormalizeFinishReason("google", fr)
			}
			if gm, ok := c0["groundingMetadata"].(map[string]any); ok && len(gm) > 0 {
				query := ""
				if wq, ok := gm["webSearchQueries"].([]any); ok && len(wq) > 0 {
					qs := make([]string, 0, len(wq))
					for _, q := range wq {
						if s, ok := q.(string); ok {
							qs = append(qs, s)
						}
					}
					query = strings.Join(qs, "; ")
				}
				raw, _ := json.Marshal(gm)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentWebSearch,
					WebSearch: &llm.WebSearchData{
						Query: query,
						Raw:   raw,
					},
				})
			}
		}
	}

	r.Message = msg
	// Gemini returns finishReason "STOP" even when making tool calls (it has no
	// distinct tool-call finish reason). Override to "tool_calls" so the Generate
	// loop correctly continues with tool execution.
	if len(r.ToolCalls()) > 0 {
		r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: r.Finish.Raw}
	} else if r.Finish.Reason == "" {
		r.Finish = llm.FinishReason{Reason: "stop"}
	}

	if um, ok := raw["usageMetadata"].(map[string]any); ok {
		r.Usage = parseUsage(um)
	}
	return r
}

func parseUsage(u map[string]any) llm.Usage {
	usage := llm.Usage{
		InputTokens:  llm.IntFromAny(u["promptTokenCount"]),
		OutputTokens: llm.IntFromAny(u["candidatesTokenCount"]),
		TotalTokens:  llm.IntFromAny(u["totalTokenCount"]),
		Raw:          u,
	}
	if v := llm.IntFromAny(u["cachedContentTokenCount"]); v > 0 {
		usage.CacheReadTokens = &v
	}
	if v := llm.IntFromAny(u["thoughtsTokenCount"]); v > 0 {
		usage.ReasoningTokens = &v
	}
	return usage
}

// classifyGeminiError reclassifies an HTTP-based error using the gRPC status
// from the Gemini error response body. Gemini can return HTTP 400 with gRPC
// statuses like RESOURCE_EXHAUSTED, which should map to RateLimitError rather
// than InvalidRequestError. Only reclassifies when the HTTP status is ambiguous
// (400, 422); specific HTTP codes like 429 or 503 already map correctly.
func classifyGeminiError(httpStatus int, body []byte, retryAfter *time.Duration, httpErr error) error {
	// Only reclassify ambiguous HTTP statuses. Specific statuses (401, 403,
	// 404, 429, 5xx) already produce the correct error type.
	if httpStatus != 400 && httpStatus != 422 {
		return httpErr
	}

	var errResp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error.Status == "" {
		return httpErr
	}

	// Map gRPC statuses to the HTTP status code that produces the correct
	// error type via ErrorFromHTTPStatus.
	var syntheticHTTP int
	switch errResp.Error.Status {
	case "RESOURCE_EXHAUSTED":
		syntheticHTTP = 429
	case "DEADLINE_EXCEEDED":
		syntheticHTTP = 408
	case "UNAVAILABLE", "INTERNAL":
		syntheticHTTP = 503
	default:
		return httpErr
	}

	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	return llm.ErrorFromHTTPStatus("google", syntheticHTTP, errResp.Error.Message, raw, retryAfter)
}

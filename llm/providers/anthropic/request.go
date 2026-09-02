package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"

	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// fallbackMaxTokens is the output cap requested when the caller names none.
// Liberal on purpose: a model that can't honor it fails loudly with a 400
// (a registry row to fix) instead of silently truncating output.
const fallbackMaxTokens = 32000

// cacheMarker returns an ephemeral cache_control marker, adding ttl when the
// caller has one (the extended-cache-ttl beta; the old builder always passes
// "").
func cacheMarker(ttl string) map[string]any {
	m := map[string]any{"type": "ephemeral"}
	if ttl != "" {
		m["ttl"] = ttl
	}
	return m
}

// applyAnthropicTools writes tool_choice, tools, and the web-search tool
// (when webSearch is on), and marks the last tool for caching.
func applyAnthropicTools(body map[string]any, req llm.Request, webSearch bool) error {
	includeTools := len(req.Tools) > 0
	if req.ToolChoice != nil {
		switch strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode)) {
		case "", "auto":
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "auto"}
			}
		case "none":
			includeTools = false
		case "required":
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "any"}
			}
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				return &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice.Name}
			}
		default:
			return llm.NewUnsupportedToolChoiceError("anthropic", req.ToolChoice.Mode)
		}
	}
	if includeTools || webSearch {
		var tools []map[string]any
		if includeTools {
			toolDefs := req.Tools
			if webSearch {
				// Strip any function-type "web_search" tool to avoid a
				// duplicate name collision with the server-side web_search
				// tool injected below.
				filtered := toolDefs[:0:0]
				for _, td := range toolDefs {
					if td.Name != "web_search" {
						filtered = append(filtered, td)
					}
				}
				toolDefs = filtered
			}
			tools = toAnthropicTools(toolDefs)
		}
		if webSearch {
			tools = append(tools, map[string]any{
				"type": "web_search_20250305",
				"name": "web_search",
			})
		}
		if len(tools) > 0 {
			tools[len(tools)-1]["cache_control"] = cacheMarker("")
		}
		body["tools"] = tools
	}
	return nil
}

// reconcileThinkingContract enforces the Anthropic request contracts that
// depend on the FINAL overlaid body state. Any active thinking shape triggers
// tool-choice downgrade; only budget-shaped thinking triggers the strict
// max_tokens > budget_tokens rule.
func reconcileThinkingContract(body map[string]any, req llm.Request, res registry.Resolved) error {
	thinkingBudget := finalThinkingBudget(body)
	if finalThinkingActive(body) {
		if tc, ok := body["tool_choice"].(map[string]any); ok {
			if t, _ := tc["type"].(string); t == "any" || t == "tool" {
				body["tool_choice"] = map[string]any{"type": "auto"}
			}
		}
	}
	reconcileOutputField(body, "max_tokens", req.MaxTokens, res.Caps.MaxOutputTokens)
	if thinkingBudget > 0 {
		mt := intFromAny(body["max_tokens"])
		if mt <= thinkingBudget {
			return anthropicThinkingBudgetError(req, res, thinkingBudget+1, mt)
		}
	}
	if invariant.Enabled {
		// When the final body carries a positive thinking budget, Anthropic rejects
		// a request that also forces tool use, or whose max_tokens does not
		// strictly exceed that budget. The guards above establish both contracts;
		// assert they survived everything that runs after them.
		if thinkingBudget > 0 {
			if tc, ok := body["tool_choice"].(map[string]any); ok {
				t, _ := tc["type"].(string)
				invariant.Hold(t != "any" && t != "tool",
					"anthropic request contract: tool_choice %q forces tool use while budget thinking is enabled", t)
			}
			mt, _ := body["max_tokens"].(int)
			invariant.Hold(mt > thinkingBudget,
				"anthropic request contract: max_tokens %d does not exceed thinking budget %d", mt, thinkingBudget)
		}
	}
	return nil
}

func finalThinkingActive(body map[string]any) bool {
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		return false
	}
	typ, _ := thinking["type"].(string)
	return !strings.EqualFold(strings.TrimSpace(typ), "disabled")
}

func finalThinkingBudget(body map[string]any) int {
	thinking, _ := body["thinking"].(map[string]any)
	if thinking == nil {
		return 0
	}
	if typ, _ := thinking["type"].(string); typ != "enabled" {
		return 0
	}
	return intFromAny(thinking["budget_tokens"])
}

func anthropicThinkingBudgetError(req llm.Request, res registry.Resolved, requiredOutput, maximum int) error {
	provider := strings.TrimSpace(res.Instance)
	if provider == "" {
		provider = strings.TrimSpace(req.Provider)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(res.ModelID)
	}
	return &llm.ContextBudgetError{
		Provider:     provider,
		Model:        model,
		Limit:        "max_output_tokens",
		InputTokens:  0,
		OutputTokens: requiredOutput,
		Maximum:      maximum,
	}
}

func reconcileOutputField(body map[string]any, field string, admitted, outputCap *int) {
	if ceiling := minPositiveInt(intFromAny(body[field]), positivePointerInt(admitted), positivePointerInt(outputCap)); ceiling > 0 {
		body[field] = ceiling
	}
}

func positivePointerInt(v *int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return 0
}

func minPositiveInt(values ...int) int {
	best := 0
	for _, v := range values {
		if v <= 0 {
			continue
		}
		if best == 0 || v < best {
			best = v
		}
	}
	return best
}

func applyAnthropicResponseFormat(system string, rf *llm.ResponseFormat) (string, error) {
	if rf == nil {
		return system, nil
	}
	typ := strings.ToLower(strings.TrimSpace(rf.Type))
	switch typ {
	case "", "text":
		return system, nil
	case "json":
		return strings.TrimSpace(system + "\n\nOutput only valid JSON. Do not include any extra text."), nil
	case "json_schema":
		if rf.JSONSchema == nil {
			return system, nil
		}
		b, err := json.Marshal(rf.JSONSchema)
		if err != nil {
			return "", err
		}
		inst := "Output only valid JSON that matches this JSON Schema. Do not include any extra text.\n\nJSON Schema:\n" + string(b)
		return strings.TrimSpace(system + "\n\n" + inst), nil
	default:
		return system, nil
	}
}

func betaHeaderFromProviderOptions(opts map[string]any) string {
	if opts == nil {
		return ""
	}
	aAny, ok := opts["anthropic"]
	if !ok {
		return ""
	}
	m, ok := aAny.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m["beta_headers"]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []string:
		return strings.Join(x, ",")
	case []any:
		var parts []string
		for _, it := range x {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func toAnthropicTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		// Anthropic rejects input_schema with oneOf/anyOf/allOf at the top level.
		// Copy-on-write: only allocate if we need to strip keys.
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if _, has := params[key]; has {
				params = shallowCopyMap(params)
				delete(params, "anyOf")
				delete(params, "oneOf")
				delete(params, "allOf")
				break
			}
		}
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": params,
		})
	}
	return out
}

func shallowCopyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

func anthropicImageBlock(p llm.ContentPart) (map[string]any, error) {
	u := strings.TrimSpace(p.Image.URL)
	if len(p.Image.Data) > 0 || llm.IsLocalPath(u) {
		var b []byte
		var err error
		mt := strings.TrimSpace(p.Image.MediaType)
		if len(p.Image.Data) > 0 {
			b = p.Image.Data
			if mt == "" {
				mt = "image/png"
			}
		} else {
			path := llm.ExpandTilde(u)
			b, err = os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if mt == "" {
				mt = llm.InferMimeTypeFromPath(path)
			}
			if mt == "" {
				mt = "image/png"
			}
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mt,
				"data":       base64.StdEncoding.EncodeToString(b),
			},
		}, nil
	}
	if u != "" {
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "url",
				"url":  u,
			},
		}, nil
	}
	return nil, nil
}

func toAnthropicMessages(msgs []llm.Message) (system string, messages []map[string]any, _ error) {
	var sysParts []string
	appendMessage := func(role string, content []map[string]any) {
		if len(content) == 0 {
			return
		}
		// Anthropic requires user/assistant alternation; merge same-role neighbors.
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			if lastRole, _ := last["role"].(string); lastRole == role {
				if lastContent, ok := last["content"].([]map[string]any); ok {
					last["content"] = append(lastContent, content...)
					return
				}
			}
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": content,
		})
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
				sysParts = append(sysParts, t)
			}
		case llm.RoleUser:
			var blocks []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					block, err := anthropicImageBlock(p)
					if err != nil {
						return "", nil, err
					}
					if block != nil {
						blocks = append(blocks, block)
					}
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for anthropic: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendMessage("user", blocks)
		case llm.RoleAssistant:
			var blocks []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					block, err := anthropicImageBlock(p)
					if err != nil {
						return "", nil, err
					}
					if block != nil {
						blocks = append(blocks, block)
					}
				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					in := map[string]any{}
					if len(p.ToolCall.Arguments) > 0 {
						_ = json.Unmarshal(p.ToolCall.Arguments, &in)
					}
					// Anthropic requires input to be a dictionary, never null.
					if in == nil {
						in = map[string]any{}
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    p.ToolCall.ID,
						"name":  p.ToolCall.Name,
						"input": in,
					})
				case llm.ContentThinking:
					if p.Thinking == nil || p.Thinking.Text == "" {
						// Encrypted-only thinking parts (OpenAI-compatible or
						// Responses transcripts riding a cross-provider model
						// switch) carry no replayable Anthropic thinking; an
						// empty thinking:"" block is invalid continuation state.
						continue
					}
					sig := p.Thinking.Signature
					if llm.IsOpenAICompatReasoningField(sig) {
						// Thinking that arrived via an OpenAI-compatible
						// provider carries its wire field name here, not an
						// Anthropic cryptographic signature; replay unsigned.
						sig = ""
					}
					blocks = append(blocks, map[string]any{
						"type":      "thinking",
						"thinking":  p.Thinking.Text,
						"signature": sig,
					})
				case llm.ContentRedThinking:
					if p.Thinking == nil {
						continue
					}
					blocks = append(blocks, map[string]any{
						"type": "redacted_thinking",
						"data": p.Thinking.Text,
					})
				case llm.ContentWebSearch:
					if p.WebSearch == nil || len(p.WebSearch.Raw) == 0 {
						continue
					}
					var rawBlock map[string]any
					if err := json.Unmarshal(p.WebSearch.Raw, &rawBlock); err == nil {
						blocks = append(blocks, rawBlock)
					}
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for anthropic: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendMessage("assistant", blocks)
		case llm.RoleTool:
			// Tool results are provided as user messages with tool_result blocks.
			var blocks []map[string]any
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				var outStr string
				switch v := p.ToolResult.Content.(type) {
				case string:
					outStr = v
				default:
					b, _ := json.Marshal(v)
					outStr = string(b)
				}
				var resultContent any
				if len(p.ToolResult.ImageData) > 0 {
					mediaType := p.ToolResult.ImageMediaType
					if mediaType == "" {
						mediaType = "image/png"
					}
					resultContent = []map[string]any{
						{"type": "text", "text": outStr},
						{"type": "image", "source": map[string]any{
							"type":       "base64",
							"media_type": mediaType,
							"data":       base64.StdEncoding.EncodeToString(p.ToolResult.ImageData),
						}},
					}
				} else {
					resultContent = outStr
				}

				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": p.ToolResult.ToolCallID,
					"content":     resultContent,
					"is_error":    p.ToolResult.IsError,
				})
			}
			appendMessage("user", blocks)
		default:
			// ignore
		}
	}

	return strings.Join(sysParts, "\n\n"), messages, nil
}

// estimateThinkingTokens returns a rough char/4 estimate of thinking
// content in a response. Used only when the provider doesn't report a
// native reasoning-token count. Deliberately imprecise — caller must
// route this into Usage.ReasoningTokensEstimated (not ReasoningTokens).
func estimateThinkingTokens(parts []llm.ContentPart) int {
	chars := 0
	for _, p := range parts {
		if (p.Kind == llm.ContentThinking || p.Kind == llm.ContentRedThinking) && p.Thinking != nil {
			chars += len(p.Thinking.Text)
		}
	}
	if chars == 0 {
		return 0
	}
	est := max(chars/4, 1)
	return est
}

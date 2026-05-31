package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
)

// buildRequestBody constructs the Anthropic Messages API request body from a
// unified llm.Request.
func (a *Adapter) buildRequestBody(req llm.Request) (map[string]any, error) {
	system, messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	system, err = applyAnthropicResponseFormat(system, req.ResponseFormat)
	if err != nil {
		return nil, err
	}

	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	// Strip the [1m] suffix — it's a client-side convention, not an API model ID.
	apiModel := strings.TrimSuffix(req.Model, "[1m]")

	body := map[string]any{
		"model":         apiModel,
		"max_tokens":    maxTokens,
		"messages":      messages,
		"cache_control": map[string]any{"type": "ephemeral"},
	}
	if strings.TrimSpace(system) != "" {
		body["system"] = []map[string]any{{
			"type":          "text",
			"text":          system,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop_sequences"] = req.StopSequences
	}
	if strings.TrimSpace(req.ServiceTier) != "" {
		body["service_tier"] = strings.TrimSpace(req.ServiceTier)
	}

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
				return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice.Name}
			}
		default:
			return nil, llm.NewUnsupportedToolChoiceError("anthropic", req.ToolChoice.Mode)
		}
	}
	if includeTools || req.WebSearch {
		var tools []map[string]any
		if includeTools {
			toolDefs := req.Tools
			if req.WebSearch {
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
		if req.WebSearch {
			tools = append(tools, map[string]any{
				"type": "web_search_20250305",
				"name": "web_search",
			})
		}
		if len(tools) > 0 {
			tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		body["tools"] = tools
	}
	// Determine model capabilities from catalog.
	var adaptiveThinking, supportsEffort bool
	var effortLevels []string
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.GetModelInfo(apiModel); mi != nil {
			adaptiveThinking = mi.SupportsAdaptiveThinking
			supportsEffort = mi.SupportsEffortParameter
			effortLevels = mi.ReasoningEffortLevels
		}
	}

	if adaptiveThinking {
		// New path: Opus 4.6, Sonnet 4.6, Mythos — adaptive thinking.
		body["thinking"] = map[string]any{"type": "adaptive"}
		if req.ReasoningEffort != nil {
			effort := clampEffort(*req.ReasoningEffort, effortLevels)
			body["output_config"] = map[string]any{"effort": effort}
		}
	} else if req.ReasoningEffort != nil {
		// Legacy manual thinking path.
		effort := clampEffort(*req.ReasoningEffort, effortLevels)
		budget := llm.ReasoningBudget(effort)
		if budget > 0 {
			body["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": budget,
			}
			// Anthropic requires max_tokens > budget_tokens. The unified MaxTokens
			// represents desired output tokens, so add the budget on top.
			if maxTokens <= budget {
				body["max_tokens"] = budget + maxTokens
			}
		}
		// Hybrid: Opus 4.5 accepts effort even with manual thinking.
		if supportsEffort {
			body["output_config"] = map[string]any{"effort": effort}
		}
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["anthropic"].(map[string]any); ok {
			for k, v := range ov {
				if k == "beta_headers" {
					continue
				}
				body[k] = v
			}
		}
	}
	return body, nil
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
	for k, v := range in {
		out[k] = v
	}
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
					var in any
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
					if p.Thinking == nil {
						continue
					}
					blocks = append(blocks, map[string]any{
						"type":      "thinking",
						"thinking":  p.Thinking.Text,
						"signature": p.Thinking.Signature,
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
	est := chars / 4
	if est < 1 {
		est = 1
	}
	return est
}

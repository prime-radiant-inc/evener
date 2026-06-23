package openaicompat

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
)

func buildRequestBody(req llm.Request, stream bool, quirks ProviderQuirks) (map[string]any, error) {
	body := map[string]any{
		"model": req.Model,
	}

	// Check if provider options supply a custom "reasoning" field (e.g. OpenRouter
	// MiniMax format: {"reasoning": {"enabled": true}}). When present, skip the
	// default reasoning_effort field and use reasoning_details for multi-turn.
	useReasoningDetails := false
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any); ok {
			if _, has := ov["reasoning"]; has {
				useReasoningDetails = true
			}
		}
	}

	msgs, err := toChatMessages(req.Messages, quirks, useReasoningDetails)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		body["tools"] = openaichat.ToChatTools(req.Tools)
	}
	if req.ToolChoice != nil {
		tc, err := toChatToolChoice(*req.ToolChoice)
		if err != nil {
			return nil, err
		}
		body["tool_choice"] = tc
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		body["response_format"] = openaichat.ToChatResponseFormat(*req.ResponseFormat)
	}
	if req.ReasoningEffort != nil && !useReasoningDetails {
		effort := *req.ReasoningEffort
		// OpenRouter uses "xhigh" where we say "max".
		if quirks.TranslateMaxToXHigh && strings.EqualFold(effort, "max") {
			effort = "xhigh"
		}
		body["reasoning_effort"] = effort
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	// Provider options passthrough.
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}

	// Apply provider quirks.
	if quirks.LockTemperature {
		delete(body, "temperature")
	}
	if quirks.LockTopP {
		delete(body, "top_p")
	}
	if quirks.LockFrequencyPenalty {
		delete(body, "frequency_penalty")
	}
	if quirks.LockPresencePenalty {
		delete(body, "presence_penalty")
	}
	if quirks.ToolChoiceAutoOnly {
		if tc, ok := body["tool_choice"]; ok {
			switch tc {
			case "auto", "none":
				// allowed
			default:
				body["tool_choice"] = "auto"
			}
		}
	}
	if quirks.MaxStopSequences > 0 {
		if stops, ok := body["stop"].([]string); ok && len(stops) > quirks.MaxStopSequences {
			body["stop"] = stops[:quirks.MaxStopSequences]
		}
	}
	if quirks.NoJSONSchema {
		if rf, ok := body["response_format"].(map[string]any); ok {
			if rf["type"] == "json_schema" {
				body["response_format"] = map[string]any{"type": "json_object"}
			}
		}
	}

	return body, nil
}

func toChatMessages(messages []llm.Message, quirks ProviderQuirks, useReasoningDetails bool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		content := m.Content
		if quirks.StripEmptyContent {
			filtered := make([]llm.ContentPart, 0, len(content))
			for _, p := range content {
				if p.Kind == llm.ContentText && p.Text == "" {
					continue
				}
				filtered = append(filtered, p)
			}
			content = filtered
		}

		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			out = append(out, map[string]any{
				"role":    "system",
				"content": textFromParts(content),
			})
		case llm.RoleUser:
			if hasImageContent(content) {
				out = append(out, map[string]any{
					"role":    "user",
					"content": buildMultimodalParts(content),
				})
			} else {
				out = append(out, map[string]any{
					"role":    "user",
					"content": textFromParts(content),
				})
			}
		case llm.RoleAssistant:
			msg := map[string]any{
				"role": "assistant",
			}
			text := textFromParts(content)
			calls := toolCallsFromParts(content)
			reasoning := thinkingFromParts(content)
			if reasoning != "" {
				if useReasoningDetails {
					// MiniMax format: {type: "reasoning.text", text: "...", format: "unknown", index: 0}
					msg["reasoning_details"] = []map[string]any{
						{
							"type":   "reasoning.text",
							"text":   reasoning,
							"format": "unknown",
							"index":  0,
						},
					}
				} else {
					msg["reasoning_content"] = reasoning
				}
			}
			if len(calls) > 0 {
				msg["tool_calls"] = calls
				if text != "" {
					msg["content"] = text
				}
			} else {
				msg["content"] = text
			}
			out = append(out, msg)
		case llm.RoleTool:
			for _, p := range content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					resultContent := ""
					switch v := p.ToolResult.Content.(type) {
					case string:
						resultContent = v
					default:
						b, _ := json.Marshal(v)
						resultContent = string(b)
					}
					out = append(out, map[string]any{
						"role":         "tool",
						"tool_call_id": p.ToolResult.ToolCallID,
						"content":      resultContent,
					})
				}
			}
		}
	}
	return out, nil
}

func textFromParts(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == llm.ContentText {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func thinkingFromParts(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Thinking.Text)
		}
	}
	return b.String()
}

// hasImageContent returns true if any content part is an image.
func hasImageContent(parts []llm.ContentPart) bool {
	for _, p := range parts {
		if p.Kind == llm.ContentImage {
			return true
		}
	}
	return false
}

// buildMultimodalParts converts content parts to Chat Completions content array format
// with {"type": "text", ...} and {"type": "image_url", ...} objects.
func buildMultimodalParts(parts []llm.ContentPart) []map[string]any {
	var out []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case llm.ContentImage:
			if p.Image == nil {
				continue
			}
			imgURL := p.Image.URL
			if imgURL == "" && len(p.Image.Data) > 0 {
				mt := p.Image.MediaType
				if mt == "" {
					mt = "image/png"
				}
				imgURL = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(p.Image.Data)
			}
			if imgURL == "" {
				continue
			}
			urlObj := map[string]any{"url": imgURL}
			if p.Image.Detail != "" {
				urlObj["detail"] = p.Image.Detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": urlObj})
		}
	}
	return out
}

func toolCallsFromParts(parts []llm.ContentPart) []map[string]any {
	var calls []map[string]any
	for _, p := range parts {
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
			calls = append(calls, map[string]any{
				"id":   p.ToolCall.ID,
				"type": "function",
				"function": map[string]any{
					"name":      p.ToolCall.Name,
					"arguments": openaichat.ToolArgumentsString(p.ToolCall.Arguments),
				},
			})
		}
	}
	return calls
}

func toChatToolChoice(tc llm.ToolChoice) (any, error) {
	switch strings.ToLower(strings.TrimSpace(tc.Mode)) {
	case "", "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "required":
		return "required", nil
	case "named":
		if strings.TrimSpace(tc.Name) == "" {
			return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}, nil
	default:
		return nil, llm.NewUnsupportedToolChoiceError("openai-compatible", tc.Mode)
	}
}

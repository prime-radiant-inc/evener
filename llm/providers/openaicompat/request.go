package openaicompat

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
)

func buildRequestBody(req llm.Request, stream bool, mc ModelCompat) (map[string]any, error) {
	quirks := mc.Quirks
	body := map[string]any{
		"model": req.Model,
	}

	// Check if provider options supply a custom "reasoning" field (e.g. OpenRouter
	// MiniMax format: {"reasoning": {"enabled": true}}). When present, skip the
	// default reasoning_effort field and use reasoning_details for multi-turn.
	// A model declared reasoning=false ignores the trigger — its replay must
	// not route through reasoning_details either.
	useReasoningDetails := false
	if req.ProviderOptions != nil && !mc.ReasoningOff {
		if ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any); ok {
			if _, has := ov["reasoning"]; has {
				useReasoningDetails = true
			}
		}
	}

	msgs, err := toChatMessages(req.Messages, mc, useReasoningDetails)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		tools := openaichat.ToChatTools(req.Tools)
		// Only emit strict when the instance explicitly opts in. Serf diverges
		// from Pi (whose default always sends strict:false); see
		// providercfg.CompatConfig.SupportsStrictMode.
		if quirks.SupportsStrictMode != nil && *quirks.SupportsStrictMode {
			for _, t := range tools {
				if fn, ok := t["function"].(map[string]any); ok {
					fn["strict"] = false
				}
			}
		}
		body["tools"] = tools
		if quirks.ToolStream {
			body["tool_stream"] = true
		}
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
	maxTokensField := quirks.MaxTokensField
	if maxTokensField == "" {
		maxTokensField = "max_tokens"
	}
	if req.MaxTokens != nil {
		body[maxTokensField] = *req.MaxTokens
	} else if mc.DefaultMaxTokens > 0 {
		body[maxTokensField] = mc.DefaultMaxTokens
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		body["response_format"] = openaichat.ToChatResponseFormat(*req.ResponseFormat)
	}
	if !useReasoningDetails {
		applyThinkingFormat(body, req, mc)
	}
	if quirks.SendStoreFalse {
		body["store"] = false
	}
	if quirks.SupportsLongCacheRetention {
		if key := promptCacheKey(req); key != "" {
			body["prompt_cache_key"] = key
			body["prompt_cache_retention"] = "24h"
		}
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if stream {
		body["stream"] = true
		if !quirks.OmitStreamUsage {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	}

	// Provider options passthrough. A reasoning=false model drops the known
	// reasoning-control keys — profile-injected options (e.g. the
	// openrouter/minimax reasoning:{enabled:true}) must not re-enable what
	// the model config turned off; everything else passes through untouched.
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any); ok {
			for k, v := range ov {
				if mc.ReasoningOff && isReasoningControlKey(k) {
					continue
				}
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
	if quirks.CacheControlFormat == "anthropic" {
		anthropicCacheControl(body, quirks.SupportsLongCacheRetention)
	}

	return body, nil
}

// isReasoningControlKey reports whether a request-body key is one of the
// reasoning controls the thinking formats emit — the set a reasoning=false
// model must never carry, whatever the source.
func isReasoningControlKey(k string) bool {
	switch k {
	case "reasoning", "reasoning_effort", "thinking", "enable_thinking", "chat_template_kwargs":
		// chat_template_kwargs is how the qwen/chat-template formats toggle
		// thinking; on a reasoning=false model any passthrough kwargs are
		// presumed thinking controls and dropped with the rest.
		return true
	}
	return false
}

// promptCacheKey returns the prompt cache key to send: the request's explicit
// PromptCacheKey when set, else the session-derived "serf-session-"+SessionID
// (mirroring agent.Session's openai-path convention so the two agree), else ""
// when there is no key material to cache on.
func promptCacheKey(req llm.Request) string {
	if k := strings.TrimSpace(req.PromptCacheKey); k != "" {
		return k
	}
	if sid := strings.TrimSpace(req.SessionID); sid != "" {
		return "serf-session-" + sid
	}
	return ""
}

// applyThinkingFormat emits the reasoning controls in the wire dialect the
// provider speaks. No effort on the request means "send nothing" for every
// format — serf's "none" clears the setting to the provider default rather
// than forcing an explicit disable. A model declared reasoning=false
// (mc.ReasoningOff) emits nothing regardless of the request's effort: the
// adapter enforces its own declared non-reasoning models rather than relying
// on the caller (e.g. the session-side profile clamp) to have done so.
func applyThinkingFormat(body map[string]any, req llm.Request, mc ModelCompat) {
	if mc.ReasoningOff {
		return
	}
	quirks := mc.Quirks
	wire := ""
	if req.ReasoningEffort != nil {
		wire = mc.wireEffort(*req.ReasoningEffort)
	}
	if wire == "" {
		return
	}
	switch quirks.ThinkingFormat {
	case "", "openai":
		if quirks.supportsEffort(true) {
			body["reasoning_effort"] = wire
		}
	case "zai":
		// clear_thinking:false keeps z.ai from pruning prior-turn reasoning
		// server-side; serf manages thinking replay itself.
		body["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
		if quirks.supportsEffort(false) {
			body["reasoning_effort"] = wire
		}
	case "deepseek":
		body["thinking"] = map[string]any{"type": "enabled"}
		if quirks.supportsEffort(true) {
			body["reasoning_effort"] = wire
		}
	case "openrouter":
		body["reasoning"] = map[string]any{"effort": wire}
	case "together":
		body["reasoning"] = map[string]any{"enabled": true}
		if quirks.supportsEffort(true) {
			body["reasoning_effort"] = wire
		}
	case "qwen":
		body["enable_thinking"] = true
	case "qwen-chat-template":
		// Divergence from Pi: Pi always emits enable_thinking (false when off).
		// serf's none-clears convention means we only reach here with an effort
		// set (wire != "" above), so enable_thinking is always true.
		body["chat_template_kwargs"] = map[string]any{
			"enable_thinking":   true,
			"preserve_thinking": true,
		}
	case "chat-template":
		// Emit the configured kwargs verbatim. An empty/absent map omits the
		// field entirely — the deliberate "resolved-empty" rule that keeps
		// cross-layer validation out of Load.
		if len(quirks.ChatTemplateKwargs) > 0 {
			body["chat_template_kwargs"] = quirks.ChatTemplateKwargs
		}
	case "string-thinking":
		body["thinking"] = wire
	}
}

func toChatMessages(messages []llm.Message, mc ModelCompat, useReasoningDetails bool) ([]map[string]any, error) {
	quirks := mc.Quirks
	systemRole := "system"
	if quirks.UseDeveloperRole {
		systemRole = "developer"
	}
	// Tool names for RequireToolResultName: a tool result only carries the
	// call id, so recover the name from the assistant tool_call that issued it.
	var toolNamesByCallID map[string]string
	if quirks.RequireToolResultName {
		toolNamesByCallID = map[string]string{}
		for _, m := range messages {
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					toolNamesByCallID[p.ToolCall.ID] = p.ToolCall.Name
				}
			}
		}
	}

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
				"role":    systemRole,
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
			if mc.ReasoningOff {
				// A declared non-reasoning model accepts no reasoning fields —
				// prior-turn thinking (a fallback or model switch can carry it)
				// is dropped from replay entirely. ThinkingAsText below is the
				// deliberate exception: it rides ordinary text content.
				if !quirks.ThinkingAsText {
					reasoning = ""
				}
			}
			if reasoning != "" && quirks.ThinkingAsText {
				// Replay thinking as ordinary text (no tags — tagged thinking
				// teaches the model to mimic the tags). The reasoning field
				// stays unset.
				if text != "" {
					text = reasoning + "\n\n" + text
				} else {
					text = reasoning
				}
				reasoning = ""
			}
			var encrypted []map[string]any
			if !mc.ReasoningOff {
				encrypted = encryptedDetailsFromParts(content)
			}
			// Encrypted reasoning (OpenRouter Gemini/o-series) must round-trip
			// through reasoning_details regardless of the useReasoningDetails
			// flag. When present, text rides the same array (text first, then
			// encrypted) to mirror OpenRouter's unified reasoning_details shape.
			if reasoning != "" || len(encrypted) > 0 {
				if useReasoningDetails || len(encrypted) > 0 {
					var details []map[string]any
					if reasoning != "" {
						// MiniMax format: {type: "reasoning.text", text: "...", format: "unknown", index: 0}
						details = append(details, map[string]any{
							"type":   "reasoning.text",
							"text":   reasoning,
							"format": "unknown",
							"index":  0,
						})
					}
					details = append(details, encrypted...)
					msg["reasoning_details"] = details
				} else {
					msg[reasoningReplayField(content)] = reasoning
				}
			} else if quirks.EmptyReasoningContentOnAssistant && !useReasoningDetails && !mc.ReasoningOff {
				msg["reasoning_content"] = ""
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
					toolMsg := map[string]any{
						"role":         "tool",
						"tool_call_id": p.ToolResult.ToolCallID,
						"content":      resultContent,
					}
					if quirks.RequireToolResultName {
						name := p.ToolResult.Name
						if name == "" {
							name = toolNamesByCallID[p.ToolResult.ToolCallID]
						}
						if name != "" {
							toolMsg["name"] = name
						}
					}
					out = append(out, toolMsg)
				}
			}
		}
	}
	if quirks.RequireAssistantAfterToolResult {
		out = insertAssistantAfterToolResults(out)
	}
	return out, nil
}

// insertAssistantAfterToolResults places an empty assistant message between a
// tool result and a following user message, for providers whose templates
// reject the tool→user transition.
func insertAssistantAfterToolResults(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for i, m := range msgs {
		out = append(out, m)
		if m["role"] == "tool" && i+1 < len(msgs) && msgs[i+1]["role"] == "user" {
			out = append(out, map[string]any{"role": "assistant", "content": ""})
		}
	}
	return out
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

// reasoningReplayField returns the wire field assistant thinking replays to:
// the field the thinking originally arrived on (recorded in the part's
// Signature) when it names a known reasoning field, else reasoning_content.
// Signatures that aren't field names (e.g. Anthropic crypto blobs riding a
// cross-provider transcript) fall back to the default.
func reasoningReplayField(parts []llm.ContentPart) string {
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
			if isReasoningFieldName(p.Thinking.Signature) {
				return p.Thinking.Signature
			}
			break
		}
	}
	return "reasoning_content"
}

// encryptedDetailsFromParts decodes the opaque encrypted reasoning_details
// items carried on assistant thinking parts (serialized in EncryptedContent),
// concatenated in part order, ready to replay in the reasoning_details array.
func encryptedDetailsFromParts(parts []llm.ContentPart) []map[string]any {
	var out []map[string]any
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil &&
			llm.IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
			// The guard also keeps a foreign provider's opaque blob (OpenAI
			// Responses encrypted_content) off the wire on a cross-provider
			// transcript — it would not parse as our item array anyway.
			var items []map[string]any
			if err := json.Unmarshal([]byte(p.Thinking.EncryptedContent), &items); err == nil {
				out = append(out, items...)
			}
		}
	}
	return out
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

package chatcompletions

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

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

// toChatMessages converts messages to the Chat Completions wire array (spec
// §8.4). Every per-model quirk it applies reads off caps.
func toChatMessages(messages []llm.Message, caps registry.Caps, useReasoningDetails bool) ([]map[string]any, error) {
	reasoningOff := caps.Reasoning != nil && !*caps.Reasoning
	systemRole := "system"
	if caps.Fields[registry.FieldDeveloperRole] {
		systemRole = "developer"
	}
	// Tool names for ToolResultName: a tool result only carries the call id,
	// so recover the name from the assistant tool_call that issued it.
	var toolNamesByCallID map[string]string
	if registry.BoolValue(caps.ToolResultName) {
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
		if registry.BoolValue(caps.StripEmptyContent) {
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
			if hasImageOrDocumentContent(content) {
				parts, err := buildMultimodalParts(content, caps)
				if err != nil {
					return nil, err
				}
				out = append(out, map[string]any{
					"role":    "user",
					"content": parts,
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
			var encrypted []json.RawMessage
			if !reasoningOff {
				encrypted = encryptedDetailsFromParts(content)
			}
			thinkingAsText := registry.BoolValue(caps.ThinkingAsText)
			scalarReplay := !useReasoningDetails && len(encrypted) == 0 && !thinkingAsText
			reasoning := thinkingFromParts(content, scalarReplay)
			if reasoningOff {
				// A declared non-reasoning model accepts no reasoning fields —
				// prior-turn thinking (a fallback or model switch can carry it)
				// is dropped from replay entirely. ThinkingAsText below is the
				// deliberate exception: it rides ordinary text content.
				if !thinkingAsText {
					reasoning = ""
				}
			}
			if reasoning != "" && thinkingAsText {
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
			// Encrypted reasoning (OpenRouter Gemini/o-series) must round-trip
			// through reasoning_details regardless of the useReasoningDetails
			// flag. When present, text rides the same array (text first, then
			// encrypted) to mirror OpenRouter's unified reasoning_details shape.
			if reasoning != "" || len(encrypted) > 0 {
				if useReasoningDetails || len(encrypted) > 0 {
					// A signature-bearing reasoning.text item (OpenRouter's
					// Anthropic route) absorbs the accumulated text: the
					// provider's canonical non-stream shape is ONE item with
					// text+signature merged, not a synthetic text item beside
					// a text-less signature item.
					merged := false
					if reasoning != "" {
						for i, e := range encrypted {
							if spliced, isText := reasoningTextWith(e, reasoning); isText {
								encrypted[i], merged = spliced, true
								break
							}
						}
					}
					if reasoning != "" && !merged {
						// MiniMax format: {type: "reasoning.text", text: "...", format: "unknown", index: 0}
						synthetic, _ := json.Marshal(map[string]any{"type": "reasoning.text", "text": reasoning, "format": "unknown", "index": 0})
						encrypted = append([]json.RawMessage{synthetic}, encrypted...)
					}
					msg["reasoning_details"] = encrypted
				} else {
					msg[reasoningReplayField(content, caps)] = reasoning
				}
			} else if registry.BoolValue(caps.EmptyReasoningContent) && !useReasoningDetails && !reasoningOff {
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
					if registry.BoolValue(caps.ToolResultName) {
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
	if registry.BoolValue(caps.AssistantAfterToolResult) {
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
// caps.ReasoningField when the row names one (reasoning_details is excluded —
// that shape is an array, assembled separately, not a scalar replay field);
// else the original scalar field from the first thinking part with text;
// else reasoning_content.
// Signatures that aren't field names (e.g. Anthropic crypto blobs riding a
// cross-provider transcript) fall back to the default.
func reasoningReplayField(parts []llm.ContentPart, caps registry.Caps) string {
	if f := registry.StringValue(caps.ReasoningField); f != "" && f != "reasoning_details" {
		return f
	}
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			if p.Thinking.GoogleInteraction {
				continue
			}
			if p.Thinking.ScalarReasoningText != "" && llm.IsOpenAICompatReasoningField(p.Thinking.ScalarReasoningField) {
				return p.Thinking.ScalarReasoningField
			}
			if p.Thinking.Text == "" {
				continue
			}
			if llm.IsOpenAICompatReasoningField(p.Thinking.Signature) {
				return p.Thinking.Signature
			}
			break
		}
	}
	return "reasoning_content"
}

// reasoningTextWith merges text into a reasoning.text item without moving a key.
func reasoningTextWith(item []byte, text string) (json.RawMessage, bool) {
	d := jsontext.NewDecoder(bytes.NewReader(item), jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))
	_, _ = d.ReadToken() // the opening brace; item is JSON that already decoded
	var isText, has, empty bool
	var start, end int64
	for d.PeekKind() == jsontext.KindString {
		name, _ := d.ReadToken()
		key := name.String() // ReadValue invalidates the token buffer
		v, _ := d.ReadValue()
		switch key {
		case "type":
			var s string
			isText = json.Unmarshal(v, &s) == nil && s == "reasoning.text"
		case "text":
			has, empty, end = true, string(v) == "null" || string(v) == `""`, d.InputOffset()
			start = end - int64(len(v))
		}
	}
	if !isText || has && !empty {
		return item, isText
	}
	quoted, _ := jsontext.AppendQuote(nil, text)
	if has {
		return append(append(item[:start:start], quoted...), item[end:]...), true
	}
	at := int64(len(item)) - 1
	return append(append(append(item[:at:at], `,"text":`...), quoted...), item[at:]...), true
}

// encryptedDetailsFromParts decodes the opaque encrypted reasoning_details
// items carried on assistant thinking parts (serialized in EncryptedContent),
// concatenated in part order, ready to replay in the reasoning_details array.
func encryptedDetailsFromParts(parts []llm.ContentPart) []json.RawMessage {
	var out []json.RawMessage
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil &&
			!p.Thinking.GoogleInteraction &&
			llm.IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
			// The guard also keeps a foreign provider's opaque blob (OpenAI
			// Responses encrypted_content) off the wire on a cross-provider
			// transcript — it would not parse as our item array anyway.
			var items []json.RawMessage
			if err := json.Unmarshal([]byte(p.Thinking.EncryptedContent), &items); err == nil {
				out = append(out, items...)
			}
		}
	}
	return out
}

func thinkingFromParts(parts []llm.ContentPart, scalarReplay bool) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind != llm.ContentThinking || p.Thinking == nil {
			continue
		}
		if text := p.Thinking.ChatReplayText(scalarReplay); text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text)
		}
	}
	return b.String()
}

// hasImageOrDocumentContent reports whether the multimodal content form is
// needed: the plain-text builder silently drops every non-text part, so an
// image, or a document carrying inline bytes, must ride the array form. A
// URL-only document stays plain — chat has no URL file mechanism — and
// audio never reaches this gate: no production code sends one.
func hasImageOrDocumentContent(parts []llm.ContentPart) bool {
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentImage:
			return true
		case llm.ContentDocument:
			// Inline bytes only, tested without encoding the payload —
			// the builder pays that once when it emits the file part.
			if p.Document != nil && len(p.Document.Data) > 0 {
				return true
			}
		}
	}
	return false
}

// buildMultimodalParts converts content parts to Chat Completions content array format
// with {"type": "text", ...} and {"type": "image_url", ...} objects.
func buildMultimodalParts(parts []llm.ContentPart, caps registry.Caps) ([]map[string]any, error) {
	var out []map[string]any
	capDetail := registry.StringValue(caps.ImageDetail)
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
				data, normalizedType, err := chatImageBytes(p.Image.Data, p.Image.MediaType)
				if err != nil {
					return nil, err
				}
				imgURL = llm.CachedDataURI(normalizedType, data)
			}
			if imgURL == "" {
				continue
			}
			urlObj := map[string]any{"url": imgURL}
			if detail := llm.ChatImageDetail(p.Image.Detail, capDetail); detail != "" {
				urlObj["detail"] = detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": urlObj})
		case llm.ContentDocument:
			// A document rides the chat wire as a file part whose file_data
			// carries the bytes as a data URI, matching the Responses
			// protocol's input_file and Anthropic's document block. A
			// URL-only document stays with the plain-text form: chat file
			// parts take base64 data only and have no URL mechanism.
			fileData := llm.DocumentFileData(p.Document)
			if fileData == "" {
				continue
			}
			out = append(out, map[string]any{"type": "file", "file": map[string]any{"file_data": fileData}})
		}
	}
	return out, nil
}

// chatImageBytes returns bytes and a media type the Chat Completions image
// block accepts. A claimed type already in the set keeps its bytes; any other
// decodable raster is re-encoded as PNG (a BMP or TIFF attachment would
// otherwise be rejected and fail the whole turn, #4584); bytes that are not a
// decodable raster are refused loudly.
func chatImageBytes(data []byte, claimedMediaType string) ([]byte, string, error) {
	return llm.NormalizeImageForWire(
		data,
		claimedMediaType,
		llm.ImageBlockMediaType,
		fmt.Sprintf("chat completions image %q is not a decodable raster", claimedMediaType),
	)
}

// requestWithoutToolResultImages returns a request whose tool-result image
// fields have been removed from a private copy. Chat Completions has no
// portable image-bearing tool-message representation, so buildBody uses this
// whenever the row's MultimodalToolResults cap is off. The original request
// remains suitable for a Responses attempt and for transcript/API logging.
func requestWithoutToolResultImages(req llm.Request) llm.Request {
	needsCopy := false
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil &&
				(len(p.ToolResult.ImageData) > 0 || p.ToolResult.ImageMediaType != "") {
				needsCopy = true
				break
			}
		}
		if needsCopy {
			break
		}
	}
	if !needsCopy {
		return req
	}

	out := req
	out.Messages = make([]llm.Message, len(req.Messages))
	copy(out.Messages, req.Messages)
	for i, m := range req.Messages {
		out.Messages[i].Content = append([]llm.ContentPart(nil), m.Content...)
		for j, p := range m.Content {
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil ||
				(len(p.ToolResult.ImageData) == 0 && p.ToolResult.ImageMediaType == "") {
				continue
			}
			result := *p.ToolResult
			result.ImageData = nil
			result.ImageMediaType = ""
			out.Messages[i].Content[j].ToolResult = &result
		}
	}
	return out
}

func toolCallsFromParts(parts []llm.ContentPart) []map[string]any {
	var calls []map[string]any
	for _, p := range parts {
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
			call := map[string]any{
				"id":   p.ToolCall.ID,
				"type": "function",
				"function": map[string]any{
					"name":      p.ToolCall.Name,
					"arguments": openaichat.ToolArgumentsString(json.RawMessage(p.ToolCall.WireArguments())),
				},
			}
			if !p.ToolCall.GoogleInteraction {
				if sig := strings.TrimSpace(p.ToolCall.ThoughtSignature); sig != "" {
					call["extra_content"] = map[string]any{
						"google": map[string]any{"thought_signature": sig},
					}
				}
			}
			calls = append(calls, call)
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
		return nil, llm.NewUnsupportedToolChoiceError(registry.ProtocolOpenAIChat, tc.Mode)
	}
}

package chatcompletions

import (
	"encoding/json"
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
			asText := registry.BoolValue(caps.ThinkingAsText)
			scalarReplay := caps.ReplaysReasoningText() && !useReasoningDetails && !asText
			reasoning, details := reasoningFromParts(content, !reasoningOff, scalarReplay)
			if reasoningOff && !asText {
				// A declared non-reasoning model accepts no reasoning fields —
				// prior-turn thinking (a fallback or model switch can carry it)
				// is dropped from replay entirely. ThinkingAsText below is the
				// deliberate exception: it rides ordinary text content.
				reasoning = ""
			}
			if reasoning != "" && asText {
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
			// A unified array puts reasoning text before the opaque items, or
			// merges it into the signed item that carries it.
			if reasoning != "" && (useReasoningDetails || (len(details) != 0 && !scalarReplay)) {
				if !mergeSignedReasoningText(details, reasoning) {
					details = append([]map[string]any{{
						"type": "reasoning.text", "text": reasoning, "format": "unknown", "index": 0,
					}}, details...)
				}
				reasoning = ""
			}
			if len(details) != 0 {
				msg["reasoning_details"] = details
			}
			if reasoning != "" {
				msg[reasoningReplayField(content, caps)] = reasoning
			} else if len(details) == 0 && registry.BoolValue(caps.EmptyReasoningContent) && !useReasoningDetails && !reasoningOff {
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
// else the field the thinking originally arrived on (recorded in the part's
// Signature) when it names a known reasoning field; else reasoning_content.
// Signatures that aren't field names (e.g. Anthropic crypto blobs riding a
// cross-provider transcript) fall back to the default.
func reasoningReplayField(parts []llm.ContentPart, caps registry.Caps) string {
	if f := registry.StringValue(caps.ReasoningField); f != "" && f != "reasoning_details" {
		return f
	}
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
			if llm.IsOpenAICompatReasoningField(p.Thinking.Signature) {
				return p.Thinking.Signature
			}
			break
		}
	}
	return "reasoning_content"
}

// reasoningFromParts keeps each signed item's text with its own signature
// when independent plaintext goes to a scalar field.
func reasoningFromParts(parts []llm.ContentPart, keepDetails, scalarReplay bool) (string, []map[string]any) {
	var text strings.Builder
	var details []map[string]any
	for _, p := range parts {
		if p.Kind != llm.ContentThinking || p.Thinking == nil {
			continue
		}
		piece := p.Thinking.Text
		if keepDetails && llm.IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
			var items []map[string]any
			if err := json.Unmarshal([]byte(p.Thinking.EncryptedContent), &items); err == nil {
				details = append(details, items...)
				if scalarReplay && piece != "" && mergeSignedReasoningText(items, piece) {
					continue
				}
			}
		}
		if piece != "" {
			if text.Len() != 0 {
				text.WriteString("\n")
			}
			text.WriteString(piece)
		}
	}
	return text.String(), details
}

func mergeSignedReasoningText(details []map[string]any, text string) bool {
	for _, item := range details {
		if item["type"] == "reasoning.text" {
			if existing, _ := item["text"].(string); existing == "" {
				item["text"] = text
			}
			return true
		}
	}
	return false
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
					"arguments": openaichat.ToolArgumentsString(p.ToolCall.Arguments),
				},
			}
			if sig := strings.TrimSpace(p.ToolCall.ThoughtSignature); sig != "" {
				call["extra_content"] = map[string]any{
					"google": map[string]any{"thought_signature": sig},
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

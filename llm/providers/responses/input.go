package responses

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
)

func toResponsesResponseFormat(rf llm.ResponseFormat, structured bool) any {
	switch strings.ToLower(strings.TrimSpace(rf.Type)) {
	case "", "text":
		return nil
	case "json":
		return map[string]any{"type": "json"}
	case "json_schema":
		if !structured {
			return map[string]any{"type": "json_object"}
		}
		// Responses API requires a name for json_schema output and expects the
		// actual JSON Schema under "schema".
		return map[string]any{
			"type":   "json_schema",
			"name":   "output",
			"schema": rf.JSONSchema,
			"strict": rf.Strict,
		}
	default:
		return nil
	}
}

func toResponsesTools(tools []llm.ToolDefinition, strict bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		entry := map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		}
		if strict {
			toolStrict := true
			if t.Strict != nil {
				toolStrict = *t.Strict
			}
			if toolStrict {
				// OpenAI strict mode requires a fully-specified JSON Schema:
				// - object schemas must set additionalProperties=false
				// - required must include every key in properties (even for "optional" fields)
				// See API validation errors like:
				// "Invalid schema for function 'read_file': ... 'required' ... Missing 'limit'."
				params = openaichat.StrictifyJSONSchema(params)
				entry["parameters"] = params
			}
			entry["strict"] = toolStrict
		}
		out = append(out, entry)
	}
	return out
}

// toResponsesToolChoice converts a tool choice to the OpenAI Responses API wire
// shape. A forced function is expressed as {"type":"function","name":"X"} — the
// function name lives at the TOP LEVEL. This differs from Chat Completions, which
// nests it as {"type":"function","function":{"name":"X"}} (see
// toChatCompletionsToolChoice); sending the nested shape to /v1/responses is
// rejected with "missing required parameter: 'tool_choice.name'".
func toResponsesToolChoice(tc llm.ToolChoice) (any, error) {
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
		return map[string]any{"type": "function", "name": tc.Name}, nil
	default:
		// Backward-compatible: some callers may have used an unspecified mode to force
		// a particular tool. Prefer explicit mode="named".
		if strings.TrimSpace(tc.Name) != "" {
			return map[string]any{"type": "function", "name": tc.Name}, nil
		}
		return nil, llm.NewUnsupportedToolChoiceError("openai", tc.Mode)
	}
}

func toResponsesInput(msgs []llm.Message, imageCap string, replayReasoningText bool) (instructions string, items []any, _ error) {
	var instrParts []string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := m.Text(); t != "" {
				instrParts = append(instrParts, t)
			}
		}
	}
	instructions = strings.Join(instrParts, "\n\n")

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			continue
		case llm.RoleUser, llm.RoleAssistant:
			if m.Role == llm.RoleAssistant {
				var err error
				ordered := replayReasoningText || hasAssistantItemOrder(m.Content)
				items, err = appendAssistantInput(items, m.Content, ordered, replayReasoningText)
				if err != nil {
					return "", nil, err
				}
				if ordered {
					continue
				}
			} else {
				// User messages: no phase grouping needed.
				content := make([]any, 0, len(m.Content))
				for _, p := range m.Content {
					switch p.Kind {
					case llm.ContentText:
						if p.Text == "" {
							continue
						}
						content = append(content, map[string]any{
							"type": "input_text",
							"text": p.Text,
						})
					case llm.ContentImage:
						if p.Image == nil {
							continue
						}
						url := strings.TrimSpace(p.Image.URL)
						if len(p.Image.Data) > 0 {
							data, mt, err := normalizeImageInput(p.Image.Data, p.Image.MediaType)
							if err != nil {
								return "", nil, &llm.ConfigurationError{Message: err.Error()}
							}
							url = llm.CachedDataURI(mt, data)
						} else if llm.IsLocalPath(url) {
							path := llm.ExpandTilde(url)
							b, err := os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							claimed := strings.TrimSpace(p.Image.MediaType)
							if claimed == "" {
								claimed = llm.InferMimeTypeFromPath(path)
							}
							data, mt, err := normalizeImageInput(b, claimed)
							if err != nil {
								return "", nil, &llm.ConfigurationError{Message: err.Error()}
							}
							url = llm.CachedDataURI(mt, data)
						}
						if url != "" {
							img := map[string]any{
								"type":      "input_image",
								"image_url": url,
							}
							if detail := llm.ResponsesImageDetail(p.Image.Detail, imageCap); detail != "" {
								img["detail"] = detail
							}
							content = append(content, img)
						}
					case llm.ContentDocument:
						// Bytes ride file_data as an inline data URI; a source
						// URL uses the Responses API's separate file_url field.
						// file_data is inline content only, so a URL there is the
						// wrong wire shape (#5491). An input_file with neither
						// source (only a filename) makes the provider reject the
						// whole request, so append it only when it has one.
						entry := map[string]any{"type": "input_file"}
						hasSource := false
						if p.Document != nil {
							if fileData := llm.DocumentFileData(p.Document); fileData != "" {
								entry["file_data"] = fileData
								hasSource = true
							} else if p.Document.URL != "" {
								entry["file_url"] = p.Document.URL
								hasSource = true
							}
							if p.Document.FileName != "" {
								entry["filename"] = p.Document.FileName
							}
						}
						if hasSource {
							content = append(content, entry)
						}
					case llm.ContentAudio:
						return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
					default:
						// ignore (tool calls are top-level items)
					}
				}
				if len(content) > 0 {
					items = append(items, map[string]any{
						"type":    "message",
						"role":    "user",
						"content": content,
					})
				}
			}
			// These wire shapes group message text first, then reasoning and
			// function calls, then web searches.
			var searches []any
			for _, p := range m.Content {
				item := standaloneInputItem(p, false)
				if item == nil {
					continue
				}
				if p.Kind == llm.ContentWebSearch {
					searches = append(searches, item)
				} else {
					items = append(items, item)
				}
			}
			items = append(items, searches...)
		case llm.RoleTool:
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				outStr := ""
				if p.ToolResult.IsError {
					// OpenAI Responses API rejects unknown params like "is_error" on
					// function_call_output items. Preserve error semantics by wrapping
					// the original content in a JSON string.
					wrapped := map[string]any{
						"is_error": true,
						"content":  p.ToolResult.Content,
					}
					if b, err := json.Marshal(wrapped); err == nil {
						outStr = string(b)
					} else {
						outStr = fmt.Sprintf(`{"is_error":true,"content":%q}`, fmt.Sprint(p.ToolResult.Content))
					}
				} else {
					switch v := p.ToolResult.Content.(type) {
					case string:
						outStr = v
					default:
						b, _ := json.Marshal(v)
						outStr = string(b)
					}
				}
				item := map[string]any{
					"type":    "function_call_output",
					"call_id": p.ToolResult.ToolCallID,
					"output":  outStr,
				}

				if toolResultHasProviderImage(p.ToolResult) {
					data, mt, err := normalizeImageInput(p.ToolResult.ImageData, p.ToolResult.ImageMediaType)
					if err != nil {
						return "", nil, &llm.ConfigurationError{Message: err.Error()}
					}
					img := map[string]any{
						"type":      "input_image",
						"image_url": llm.CachedDataURI(mt, data),
					}
					if detail := llm.ResponsesImageDetail("", imageCap); detail != "" {
						img["detail"] = detail
					}
					item["output"] = []any{
						map[string]any{"type": "input_text", "text": outStr},
						img,
					}
				}
				items = append(items, item)
			}
		default:
			// ignore unknown roles
		}
	}
	return instructions, items, nil
}

// Native message boundaries locate text among the other top-level items.
// A textless turn already consists of top-level items. Only unmarked text
// needs the older projection that groups by phase before appending calls.
func hasAssistantItemOrder(parts []llm.ContentPart) bool {
	var hasText bool
	for _, part := range parts {
		if part.Kind == llm.ContentText {
			if part.ResponsesMessageStart {
				return true
			}
			hasText = true
		}
	}
	return !hasText
}

func appendAssistantInput(items []any, parts []llm.ContentPart, ordered, replayReasoningText bool) ([]any, error) {
	var group map[string]any
	var phase string
	var nativeGroup bool
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			if p.ResponsesMessageStart {
				group = nil
				nativeGroup = true
			} else if group == nil || phase != p.Phase {
				nativeGroup = false
			}
			if !nativeGroup && !p.ResponsesRefusal && strings.TrimSpace(p.Text) == "" && p.Phase == "" {
				continue
			}
			if group == nil || phase != p.Phase {
				phase = p.Phase
				group = map[string]any{"type": "message", "role": "assistant"}
				if phase != "" {
					group["phase"] = phase
				}
				items = append(items, group)
			}
			content, _ := group["content"].([]any)
			kind, field := "output_text", "text"
			if p.ResponsesRefusal {
				kind, field = "refusal", "refusal"
			}
			group["content"] = append(content, map[string]any{"type": kind, field: p.Text})
		case llm.ContentAudio, llm.ContentDocument:
			return nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
		default:
			if ordered {
				if item := standaloneInputItem(p, replayReasoningText); item != nil {
					items = append(items, item)
					group = nil // A top-level item separates adjacent text groups.
				}
			}
		}
	}
	return items, nil
}

func standaloneInputItem(p llm.ContentPart, replayReasoningText bool) map[string]any {
	switch p.Kind {
	case llm.ContentThinking:
		t := p.Thinking
		if t == nil || llm.IsOpenAICompatEncryptedReasoning(t.EncryptedContent) {
			return nil
		}
		var item map[string]any
		switch {
		case t.EncryptedContent != "":
			item = map[string]any{"type": "reasoning", "encrypted_content": t.EncryptedContent}
		case replayReasoningText:
			var content []any
			for text := range t.ResponsesReasoningTextParts() {
				content = append(content, map[string]any{"type": "reasoning_text", "text": text})
			}
			if len(content) == 0 {
				return nil
			}
			item = map[string]any{"type": "reasoning", "content": content}
		default:
			return nil
		}
		item["summary"] = reasoningSummaryInput(t.Summary)
		if t.ID != "" {
			item["id"] = t.ID
		}
		return item
	case llm.ContentToolCall:
		if t := p.ToolCall; t != nil {
			return map[string]any{
				"type":      "function_call",
				"call_id":   t.ID,
				"name":      t.Name,
				"arguments": openaichat.ToolArgumentsString(t.Arguments),
			}
		}
	case llm.ContentWebSearch:
		if p.WebSearch != nil {
			var item map[string]any
			if err := json.Unmarshal(p.WebSearch.Raw, &item); err == nil {
				return item
			}
		}
	}
	return nil
}

func reasoningSummaryInput(summary []string) []any {
	out := make([]any, 0, len(summary))
	for _, text := range summary {
		out = append(out, map[string]any{
			"type": "summary_text",
			"text": text,
		})
	}
	return out
}

func parseReasoningSummary(items []any, finals map[int64]responsesSummaryFinal) []string {
	if items == nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for i, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		text, ok := item["text"].(string)
		if item["text"] == nil && item["type"] == "summary_text" {
			if final, found := finals[int64(i)]; found {
				text, ok = final.text, true
			}
		}
		if !ok {
			continue
		}
		out = append(out, text)
	}
	return out
}

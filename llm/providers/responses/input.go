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

func toResponsesInput(msgs []llm.Message, imageDetail string) (instructions string, items []any, _ error) {
	var instrParts []string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
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
				// Group text parts by phase, emitting one message item per group.
				type textGroup struct {
					phase   string
					content []any
				}
				var groups []textGroup
				for _, p := range m.Content {
					switch p.Kind {
					case llm.ContentText:
						if strings.TrimSpace(p.Text) == "" && p.Phase == "" {
							continue
						}
						entry := map[string]any{"type": "output_text", "text": p.Text}
						if len(groups) > 0 && groups[len(groups)-1].phase == p.Phase {
							groups[len(groups)-1].content = append(groups[len(groups)-1].content, entry)
						} else {
							groups = append(groups, textGroup{phase: p.Phase, content: []any{entry}})
						}
					case llm.ContentAudio, llm.ContentDocument:
						return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
					default:
						// ignore (tool calls, images, web search handled separately)
					}
				}
				for _, g := range groups {
					item := map[string]any{
						"type":    "message",
						"role":    "assistant",
						"content": g.content,
					}
					if g.phase != "" {
						item["phase"] = g.phase
					}
					items = append(items, item)
				}
			} else {
				// User messages: no phase grouping needed.
				content := make([]any, 0, len(m.Content))
				for _, p := range m.Content {
					switch p.Kind {
					case llm.ContentText:
						if strings.TrimSpace(p.Text) == "" {
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
							url = llm.DataURI(mt, data)
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
							url = llm.DataURI(mt, data)
						}
						if url != "" {
							img := map[string]any{
								"type":      "input_image",
								"image_url": url,
							}
							// imageDetail == "omit" means the row rejects/mishandles
							// detail; omit it there even when explicitly set.
							if imageDetail != "omit" {
								if p.Image.Detail != "" {
									img["detail"] = p.Image.Detail
								} else {
									img["detail"] = imageDetail
								}
							}
							content = append(content, img)
						}
					case llm.ContentDocument:
						if p.Document == nil {
							continue
						}
						var fileData string
						if len(p.Document.Data) > 0 {
							mt := strings.TrimSpace(p.Document.MediaType)
							if mt == "" {
								mt = "application/pdf"
							}
							fileData = llm.DataURI(mt, p.Document.Data)
						} else if p.Document.URL != "" {
							fileData = p.Document.URL
						}
						if fileData != "" {
							entry := map[string]any{
								"type":      "input_file",
								"file_data": fileData,
							}
							if p.Document.FileName != "" {
								entry["filename"] = p.Document.FileName
							}
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
			for _, p := range m.Content {
				if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.EncryptedContent != "" &&
					!llm.IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
					// The guard skips OpenRouter-style encrypted reasoning_details
					// riding a cross-provider transcript — those are not OpenAI
					// Responses encrypted_content blobs and the API rejects them.
					item := map[string]any{
						"type":              "reasoning",
						"encrypted_content": p.Thinking.EncryptedContent,
						"summary":           reasoningSummaryInput(p.Thinking.Summary),
					}
					if strings.TrimSpace(p.Thinking.ID) != "" {
						item["id"] = strings.TrimSpace(p.Thinking.ID)
					}
					items = append(items, item)
				}
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   p.ToolCall.ID,
						"name":      p.ToolCall.Name,
						"arguments": openaichat.ToolArgumentsString(p.ToolCall.Arguments),
					})
				}
			}
			for _, p := range m.Content {
				if p.Kind == llm.ContentWebSearch && p.WebSearch != nil && len(p.WebSearch.Raw) > 0 {
					var rawItem map[string]any
					if err := json.Unmarshal(p.WebSearch.Raw, &rawItem); err == nil {
						items = append(items, rawItem)
					}
				}
			}
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
						"image_url": llm.DataURI(mt, data),
					}
					// imageDetail == "omit" means the row rejects/mishandles
					// detail; omit it there.
					if imageDetail != "omit" {
						img["detail"] = imageDetail
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

func reasoningSummaryInput(summary []string) []any {
	out := make([]any, 0, len(summary))
	for _, text := range summary {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, map[string]any{
			"type": "summary_text",
			"text": text,
		})
	}
	return out
}

func parseReasoningSummary(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

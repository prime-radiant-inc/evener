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

// toolSchema is the schema one tool is sent with, whether that tool rides the
// request under strict enforcement, and whether the builder hands back the
// caller's own map untouched (so the count the request already holds describes
// the wire). The rules are stated below in the order the builder applies them.
func toolSchema(t llm.ToolDefinition, strict, responsesLite bool) (params map[string]any, toolStrict, untouched bool) {
	toolStrict = strict
	if toolStrict && t.Strict != nil {
		toolStrict = *t.Strict
	}
	params = t.Parameters
	// own is true while params is still the caller's own map. A synthesized
	// empty schema is not, so only a definition that survives the fallback below
	// is reported untouched on the strength of identity alone. A later step that
	// returns a copy is a change, because the copy is what ships.
	own := params != nil && !t.DeclaresNoParameters()
	if t.DeclaresNoParameters() || params == nil {
		// A tool that declares no parameters takes the empty object. On the
		// strict path the root has to be explicitly closed, because strict
		// mode accepts that and only that: an open root is not certifiable,
		// and sending it non-strict would accept any keys the tool rejects.
		// With strict mode off the advertised shape is unchanged, and for a
		// prepared definition that shape is the open empty object it already
		// carries.
		params = map[string]any{"type": "object", "properties": map[string]any{}}
		if toolStrict {
			params["additionalProperties"] = false
		}
	}
	if toolStrict {
		// OpenAI strict mode requires a fully-specified JSON Schema:
		// - object schemas must set additionalProperties=false
		// - required must include every key in properties (even for "optional" fields)
		// See API validation errors like:
		// "Invalid schema for function 'read_file': ... 'required' ... Missing 'limit'."
		//
		// That force-requiring is lossless only when the schema already
		// requires every property. A tool with a documented-optional argument
		// has no legal strict form: projecting it would make that argument
		// mandatory on every call. Such a tool keeps its advertised schema and
		// is sent non-strict.
		if openaichat.StrictExpressible(params) {
			projected := openaichat.StrictifyJSONSchema(params)
			// Codex Responses Lite requires an explicit type on every node in
			// additional_tools. A strict-certified union of objects, arrays, or
			// mixed types cannot carry an enclosing type in strict mode without
			// changing its contract, so on Lite routes it falls back to
			// non-strict emission with its enclosing union type stated.
			if !responsesLite || openaichat.HasExplicitTypes(projected) {
				return projected, true, false
			}
		}
	}
	if responsesLite {
		supplied, rewritten := openaichat.SupplyExplicitTypes(params)
		return supplied, false, own && !rewritten
	}
	// A refused tool keeps the schema it advertised, because the projection
	// would change which arguments it accepts. The provider still requires an
	// explicit type on every node, so a closed value set that implies one gets
	// it stated here: the accepted values are identical either way, and an
	// argument declared {"const": "fixed"} would otherwise be rejected along
	// with the whole request. This runs on the non-strict path too because the
	// requirement is the schema dialect's, not strict mode's — additional_tools
	// on this route carries strict:false and is still rejected for a typeless
	// node.
	supplied, rewritten := openaichat.SupplyDerivedTypes(params)
	return supplied, false, own && !rewritten
}

// toResponsesTools writes the tools array. capsStrict is the row's strict_tools
// capability and stated reports whether the row states one at all.
//
// The per-tool flag is written whenever the row states the capability, not only
// when the capability is on. A native endpoint normalizes an omitted flag into
// strict enforcement, so a route configured strict_tools = false that omitted
// it would run the tool strictly anyway — and an argument the gate refused to
// force on would be forced on by the endpoint. A row that states nothing gets
// no flag: that is what a compatible endpoint which does not implement the
// field receives today, and the capability's absence is not a decision to
// announce.
func toResponsesTools(tools []llm.ToolDefinition, capsStrict, stated, responsesLite bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		// The per-tool opt-out is read before the fallback is synthesized: a
		// tool that opts out is advertised with the open empty object, which is
		// what every non-strict route already sends and what the local
		// validator allows. Only a tool that will actually be sent in strict
		// mode gets the closed empty object, because strict mode accepts that
		// and only that.
		params, toolStrict, _ := toolSchema(t, capsStrict, responsesLite)
		entry := map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		}
		if stated {
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

func toResponsesInput(msgs []llm.Message, imageCap string, reasoningDisabled ...bool) (instructions string, items []any, _ error) {
	off := len(reasoningDisabled) > 0 && reasoningDisabled[0]
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
			switch m.Role {
			case llm.RoleAssistant:
				// Group adjacent text by phase across parts omitted from replay.
				var phase string
				var content []any
				flushText := func() {
					if len(content) == 0 {
						return
					}
					item := map[string]any{"type": "message", "role": "assistant", "content": content}
					if phase != "" {
						item["phase"] = phase
					}
					items = append(items, item)
					content = nil
				}
				for _, p := range m.Content {
					switch p.Kind {
					case llm.ContentText:
						if strings.TrimSpace(p.Text) == "" && p.Phase == "" {
							continue
						}
						if phase != p.Phase {
							flushText()
							phase = p.Phase
						}
						content = append(content, map[string]any{"type": "output_text", "text": p.Text})
					case llm.ContentAudio, llm.ContentDocument:
						return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
					default:
						if item, ok := replayedOutputItem(p, off); ok {
							flushText()
							items = append(items, item)
						}
					}
				}
				flushText()
				continue
			case llm.RoleUser:
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
			for _, p := range m.Content {
				if p.Kind == llm.ContentWebSearch {
					continue
				}
				if item, ok := replayedOutputItem(p, off); ok {
					items = append(items, item)
				}
			}
			for _, p := range m.Content {
				if item, ok := replayedWebSearchItem(p); ok {
					items = append(items, item)
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

func replayedOutputItem(p llm.ContentPart, reasoningDisabled bool) (map[string]any, bool) {
	switch {
	case p.Kind == llm.ContentThinking && p.Thinking != nil && !reasoningDisabled:
		return reasoningItem(p.Thinking)
	case p.Kind == llm.ContentToolCall && p.ToolCall != nil:
		item := map[string]any{
			"type":      "function_call",
			"call_id":   p.ToolCall.ID,
			"name":      p.ToolCall.Name,
			"arguments": openaichat.ToolArgumentsString(json.RawMessage(p.ToolCall.WireArguments())),
		}
		if id := strings.TrimSpace(p.ToolCall.ItemID); id != "" {
			item["id"] = id
		}
		return item, true
	default:
		return replayedWebSearchItem(p)
	}
}

func reasoningItem(t *llm.ThinkingData) (map[string]any, bool) {
	encrypted := !t.ResponsesIncomplete && t.EncryptedContent != "" && !llm.IsOpenAICompatEncryptedReasoning(t.EncryptedContent)
	if !encrypted && !t.ReplayableResponsesPlaintext() {
		return nil, false
	}
	var summary []any
	if len(t.ResponsesSummaryParts) > 0 {
		summary = reasoningTypedInput(t.ResponsesSummaryParts) // display deduplication empties Summary, not this
	} else if encrypted {
		summary = reasoningSummaryInput(t.Summary)
	} else {
		summary = []any{}
	}
	item := map[string]any{"type": "reasoning", "summary": summary}
	if encrypted {
		item["encrypted_content"] = t.EncryptedContent
	} else if len(t.ResponsesContentParts) > 0 {
		item["content"] = reasoningTypedInput(t.ResponsesContentParts)
	}
	if id := strings.TrimSpace(t.ID); id != "" {
		item["id"] = id
	}
	return item, true
}

func replayedWebSearchItem(p llm.ContentPart) (map[string]any, bool) {
	if p.Kind != llm.ContentWebSearch || p.WebSearch == nil || len(p.WebSearch.Raw) == 0 {
		return nil, false
	}
	var item map[string]any
	err := json.Unmarshal(p.WebSearch.Raw, &item)
	return item, err == nil && item != nil
}

func reasoningTypedInput(parts []llm.ResponsesReasoningPart) []any {
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		out = append(out, map[string]any{"type": part.Type, "text": part.Text})
	}
	return out
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

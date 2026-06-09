package google

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
)

func (a *Adapter) buildRequestBody(req llm.Request, system string, contents []map[string]any) (map[string]any, error) {
	genCfg := map[string]any{}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genCfg["topP"] = *req.TopP
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		genCfg["stopSequences"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		switch strings.ToLower(strings.TrimSpace(req.ResponseFormat.Type)) {
		case "json":
			genCfg["responseMimeType"] = "application/json"
		case "json_schema":
			genCfg["responseMimeType"] = "application/json"
			if req.ResponseFormat.JSONSchema != nil {
				genCfg["responseSchema"] = sanitizeGeminiSchema(req.ResponseFormat.JSONSchema)
			}
		}
	}
	if req.ReasoningEffort != nil {
		budget := llm.ReasoningBudget(*req.ReasoningEffort)
		if budget > 0 {
			genCfg["thinkingConfig"] = map[string]any{
				"thinkingBudget": budget,
			}
		}
	}

	body := map[string]any{
		"contents":         contents,
		"generationConfig": genCfg,
	}
	if strings.TrimSpace(system) != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": system}},
		}
	}
	if len(req.Tools) > 0 || req.WebSearch {
		var toolEntries []map[string]any
		if len(req.Tools) > 0 {
			toolEntries = append(toolEntries, map[string]any{
				"functionDeclarations": toGeminiFunctionDecls(req.Tools),
			})
		}
		// Gemini does not support google_search combined with functionDeclarations.
		if req.WebSearch && len(req.Tools) == 0 {
			toolEntries = append(toolEntries, map[string]any{
				"google_search": map[string]any{},
			})
		}
		body["tools"] = toolEntries
	}
	if req.ToolChoice != nil {
		mode := strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode))
		cfg := map[string]any{}
		switch mode {
		case "", "auto":
			cfg["mode"] = "AUTO"
		case "none":
			cfg["mode"] = "NONE"
		case "required":
			cfg["mode"] = "ANY"
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			cfg["mode"] = "ANY"
			cfg["allowedFunctionNames"] = []string{req.ToolChoice.Name}
		default:
			return nil, llm.NewUnsupportedToolChoiceError("google", req.ToolChoice.Mode)
		}
		body["toolConfig"] = map[string]any{"functionCallingConfig": cfg}
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["google"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
		if ov, ok := req.ProviderOptions["gemini"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}
	return body, nil
}

func toGeminiFunctionDecls(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			// Gemini's Schema is a restricted subset; strip JSON-schema-only fields
			// (e.g., additionalProperties) so requests don't fail validation.
			"parameters": sanitizeGeminiSchema(params),
		})
	}
	return out
}

func sanitizeGeminiSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			// The Gemini Schema proto does not accept JSON Schema's additionalProperties field.
			// Omitting it preserves compatibility while keeping the rest of the schema useful.
			if k == "additionalProperties" {
				continue
			}
			if k == "type" {
				if typ, nullable, ok := geminiNullableType(vv); ok {
					out[k] = typ
					out["nullable"] = nullable
					continue
				}
			}
			out[k] = sanitizeGeminiSchema(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = sanitizeGeminiSchema(x[i])
		}
		return out
	default:
		return v
	}
}

func geminiNullableType(v any) (string, bool, bool) {
	var values []string
	switch x := v.(type) {
	case []any:
		values = make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return "", false, false
			}
			values = append(values, s)
		}
	case []string:
		values = x
	default:
		return "", false, false
	}
	if len(values) != 2 {
		return "", false, false
	}
	var typ string
	nullable := false
	for _, v := range values {
		switch v {
		case "null":
			nullable = true
		case "":
			return "", false, false
		default:
			if typ != "" {
				return "", false, false
			}
			typ = v
		}
	}
	if typ == "" || !nullable {
		return "", false, false
	}
	return typ, true, true
}

func geminiImagePart(p llm.ContentPart) (map[string]any, error) {
	u := strings.TrimSpace(p.Image.URL)
	mt := strings.TrimSpace(p.Image.MediaType)
	if len(p.Image.Data) > 0 || llm.IsLocalPath(u) {
		var b []byte
		var err error
		if len(p.Image.Data) > 0 {
			b = p.Image.Data
		} else {
			path := llm.ExpandTilde(u)
			b, err = os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if mt == "" {
				mt = llm.InferMimeTypeFromPath(path)
			}
		}
		if mt == "" {
			mt = "image/png"
		}
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     base64.StdEncoding.EncodeToString(b),
			},
		}, nil
	}
	if u != "" {
		if mt == "" {
			mt = "image/png"
		}
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": mt,
				"fileUri":  u,
			},
		}, nil
	}
	return nil, nil
}

func toGeminiContents(msgs []llm.Message) (system string, contents []map[string]any, _ error) {
	var sysParts []string
	appendContent := func(role string, parts []map[string]any) {
		if len(parts) == 0 {
			return
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": parts,
		})
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
				sysParts = append(sysParts, t)
			}
		case llm.RoleUser:
			var parts []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						parts = append(parts, map[string]any{"text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					part, err := geminiImagePart(p)
					if err != nil {
						return "", nil, err
					}
					if part != nil {
						parts = append(parts, part)
					}
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for google: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendContent("user", parts)
		case llm.RoleAssistant:
			var parts []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						parts = append(parts, map[string]any{"text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					part, err := geminiImagePart(p)
					if err != nil {
						return "", nil, err
					}
					if part != nil {
						parts = append(parts, part)
					}
				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					var args any
					if len(p.ToolCall.Arguments) > 0 {
						_ = json.Unmarshal(p.ToolCall.Arguments, &args)
					}
					part := map[string]any{
						"functionCall": map[string]any{
							"name": p.ToolCall.Name,
							"args": args,
						},
					}
					if sig := strings.TrimSpace(p.ToolCall.ThoughtSignature); sig != "" {
						// Gemini requires replaying the thought signature that accompanied prior tool calls.
						part["thoughtSignature"] = sig
					}
					parts = append(parts, part)
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for google: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendContent("model", parts)
		case llm.RoleTool:
			var parts []map[string]any
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				name := strings.TrimSpace(p.ToolResult.Name)
				if name == "" {
					name = toolNameFromCallID(msgs, p.ToolResult.ToolCallID)
				}
				if name == "" {
					name = p.ToolResult.ToolCallID
				}
				// Gemini expects a functionResponse part.
				var respObj map[string]any
				switch v := p.ToolResult.Content.(type) {
				case map[string]any:
					respObj = v
				case string:
					respObj = map[string]any{"result": v}
				default:
					b, _ := json.Marshal(v)
					respObj = map[string]any{"result": string(b)}
				}
				if p.ToolResult.IsError {
					respObj["error"] = true
				}
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name":     name,
						"response": respObj,
					},
				})
				if len(p.ToolResult.ImageData) > 0 {
					mt := p.ToolResult.ImageMediaType
					if mt == "" {
						mt = "image/png"
					}
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": mt,
							"data":     base64.StdEncoding.EncodeToString(p.ToolResult.ImageData),
						},
					})
				}
			}
			appendContent("user", parts)
		default:
			// ignore
		}
	}
	return strings.Join(sysParts, "\n\n"), contents, nil
}

func toolNameFromCallID(msgs []llm.Message, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && strings.TrimSpace(p.ToolCall.ID) == callID {
				return strings.TrimSpace(p.ToolCall.Name)
			}
		}
	}
	return ""
}

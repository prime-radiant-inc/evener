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
	"primeradiant.com/evener/llm/providers/internal/requestutil"
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

// normalizeThinkingToolChoice applies the tool-choice restriction shared by
// completion and token-count requests whenever thinking is active.
func normalizeThinkingToolChoice(body map[string]any) {
	if finalThinkingActive(body) {
		if tc, ok := body["tool_choice"].(map[string]any); ok {
			if t, _ := tc["type"].(string); t == "any" || t == "tool" {
				body["tool_choice"] = map[string]any{"type": "auto"}
			}
		}
	}
}

// reconcileThinkingContract enforces the completion-only Anthropic request
// contracts that depend on the FINAL overlaid body state.
func reconcileThinkingContract(body map[string]any, req llm.Request, res registry.Resolved) error {
	normalizeThinkingToolChoice(body)
	thinkingBudget := finalThinkingBudget(body)
	requestutil.ReconcileOutputField(body, "max_tokens", req.MaxTokens, res.Caps.MaxOutputTokens)
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
	if typ, _ := thinking["type"].(string); !strings.EqualFold(strings.TrimSpace(typ), "enabled") {
		return 0
	}
	return intFromAny(thinking["budget_tokens"])
}

func anthropicThinkingBudgetError(req llm.Request, res registry.Resolved, requiredOutput, maximum int) error {
	provider, model := llm.ResolveContextBudgetIdentity(req, res)
	return &llm.ContextBudgetError{
		Provider:     provider,
		Model:        model,
		Limit:        "max_output_tokens",
		InputTokens:  0,
		OutputTokens: requiredOutput,
		Maximum:      maximum,
	}
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
		normalized, normalizedMediaType, normErr := anthropicImageBytes(b, mt)
		if normErr != nil {
			return nil, normErr
		}
		return map[string]any{
			"type":   "image",
			"source": anthropicBase64Source(normalizedMediaType, normalized),
		}, nil
	}
	if u != "" {
		return map[string]any{
			"type":   "image",
			"source": anthropicURLSource(u),
		}, nil
	}
	return nil, nil
}

// anthropicImageBytes returns bytes and a media type Anthropic's image block
// accepts. A claimed media type already in that set is kept and the bytes pass
// through unchanged: Anthropic validates its own bytes, and the local decoder
// cannot read every raster the wire accepts (an animated WebP, say). A type
// outside the set is decoded and re-encoded as PNG (a BMP or TIFF result would
// otherwise be rejected and fail the whole turn, #4464); bytes that are not a
// decodable raster are refused loudly through anthropicImageRefusalMessage.
func anthropicImageBytes(data []byte, claimedMediaType string) ([]byte, string, error) {
	if mt := llm.ImageBlockMediaType(claimedMediaType); mt != "" {
		return data, mt, nil
	}
	normalized, normalizedType, err := llm.NormalizeImageBlock(data)
	if err != nil {
		return nil, "", &llm.ConfigurationError{
			Message: anthropicImageRefusalMessage(claimedMediaType),
			Cause:   err,
		}
	}
	return normalized, normalizedType, nil
}

// anthropicImageRefusalMessage renders the loud configuration error the builder
// returns when image bytes cannot be normalized into a media type Anthropic's
// image block accepts. The claimed type and the accepted set are the whole
// point: the provider would otherwise reject the request with an opaque 400
// that fails the turn. The decoder's own error rides ConfigurationError.Cause
// rather than this prose, so the message stays the builder's to own.
func anthropicImageRefusalMessage(claimedMediaType string) string {
	claimed := strings.TrimSpace(claimedMediaType)
	if claimed == "" {
		claimed = "unknown"
	}
	return fmt.Sprintf("cannot send image to anthropic: cannot decode image with media type %q; Anthropic accepts image/png, image/jpeg, image/gif, and image/webp", claimed)
}

// anthropicBase64Source builds the inline source an image or document content
// block shares.
func anthropicBase64Source(mediaType string, data []byte) map[string]any {
	return map[string]any{
		"type":       "base64",
		"media_type": mediaType,
		"data":       base64.StdEncoding.EncodeToString(data),
	}
}

// anthropicURLSource builds the url source an image or document content block
// shares.
func anthropicURLSource(u string) map[string]any {
	return map[string]any{"type": "url", "url": u}
}

// anthropicDocumentBlock maps a document content part to Anthropic's document
// content block: a base64 source for inline bytes (the application/pdf default
// the tool layer and the Responses builder apply) or a url source, mirroring
// the Responses builder's input_file path. A part with neither returns nil, and
// the caller drops it. Anthropic's Messages API accepts base64 application/pdf
// documents in user messages with no beta header (#4209).
func anthropicDocumentBlock(p llm.ContentPart) map[string]any {
	if p.Document == nil {
		return nil
	}
	doc := p.Document
	if len(doc.Data) > 0 {
		mt := strings.TrimSpace(doc.MediaType)
		if mt == "" {
			mt = "application/pdf"
		}
		return map[string]any{"type": "document", "source": anthropicBase64Source(mt, doc.Data)}
	}
	if u := strings.TrimSpace(doc.URL); u != "" {
		return map[string]any{"type": "document", "source": anthropicURLSource(u)}
	}
	return nil
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
				case llm.ContentDocument:
					if block := anthropicDocumentBlock(p); block != nil {
						blocks = append(blocks, block)
					}
				case llm.ContentAudio:
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
				// Tool-result bytes ride inline as an image block for image
				// media only, normalized to a media type Anthropic's image
				// block accepts (a BMP or TIFF result would otherwise 400 the
				// whole turn, #4464, as a document media type does, #4209).
				// The design keeps documents on the side-channel
				// (llm.DeliversToolResultMedia is image-only), so a non-image
				// result contributes its text alone.
				resultContent := any(outStr)
				if len(p.ToolResult.ImageData) > 0 {
					mediaType := strings.TrimSpace(p.ToolResult.ImageMediaType)
					if mediaType == "" {
						mediaType = "image/png"
					}
					if strings.HasPrefix(strings.ToLower(mediaType), "image/") {
						data, normalizedType, normErr := anthropicImageBytes(p.ToolResult.ImageData, mediaType)
						if normErr != nil {
							return "", nil, normErr
						}
						resultContent = []map[string]any{
							{"type": "text", "text": outStr},
							{"type": "image", "source": anthropicBase64Source(normalizedType, data)},
						}
					}
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

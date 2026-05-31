package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type Adapter struct {
	name           string
	APIKey         string
	BaseURL        string
	Client         *http.Client
	DefaultHeaders map[string]string
}

// AnthropicInstanceParams holds the configuration for a single Anthropic adapter instance.
type AnthropicInstanceParams struct {
	Name    string
	APIKey  string
	BaseURL string
}

// NewForInstance constructs an Adapter from explicit parameters.
// Empty BaseURL falls back to the default Anthropic API endpoint.
func NewForInstance(params AnthropicInstanceParams) (*Adapter, error) {
	if strings.TrimSpace(params.APIKey) == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &Adapter{
		name:   params.Name,
		APIKey: params.APIKey,
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		BaseURL: strings.TrimRight(base, "/"),
		Client:  &http.Client{Timeout: 0},
	}, nil
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
			return nil, false, nil
		}
		a, err := NewFromEnv()
		if err != nil {
			return nil, true, err
		}
		return a, true, nil
	})
	llm.RegisterInstanceAdapterFactory("anthropic", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(AnthropicInstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		})
	})
}

func NewFromEnv() (*Adapter, error) {
	return NewForInstance(AnthropicInstanceParams{
		Name:    "anthropic",
		APIKey:  strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")),
		BaseURL: strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")),
	})
}

func (a *Adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "anthropic"
}

func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	var models []llm.ModelInfo
	var afterID string
	for {
		u := a.BaseURL + "/v1/models?limit=1000"
		if afterID != "" {
			u += "&after_id=" + afterID
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range a.DefaultHeaders {
			httpReq.Header.Set(k, v)
		}
		httpReq.Header.Set("x-api-key", a.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")

		resp, err := a.Client.Do(httpReq)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
		}

		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		for _, m := range page.Data {
			displayName := m.DisplayName
			if displayName == "" {
				displayName = m.ID
			}
			models = append(models, llm.ModelInfo{
				ID:          m.ID,
				Provider:    "anthropic",
				DisplayName: displayName,
			})
		}

		if !page.HasMore || page.LastID == "" {
			break
		}
		afterID = page.LastID
	}

	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	// Generate synthetic [1m] variants for models that support 1M context.
	// Eligible: claude-opus-4- and claude-sonnet-4- prefixes (not haiku).
	// Inherit pricing and other metadata from the base model so the model
	// picker shows complete info for the variants too.
	eligible1M := []string{"claude-opus-4-", "claude-sonnet-4-"}
	var extras []llm.ModelInfo
	for _, m := range models {
		for _, prefix := range eligible1M {
			if strings.HasPrefix(m.ID, prefix) {
				variant := m
				variant.ID = m.ID + "[1m]"
				variant.DisplayName = m.DisplayName + " (1M context)"
				variant.ContextWindow = 1_000_000
				extras = append(extras, variant)
				break
			}
		}
	}
	models = append(models, extras...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	return models, nil
}

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

func (a *Adapter) setAnthropicHeaders(httpReq *http.Request, providerOptions map[string]any) {
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if beta := betaHeaderFromProviderOptions(providerOptions); strings.TrimSpace(beta) != "" {
		httpReq.Header.Set("anthropic-beta", beta)
	}
}

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := a.buildRequestBody(req)
	if err != nil {
		return llm.Response{}, err
	}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	a.setAnthropicHeaders(httpReq, req.ProviderOptions)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.Response{}, llm.WrapContextError("anthropic", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	_ = json.Unmarshal(rawBytes, &raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("messages.create failed: %s", strings.TrimSpace(string(rawBytes)))
		return llm.Response{}, llm.ErrorFromHTTPStatus("anthropic", resp.StatusCode, msg, raw, ra)
	}

	r := fromAnthropicResponse(raw, req.Model)
	llm.StampEndpointURL(&r, a.BaseURL+"/v1/messages")
	r.RateLimit = llm.ParseRateLimitHeaders(resp.Header)
	if llm.RawBodyEnabled() {
		r.RawRequestBody = string(b)
		r.RawResponseBody = string(rawBytes)
	}
	return r, nil
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}
	sctx, cancel := context.WithCancel(ctx)
	sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, req.AdapterTimeout, true)
	defer timeoutCancel()

	body, err := a.buildRequestBody(req)
	if err != nil {
		cancel()
		return nil, err
	}
	body["stream"] = true

	b, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	a.setAnthropicHeaders(httpReq, req.ProviderOptions)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError("anthropic", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		_ = json.Unmarshal(rawBytes, &raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("messages.create(stream) failed: %s", strings.TrimSpace(string(rawBytes)))
		cancel()
		return nil, llm.ErrorFromHTTPStatus("anthropic", resp.StatusCode, msg, raw, ra)
	}

	s := llm.NewChanStream(cancel)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			_ = resp.Body.Close()
			s.CloseSend()
		}()

		finished := false
		type blockState struct {
			typ      string
			rawStart map[string]any // raw content_block from content_block_start

			// text
			textID      string
			textStarted bool
			text        strings.Builder

			// tool_use
			toolID      string
			toolName    string
			toolStarted bool
			toolArgs    strings.Builder

			// thinking / redacted_thinking
			thinkingStarted bool
			thinking        strings.Builder
			signature       strings.Builder
			redacted        bool
		}
		blocks := map[int]*blockState{}
		maxIdx := -1

		getBlock := func(idx int) *blockState {
			st := blocks[idx]
			if st == nil {
				st = &blockState{}
				blocks[idx] = st
			}
			if idx > maxIdx {
				maxIdx = idx
			}
			return st
		}

		var usage llm.Usage
		finish := llm.FinishReason{Reason: "stop"}
		var msgID string
		var actualModel string
		var rawMessage map[string]any

		var sseBody io.Reader = resp.Body
		var sseBuf *bytes.Buffer
		if llm.RawBodyEnabled() {
			sseBuf = &bytes.Buffer{}
			sseBody = io.TeeReader(resp.Body, sseBuf)
		}
		rawReqBody := string(b) // captured above

		parseErr := llm.ParseSSE(sctx, sseBody, func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var payload map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&payload); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil
			}

			switch ev.Event {
			case "message_start":
				if msgAny, ok := payload["message"].(map[string]any); ok {
					if id, _ := msgAny["id"].(string); id != "" {
						msgID = id
					}
					if m, _ := msgAny["model"].(string); m != "" {
						actualModel = m
					}
					rawMessage = msgAny
					if u, ok := msgAny["usage"].(map[string]any); ok {
						usage = parseUsage(u)
					}
				}
			case "content_block_start":
				idx := llm.IntFromAny(payload["index"])
				cb, _ := payload["content_block"].(map[string]any)
				typ, _ := cb["type"].(string)
				st := getBlock(idx)
				st.typ = typ
				st.rawStart = cb
				if cb, ok := payload["content_block"].(map[string]any); ok {
					switch typ {
					case "text":
						if st.textID == "" {
							st.textID = fmt.Sprintf("text_%d", idx)
						}
						if !st.textStarted {
							st.textStarted = true
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.textID})
						}
					case "tool_use":
						st.toolID, _ = cb["id"].(string)
						st.toolName, _ = cb["name"].(string)
						// Note: content_block_start always has input:{} as a placeholder.
						// Actual arguments arrive via input_json_delta events; capturing
						// the placeholder here would corrupt them (e.g. {}{"city":"Paris"}).
						if !st.toolStarted && strings.TrimSpace(st.toolID) != "" {
							st.toolStarted = true
							tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
						}
					case "thinking", "redacted_thinking":
						st.redacted = (typ == "redacted_thinking")
						if sig, _ := cb["signature"].(string); sig != "" && st.signature.Len() == 0 {
							st.signature.WriteString(sig)
						}
						if !st.thinkingStarted {
							st.thinkingStarted = true
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
						}
						// Some implementations may include initial thinking in the start block.
						t, _ := cb["thinking"].(string)
						if t == "" {
							t, _ = cb["text"].(string)
						}
						if t == "" {
							t, _ = cb["data"].(string)
						}
						if t != "" {
							st.thinking.WriteString(t)
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: t})
						}
					case "server_tool_use":
						st.toolID, _ = cb["id"].(string)
						st.toolName, _ = cb["name"].(string)
					case "web_search_tool_result":
						// raw payload stored in st.rawStart above
					}
				}
			case "content_block_delta":
				idx := llm.IntFromAny(payload["index"])
				st := getBlock(idx)
				if d, ok := payload["delta"].(map[string]any); ok {
					switch typ, _ := d["type"].(string); typ {
					case "text_delta":
						if delta, _ := d["text"].(string); delta != "" {
							if st.textID == "" {
								st.textID = fmt.Sprintf("text_%d", idx)
							}
							if !st.textStarted {
								st.textStarted = true
								s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.textID})
							}
							st.text.WriteString(delta)
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: st.textID, Delta: delta})
						}
					case "input_json_delta":
						if delta, _ := d["partial_json"].(string); delta != "" {
							st.toolArgs.WriteString(delta)
							if !st.toolStarted && strings.TrimSpace(st.toolID) != "" {
								st.toolStarted = true
								tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
							}
							if strings.TrimSpace(st.toolID) != "" {
								tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(st.toolArgs.String()), Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
							}
						}
					case "thinking_delta":
						delta, _ := d["thinking"].(string)
						if delta == "" {
							delta, _ = d["text"].(string)
						}
						if delta != "" {
							if !st.thinkingStarted {
								st.thinkingStarted = true
								s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
							}
							st.thinking.WriteString(delta)
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
						}
					case "signature_delta":
						if delta, _ := d["signature"].(string); delta != "" {
							st.signature.WriteString(delta)
						}
					}
				}
			case "content_block_stop":
				idx := llm.IntFromAny(payload["index"])
				st := blocks[idx]
				if st == nil {
					return nil
				}
				switch st.typ {
				case "text":
					if st.textStarted {
						if st.textID == "" {
							st.textID = fmt.Sprintf("text_%d", idx)
						}
						s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: st.textID})
						st.textStarted = false
					}
				case "tool_use":
					if strings.TrimSpace(st.toolID) != "" {
						if !st.toolStarted {
							st.toolStarted = true
							tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
						}
						tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(st.toolArgs.String()), Type: "function"}
						s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
						st.toolStarted = false
					}
				case "thinking", "redacted_thinking":
					if st.thinkingStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
						st.thinkingStarted = false
					}
				}
			case "message_delta":
				if sr, _ := payload["stop_reason"].(string); sr != "" {
					finish = llm.NormalizeFinishReason("anthropic", sr)
				}
				if u, ok := payload["usage"].(map[string]any); ok {
					u2 := parseUsage(u)
					if u2.OutputTokens > 0 {
						usage.OutputTokens = u2.OutputTokens
					}
					if u2.InputTokens > 0 {
						usage.InputTokens = u2.InputTokens
					}
				}
			case "message_stop":
				var parts []llm.ContentPart
				for i := 0; i <= maxIdx; i++ {
					st := blocks[i]
					if st == nil {
						continue
					}
					switch st.typ {
					case "text":
						if st.text.Len() > 0 {
							parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: st.text.String()})
						}
					case "tool_use":
						if strings.TrimSpace(st.toolID) != "" {
							args := st.toolArgs.String()
							if args == "" {
								args = "{}"
							}
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:        st.toolID,
									Name:      st.toolName,
									Arguments: json.RawMessage(args),
									Type:      "function",
								},
							})
						}
					case "thinking":
						if st.thinking.Len() > 0 {
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentThinking,
								Thinking: &llm.ThinkingData{
									Text:      st.thinking.String(),
									Signature: st.signature.String(),
									Redacted:  false,
								},
							})
						}
					case "redacted_thinking":
						if st.thinking.Len() > 0 {
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentRedThinking,
								Thinking: &llm.ThinkingData{
									Text:     st.thinking.String(),
									Redacted: true,
								},
							})
						}
					case "server_tool_use":
						if st.rawStart != nil {
							query := ""
							if input, _ := st.rawStart["input"].(map[string]any); input != nil {
								query, _ = input["query"].(string)
							}
							raw, _ := json.Marshal(st.rawStart)
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentWebSearch,
								WebSearch: &llm.WebSearchData{
									Query: query,
									Raw:   raw,
								},
							})
						}
					case "web_search_tool_result":
						if st.rawStart != nil {
							raw, _ := json.Marshal(st.rawStart)
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentWebSearch,
								WebSearch: &llm.WebSearchData{
									Raw: raw,
								},
							})
						}
					}
				}

				msg := llm.Message{Role: llm.RoleAssistant, Content: parts}
				model := actualModel
				if model == "" {
					model = req.Model
				}
				r := llm.Response{
					ID:       msgID,
					Provider: "anthropic",
					Model:    model,
					Message:  msg,
					Finish:   finish,
					Usage:    usage,
					Raw:      rawMessage,
				}
				llm.StampEndpointURL(&r, a.BaseURL+"/v1/messages")
				if len(r.ToolCalls()) > 0 {
					r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: "tool_use"}
				}

				// Best-effort thinking-token estimate from visible thinking content,
				// only when provider didn't supply a native ReasoningTokens count.
				// Informational only — never enters the billing path.
				if r.Usage.ReasoningTokens == nil && r.Usage.ReasoningTokensEstimated == nil {
					if est := estimateThinkingTokens(parts); est > 0 {
						e := est
						r.Usage.ReasoningTokensEstimated = &e
					}
				}

				rp := r
				if sseBuf != nil {
					rp.RawRequestBody = rawReqBody
					rp.RawResponseBody = sseBuf.String()
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
				finished = true
				cancel()
			default:
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
			return nil
		}, llm.StreamReadSSEOptions(req.AdapterTimeout)...)

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("anthropic", err)})
			} else {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError("anthropic", "anthropic stream ended without completion", parseErr)})
			}
		}
	}()

	return s, nil
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

func fromAnthropicResponse(raw map[string]any, requestedModel string) llm.Response {
	r := llm.Response{
		Provider: "anthropic",
		Model:    requestedModel,
		Raw:      raw,
	}
	if id, _ := raw["id"].(string); id != "" {
		r.ID = id
	}
	if m, _ := raw["model"].(string); m != "" {
		r.Model = m
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	if content, ok := raw["content"].([]any); ok {
		for _, itAny := range content {
			it, ok := itAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := it["type"].(string)
			switch typ {
			case "text":
				if t, _ := it["text"].(string); t != "" {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: t})
				}
			case "tool_use":
				id, _ := it["id"].(string)
				name, _ := it["name"].(string)
				argsAny := it["input"]
				argsRaw, _ := json.Marshal(argsAny)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        id,
						Name:      name,
						Arguments: argsRaw,
						Type:      "function",
					},
				})
			case "thinking":
				t, _ := it["text"].(string)
				if t == "" {
					t, _ = it["thinking"].(string)
				}
				if t != "" {
					sig, _ := it["signature"].(string)
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentThinking,
						Thinking: &llm.ThinkingData{
							Text:      t,
							Signature: sig,
							Redacted:  false,
						},
					})
				}
			case "redacted_thinking":
				if d, _ := it["data"].(string); d != "" {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentRedThinking,
						Thinking: &llm.ThinkingData{
							Text:     d,
							Redacted: true,
						},
					})
				}
			case "server_tool_use":
				query := ""
				if input, _ := it["input"].(map[string]any); input != nil {
					query, _ = input["query"].(string)
				}
				raw, _ := json.Marshal(it)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentWebSearch,
					WebSearch: &llm.WebSearchData{
						Query: query,
						Raw:   raw,
					},
				})
			case "web_search_tool_result":
				raw, _ := json.Marshal(it)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentWebSearch,
					WebSearch: &llm.WebSearchData{
						Raw: raw,
					},
				})
			default:
				// ignore
			}
		}
	}

	r.Message = msg
	if len(r.ToolCalls()) > 0 {
		r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: "tool_use"}
	} else {
		sr, _ := raw["stop_reason"].(string)
		r.Finish = llm.NormalizeFinishReason("anthropic", sr)
	}

	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseUsage(u)
	}

	if r.Usage.ReasoningTokens == nil && r.Usage.ReasoningTokensEstimated == nil {
		if est := estimateThinkingTokens(msg.Content); est > 0 {
			e := est
			r.Usage.ReasoningTokensEstimated = &e
		}
	}

	return r
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

func parseUsage(u map[string]any) llm.Usage {
	// Anthropic already separates new input, cache reads, and cache creations,
	// matching llm.Usage's invariant.
	input := llm.IntFromAny(u["input_tokens"])
	output := llm.IntFromAny(u["output_tokens"])
	usage := llm.Usage{
		InputTokens:  input,
		OutputTokens: output,
		Raw:          u,
	}
	total := input + output
	if vAny, ok := u["cache_read_input_tokens"]; ok {
		v := llm.IntFromAny(vAny)
		usage.CacheReadTokens = &v
		total += v
	}
	// When extended-cache-ttl is requested, Anthropic reports a
	// `cache_creation` breakdown alongside the aggregate
	// `cache_creation_input_tokens`. Prefer the breakdown so 5m and 1h
	// writes are priced at their correct rates downstream.
	if breakdown, ok := u["cache_creation"].(map[string]any); ok {
		if vAny, ok := breakdown["ephemeral_5m_input_tokens"]; ok {
			v := llm.IntFromAny(vAny)
			usage.CacheWriteTokens = &v
			total += v
		}
		if vAny, ok := breakdown["ephemeral_1h_input_tokens"]; ok {
			v := llm.IntFromAny(vAny)
			usage.CacheWrite1hTokens = &v
			total += v
		}
	} else if vAny, ok := u["cache_creation_input_tokens"]; ok {
		// Fallback: no breakdown, assume default 5m TTL.
		v := llm.IntFromAny(vAny)
		usage.CacheWriteTokens = &v
		total += v
	}
	usage.TotalTokens = total
	return usage
}

// clampEffort ensures the requested effort level is within the model's supported
// range. If the requested level isn't in supportedLevels, clamp down to the
// highest supported level. If supportedLevels is empty, return the input unchanged.
func clampEffort(requested string, supportedLevels []string) string {
	if len(supportedLevels) == 0 {
		return requested
	}
	requested = strings.ToLower(strings.TrimSpace(requested))

	// Check if requested is directly supported.
	for _, lvl := range supportedLevels {
		if strings.ToLower(lvl) == requested {
			return requested
		}
	}

	// Effort hierarchy for comparison: low < medium < high < max.
	hierarchy := []string{"low", "medium", "high", "max"}
	reqIdx := -1
	for i, h := range hierarchy {
		if h == requested {
			reqIdx = i
			break
		}
	}
	if reqIdx < 0 {
		// Unknown level; return as-is and let the API handle it.
		return requested
	}

	// Find highest supported level that's at or below requested.
	highestIdx := -1
	for _, lvl := range supportedLevels {
		lvlLower := strings.ToLower(lvl)
		for i, h := range hierarchy {
			if h == lvlLower && i <= reqIdx && i > highestIdx {
				highestIdx = i
			}
		}
	}
	if highestIdx >= 0 {
		return hierarchy[highestIdx]
	}
	// No lower level available; return the lowest supported level.
	for _, h := range hierarchy {
		for _, lvl := range supportedLevels {
			if strings.ToLower(lvl) == h {
				return h
			}
		}
	}
	return requested
}

package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/llm"
)

const (
	defaultAPIBaseURL      = "https://api.openai.com"
	defaultResponsesPath   = "/v1/responses"
	defaultChatGPTBaseURL  = "https://chatgpt.com"
	defaultCodexResponses  = "/backend-api/codex/responses"
)

type Config struct {
	StateDir string
}

type Adapter struct {
	APIKey         string
	BaseURL        string
	ResponsesPath  string
	OrgID          string
	ProjectID      string
	ChatGPTAccountID string
	Client         *http.Client
	DefaultHeaders map[string]string
}

func init() {
	llm.RegisterEnvAdapterFactory(func(cfg llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		a, err := NewFromEnv(Config{StateDir: cfg.StateDir})
		if err != nil {
			if isUnconfigured(err) {
				return nil, false, nil
			}
			return nil, true, err
		}
		return a, true, nil
	})
}

func NewFromEnv(cfgs ...Config) (*Adapter, error) {
	var cfg Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}

	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key != "" {
		base := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
		if base == "" {
			base = defaultAPIBaseURL
		}
		return &Adapter{
			APIKey:        key,
			BaseURL:       strings.TrimRight(base, "/"),
			ResponsesPath: defaultResponsesPath,
			OrgID:         strings.TrimSpace(os.Getenv("OPENAI_ORG_ID")),
			ProjectID:     strings.TrimSpace(os.Getenv("OPENAI_PROJECT_ID")),
			// Avoid short client-level timeouts; rely on request context deadlines instead.
			Client: &http.Client{Timeout: 0},
		}, nil
	}

	if strings.TrimSpace(cfg.StateDir) == "" {
		return nil, fmt.Errorf("no OpenAI credentials configured")
	}

	record, err := authopenai.LoadAuth(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("load OpenAI auth: %w", err)
	}
	base := strings.TrimSpace(os.Getenv("OPENAI_CHATGPT_BASE_URL"))
	if base == "" {
		base = defaultChatGPTBaseURL
	}
	accountID := strings.TrimSpace(record.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(record.WorkspaceID)
	}
	return &Adapter{
		APIKey:           record.AccessToken,
		BaseURL:          strings.TrimRight(base, "/"),
		ResponsesPath:    defaultCodexResponses,
		ChatGPTAccountID: accountID,
		Client:           &http.Client{Timeout: 0},
	}, nil
}

func (a *Adapter) Name() string { return "openai" }

func (a *Adapter) setHeaders(req *http.Request) {
	// Apply default headers first so provider-specific headers take precedence.
	for k, v := range a.DefaultHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if a.OrgID != "" {
		req.Header.Set("OpenAI-Organization", a.OrgID)
	}
	if a.ProjectID != "" {
		req.Header.Set("OpenAI-Project", a.ProjectID)
	}
	if a.ChatGPTAccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", a.ChatGPTAccountID)
	}
}

func (a *Adapter) buildRequestBody(req llm.Request) (map[string]any, error) {
	instructions, inputItems, err := toResponsesInput(req.Messages, req.Model)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":               req.Model,
		"instructions":        instructions,
		"input":               inputItems,
		"parallel_tool_calls": true,
		"store":               false,
	}

	var tools []map[string]any
	if len(req.Tools) > 0 {
		tools = toResponsesTools(req.Tools)
	}
	if req.WebSearch {
		tools = append(tools, map[string]any{"type": "web_search"})
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
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
		body["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.ReasoningEffort != nil {
		body["reasoning"] = map[string]any{"effort": *req.ReasoningEffort}
	}
	if req.ResponseFormat != nil {
		if rf := toResponsesResponseFormat(*req.ResponseFormat); rf != nil {
			text, _ := body["text"].(map[string]any)
			if text == nil {
				text = map[string]any{}
			}
			text["format"] = rf
			body["text"] = text
		}
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}
	return body, nil
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.responsesURL(), bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	a.setHeaders(httpReq)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.Response{}, llm.WrapContextError("openai", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return llm.Response{}, err
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return llm.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("responses.create failed: %v", raw)
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
	}

	r := fromResponses(raw, req.Model)
	r.RateLimit = llm.ParseRateLimitHeaders(resp.Header)
	if llm.RawBodyEnabled() {
		r.RawRequestBody = string(b)
		r.RawResponseBody = string(rawBytes)
	}
	return r, nil
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0} // streaming uses request context for cancellation
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

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.responsesURL(), bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	a.setHeaders(httpReq)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError("openai", err)
	}

	// Handle non-2xx immediately.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		var raw map[string]any
		dec := json.NewDecoder(resp.Body)
		dec.UseNumber()
		_ = dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("responses.create(stream) failed: %v", raw)
		cancel()
		return nil, llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
	}

	s := llm.NewChanStream(cancel)
	// STREAM_START
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			_ = resp.Body.Close()
			s.CloseSend()
		}()

		textID := "text_1"
		textStarted := false
		finished := false
		type toolState struct {
			id      string
			name    string
			started bool
			args    strings.Builder
		}
		toolStates := map[string]*toolState{}

		var sseBody io.Reader = resp.Body
		var sseBuf *bytes.Buffer
		if llm.RawBodyEnabled() {
			sseBuf = &bytes.Buffer{}
			sseBody = io.TeeReader(resp.Body, sseBuf)
		}
		rawReqBody := string(b)

		_ = llm.ParseSSE(sctx, sseBody, func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var payload map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&payload); err != nil {
				// Emit raw passthrough and continue.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil
			}
			typ, _ := payload["type"].(string)
			if typ == "" {
				typ = ev.Event
			}

			switch typ {
			case "response.output_text.delta":
				delta, _ := payload["delta"].(string)
				if delta == "" {
					delta, _ = payload["text"].(string)
				}
				if delta == "" {
					return nil
				}
				if !textStarted {
					textStarted = true
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: delta})
			case "response.function_call_arguments.delta":
				delta, _ := payload["delta"].(string)
				if delta == "" {
					delta, _ = payload["arguments"].(string)
				}
				callID, _ := payload["call_id"].(string)
				if callID == "" {
					callID, _ = payload["item_id"].(string)
				}
				if callID == "" {
					callID, _ = payload["id"].(string)
				}
				name, _ := payload["name"].(string)
				if callID == "" || (delta == "" && name == "") {
					// Can't map reliably; pass through.
					s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
					return nil
				}

				st := toolStates[callID]
				if st == nil {
					st = &toolState{id: callID, name: name}
					toolStates[callID] = st
				}
				if st.name == "" && name != "" {
					st.name = name
				}
				if delta != "" {
					st.args.WriteString(delta)
				}
				if !st.started {
					st.started = true
					tc := llm.ToolCallData{ID: st.id, Name: st.name, Type: "function"}
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
				}
				tc := llm.ToolCallData{ID: st.id, Name: st.name, Arguments: []byte(st.args.String()), Type: "function"}
				s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
			case "response.output_item.done":
				itemAny := payload["item"]
				if itemAny == nil {
					itemAny = payload["output_item"]
				}
				if item, ok := itemAny.(map[string]any); ok {
					it, _ := item["type"].(string)
					switch it {
					case "function_call":
						callID, _ := item["call_id"].(string)
						name, _ := item["name"].(string)
						argsStr, _ := item["arguments"].(string)
						if callID != "" {
							st := toolStates[callID]
							if st == nil {
								st = &toolState{id: callID, name: name}
								toolStates[callID] = st
							}
							if st.name == "" && name != "" {
								st.name = name
							}
							if argsStr != "" && st.args.Len() == 0 {
								st.args.WriteString(argsStr)
							}
							if argsStr == "" {
								argsStr = st.args.String()
							}
							if !st.started {
								st.started = true
								tc := llm.ToolCallData{ID: st.id, Name: st.name, Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
							}
							tc := llm.ToolCallData{ID: st.id, Name: st.name, Arguments: json.RawMessage(argsStr), Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
						} else {
							s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
						}
					default:
						// Best-effort: treat as end-of-text.
						if textStarted {
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
							textStarted = false
						}
					}
				} else if textStarted {
					// Best-effort: treat as end-of-text.
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
					textStarted = false
				}
			case "response.completed":
				// Response object may be nested under "response" or be the payload itself.
				rawResp, _ := payload["response"].(map[string]any)
				if rawResp == nil {
					rawResp = payload
				}
				r := fromResponses(rawResp, req.Model)
				// Ensure text segment is closed.
				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
					textStarted = false
				}
				rp := r
				if sseBuf != nil {
					rp.RawRequestBody = rawReqBody
					rp.RawResponseBody = sseBuf.String()
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
				// Stop parsing after finish.
				finished = true
				cancel()
			default:
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
			return nil
		}, llm.StreamReadSSEOptions(req.AdapterTimeout)...)

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("openai", err)})
			}
		}
	}()

	return s, nil
}

func (a *Adapter) responsesURL() string {
	base := strings.TrimRight(a.BaseURL, "/")
	path := a.ResponsesPath
	if path == "" {
		path = defaultResponsesPath
	}
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

func isUnconfigured(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "no OpenAI credentials configured") {
		return true
	}
	if strings.Contains(err.Error(), authopenai.ErrAuthNotFound.Error()) {
		return true
	}
	return false
}

func toResponsesResponseFormat(rf llm.ResponseFormat) any {
	switch strings.ToLower(strings.TrimSpace(rf.Type)) {
	case "", "text":
		return nil
	case "json":
		return map[string]any{"type": "json"}
	case "json_schema":
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

func toResponsesTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		strict := true
		if t.Strict != nil {
			strict = *t.Strict
		}
		if strict {
			// OpenAI strict mode requires a fully-specified JSON Schema:
			// - object schemas must set additionalProperties=false
			// - required must include every key in properties (even for "optional" fields)
			// See API validation errors like:
			// "Invalid schema for function 'read_file': ... 'required' ... Missing 'limit'."
			params = strictifyJSONSchema(params)
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
			"strict":      strict,
		})
	}
	return out
}

func strictifyJSONSchema(in map[string]any) map[string]any {
	// Best-effort deep copy + strictification for OpenAI tool schemas.
	// This intentionally handles only the constructs we emit (object/array) and is safe to
	// apply repeatedly (idempotent for our shapes).
	cp := deepCopyAny(in).(map[string]any)
	strictifyJSONSchemaInPlace(cp)
	return cp
}

func strictifyJSONSchemaInPlace(m map[string]any) {
	if m == nil {
		return
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		// OpenAI strict mode requires this be present and false.
		m["additionalProperties"] = false

		props, _ := m["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			m["properties"] = props
		}
		// Required must include all properties keys (even for "optional" fields).
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m["required"] = keys

		// Recurse into property schemas.
		for _, k := range keys {
			if child, ok := props[k].(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok {
			strictifyJSONSchemaInPlace(items)
		}
	}

	// If the schema uses combinators, strictify any subschemas we can find.
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[comb]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	}
}

func deepCopyAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = deepCopyAny(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = deepCopyAny(x[i])
		}
		return out
	default:
		return v
	}
}

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
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}, nil
	default:
		// Backward-compatible: some callers may have used an unspecified mode to force
		// a particular tool. Prefer explicit mode="named".
		if strings.TrimSpace(tc.Name) != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			}, nil
		}
		return nil, llm.NewUnsupportedToolChoiceError("openai", tc.Mode)
	}
}

// defaultImageDetail returns the best image detail level for the model.
// GPT-5.4+ supports "original" (full fidelity); older models use "high".
func defaultImageDetail(model string) string {
	if strings.HasPrefix(model, "gpt-5.4") || strings.HasPrefix(model, "gpt-5.5") ||
		strings.HasPrefix(model, "gpt-6") {
		return "original"
	}
	return "high"
}

func toResponsesInput(msgs []llm.Message, model string) (instructions string, items []any, _ error) {
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
							mt := strings.TrimSpace(p.Image.MediaType)
							if mt == "" {
								mt = "image/png"
							}
							url = llm.DataURI(mt, p.Image.Data)
						} else if llm.IsLocalPath(url) {
							path := llm.ExpandTilde(url)
							b, err := os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							mt := strings.TrimSpace(p.Image.MediaType)
							if mt == "" {
								mt = llm.InferMimeTypeFromPath(path)
							}
							if mt == "" {
								mt = "image/png"
							}
							url = llm.DataURI(mt, b)
						}
						if url != "" {
							img := map[string]any{
								"type":      "input_image",
								"image_url": url,
							}
							if p.Image.Detail != "" {
								img["detail"] = p.Image.Detail
							} else {
								img["detail"] = defaultImageDetail(model)
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
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   p.ToolCall.ID,
						"name":      p.ToolCall.Name,
						"arguments": string(p.ToolCall.Arguments),
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
				items = append(items, item)

				if len(p.ToolResult.ImageData) > 0 {
					mt := p.ToolResult.ImageMediaType
					if mt == "" {
						mt = "image/png"
					}
					items = append(items, map[string]any{
						"type":      "input_image",
						"image_url": llm.DataURI(mt, p.ToolResult.ImageData),
						"detail":    defaultImageDetail(model),
					})
				}
			}
		default:
			// ignore unknown roles
		}
	}
	return instructions, items, nil
}

func fromResponses(raw map[string]any, requestedModel string) llm.Response {
	// Best-effort mapping. OpenAI Responses output is a list of typed items.
	r := llm.Response{
		Provider: "openai",
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

	// Parse output items.
	if out, ok := raw["output"].([]any); ok {
		for _, itemAny := range out {
			item, ok := itemAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := item["type"].(string)
			switch typ {
			case "message":
				phase, _ := item["phase"].(string)
				// content: [{type:"output_text", text:"..."}]
				if content, ok := item["content"].([]any); ok {
					for _, cAny := range content {
						c, ok := cAny.(map[string]any)
						if !ok {
							continue
						}
						ct, _ := c["type"].(string)
						if ct == "output_text" {
							text, _ := c["text"].(string)
							if text != "" || phase != "" {
								msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: text, Phase: phase})
							}
						}
					}
				}
			case "function_call":
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				callID, _ := item["call_id"].(string)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        callID,
						Name:      name,
						Arguments: json.RawMessage(args),
						Type:      "function",
					},
				})
			case "web_search_call":
				query := ""
				if action, _ := item["action"].(map[string]any); action != nil {
					query, _ = action["query"].(string)
				}
				raw, _ := json.Marshal(item)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentWebSearch,
					WebSearch: &llm.WebSearchData{
						Query: query,
						Raw:   raw,
					},
				})
			default:
				// ignore (reasoning, etc.)
			}
		}
	}

	r.Message = msg

	// Check Responses API status/incomplete_details for finish reason.
	status, _ := raw["status"].(string)
	switch {
	case status == "incomplete":
		reason := "length" // default for incomplete
		if details, ok := raw["incomplete_details"].(map[string]any); ok {
			if dr, _ := details["reason"].(string); dr != "" {
				switch dr {
				case "max_output_tokens":
					reason = "length"
				case "content_filter":
					reason = "content_filter"
				default:
					reason = "other"
				}
			}
		}
		r.Finish = llm.FinishReason{Reason: reason, Raw: status}
	case len(r.ToolCalls()) > 0:
		r.Finish = llm.FinishReason{Reason: "tool_calls"}
	default:
		r.Finish = llm.FinishReason{Reason: "stop"}
	}

	// usage
	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseUsage(u)
	}
	return r
}

func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	a.setHeaders(httpReq)

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []llm.ModelInfo
	for _, m := range result.Data {
		if skipOpenAIModel(m.ID) {
			continue
		}
		models = append(models, llm.ModelInfo{
			ID:          m.ID,
			Provider:    "openai",
			DisplayName: m.ID,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func skipOpenAIModel(id string) bool {
	lower := strings.ToLower(id)
	skip := []string{
		"embedding", "dall-e", "whisper", "davinci", "babbage",
		"tts", "audio", "realtime", "transcribe", "image",
		"moderation", "sora",
	}
	for _, s := range skip {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func parseUsage(u map[string]any) llm.Usage {
	// OpenAI's Responses API reports input_tokens as total-including-cached,
	// with cached_tokens a subset in input_tokens_details. The llm.Usage
	// invariant is that InputTokens means new uncached input only, so we
	// subtract cached here.
	rawInput := llm.IntFromAny(u["input_tokens"])
	output := llm.IntFromAny(u["output_tokens"])
	var cachedRead int
	if inDetails, ok := u["input_tokens_details"].(map[string]any); ok {
		cachedRead = llm.IntFromAny(inDetails["cached_tokens"])
	}
	uncachedInput := rawInput - cachedRead
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	usage := llm.Usage{
		InputTokens:  uncachedInput,
		OutputTokens: output,
		TotalTokens:  llm.IntFromAny(u["total_tokens"]),
		Raw:          u,
	}
	if outDetails, ok := u["output_tokens_details"].(map[string]any); ok {
		rt := llm.IntFromAny(outDetails["reasoning_tokens"])
		usage.ReasoningTokens = &rt
	}
	if _, ok := u["input_tokens_details"].(map[string]any); ok {
		ct := cachedRead
		usage.CacheReadTokens = &ct
	}
	return usage
}

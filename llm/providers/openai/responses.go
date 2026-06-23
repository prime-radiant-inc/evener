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

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
)

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
	if req.Temperature != nil && !a.usesCodexBackend() {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil && !a.usesCodexBackend() {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil && !a.usesCodexBackend() {
		body["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 && !a.usesCodexBackend() {
		body["stop"] = req.StopSequences
	}
	if strings.TrimSpace(req.PromptCacheKey) != "" {
		body["prompt_cache_key"] = strings.TrimSpace(req.PromptCacheKey)
	}
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		body["previous_response_id"] = strings.TrimSpace(req.PreviousResponseID)
	}
	if strings.TrimSpace(req.ConversationID) != "" {
		body["conversation"] = strings.TrimSpace(req.ConversationID)
	}
	if strings.TrimSpace(req.ServiceTier) != "" && !a.usesCodexBackend() {
		body["service_tier"] = strings.TrimSpace(req.ServiceTier)
	}
	if strings.TrimSpace(req.SafetyIdentifier) != "" && !a.usesCodexBackend() {
		body["safety_identifier"] = strings.TrimSpace(req.SafetyIdentifier)
	}
	if strings.TrimSpace(req.PromptCacheRetention) != "" && !a.usesCodexBackend() {
		body["prompt_cache_retention"] = strings.TrimSpace(req.PromptCacheRetention)
	}
	if strings.TrimSpace(req.Truncation) != "" && !a.usesCodexBackend() {
		body["truncation"] = strings.TrimSpace(req.Truncation)
	}
	if req.MaxToolCalls != nil && !a.usesCodexBackend() {
		body["max_tool_calls"] = *req.MaxToolCalls
	}
	if req.Background != nil && !a.usesCodexBackend() {
		body["background"] = *req.Background
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if a.usesCodexBackend() {
		clientMetadata := mergeStringMaps(req.Metadata, req.ClientMetadata)
		if len(clientMetadata) > 0 {
			body["client_metadata"] = clientMetadata
		}
	} else if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.ReasoningEffort != nil {
		body["reasoning"] = map[string]any{
			"effort":  *req.ReasoningEffort,
			"summary": reasoningSummaryLevel(req.Model),
		}
	}
	include := append([]string{}, req.Include...)
	if req.ReasoningEffort != nil {
		include = appendUniqueString(include, encryptedReasoning)
	}
	if len(include) > 0 {
		body["include"] = include
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
				if a.usesCodexBackend() && openAICodexUnsupportedRequestField(k) {
					continue
				}
				body[k] = v
			}
		}
	}
	return body, nil
}

func openAICodexUnsupportedRequestField(key string) bool {
	switch key {
	case "temperature",
		"top_p",
		"max_output_tokens",
		"stop",
		"metadata",
		"service_tier",
		"safety_identifier",
		"prompt_cache_retention",
		"truncation",
		"max_tool_calls",
		"background":
		return true
	default:
		return false
	}
}

// streamResponses is the raw Responses API streaming path.
// It emits errEmptyResponsesStream if the stream closes 200 OK with zero content.
func (a *Adapter) streamResponses(ctx context.Context, req llm.Request) (llm.Stream, error) {
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
	a.setRequestHeaders(httpReq, req)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError("openai", err)
	}

	// Handle non-2xx immediately.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(rawBytes))
		dec.UseNumber()
		_ = dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("responses.create(stream) failed: %v", raw)
		cancel()
		return nil, llm.ErrorFromHTTPStatusWithRawBodies("openai", resp.StatusCode, msg, raw, ra, string(b), string(rawBytes))
	}

	s := llm.NewChanStream(cancel)
	// STREAM_START
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go a.decodeResponsesStream(sctx, cancel, resp, s, req, b)

	return s, nil
}

// decodeResponsesStream consumes the Responses API SSE stream in its own
// goroutine: it translates each event into llm stream events and emits the final
// accumulated Response on response.completed. It emits errEmptyResponsesStream if
// the stream closes 200 OK with zero content. It owns closing the response body
// and the ChanStream.
func (a *Adapter) decodeResponsesStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, b []byte) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	textID := "text_1"
	textStarted := false
	reasoningStarted := false
	finished := false
	// sentContent tracks whether any meaningful content event was emitted.
	// If the stream closes without content, we emit errEmptyResponsesStream.
	sentContent := false
	type toolState struct {
		id      string
		itemID  string
		name    string
		started bool
		args    strings.Builder
	}
	toolStates := map[string]*toolState{}
	itemToCallID := map[string]string{}

	var sseBody io.Reader = resp.Body
	var sseBuf *bytes.Buffer
	if llm.RawBodyEnabled() {
		sseBuf = &bytes.Buffer{}
		sseBody = io.TeeReader(resp.Body, sseBuf)
	}
	rawReqBody := string(b)

	parseErr := llm.ParseSSE(sctx, sseBody, func(ev llm.SSEEvent) error {
		if len(ev.Data) == 0 {
			return nil
		}
		var payload map[string]any
		dec := json.NewDecoder(bytes.NewReader(ev.Data))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			// Emit raw passthrough and continue; returning the error would
			// abort the whole stream on a single malformed event.
			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
			return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
		}
		typ, _ := payload["type"].(string)
		if typ == "" {
			typ = ev.Event
		}

		switch typ {
		case "response.output_item.added":
			itemAny := payload["item"]
			if item, ok := itemAny.(map[string]any); ok {
				if itemType, _ := item["type"].(string); itemType == "function_call" {
					callID, _ := item["call_id"].(string)
					itemID, _ := item["id"].(string)
					name, _ := item["name"].(string)
					stateID := callID
					if stateID == "" {
						stateID = itemID
					}
					if stateID != "" {
						st := toolStates[stateID]
						if st == nil {
							st = &toolState{id: stateID}
						}
						if callID != "" {
							st.id = callID
							toolStates[callID] = st
						}
						if itemID != "" {
							st.itemID = itemID
							itemToCallID[itemID] = st.id
							toolStates[itemID] = st
						}
						if st.name == "" && name != "" {
							st.name = name
						}
						toolStates[stateID] = st
					}
				}
			}
		case "response.output_text.delta":
			delta, _ := payload["delta"].(string)
			if delta == "" {
				delta, _ = payload["text"].(string)
			}
			if delta == "" {
				return nil
			}
			sentContent = true
			if !textStarted {
				textStarted = true
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
			}
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: delta})
		case "response.reasoning_summary_text.delta":
			delta, _ := payload["delta"].(string)
			if delta == "" {
				return nil
			}
			sentContent = true
			s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
		case "response.reasoning_summary_part.added":
			// Detailed reasoning arrives as multiple summary parts; separate
			// them with a blank line so the rendered thought stays readable.
			if reasoningStarted {
				s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "\n\n"})
			}
			reasoningStarted = true
		case "response.function_call_arguments.delta":
			delta, _ := payload["delta"].(string)
			if delta == "" {
				delta, _ = payload["arguments"].(string)
			}
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			if callID == "" && itemID != "" {
				callID = itemToCallID[itemID]
			}
			if callID == "" {
				callID = itemID
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
			if st == nil && itemID != "" {
				st = toolStates[itemID]
				if st != nil {
					callID = st.id
				}
			}
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
				sentContent = true
				st.started = true
				tc := llm.ToolCallData{ID: st.id, Name: st.name, Type: "function"}
				s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
			}
			tc := llm.ToolCallData{ID: st.id, Name: st.name, Arguments: []byte(delta), Type: "function"}
			s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
		case "response.function_call_arguments.done":
			argsStr, _ := payload["arguments"].(string)
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			if callID == "" && itemID != "" {
				callID = itemToCallID[itemID]
			}
			if callID == "" {
				callID = itemID
			}
			if callID == "" {
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
				return nil
			}
			st := toolStates[callID]
			if st == nil && itemID != "" {
				st = toolStates[itemID]
				if st != nil {
					callID = st.id
				}
			}
			if st == nil {
				st = &toolState{id: callID}
				toolStates[callID] = st
			}
			if itemID != "" {
				st.itemID = itemID
				itemToCallID[itemID] = st.id
			}
			if argsStr != "" {
				st.args.Reset()
				st.args.WriteString(argsStr)
			}
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
					itemID, _ := item["id"].(string)
					name, _ := item["name"].(string)
					argsStr, _ := item["arguments"].(string)
					if callID == "" && itemID != "" {
						callID = itemToCallID[itemID]
					}
					if callID != "" {
						st := toolStates[callID]
						if st == nil && itemID != "" {
							st = toolStates[itemID]
							if st != nil {
								callID = st.id
							}
						}
						if st == nil {
							st = &toolState{id: callID, name: name}
							toolStates[callID] = st
						}
						if itemID != "" {
							st.itemID = itemID
							itemToCallID[itemID] = st.id
							toolStates[itemID] = st
						}
						if st.name == "" && name != "" {
							st.name = name
						}
						if argsStr != "" {
							st.args.Reset()
							st.args.WriteString(argsStr)
						}
						if argsStr == "" {
							argsStr = st.args.String()
						}
						if !st.started {
							sentContent = true
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
			llm.StampEndpointURL(&r, a.responsesURL())
			// Ensure text segment is closed.
			if textStarted {
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
				textStarted = false
			}
			sentContent = true
			if responseHasAssistantContent(r) {
				rp := r
				if sseBuf != nil {
					rp.RawRequestBody = rawReqBody
					rp.RawResponseBody = sseBuf.String()
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
			} else {
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage})
			}
			// Stop parsing after finish.
			finished = true
			cancel()
		default:
			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
		}
		return nil
	}, llm.StreamReadSSEOptions(req.AdapterTimeout)...)

	if !finished {
		switch {
		case sctx.Err() != nil:
			s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("openai", sctx.Err())})
		case !sentContent:
			// Stream closed 200 OK with zero content: model likely does not
			// support /v1/responses. Signal the caller to try Chat Completions.
			s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: errEmptyResponsesStream})
		default:
			// Content was streamed, then the stream ended without completion —
			// a genuine mid-stream read failure; surface its cause.
			s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError("openai", "openai responses stream ended without completion", parseErr)})
		}
	}
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

// defaultImageDetail returns the best image detail level for the model.
// GPT-5.4+ supports "original" (full fidelity); older models use "high".
func defaultImageDetail(model string) string {
	if strings.HasPrefix(model, "gpt-5.4") || strings.HasPrefix(model, "gpt-5.5") ||
		strings.HasPrefix(model, "gpt-6") {
		return "original"
	}
	return "high"
}

// reasoningSummaryLevel returns the reasoning.summary level to request for the
// model. The Responses API rejects an unsupported summary level with a 400, and
// support is per-model (e.g. the computer-use model only does "concise"), so we
// cannot send "detailed" unconditionally.
//
// gpt-5.x exposes "detailed" plaintext summaries, which the serf live-thinking
// block depends on (verified live on gpt-5.5). For every other reasoning model
// we send "auto": the OpenAI docs define "auto" as equivalent to "detailed" for
// most reasoning models today while letting the API pick a supported level, so
// it never 400s and still yields the richest summary the model offers.
func reasoningSummaryLevel(model string) string {
	if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") {
		return "detailed"
	}
	return "auto"
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
				if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.EncryptedContent != "" {
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
			case "reasoning":
				id, _ := item["id"].(string)
				encryptedContent, _ := item["encrypted_content"].(string)
				if encryptedContent != "" {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentThinking,
						Thinking: &llm.ThinkingData{
							ID:               id,
							EncryptedContent: encryptedContent,
							Summary:          parseReasoningSummary(item["summary"]),
						},
					})
				}
			default:
				// ignore
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

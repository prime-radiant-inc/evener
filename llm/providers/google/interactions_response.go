package google

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
)

var (
	// ErrPendingInteractionStatus marks an interaction returned in "queued" or
	// "in_progress" status where foreground completion cannot settle.
	ErrPendingInteractionStatus = errors.New("google: interaction status is pending")
	// ErrTerminalStatusStepFailure wraps a step RPC failure on a terminal
	// budget/continuation/incomplete status so callers do not retry automatically.
	ErrTerminalStatusStepFailure = errors.New("google: interaction ended with terminal status and step failure")
	// ErrUnexpectedInteractionStatus marks a missing or unrecognized interaction
	// terminal status.
	ErrUnexpectedInteractionStatus = errors.New("google: unexpected interaction status")
)

type interactionError struct {
	sentinel error
	provider string
	message  string
	raw      any
	cause    error
}

func (e *interactionError) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	if e.provider == "" {
		return msg
	}
	return fmt.Sprintf("%s error: %s", e.provider, msg)
}

func (e *interactionError) Provider() string           { return e.provider }
func (e *interactionError) StatusCode() int            { return 0 }
func (e *interactionError) ErrorCode() string          { return "" }
func (e *interactionError) Retryable() bool            { return false }
func (e *interactionError) RetryAfter() *time.Duration { return nil }
func (e *interactionError) Raw() any                   { return e.raw }
func (e *interactionError) Unwrap() error              { return e.cause }
func (e *interactionError) Is(target error) bool       { return target != nil && target == e.sentinel }

func (e *interactionError) CopyWithProvider(name string) error {
	return &interactionError{
		sentinel: e.sentinel,
		provider: strings.TrimSpace(name),
		message:  e.message,
		raw:      e.raw,
		cause:    e,
	}
}

type interactionStep struct {
	stepIdx           int
	stepType          string
	rawMap            map[string]any
	signature         string
	summaryItems      []string
	textItems         []string
	citations         []llm.WebSearchSource
	stepError         map[string]any
	nonTextModalities []string
	callID            string
	callName          string
	settledArgs       json.RawMessage
	settledSpelling   llm.Spelling
}

func fromInteraction(raw map[string]any, requestedModel string, body []byte, instance, wireModel string) (llm.Response, error) {
	r := llm.Response{
		ID:       stringVal(raw["id"]),
		Provider: cmp.Or(strings.TrimSpace(instance), "google"),
		Model:    cmp.Or(stringVal(raw["model"]), requestedModel),
		Raw:      raw,
	}
	if instance == "" && wireModel == "" {
		wireModel = r.Model
	}

	rawSteps, _ := raw["steps"].([]any)
	steps := make([]interactionStep, 0, len(rawSteps))
	var rawArgs []json.RawMessage
	var rawCaptured bool

	for stepIdx, rawStep := range rawSteps {
		stepMap, ok := rawStep.(map[string]any)
		if !ok {
			continue
		}
		stepType := stringVal(stepMap["type"])
		step := interactionStep{
			stepIdx:  stepIdx,
			stepType: stepType,
			rawMap:   stepMap,
		}
		switch stepType {
		case "thought":
			step.signature = strings.TrimSpace(stringVal(stepMap["signature"]))
			step.summaryItems = extractThoughtSummaryItems(stepMap["summary"])
		case "model_output":
			if errMap, ok := stepMap["error"].(map[string]any); ok {
				step.stepError = errMap
			}
			if content, ok := stepMap["content"].([]any); ok {
				step.textItems, step.citations, step.nonTextModalities = extractModelOutputContent(content, true)
			}
		case "function_call":
			if !rawCaptured {
				rawCaptured = true
				rawArgs = captureInteractionArgsRaw(body)
			}
			var rawTok json.RawMessage
			if stepIdx < len(rawArgs) {
				rawTok = rawArgs[stepIdx]
			}
			step.callID = stringVal(stepMap["id"])
			step.callName = stringVal(stepMap["name"])
			step.signature = strings.TrimSpace(stringVal(stepMap["signature"]))
			step.settledArgs, step.settledSpelling = resolveInteractionStepArgs(rawTok, stepMap["arguments"])
		}
		steps = append(steps, step)
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	stepErrMap, nonTextModalities := appendInteractionSteps(&msg, steps, instance, wireModel)
	r.Message = msg

	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseInteractionUsage(u)
	}
	if err := settleInteractionStatus(&r, raw, stepErrMap, nonTextModalities); err != nil {
		return llm.Response{}, err
	}
	return r, nil
}

func extractModelOutputContent(content []any, collectText bool) (texts []string, citations []llm.WebSearchSource, modalities []string) {
	for _, itemAny := range content {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		itemType := stringVal(item["type"])
		switch itemType {
		case "text":
			if collectText {
				if t := stringVal(item["text"]); t != "" {
					texts = append(texts, t)
				}
			}
			if ann := item["annotations"]; ann != nil {
				citations = appendUniqueSources(citations, extractCitationAnnotations(ann))
			}
		case "image", "audio", "video", "document":
			modalities = appendUniqueStrings(modalities, itemType)
		}
	}
	return texts, citations, modalities
}

func appendInteractionSteps(msg *llm.Message, steps []interactionStep, instance, wireModel string) (map[string]any, []string) {
	var stepErrMap map[string]any
	var nonTextModalities []string

	for i := 0; i < len(steps); i++ {
		step := steps[i]
		switch step.stepType {
		case "thought":
			if step.signature != "" || len(step.summaryItems) > 0 {
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentThinking,
					Thinking: &llm.ThinkingData{
						GoogleInteraction: true,
						GoogleInstance:    instance,
						GoogleModel:       wireModel,
						Text:              strings.Join(step.summaryItems, "\n\n"),
						Signature:         step.signature,
						Summary:           append([]string(nil), step.summaryItems...),
					},
				})
			}
		case "model_output":
			if len(step.stepError) > 0 {
				stepErrMap = step.stepError
			}
			nonTextModalities = appendUniqueStrings(nonTextModalities, step.nonTextModalities...)
			textStartIdx := len(msg.Content)
			for _, t := range step.textItems {
				if t == "" {
					continue
				}
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind:       llm.ContentText,
					Text:       t,
					GoogleStep: step.stepIdx + 1,
				})
			}
			if len(step.citations) > 0 {
				attached := false
				for idx := textStartIdx - 1; idx >= 0; idx-- {
					if msg.Content[idx].Kind == llm.ContentWebSearch && msg.Content[idx].WebSearch != nil {
						msg.Content[idx].WebSearch.Sources = appendUniqueSources(msg.Content[idx].WebSearch.Sources, step.citations)
						attached = true
						break
					}
				}
				if !attached {
					carrier := llm.ContentPart{
						Kind: llm.ContentWebSearch,
						WebSearch: &llm.WebSearchData{
							Sources: appendUniqueSources(nil, step.citations),
						},
					}
					msg.Content = slices.Insert(msg.Content, textStartIdx, carrier)
				}
			}
		case "function_call":
			callID := step.callID
			if callID == "" {
				callID = identifier.MustNewSyntheticCallID()
			}
			args := step.settledArgs
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			msg.Content = append(msg.Content, llm.ContentPart{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					GoogleInteraction: true,
					GoogleInstance:    instance,
					GoogleModel:       wireModel,
					ID:                callID,
					Name:              step.callName,
					Arguments:         args,
					Type:              "function",
					ThoughtSignature:  step.signature,
					ArgumentsSpelling: step.settledSpelling,
				},
			})
		case "google_search_call":
			callID := stringVal(step.rawMap["id"])
			queries := extractSearchQueries(step.rawMap)
			paired := []any{step.rawMap}
			if i+1 < len(steps) && steps[i+1].stepType == "google_search_result" {
				nextCallID := stringVal(steps[i+1].rawMap["call_id"])
				if callID == "" || nextCallID == "" || nextCallID == callID {
					paired = append(paired, normalizeGoogleSearchResultStep(steps[i+1].rawMap))
					i++
				}
			}
			rawJSON, _ := json.Marshal(normalizeJSONNumbers(paired))
			msg.Content = append(msg.Content, llm.ContentPart{
				Kind: llm.ContentWebSearch,
				WebSearch: &llm.WebSearchData{
					Query: strings.Join(queries, "; "),
					Raw:   rawJSON,
				},
			})
		case "google_search_result":
			norm := normalizeGoogleSearchResultStep(step.rawMap)
			rawJSON, _ := json.Marshal(normalizeJSONNumbers([]any{norm}))
			msg.Content = append(msg.Content, llm.ContentPart{
				Kind: llm.ContentWebSearch,
				WebSearch: &llm.WebSearchData{
					Raw: rawJSON,
				},
			})
		}
	}
	return stepErrMap, nonTextModalities
}

func extractThoughtSummaryItems(raw any) []string {
	switch v := raw.(type) {
	case []any:
		var out []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if typ := stringVal(m["type"]); typ != "" && typ != "text" {
				continue
			}
			if t := stringVal(m["text"]); t != "" {
				out = append(out, t)
			}
		}
		return out
	case map[string]any:
		if typ := stringVal(v["type"]); typ != "" && typ != "text" {
			return nil
		}
		if t := stringVal(v["text"]); t != "" {
			return []string{t}
		}
	}
	return nil
}

func extractCitationAnnotations(raw any) []llm.WebSearchSource {
	var out []llm.WebSearchSource
	var collect func(v any)
	collect = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, elem := range x {
				collect(elem)
			}
		case map[string]any:
			if ann, ok := x["annotations"]; ok && ann != nil {
				collect(ann)
			}
			if ann, ok := x["annotation"]; ok && ann != nil {
				collect(ann)
			}
			if src, ok := extractSingleCitation(x); ok {
				out = appendUniqueSources(out, []llm.WebSearchSource{src})
			}
		}
	}
	collect(raw)
	return out
}

func extractSingleCitation(m map[string]any) (llm.WebSearchSource, bool) {
	typ := stringVal(m["type"])
	if typ != "" && typ != "url_citation" && typ != "text_annotation_delta" {
		return llm.WebSearchSource{}, false
	}
	if nested, ok := m["url_citation"].(map[string]any); ok {
		src := llm.WebSearchSource{
			Title: stringVal(nested["title"]),
			URL:   stringVal(nested["url"]),
		}
		if src.Title != "" || src.URL != "" {
			return src, true
		}
	}
	if (typ == "" || typ == "text_annotation_delta") && m["url_citation"] == nil && m["url"] == nil && m["title"] == nil {
		return llm.WebSearchSource{}, false
	}
	src := llm.WebSearchSource{
		Title: stringVal(m["title"]),
		URL:   stringVal(m["url"]),
	}
	if src.Title == "" && src.URL == "" {
		return llm.WebSearchSource{}, false
	}
	return src, true
}

func normalizeGoogleSearchResultStep(step map[string]any) map[string]any {
	out := maps.Clone(step)
	if out == nil {
		out = map[string]any{}
	}
	var items []any
	switch v := out["result"].(type) {
	case map[string]any:
		items = []any{maps.Clone(v)}
	case []any:
		items = make([]any, 0, len(v))
		for _, elem := range v {
			if m, ok := elem.(map[string]any); ok {
				items = append(items, maps.Clone(m))
			} else {
				items = append(items, elem)
			}
		}
	}

	hasSuggestion := false
	for _, elem := range items {
		m, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		if _, has := m["search_suggestions"]; !has {
			if rc, ok := m["rendered_content"]; ok && rc != nil {
				m["search_suggestions"] = rc
			}
		}
		if ss, ok := m["search_suggestions"]; ok && ss != nil && ss != "" {
			hasSuggestion = true
		}
	}

	topSS := out["search_suggestions"]
	if topSS == nil {
		topSS = out["rendered_content"]
	}
	if topSS != nil && !hasSuggestion {
		switch x := topSS.(type) {
		case []any:
			for _, elem := range x {
				if m, ok := elem.(map[string]any); ok {
					items = append(items, maps.Clone(m))
				} else {
					items = append(items, map[string]any{"search_suggestions": elem})
				}
			}
		case map[string]any:
			m := maps.Clone(x)
			if _, has := m["search_suggestions"]; !has {
				if rc, ok := m["rendered_content"]; ok && rc != nil {
					m["search_suggestions"] = rc
				}
			}
			items = append(items, m)
		default:
			items = append(items, map[string]any{"search_suggestions": x})
		}
	}

	if len(items) > 0 {
		out["result"] = items
	}
	return out
}

func normalizeJSONNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalizeJSONNumbers(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalizeJSONNumbers(x[i])
		}
		return out
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		return v
	}
}

// rawArgsBodyDecode is the focused raw-args body decode. Tests replace it to
// count focused decodes per response body or streamed event body.
var rawArgsBodyDecode = json.Unmarshal

// classifyGeminiError reclassifies an ambiguous HTTP 400/422 using a Gemini
// gRPC status in the error response body.
func classifyGeminiError(httpStatus int, body []byte, retryAfter *time.Duration, httpErr error) error {
	if httpStatus != 400 && httpStatus != 422 {
		return httpErr
	}
	var errResp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error.Status == "" {
		return httpErr
	}
	var syntheticHTTP int
	switch errResp.Error.Status {
	case "RESOURCE_EXHAUSTED":
		syntheticHTTP = http.StatusTooManyRequests
	case "DEADLINE_EXCEEDED":
		syntheticHTTP = http.StatusRequestTimeout
	case "UNAVAILABLE", "INTERNAL":
		syntheticHTTP = http.StatusServiceUnavailable
	default:
		return httpErr
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	return llm.ErrorFromHTTPStatus("google", syntheticHTTP, errResp.Error.Message, raw, retryAfter)
}

func settleInteractionStatus(r *llm.Response, raw map[string]any, stepErrMap map[string]any, nonTextModalities []string) error {
	status := strings.TrimSpace(stringVal(raw["status"]))
	emptyTextAndCalls := len(r.ToolCalls()) == 0 && !hasTextPart(r.Message)

	emptyOutputErr := func() (err error, isStepErr bool) {
		if len(stepErrMap) > 0 {
			return classifyStepRPCStatus(stepErrMap), true
		}
		if len(nonTextModalities) > 0 {
			return &llm.ConfigurationError{
				ProviderInstance: r.Provider,
				Message:          fmt.Sprintf("google interactions returned unsupported non-text output modality (%s)", strings.Join(nonTextModalities, ", ")),
			}, false
		}
		return nil, false
	}

	switch status {
	case "completed":
		if emptyTextAndCalls {
			if err, _ := emptyOutputErr(); err != nil {
				return err
			}
		}
		if len(r.ToolCalls()) > 0 {
			r.Finish = llm.FinishReason{Reason: llm.FinishReasonToolCalls, Raw: "completed"}
		} else {
			r.Finish = llm.FinishReason{Reason: llm.FinishReasonStop, Raw: "completed"}
		}
		return nil
	case "requires_action":
		r.Finish = llm.FinishReason{Reason: llm.FinishReasonToolCalls, Raw: "requires_action"}
		return nil
	case "incomplete", "budget_exceeded", "requires_continuation":
		if emptyTextAndCalls {
			if err, isStepErr := emptyOutputErr(); err != nil {
				if !isStepErr {
					return err
				}
				return &interactionError{
					sentinel: ErrTerminalStatusStepFailure,
					provider: cmp.Or(r.Provider, "google"),
					message:  fmt.Sprintf("interaction %q ended with status %q after step failure", r.ID, status),
					raw:      map[string]any{"status": status, "interaction_id": r.ID, "raw": raw},
					cause:    err,
				}
			}
		}
		r.Finish = llm.FinishReason{Reason: llm.FinishReasonLength, Raw: status}
		return nil
	case "cancelled":
		r.Finish = llm.FinishReason{Reason: llm.FinishReasonOther, Raw: "cancelled"}
		return nil
	case "queued", "in_progress":
		return &interactionError{
			sentinel: ErrPendingInteractionStatus,
			provider: cmp.Or(r.Provider, "google"),
			message:  fmt.Sprintf("interaction %q has pending status %q", r.ID, status),
			raw:      map[string]any{"status": status, "interaction_id": r.ID, "raw": raw},
		}
	case "failed":
		var code, msg string
		if errs, ok := raw["errors"].([]any); ok && len(errs) > 0 {
			if first, ok := errs[0].(map[string]any); ok {
				code = stringVal(first["code"])
				msg = stringVal(first["message"])
			}
		}
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "safety", "recitation", "prohibited_content", "blocklist":
			r.Finish = llm.FinishReason{Reason: llm.FinishReasonContentFilter, Raw: code}
			return nil
		}
		errRaw := maps.Clone(raw)
		if errRaw == nil {
			errRaw = map[string]any{}
		}
		if _, hasErr := errRaw["error"]; !hasErr {
			errRaw["error"] = map[string]any{"code": code, "message": msg}
		}
		if msg == "" {
			msg = "interaction failed"
		}
		return classifyInteractionError(code, msg, errRaw)
	default:
		return &interactionError{
			sentinel: ErrUnexpectedInteractionStatus,
			provider: cmp.Or(r.Provider, "google"),
			message:  fmt.Sprintf("unexpected interaction status %q", status),
			raw:      raw,
		}
	}
}

func classifyStepRPCStatus(status map[string]any) error {
	code := llm.IntFromAny(status["code"])
	msg := stringVal(status["message"])
	var httpStatus int
	switch code {
	case 3:
		httpStatus = http.StatusBadRequest
	case 4:
		httpStatus = http.StatusRequestTimeout
	case 5:
		httpStatus = http.StatusNotFound
	case 7:
		httpStatus = http.StatusForbidden
	case 8:
		httpStatus = http.StatusTooManyRequests
	case 13, 14:
		httpStatus = http.StatusServiceUnavailable
	default:
		httpStatus = http.StatusInternalServerError
	}
	return llm.ErrorFromHTTPStatus("google", httpStatus, msg, status, nil)
}

func classifyInteractionError(code, msg string, raw any) error {
	var httpStatus int
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "not_found":
		httpStatus = http.StatusNotFound
	case "permission_denied":
		httpStatus = http.StatusForbidden
	case "unauthenticated":
		httpStatus = http.StatusUnauthorized
	case "gateway_timeout", "deadline_exceeded":
		httpStatus = http.StatusRequestTimeout
	case "resource_exhausted", "rate_limit_exceeded":
		httpStatus = http.StatusTooManyRequests
	case "internal", "unavailable":
		httpStatus = http.StatusServiceUnavailable
	case "invalid_argument", "failed_precondition":
		httpStatus = http.StatusBadRequest
	default:
		httpStatus = http.StatusInternalServerError
	}
	return llm.ErrorFromHTTPStatus("google", httpStatus, msg, raw, nil)
}

func parseInteractionUsage(u map[string]any) llm.Usage {
	baseIn := max(llm.IntFromAny(u["total_input_tokens"]), 0)
	toolIn := max(llm.IntFromAny(u["total_tool_use_tokens"]), 0)
	cachedRead := max(llm.IntFromAny(u["total_cached_tokens"]), 0)
	rawOut := max(llm.IntFromAny(u["total_output_tokens"]), 0)
	thought := max(llm.IntFromAny(u["total_thought_tokens"]), 0)
	total := max(llm.IntFromAny(u["total_tokens"]), 0)

	type candidate struct {
		cacheIncluded   bool
		toolAdditive    bool
		thoughtAdditive bool
	}
	candidates := [...]candidate{
		{true, false, true},
		{true, false, false},
		{true, true, true},
		{true, true, false},
		{false, false, true},
		{false, false, false},
		{false, true, true},
		{false, true, false},
	}

	var inputTokens, outputTokens, totalTokens int
	matched := false
	if total > 0 {
		for _, c := range candidates {
			var effectivePrompt int
			if c.toolAdditive {
				if toolIn <= 0 {
					continue
				}
				effectivePrompt = baseIn + toolIn
			} else {
				if toolIn > baseIn {
					continue
				}
				effectivePrompt = baseIn
			}

			var billedOut int
			if c.thoughtAdditive {
				if thought <= 0 {
					continue
				}
				billedOut = rawOut + thought
			} else {
				if thought > rawOut {
					continue
				}
				billedOut = rawOut
			}

			var uncachedIn int
			if c.cacheIncluded {
				if cachedRead > effectivePrompt {
					continue
				}
				uncachedIn = effectivePrompt - cachedRead
			} else {
				if cachedRead <= 0 {
					continue
				}
				uncachedIn = effectivePrompt
			}

			if uncachedIn+cachedRead+billedOut == total {
				inputTokens = uncachedIn
				outputTokens = billedOut
				totalTokens = total
				matched = true
				break
			}
		}
	}

	if !matched {
		effectivePrompt := baseIn
		if toolIn > baseIn {
			effectivePrompt = baseIn + toolIn
		}
		inputTokens = effectivePrompt
		if cachedRead <= effectivePrompt {
			inputTokens = effectivePrompt - cachedRead
		}
		outputTokens = rawOut
		if thought > rawOut || total == 0 || total >= inputTokens+cachedRead+rawOut+thought {
			outputTokens = rawOut + thought
		}
		totalTokens = inputTokens + cachedRead + outputTokens
	}

	usage := llm.Usage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
		Raw:          u,
	}
	if cachedRead > 0 {
		usage.CacheReadTokens = new(cachedRead)
	}
	if thought > 0 {
		usage.ReasoningTokens = new(thought)
	}
	return usage
}

type interactionStepRaw struct {
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func captureInteractionArgsRaw(body []byte) []json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	var focused struct {
		Steps []interactionStepRaw `json:"steps"`
	}
	if err := rawArgsBodyDecode(body, &focused); err != nil {
		return nil
	}
	return collectStepArgsRaw(focused.Steps)
}

func collectStepArgsRaw(steps []interactionStepRaw) []json.RawMessage {
	out := make([]json.RawMessage, len(steps))
	for i, s := range steps {
		out[i] = s.Arguments
	}
	return out
}

func resolveInteractionStepArgs(rawTok json.RawMessage, fallback any) (json.RawMessage, llm.Spelling) {
	rawTok = bytes.TrimSpace(rawTok)
	if len(rawTok) > 0 && !bytes.Equal(rawTok, []byte("null")) {
		if rawTok[0] == '"' {
			if unescaped, err := protocolhttp.RawStringContent(rawTok); err == nil {
				if len(bytes.TrimSpace(unescaped)) > 0 {
					return llm.NormalizeToolArguments(unescaped)
				}
				return json.RawMessage("{}"), ""
			}
		} else {
			return llm.NormalizeToolArguments(rawTok)
		}
	}
	if fallback != nil {
		switch v := fallback.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return llm.NormalizeToolArguments([]byte(v))
			}
		default:
			if b, err := json.Marshal(normalizeJSONNumbers(v)); err == nil && len(b) > 0 && !bytes.Equal(b, []byte("null")) {
				return llm.NormalizeToolArguments(b)
			}
		}
	}
	return json.RawMessage("{}"), ""
}

func stringVal(v any) string {
	s, _ := v.(string)
	return s
}

func hasTextPart(msg llm.Message) bool {
	for _, p := range msg.Content {
		if p.Kind == llm.ContentText && p.Text != "" {
			return true
		}
	}
	return false
}

func appendUniqueStrings(dst []string, src ...string) []string {
	for _, s := range src {
		if s == "" || slices.Contains(dst, s) {
			continue
		}
		dst = append(dst, s)
	}
	return dst
}

func appendUniqueSources(dst, src []llm.WebSearchSource) []llm.WebSearchSource {
	for _, s := range src {
		if s.Title == "" && s.URL == "" {
			continue
		}
		if slices.Contains(dst, s) {
			continue
		}
		dst = append(dst, s)
	}
	return dst
}

func extractSearchQueries(step map[string]any) []string {
	args, _ := step["arguments"].(map[string]any)
	rawQueries, _ := args["queries"].([]any)
	var out []string
	for _, q := range rawQueries {
		if s, ok := q.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

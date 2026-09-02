package responses

import (
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/requestutil"
	"primeradiant.com/evener/llm/registry"
)

const encryptedReasoning = "reasoning.encrypted_content"

// buildBody builds the Responses body for a shaped request (spec §8.2 step
// 1, §8.4, §9.5). Prunable control fields are emitted whenever the request
// carries them and pruned by the row's Fields; the lite shape, strict
// tools, image detail, and the reasoning object are cap decisions.
func buildBody(req llm.Request, res registry.Resolved, stream bool) (out map[string]any, err error) {
	// Every error this builder returns carries the instance, not the
	// protocol id or a vendor literal (spec §7.5: provider identity is
	// res.Instance). RewriteErrorProvider leaves errors with no provider
	// attribution alone.
	defer func() { err = llm.RewriteErrorProvider(err, res.Instance) }()
	caps := res.Caps
	lite := registry.BoolValue(caps.ResponsesLite)
	detail := "high"
	if d := registry.StringValue(caps.ImageDetail); d != "" {
		detail = d
	}
	instructions, inputItems, err := toResponsesInput(req.Messages, detail)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"instructions": instructions, "input": inputItems}
	if protocolhttp.ModelInBody(res) {
		body["model"] = res.WireID
	}
	if caps.Fields["store"] {
		// Today's privacy default: never let the endpoint store the turn
		// unless a continuation plan asked for it through req.Store.
		body["store"] = false
		if req.Store != nil {
			body["store"] = *req.Store
		}
	}
	var tools []map[string]any
	if len(req.Tools) > 0 {
		tools = toResponsesTools(req.Tools, registry.BoolValue(caps.StrictTools))
	}
	if req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch) {
		tools = append(tools, map[string]any{"type": "web_search"})
	}
	if lite {
		// codex-rs build_responses_request: tools ride as a developer
		// additional_tools item (always present, even empty), then the
		// instructions as a developer message, and the top-level fields go
		// empty.
		toolsAny := make([]any, 0, len(tools))
		for _, t := range tools {
			toolsAny = append(toolsAny, t)
		}
		prefix := []any{map[string]any{"type": "additional_tools", "role": "developer", "tools": toolsAny}}
		if instructions != "" {
			prefix = append(prefix, map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": instructions}}})
		}
		body["input"] = append(prefix, inputItems...)
		body["instructions"] = ""
	} else if len(tools) > 0 {
		body["tools"] = tools
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
		if err != nil {
			return nil, err
		}
		if caps.ToolChoiceForcing != nil && !*caps.ToolChoiceForcing && tc != "auto" && tc != "none" {
			tc = "auto"
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
	for key, value := range map[string]string{
		"prompt_cache_key": req.PromptCacheKey, "prompt_cache_retention": req.PromptCacheRetention,
		"previous_response_id": req.PreviousResponseID, "conversation": req.ConversationID,
		"service_tier": req.ServiceTier, "safety_identifier": req.SafetyIdentifier, "truncation": req.Truncation,
	} {
		if v := strings.TrimSpace(value); v != "" {
			body[key] = v
		}
	}
	if req.MaxToolCalls != nil {
		body["max_tool_calls"] = *req.MaxToolCalls
	}
	if req.Background != nil {
		body["background"] = *req.Background
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	reasoningOff := caps.Reasoning != nil && !*caps.Reasoning
	if reasoning := reasoningObject(req, caps); reasoning != nil {
		body["reasoning"] = reasoning
		// The include rides an {effort: none} object too. It is inert when the
		// model honors the off, and it is the only thing that keeps replay
		// working on a gateway that reasons anyway — which is not knowable in
		// advance, so we send it rather than guess, as tool_choice does.
		body["include"] = appendUnique(slices.Clone(req.Include), encryptedReasoning)
	} else if len(req.Include) > 0 {
		body["include"] = slices.Clone(req.Include)
	}
	if req.ResponseFormat != nil {
		structured := caps.StructuredOutput == nil || *caps.StructuredOutput
		if format := toResponsesResponseFormat(*req.ResponseFormat, structured); format != nil {
			text, _ := body["text"].(map[string]any)
			if text == nil {
				text = map[string]any{}
			}
			text["format"] = format
			body["text"] = text
		}
	}
	if stream {
		body["stream"] = true
	}
	if options, ok := req.ProviderOptions[registry.ProtocolOpenAIResponses].(map[string]any); ok {
		for k, v := range options {
			if reasoningOff && (k == "reasoning" || k == "include") {
				continue
			}
			body[k] = v
		}
	}
	reconcileOutputField(body, "max_output_tokens", req.MaxTokens, caps.MaxOutputTokens)
	return body, nil
}

// reasoningObject is spec §8.4 for openai-responses: effort when set and
// the row is effort-capable, summary from ReasoningSummary, and with
// ThinkingAlwaysOn and no effort the summary alone. nil means no reasoning
// object.
func reasoningObject(req llm.Request, caps registry.Caps) map[string]any {
	if caps.Reasoning != nil && !*caps.Reasoning {
		return nil
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort == "none" {
		// The user turned thinking off. A model whose ladder lists the off
		// level is told so; on any other the whole object goes, summary
		// included, so a mandatory-thinking row does not keep reasoning on
		// against the user's stated intent.
		if caps.EffortOffCapable() && caps.EffortCapable() {
			return map[string]any{"effort": *req.ReasoningEffort}
		}
		return nil
	}
	out := map[string]any{}
	if req.ReasoningEffort != nil && caps.EffortCapable() {
		out["effort"] = *req.ReasoningEffort
	}
	summary := registry.StringValue(caps.ReasoningSummary)
	if summary == "none" {
		summary = ""
	}
	if summary != "" && (len(out) > 0 || registry.BoolValue(caps.ThinkingAlwaysOn)) {
		out["summary"] = summary
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func reconcileOutputField(body map[string]any, field string, admitted, outputCap *int) {
	if ceiling := requestutil.MinPositiveInt(requestutil.PositiveInt(body[field]), requestutil.PositivePointerInt(admitted), requestutil.PositivePointerInt(outputCap)); ceiling > 0 {
		body[field] = ceiling
	}
}

package chatcompletions

import (
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// buildBody builds the Chat Completions body for a shaped request (spec
// §8.2 step 1, §8.4). Every structural decision reads a cap; nothing here
// branches on the model id. Prunable paths are emitted whenever the
// request carries them; the runner's prune removes the ones the row turns
// off.
func buildBody(req llm.Request, res registry.Resolved, stream bool) (out map[string]any, err error) {
	// Every error this builder returns carries the instance, not the
	// protocol id or a vendor literal (spec §7.5: provider identity is
	// res.Instance). RewriteErrorProvider leaves errors with no provider
	// attribution alone.
	defer func() { err = llm.RewriteErrorProvider(err, res.Instance) }()
	caps := res.Caps
	if !registry.BoolValue(caps.MultimodalToolResults) {
		req = requestWithoutToolResultImages(req)
	}
	body := map[string]any{}
	if protocolhttp.ModelInBody(res) {
		body["model"] = res.WireID
	}
	reasoningOff := caps.Reasoning != nil && !*caps.Reasoning
	options, _ := req.ProviderOptions[registry.ProtocolOpenAIChat].(map[string]any)
	// useReasoningDetails picks the assistant-replay shape (spec §8.4
	// Replay): a row can declare it (ReasoningField == "reasoning_details")
	// or a request can trigger it via a "reasoning" provider option. Only
	// the latter also means applyThinkingFormat's dialect control is
	// redundant — the option's own "reasoning" object reaches the wire
	// through the passthrough loop below, standing in for it. A row that
	// merely declares the replay shape still needs its dialect control
	// written, so optionCarriesReasoning tracks that narrower condition
	// separately from useReasoningDetails.
	useReasoningDetails := registry.StringValue(caps.ReasoningField) == "reasoning_details"
	optionCarriesReasoning := false
	if _, ok := options["reasoning"]; ok && !reasoningOff {
		optionCarriesReasoning, useReasoningDetails = true, true
	}
	msgs, err := toChatMessages(req.Messages, caps, useReasoningDetails)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		tools := openaichat.ToChatTools(req.Tools)
		if registry.BoolValue(caps.StrictTools) {
			for _, t := range tools {
				if fn, ok := t["function"].(map[string]any); ok {
					fn["strict"] = true
					if params, ok := fn["parameters"].(map[string]any); ok {
						fn["parameters"] = openaichat.StrictifyJSONSchema(params)
					}
				}
			}
		}
		body["tools"] = tools
		if registry.BoolValue(caps.ToolStream) {
			body["tool_stream"] = true
		}
	}
	if req.ToolChoice != nil {
		tc, err := toChatToolChoice(*req.ToolChoice)
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
		body[maxTokensField(caps)] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		format := openaichat.ToChatResponseFormat(*req.ResponseFormat)
		if caps.StructuredOutput != nil && !*caps.StructuredOutput && format["type"] == "json_schema" {
			format = map[string]any{"type": "json_object"}
		}
		body["response_format"] = format
	}
	if !optionCarriesReasoning {
		applyThinkingFormat(body, req, caps)
	}
	if caps.Fields["store"] {
		body["store"] = false
	}
	if key := promptCacheKey(req, caps); key != "" {
		body["prompt_cache_key"] = key
	}
	if retention := strings.TrimSpace(req.PromptCacheRetention); retention != "" {
		body["prompt_cache_retention"] = retention
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	for k, v := range options {
		if reasoningOff && isReasoningControlKey(k) {
			continue
		}
		body[k] = v
	}
	if registry.StringValue(caps.CacheControl) == "anthropic" {
		anthropicCacheControl(body, registry.StringValue(caps.CacheTTL))
	}
	return body, nil
}

// maxTokensField is the spelling Caps.MaxTokensField selects, max_tokens by
// default (the compatible-server default; the openai overlay pins
// max_completion_tokens).
func maxTokensField(caps registry.Caps) string {
	if f := registry.StringValue(caps.MaxTokensField); f != "" {
		return f
	}
	return "max_tokens"
}

// promptCacheKey is the request's key, else the session-derived key when
// the row sends prompt_cache_key at all.
func promptCacheKey(req llm.Request, caps registry.Caps) string {
	if k := strings.TrimSpace(req.PromptCacheKey); k != "" {
		return k
	}
	if caps.Fields["prompt_cache_key"] {
		if sid := strings.TrimSpace(req.SessionID); sid != "" {
			return "evener-session-" + sid
		}
	}
	return ""
}

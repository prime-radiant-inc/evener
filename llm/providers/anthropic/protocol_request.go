package anthropic

import (
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// buildProtocolBody builds the Messages body from the shaped request and
// the row's caps (spec §8.2, §8.4): ThinkingShape picks one of the three
// thinking bodies, ThinkingDisplay and ThinkingAlwaysOn refine it, and no
// model-id branch remains.
func buildProtocolBody(req llm.Request, res registry.Resolved) (out map[string]any, err error) {
	return buildProtocolBodyForOperation(req, res, true)
}

func buildProtocolBodyForOperation(req llm.Request, res registry.Resolved, enforceCompletionContract bool) (out map[string]any, err error) {
	// Every error this builder returns carries the instance, not the
	// protocol id or a vendor literal (spec §7.5: provider identity is
	// res.Instance). RewriteErrorProvider leaves errors with no provider
	// attribution alone.
	defer func() { err = llm.RewriteErrorProvider(err, res.Instance) }()
	caps := res.Caps
	system, messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	system, err = applyAnthropicResponseFormat(system, req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	maxTokens := fallbackMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	ttl := registry.StringValue(caps.CacheTTL)
	body := map[string]any{
		"max_tokens":    maxTokens,
		"messages":      messages,
		"cache_control": cacheMarker(ttl),
	}
	if protocolhttp.ModelInBody(res) {
		body["model"] = res.WireID
	}
	if strings.TrimSpace(system) != "" {
		body["system"] = []map[string]any{{"type": "text", "text": system, "cache_control": cacheMarker(ttl)}}
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
	if tier := strings.TrimSpace(req.ServiceTier); tier != "" {
		body["service_tier"] = tier
	}
	if uid := strings.TrimSpace(req.Metadata["user_id"]); uid != "" {
		body["metadata"] = map[string]any{"user_id": uid}
	}
	webSearch := req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch)
	if err := applyAnthropicTools(body, req, webSearch); err != nil {
		return nil, err
	}
	// applyAnthropicTools already marked the last tool for caching, but with
	// no ttl; re-mark it so the row's CacheTTL rides along. It writes tools
	// only when there are some, so the assertion is the whole guard.
	if tools, ok := body["tools"].([]map[string]any); ok && len(tools) > 0 && ttl != "" {
		tools[len(tools)-1]["cache_control"] = cacheMarker(ttl)
	}
	applyThinkingShape(body, req, caps)
	if ov, ok := req.ProviderOptions[registry.ProtocolAnthropic].(map[string]any); ok {
		for k, v := range ov {
			if k == "beta_headers" {
				continue
			}
			body[k] = v
		}
	}
	normalizeThinkingToolChoice(body)
	if enforceCompletionContract {
		if err := reconcileThinkingContract(body, req, res); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// applyThinkingShape writes the thinking body the row's ThinkingShape selects:
// adaptive → {type: adaptive} plus display, sent when ThinkingAlwaysOn or
// an effort is set, plus output_config.effort only for a caller effort;
// budget → {type: enabled, budget_tokens} only for an effort;
// budget+effort → both. An explicit off sends no thinking object at all,
// always-on adaptive rows included: no Claude row lists an off effort level,
// so there is no value that says "off" here, and keeping the always-on body
// would switch thinking on against the user's stated intent. An unset shape
// sends nothing.
func applyThinkingShape(body map[string]any, req llm.Request, caps registry.Caps) {
	if req.ReasoningEffort != nil && *req.ReasoningEffort == "none" {
		return
	}
	effort := ""
	if req.ReasoningEffort != nil {
		effort = *req.ReasoningEffort
	}
	switch registry.StringValue(caps.ThinkingShape) {
	case "adaptive":
		if !registry.BoolValue(caps.ThinkingAlwaysOn) && effort == "" {
			return
		}
		thinking := map[string]any{"type": "adaptive"}
		if display := registry.StringValue(caps.ThinkingDisplay); display != "" {
			thinking["display"] = display
		}
		body["thinking"] = thinking
		if effort != "" {
			body["output_config"] = map[string]any{"effort": effort}
		}
	case "budget", "budget+effort":
		if effort == "" {
			return
		}
		budget := llm.ReasoningBudget(effort)
		if budget > 0 {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
		if registry.StringValue(caps.ThinkingShape) == "budget+effort" {
			body["output_config"] = map[string]any{"effort": effort}
		}
	}
}

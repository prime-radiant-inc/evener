package google

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/requestutil"
	"primeradiant.com/evener/llm/registry"
)

type interactionBodyPolicy struct {
	stream                    bool
	wireModel                 string
	combinedTools             bool
	anchorID                  string
	storeOmitted              bool
	effectiveStore            bool
	summariesDisabled         bool
	hasUnsignedNonNativeCalls bool
}

var protectedTransportBodyRoots = map[string]bool{
	"input":                   true,
	"model":                   true,
	"response_format":         true,
	"agent":                   true,
	"previous_interaction_id": true,
	"store":                   true,
	"continuation_token":      true,
	"cached_content":          true,
	"background":              true,
}

var retiredGenerateContentKeys = map[string]string{
	"generationConfig":  `replace with "generation_config"`,
	"safetySettings":    `replace with "safety_settings"`,
	"toolConfig":        `replace with "generation_config.tool_choice"`,
	"cachedContent":     `remove "cachedContent" (not supported on Interactions)`,
	"contents":          `remove "contents" (owned by Interactions "input")`,
	"systemInstruction": `remove "systemInstruction" (owned by Interactions "system_instruction")`,
}

func validateGoogleTransportAndBodyConstants(res registry.Resolved) error {
	for _, ep := range []string{res.Transport.Endpoint, res.Transport.StreamEndpoint} {
		if strings.Contains(ep, ":generateContent") || strings.Contains(ep, ":streamGenerateContent") {
			return &llm.ConfigurationError{
				ProviderInstance: res.Instance,
				Message:          fmt.Sprintf("instance %q: legacy generateContent/streamGenerateContent endpoint override %q is incompatible with Google Interactions API; remove endpoint/stream_endpoint overrides", res.Instance, ep),
			}
		}
	}
	if isVertexTransport(res) && !registry.VertexSupportsGeminiModel(res.WireID) {
		return &llm.ConfigurationError{
			ProviderInstance: res.Instance,
			Message:          fmt.Sprintf("instance %q: model %q is not supported on Vertex /interactions; switch the session to a Vertex Interactions-supported model such as gemini-3.8-flash", res.Instance, res.WireID),
		}
	}
	for k := range res.Transport.Body {
		root, _, hasDot := strings.Cut(k, ".")
		if protectedTransportBodyRoots[root] || (root == "tools" && hasDot) {
			return &llm.ConfigurationError{
				ProviderInstance: res.Instance,
				Message:          fmt.Sprintf("instance %q: transport.body.%s cannot override Interactions-owned field %q", res.Instance, k, root),
			}
		}
		if hint, retired := retiredGenerateContentKeys[root]; retired {
			return &llm.ConfigurationError{
				ProviderInstance: res.Instance,
				Message:          fmt.Sprintf("instance %q: transport.body.%s uses retired generateContent key %q (%s)", res.Instance, k, root, hint),
			}
		}
	}
	return nil
}

func buildBodyWithPolicy(req llm.Request, res registry.Resolved, stream, fallbackUnsignedCalls bool) (map[string]any, interactionBodyPolicy, error) {
	var policy interactionBodyPolicy
	if err := validateGoogleTransportAndBodyConstants(res); err != nil {
		return nil, policy, err
	}
	caps := res.Caps
	if caps.Reasoning != nil && !*caps.Reasoning {
		req.ReasoningEffort = nil
	}

	var options map[string]any
	if rawOpts, exists := req.ProviderOptions[registry.ProtocolGoogle]; exists && rawOpts != nil {
		m, ok := rawOpts.(map[string]any)
		if !ok {
			return nil, policy, &llm.ConfigurationError{Message: "provider_options.google must be an object"}
		}
		options = m
	}

	for k := range options {
		switch k {
		case "input", "model", "response_format":
			return nil, policy, &llm.ConfigurationError{Message: fmt.Sprintf("provider_options.google.%s cannot override request-owned field", k)}
		}
	}
	if err := validateInteractionBodyGuards(options, "", false); err != nil {
		return nil, policy, err
	}
	if req.Background != nil && *req.Background {
		return nil, policy, &llm.ConfigurationError{Message: "google interactions foreground calls do not support background=true"}
	}
	serviceTier := strings.TrimSpace(req.ServiceTier)
	if rawTier, exists := options["service_tier"]; exists && rawTier != nil {
		if s, ok := rawTier.(string); ok && serviceTier == "" {
			serviceTier = strings.TrimSpace(s)
		}
	}
	if strings.EqualFold(serviceTier, "deferred") {
		return nil, policy, &llm.ConfigurationError{Message: `google interactions foreground calls do not support service_tier="deferred"`}
	}

	// Anchor and store resolution.
	anchorID := strings.TrimSpace(req.PreviousResponseID)
	if rawPrev, exists := options["previous_interaction_id"]; exists && rawPrev != nil {
		s, ok := rawPrev.(string)
		if !ok {
			return nil, policy, &llm.ConfigurationError{Message: "provider_options.google.previous_interaction_id must be a string"}
		}
		if anchorID == "" {
			anchorID = strings.TrimSpace(s)
		}
	}
	var explicitStore *bool
	if req.Store != nil {
		v := *req.Store
		explicitStore = &v
	} else if rawStore, exists := options["store"]; exists && rawStore != nil {
		b, ok := rawStore.(bool)
		if !ok {
			return nil, policy, &llm.ConfigurationError{Message: "provider_options.google.store must be a boolean"}
		}
		explicitStore = &b
	}

	storeSendable := requestutil.WireFieldEnabled(caps, "store", res.Protocol)
	if anchorID == "" {
		if !storeSendable {
			if explicitStore != nil {
				return nil, policy, &llm.ConfigurationError{Message: "google provider disables sending store; explicit store override is not permitted"}
			}
			policy.storeOmitted = true
		} else {
			policy.storeOmitted = false
			policy.effectiveStore = false
			if explicitStore != nil {
				policy.effectiveStore = *explicitStore
			}
		}
	} else {
		if !caps.Fields["previous_interaction_id"] || !storeSendable {
			return nil, policy, &llm.ConfigurationError{Message: "google provider row does not enable previous_interaction_id and store"}
		}
		if explicitStore != nil && !*explicitStore {
			return nil, policy, &llm.ConfigurationError{Message: "google previous_interaction_id requires store=true"}
		}
		policy.anchorID = anchorID
		policy.storeOmitted = false
		policy.effectiveStore = true
	}

	policy.stream = stream
	policy.wireModel = res.WireID

	system, steps, hasUnsigned, err := toInteractionInput(res, req.Messages, registry.BoolValue(caps.MultimodalToolResults), fallbackUnsignedCalls)
	if err != nil {
		return nil, policy, err
	}
	policy.hasUnsignedNonNativeCalls = hasUnsigned

	body := map[string]any{
		"model": res.WireID,
		"input": steps,
	}
	if stream {
		body["stream"] = true
	}
	if !policy.storeOmitted {
		body["store"] = policy.effectiveStore
	}
	if policy.anchorID != "" {
		body["previous_interaction_id"] = policy.anchorID
	}
	if system != "" {
		body["system_instruction"] = system
	}
	if serviceTier != "" {
		body["service_tier"] = serviceTier
	}

	// Labels from req.Metadata (silent invalid-key filter) + strict ProviderOptions["google"]["labels"].
	labels := filterGoogleLabelsFromMetadata(req.Metadata)
	if rawLabels, exists := options["labels"]; exists && rawLabels != nil {
		validated, err := validateAndCloneGoogleLabels(rawLabels)
		if err != nil {
			return nil, policy, err
		}
		if len(validated) > 0 {
			if labels == nil {
				labels = validated
			} else {
				maps.Copy(labels, validated)
				if len(labels) > 64 {
					return nil, policy, &llm.ConfigurationError{Message: "google labels exceed maximum of 64 entries"}
				}
			}
		}
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}

	// Tools and google_search.
	toolsSupported := caps.Tools == nil || *caps.Tools
	if len(req.Tools) > 0 && !toolsSupported {
		return nil, policy, &llm.ConfigurationError{Message: fmt.Sprintf("google model %q does not support function tools", res.WireID)}
	}
	var toolEntries []map[string]any
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			if openaichat.CyclicSchema(t.Parameters) {
				return nil, policy, &llm.ConfigurationError{Message: fmt.Sprintf("tool %q schema contains a cycle", t.Name), Cause: openaichat.ErrCyclicSchema}
			}
		}
		toolEntries = append(toolEntries, toInteractionFunctionDecls(req.Tools)...)
	}
	webSearch := req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch) && !registry.IsVertexGeminiOmni(res.Transport.Preset, res.WireID)
	if webSearch {
		if len(req.Tools) == 0 {
			toolEntries = append(toolEntries, map[string]any{"type": "google_search"})
		} else if toolsSupported && registry.BoolValue(caps.CombinedSearchAndFunctions) {
			if !policy.effectiveStore || !storeSendable || !caps.Fields["previous_interaction_id"] || !requestutil.WireFieldEnabled(caps, "generation_config.tool_choice", res.Protocol) {
				return nil, policy, &llm.ConfigurationError{Message: "google combined google_search and function tools require store=true, previous_interaction_id, and generation_config.tool_choice enabled"}
			}
			toolEntries = append(toolEntries, map[string]any{"type": "google_search"})
			policy.combinedTools = true
		}
	}
	if len(toolEntries) > 0 {
		body["tools"] = toolEntries
	}

	// generation_config.
	genCfg := map[string]any{}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genCfg["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		genCfg["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		genCfg["stop_sequences"] = append([]string(nil), req.StopSequences...)
	}
	if !caps.ReasoningDisabled() {
		applyInteractionThinkingConfig(genCfg, req.ReasoningEffort, caps)
	}
	policy.summariesDisabled = caps.ReasoningDisabled() || genCfg["thinking_summaries"] == "none"

	autoMode := "auto"
	if policy.combinedTools {
		autoMode = "validated"
	}
	if req.ToolChoice != nil {
		mode := strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode))
		switch mode {
		case "", "auto":
			genCfg["tool_choice"] = autoMode
		case "none":
			genCfg["tool_choice"] = "none"
		case "required":
			if caps.ToolChoiceForcing != nil && !*caps.ToolChoiceForcing {
				genCfg["tool_choice"] = autoMode
			} else {
				genCfg["tool_choice"] = "any"
			}
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				return nil, policy, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			genCfg["tool_choice"] = map[string]any{
				"allowed_tools": map[string]any{
					"mode":  "any",
					"tools": []string{req.ToolChoice.Name},
				},
			}
		default:
			return nil, policy, llm.NewUnsupportedToolChoiceError("google", req.ToolChoice.Mode)
		}
	} else if policy.combinedTools {
		genCfg["tool_choice"] = "validated"
	}

	// response_format (top-level).
	if req.ResponseFormat != nil {
		switch strings.ToLower(strings.TrimSpace(req.ResponseFormat.Type)) {
		case "json":
			body["response_format"] = map[string]any{
				"type":      "text",
				"mime_type": "application/json",
			}
		case "json_schema":
			rf := map[string]any{
				"type":      "text",
				"mime_type": "application/json",
			}
			if req.ResponseFormat.JSONSchema != nil {
				if openaichat.CyclicSchema(req.ResponseFormat.JSONSchema) {
					return nil, policy, &llm.ConfigurationError{Message: "response_format json_schema contains a cycle", Cause: openaichat.ErrCyclicSchema}
				}
				rf["schema"] = sanitizeInteractionResponseSchema(req.ResponseFormat.JSONSchema)
			}
			body["response_format"] = rf
		}
	}

	active := map[cloneContainerKey]bool{}
	if options != nil {
		if key, ok := cloneContainerIdentity(options); ok {
			active[key] = true
		}
	}

	// Merge ProviderOptions["google"] with deep-cloned copies.
	for k, v := range options {
		switch k {
		case "generation_config", "labels", "agent", "stream", "store", "previous_interaction_id", "continuation_token", "cached_content", "background", "service_tier":
			continue
		default:
			cloned, err := cloneJSONValueWithPath(v, active, res.Instance)
			if err != nil {
				return nil, policy, err
			}
			body[k] = cloned
		}
	}
	if rawGen, exists := options["generation_config"]; exists {
		overlaid, ok := rawGen.(map[string]any)
		if !ok || overlaid == nil {
			return nil, policy, &llm.ConfigurationError{Message: "provider_options.google.generation_config must be an object"}
		}
		if key, ok := cloneContainerIdentity(overlaid); ok {
			active[key] = true
		}
		for k, v := range overlaid {
			cloned, err := cloneJSONValueWithPath(v, active, res.Instance)
			if err != nil {
				return nil, policy, err
			}
			genCfg[k] = cloned
		}
	}
	if len(genCfg) > 0 {
		body["generation_config"] = genCfg
	}
	if err := reconcileOutputField(body, req.MaxTokens, caps.MaxOutputTokens, caps, res.Protocol); err != nil {
		return nil, policy, err
	}
	return body, policy, nil
}

func validateInteractionBodyGuards(m map[string]any, instance string, stripEmpty bool) error {
	for _, key := range []string{"agent", "continuation_token", "cached_content"} {
		if v, exists := m[key]; exists && v != nil {
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) != "" {
				return &llm.ConfigurationError{ProviderInstance: instance, Message: "google interactions does not support " + key}
			}
			if stripEmpty {
				delete(m, key)
			}
		}
	}
	if v, exists := m["background"]; exists && v != nil {
		b, ok := v.(bool)
		if !ok || b {
			return &llm.ConfigurationError{ProviderInstance: instance, Message: "google interactions foreground calls do not support background=true"}
		}
		if stripEmpty {
			delete(m, "background")
		}
	}
	if v, exists := m["service_tier"]; exists && v != nil {
		s, ok := v.(string)
		if !ok || strings.EqualFold(strings.TrimSpace(s), "deferred") {
			return &llm.ConfigurationError{ProviderInstance: instance, Message: `google interactions foreground calls do not support service_tier="deferred"`}
		}
	}
	for retired, hint := range retiredGenerateContentKeys {
		if _, exists := m[retired]; exists {
			return &llm.ConfigurationError{ProviderInstance: instance, Message: fmt.Sprintf("retired generateContent body key %q (%s)", retired, hint)}
		}
	}
	return nil
}

func applyInteractionThinkingConfig(genCfg map[string]any, reqEffort *string, caps registry.Caps) {
	if reqEffort != nil && llm.NormalizeReasoningEffort(*reqEffort) == llm.ReasoningEffortNone {
		genCfg["thinking_summaries"] = "none"
		return
	}
	summaryMode := "auto"
	if caps.ReasoningSummary != nil {
		s := strings.ToLower(strings.TrimSpace(*caps.ReasoningSummary))
		if s == "auto" || s == "none" {
			summaryMode = s
		}
	}
	genCfg["thinking_summaries"] = summaryMode

	if reqEffort == nil || registry.StringValue(caps.ThinkingShape) != "level" || registry.BoolValue(caps.EffortParameterUnsupported) {
		return
	}
	effort := llm.NormalizeReasoningEffort(*reqEffort)
	if effort == "" || effort == llm.ReasoningEffortNone {
		return
	}
	level := effort
	if len(caps.EffortValues) > 0 {
		level = llm.VouchedEffort(effort, caps.EffortValues)
	} else if level == "max" {
		level = "high"
	}
	wire := strings.ToLower(level)
	switch wire {
	case "minimal", "low", "medium", "high", "xhigh":
		genCfg["thinking_level"] = wire
	}
}

func reconcileOutputField(body map[string]any, admitted, outputCap *int, caps registry.Caps, protocol string) error {
	if !requestutil.WireFieldEnabled(caps, "generation_config.max_output_tokens", protocol) {
		if genCfg, ok := body["generation_config"].(map[string]any); ok {
			delete(genCfg, "max_output_tokens")
			if len(genCfg) == 0 {
				delete(body, "generation_config")
			}
		}
		return nil
	}
	if body["generation_config"] != nil {
		if _, ok := body["generation_config"].(map[string]any); !ok {
			return &llm.ConfigurationError{Message: "google generation_config must be an object"}
		}
	}
	ceiling := requestutil.MinPositiveInt(requestutil.PositivePointerInt(admitted), requestutil.PositivePointerInt(outputCap))
	if ceiling == 0 {
		return nil
	}
	genCfg, _ := body["generation_config"].(map[string]any)
	if genCfg == nil {
		genCfg = map[string]any{}
		body["generation_config"] = genCfg
	}
	genCfg["max_output_tokens"] = requestutil.MinPositiveInt(requestutil.PositiveInt(genCfg["max_output_tokens"]), ceiling)
	return nil
}

type cloneContainerKey struct {
	addr   uintptr
	length int
}

func cloneContainerIdentity(v any) (cloneContainerKey, bool) {
	if v == nil {
		return cloneContainerKey{}, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return cloneContainerKey{}, false
		}
		return cloneContainerKey{addr: rv.Pointer(), length: -1}, true
	case reflect.Slice:
		if rv.Len() == 0 {
			return cloneContainerKey{}, false
		}
		return cloneContainerKey{addr: rv.Pointer(), length: rv.Len()}, true
	default:
		return cloneContainerKey{}, false
	}
}

func cyclicOptionsError(instance string) error {
	return &llm.ConfigurationError{
		ProviderInstance: cmp.Or(strings.TrimSpace(instance), "google"),
		Message:          "provider_options.google contains a cyclic reference",
		Cause:            openaichat.ErrCyclicSchema,
	}
}

func cloneJSONValueWithPath(v any, active map[cloneContainerKey]bool, instance string) (any, error) {
	key, tracked := cloneContainerIdentity(v)
	if tracked {
		if active[key] {
			return nil, cyclicOptionsError(instance)
		}
		active[key] = true
		defer delete(active, key)
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			cloned, err := cloneJSONValueWithPath(vv, active, instance)
			if err != nil {
				return nil, err
			}
			out[k] = cloned
		}
		return out, nil
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = vv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			cloned, err := cloneJSONValueWithPath(x[i], active, instance)
			if err != nil {
				return nil, err
			}
			out[i] = cloned
		}
		return out, nil
	default:
		if v == nil {
			return nil, nil
		}
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Map:
			if rv.IsNil() {
				return map[string]any(nil), nil
			}
			if rv.Type().Key().Kind() == reflect.String {
				out := make(map[string]any, rv.Len())
				for iter := rv.MapRange(); iter.Next(); {
					cloned, err := cloneJSONValueWithPath(iter.Value().Interface(), active, instance)
					if err != nil {
						return nil, err
					}
					out[iter.Key().String()] = cloned
				}
				return out, nil
			}
			out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
			for iter := rv.MapRange(); iter.Next(); {
				cloned, err := cloneJSONValueWithPath(iter.Value().Interface(), active, instance)
				if err != nil {
					return nil, err
				}
				out.SetMapIndex(iter.Key(), reflect.ValueOf(cloned))
			}
			return out.Interface(), nil
		case reflect.Slice:
			if rv.Type().Elem().Kind() == reflect.Uint8 {
				if rv.IsNil() {
					return v, nil
				}
				cp := make([]byte, rv.Len())
				reflect.Copy(reflect.ValueOf(cp), rv)
				return cp, nil
			}
			out := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				cloned, err := cloneJSONValueWithPath(rv.Index(i).Interface(), active, instance)
				if err != nil {
					return nil, err
				}
				out[i] = cloned
			}
			return out, nil
		default:
			return v, nil
		}
	}
}

func isGoogleLabelLetter(r rune) bool {
	return unicode.IsLetter(r) && !unicode.IsUpper(r)
}

func isGoogleLabelRune(r rune) bool {
	return isGoogleLabelLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-'
}

func isValidGoogleLabelKey(k string) bool {
	n := utf8.RuneCountInString(k)
	if n < 1 || n > 63 {
		return false
	}
	first := true
	for _, r := range k {
		if first {
			first = false
			if !isGoogleLabelLetter(r) {
				return false
			}
			continue
		}
		if !isGoogleLabelRune(r) {
			return false
		}
	}
	return true
}

func isValidGoogleLabelValue(v string) bool {
	if utf8.RuneCountInString(v) > 63 {
		return false
	}
	for _, r := range v {
		if !isGoogleLabelRune(r) {
			return false
		}
	}
	return true
}

func filterGoogleLabelsFromMetadata(meta map[string]string) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	var out map[string]any
	for k, v := range meta {
		if !isValidGoogleLabelKey(k) || !isValidGoogleLabelValue(v) {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(meta))
		}
		if len(out) >= 64 {
			break
		}
		out[k] = v
	}
	return out
}

func toInteractionFunctionDecls(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			// Gemini's function-parameter Schema is a restricted subset;
			// wireFunctionParameters strips JSON-schema-only fields (e.g.,
			// additionalProperties) so requests and local pricing agree.
			"parameters": wireFunctionParameters(t),
		})
	}
	return out
}

func validateAndCloneGoogleLabels(raw any) (map[string]any, error) {
	out := map[string]any{}
	add := func(k, s string) error {
		if !isValidGoogleLabelKey(k) {
			return &llm.ConfigurationError{Message: fmt.Sprintf("google label key %q violates Google label rules", k)}
		}
		if !isValidGoogleLabelValue(s) {
			return &llm.ConfigurationError{Message: fmt.Sprintf("google label %q value %q violates Google label rules", k, s)}
		}
		out[k] = s
		return nil
	}
	switch m := raw.(type) {
	case map[string]any:
		if len(m) > 64 {
			return nil, &llm.ConfigurationError{Message: "google labels exceed maximum of 64 entries"}
		}
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return nil, &llm.ConfigurationError{Message: fmt.Sprintf("google label %q value must be a string", k)}
			}
			if err := add(k, s); err != nil {
				return nil, err
			}
		}
	case map[string]string:
		if len(m) > 64 {
			return nil, &llm.ConfigurationError{Message: "google labels exceed maximum of 64 entries"}
		}
		for k, s := range m {
			if err := add(k, s); err != nil {
				return nil, err
			}
		}
	default:
		return nil, &llm.ConfigurationError{Message: "google labels must be a string map"}
	}
	return out, nil
}

var geminiSchemaMetaKeys = map[string]bool{
	"$schema":        true,
	"$id":            true,
	"$comment":       true,
	"$anchor":        true,
	"$dynamicAnchor": true,
	"$vocabulary":    true,
}

func sanitizeInteractionResponseSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			if geminiSchemaMetaKeys[k] {
				continue
			}
			if k == "properties" {
				if pm, ok := vv.(map[string]any); ok {
					cleaned := make(map[string]any, len(pm))
					for name, sub := range pm {
						cleaned[name] = sanitizeInteractionResponseSchema(sub)
					}
					out[k] = cleaned
					continue
				}
			}
			out[k] = sanitizeInteractionResponseSchema(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = sanitizeInteractionResponseSchema(x[i])
		}
		return out
	default:
		return v
	}
}

// sanitizeGeminiSchema rewrites a JSON Schema into the restricted subset Gemini
// accepts. The pricing pass and the dispatch pass both call it and must agree
// byte for byte, so its output may not depend on map iteration order.
func sanitizeGeminiSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		// Collapse a nullable type union before the rebuild, so each output key
		// has one writer whatever order the map yields its keys; the schema's
		// own nullable field wins over the synthesized one.
		typ, _, union := geminiNullableType(x["type"])
		if union {
			out["type"] = typ
			if _, hasNullable := x["nullable"]; !hasNullable {
				out["nullable"] = true
			}
		}
		for k, vv := range x {
			if k == "additionalProperties" || geminiSchemaMetaKeys[k] {
				continue
			}
			if k == "properties" {
				out[k] = sanitizeGeminiNamedSchemas(vv)
				continue
			}
			if k == "type" && union {
				continue
			}
			if k == "enum" {
				if union {
					out[k] = geminiEnumWithoutNull(vv)
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

func sanitizeGeminiNamedSchemas(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return sanitizeGeminiSchema(v)
	}
	out := make(map[string]any, len(m))
	for name, schema := range m {
		out[name] = sanitizeGeminiSchema(schema)
	}
	return out
}

func geminiEnumWithoutNull(v any) any {
	values, ok := v.([]any)
	if !ok {
		return sanitizeGeminiSchema(v)
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, sanitizeGeminiSchema(value))
		}
	}
	return out
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

func interactionImageItem(p llm.ContentPart) (map[string]any, error) {
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
		b, mt, err = googleInlineImageBytes(b, mt)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type":      "image",
			"mime_type": mt,
			"data":      llm.CachedBase64(b),
		}, nil
	}
	if u != "" {
		if mt == "" {
			mt = "image/png"
		}
		return map[string]any{
			"type":      "image",
			"mime_type": mt,
			"uri":       u,
		}, nil
	}
	return nil, nil
}

func googleInlineImageMediaType(v string) string {
	switch mt := llm.ImageBlockMediaType(v); mt {
	case "image/png", "image/jpeg", "image/webp":
		return mt
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "heic", "image/heic":
		return "image/heic"
	case "heif", "image/heif":
		return "image/heif"
	default:
		return ""
	}
}

func googleInlineImageBytes(data []byte, claimedMediaType string) ([]byte, string, error) {
	return llm.NormalizeImageForWire(
		data,
		claimedMediaType,
		googleInlineImageMediaType,
		fmt.Sprintf("google inline image %q is not a decodable raster", claimedMediaType),
	)
}

func interactionDocumentItem(doc *llm.DocumentData) map[string]any {
	if doc == nil {
		return nil
	}
	mt := strings.TrimSpace(doc.MediaType)
	if mt == "" {
		mt = "application/pdf"
	}
	if len(doc.Data) > 0 {
		return map[string]any{
			"type":      "document",
			"mime_type": mt,
			"data":      base64.StdEncoding.EncodeToString(doc.Data),
		}
	}
	if u := strings.TrimSpace(doc.URL); u != "" {
		return map[string]any{
			"type":      "document",
			"mime_type": mt,
			"uri":       u,
		}
	}
	return nil
}

func toInteractionInput(res registry.Resolved, msgs []llm.Message, multimodalToolResults, fallbackUnsignedCalls bool) (system string, steps []any, hasUnsignedNonNative bool, _ error) {
	var sysParts []string
	callIDToName := map[string]string{}
	var syntheticCallIDs []string
	unsignedCallIDs := map[string]bool{}

	var pendingUser []any
	pendingModelStep := -1
	var pendingModelContent []any

	flushModelOutput := func() {
		if len(pendingModelContent) > 0 {
			steps = append(steps, map[string]any{
				"type":    "model_output",
				"content": pendingModelContent,
			})
			pendingModelContent = nil
			pendingModelStep = -1
		}
	}
	flushUser := func() {
		if len(pendingUser) > 0 {
			steps = append(steps, map[string]any{
				"type":    "user_input",
				"content": pendingUser,
			})
			pendingUser = nil
		}
	}
	appendNonUserStep := func(step map[string]any) {
		flushModelOutput()
		flushUser()
		steps = append(steps, step)
	}
	appendModelContent := func(googleStep int, item map[string]any) {
		flushUser()
		if len(pendingModelContent) > 0 && pendingModelStep != googleStep {
			flushModelOutput()
		}
		pendingModelStep = googleStep
		pendingModelContent = append(pendingModelContent, item)
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := m.Text(); t != "" {
				sysParts = append(sysParts, t)
			}
		case llm.RoleUser:
			flushModelOutput()
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if p.Text != "" {
						pendingUser = append(pendingUser, map[string]any{"type": "text", "text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					item, err := interactionImageItem(p)
					if err != nil {
						return "", nil, false, err
					}
					if item != nil {
						pendingUser = append(pendingUser, item)
					}
				case llm.ContentDocument:
					if item := interactionDocumentItem(p.Document); item != nil {
						pendingUser = append(pendingUser, item)
					}
				case llm.ContentAudio:
					return "", nil, false, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for google: %s", p.Kind)}
				}
			}
		case llm.RoleAssistant:
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if p.Text != "" {
						appendModelContent(p.GoogleStep, map[string]any{"type": "text", "text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					item, err := interactionImageItem(p)
					if err != nil {
						return "", nil, false, err
					}
					if item != nil {
						appendModelContent(p.GoogleStep, item)
					}
				case llm.ContentThinking:
					if !p.Thinking.ReplayableGoogleThoughtFor(res.Instance, res.WireID) {
						continue
					}
					step := map[string]any{"type": "thought"}
					if sig := strings.TrimSpace(p.Thinking.Signature); sig != "" {
						step["signature"] = sig
					}
					if len(p.Thinking.Summary) > 0 {
						summaryItems := make([]map[string]any, 0, len(p.Thinking.Summary))
						for _, s := range p.Thinking.Summary {
							if s != "" {
								summaryItems = append(summaryItems, map[string]any{"type": "text", "text": s})
							}
						}
						if len(summaryItems) > 0 {
							step["summary"] = summaryItems
						}
					}
					appendNonUserStep(step)
				case llm.ContentWebSearch:
					return "", nil, false, &llm.ConfigurationError{Message: "google interactions forbids replaying ContentWebSearch history"}
				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					tc := p.ToolCall
					callID := strings.TrimSpace(tc.ID)
					if callID == "" {
						callID = identifier.MustNewSyntheticCallID()
						syntheticCallIDs = append(syntheticCallIDs, callID)
					}
					callIDToName[callID] = strings.TrimSpace(tc.Name)

					rawArgs := []byte(tc.WireArguments())
					trimmed := bytes.TrimSpace(rawArgs)
					wireArgs := json.RawMessage("{}")
					if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
						wireArgs = json.RawMessage(trimmed)
					}

					nativeMatched := tc.GoogleInteraction && res.Instance != "" && res.WireID != "" &&
						tc.GoogleInstance == res.Instance && tc.GoogleModel == res.WireID
					if !nativeMatched {
						hasUnsignedNonNative = true
						unsignedCallIDs[callID] = true
						if fallbackUnsignedCalls {
							appendModelContent(p.GoogleStep, map[string]any{
								"type": "text",
								"text": fmt.Sprintf("[historical tool call: %s(id=%s, arguments=%s)]", tc.Name, callID, string(wireArgs)),
							})
							continue
						}
					}

					step := map[string]any{
						"type":      "function_call",
						"id":        callID,
						"name":      tc.Name,
						"arguments": wireArgs,
					}
					if nativeMatched {
						if sig := strings.TrimSpace(tc.ThoughtSignature); sig != "" {
							step["signature"] = sig
						}
					}
					appendNonUserStep(step)
				case llm.ContentDocument:
					return "", nil, false, &llm.ConfigurationError{Message: "unsupported assistant document content for google"}
				case llm.ContentAudio:
					return "", nil, false, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for google: %s", p.Kind)}
				}
			}
			if pendingModelStep > 0 {
				flushModelOutput()
			}
		case llm.RoleTool:
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				tr := p.ToolResult
				callID := strings.TrimSpace(tr.ToolCallID)
				if callID == "" && len(syntheticCallIDs) > 0 {
					callID = syntheticCallIDs[0]
					syntheticCallIDs = syntheticCallIDs[1:]
				}
				name := callIDToName[callID]
				if name == "" {
					name = strings.TrimSpace(tr.Name)
				}
				var textStr string
				switch v := tr.Content.(type) {
				case string:
					textStr = v
				default:
					b, _ := json.Marshal(v)
					textStr = string(b)
				}

				var imageItem map[string]any
				if len(tr.ImageData) > 0 {
					mediaType := strings.ToLower(strings.TrimSpace(tr.ImageMediaType))
					if mediaType == "" || strings.HasPrefix(mediaType, "image/") {
						if !multimodalToolResults {
							return "", nil, false, &llm.ConfigurationError{Message: fmt.Sprintf("google model %q does not support tool-result images: multimodal tool results require a Gemini 3+ model", res.WireID)}
						}
						imageBytes, imageType, err := googleInlineImageBytes(tr.ImageData, tr.ImageMediaType)
						if err != nil {
							return "", nil, false, err
						}
						imageItem = map[string]any{
							"type":      "image",
							"mime_type": imageType,
							"data":      llm.CachedBase64(imageBytes),
						}
					}
				}

				if fallbackUnsignedCalls && unsignedCallIDs[callID] {
					flushModelOutput()
					pendingUser = append(pendingUser, map[string]any{
						"type": "text",
						"text": fmt.Sprintf("[historical tool result: %s(id=%s, is_error=%t): %s]", name, callID, tr.IsError, textStr),
					})
					if imageItem != nil {
						pendingUser = append(pendingUser, imageItem)
					}
					continue
				}

				var wireResult any = textStr
				if imageItem != nil {
					wireResult = []map[string]any{
						{"type": "text", "text": textStr},
						imageItem,
					}
				}
				step := map[string]any{
					"type":    "function_result",
					"call_id": callID,
					"result":  wireResult,
				}
				if name != "" {
					step["name"] = name
				}
				if tr.IsError {
					step["is_error"] = true
				}
				appendNonUserStep(step)
			}
		}
	}
	flushModelOutput()
	flushUser()
	if len(steps) == 0 {
		return "", nil, false, &llm.ConfigurationError{Message: "google interactions require at least one non-empty input step"}
	}
	return strings.Join(sysParts, "\n\n"), steps, hasUnsignedNonNative, nil
}

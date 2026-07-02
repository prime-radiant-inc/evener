package providercfg

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Marshal emits providers.toml content for cfg. It never emits api_key even
// if InstanceConfig.APIKey is set. The output round-trips through Load.
func Marshal(cfg Config) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "default = %q\n", cfg.Default)
	for _, inst := range cfg.Instances {
		fmt.Fprintf(&b, "\n[instances.%s]\n", inst.Name)
		fmt.Fprintf(&b, "type = %q\n", inst.Type)
		if inst.APIStyle != "" {
			fmt.Fprintf(&b, "api_style = %q\n", inst.APIStyle)
		}
		if inst.BaseURL != "" {
			fmt.Fprintf(&b, "base_url = %q\n", inst.BaseURL)
		}
		if inst.Quirks != "" {
			fmt.Fprintf(&b, "quirks = %q\n", inst.Quirks)
		}
		// Unlike api_key, header values round-trip verbatim (including any $ENV
		// reference). That is why $ENV form is the recommended way to hold a
		// secret in a header — the reference, not the secret, lands on disk.
		if len(inst.Headers) > 0 {
			pairs := make([]string, 0, len(inst.Headers))
			for _, k := range sortedKeys(inst.Headers) {
				pairs = append(pairs, fmt.Sprintf("%q = %q", k, inst.Headers[k]))
			}
			fmt.Fprintf(&b, "headers = { %s }\n", strings.Join(pairs, ", "))
		}
		writeCompat(&b, fmt.Sprintf("instances.%s.compat", inst.Name), inst.Compat)
		for _, id := range sortedKeys(inst.Models) {
			mc := inst.Models[id]
			fmt.Fprintf(&b, "\n[instances.%s.models.%q]\n", inst.Name, id)
			if mc.ContextWindow != 0 {
				fmt.Fprintf(&b, "context_window = %d\n", mc.ContextWindow)
			}
			if mc.MaxOutputTokens != 0 {
				fmt.Fprintf(&b, "max_output_tokens = %d\n", mc.MaxOutputTokens)
			}
			if mc.Reasoning != nil {
				fmt.Fprintf(&b, "reasoning = %t\n", *mc.Reasoning)
			}
			if len(mc.ThinkingLevels) > 0 {
				pairs := make([]string, 0, len(mc.ThinkingLevels))
				for _, k := range sortedKeys(mc.ThinkingLevels) {
					pairs = append(pairs, fmt.Sprintf("%s = %q", k, mc.ThinkingLevels[k]))
				}
				fmt.Fprintf(&b, "thinking_levels = { %s }\n", strings.Join(pairs, ", "))
			}
			writeCompat(&b, fmt.Sprintf("instances.%s.models.%q.compat", inst.Name, id), mc.Compat)
		}
	}
	return []byte(b.String()), nil
}

// writeCompat emits one [<header>] compat table. Only set fields are written,
// so the output round-trips through Load without inventing overrides.
func writeCompat(b *strings.Builder, header string, c *CompatConfig) {
	if c == nil {
		return
	}
	fmt.Fprintf(b, "\n[%s]\n", header)
	if c.ThinkingFormat != "" {
		fmt.Fprintf(b, "thinking_format = %q\n", c.ThinkingFormat)
	}
	writeBool(b, "supports_strict_mode", c.SupportsStrictMode)
	writeBool(b, "supports_reasoning_effort", c.SupportsReasoningEffort)
	if c.MaxTokensField != "" {
		fmt.Fprintf(b, "max_tokens_field = %q\n", c.MaxTokensField)
	}
	writeBool(b, "tool_stream", c.ToolStream)
	writeBool(b, "supports_store", c.SupportsStore)
	writeBool(b, "supports_developer_role", c.SupportsDeveloperRole)
	writeBool(b, "supports_usage_in_streaming", c.SupportsUsageInStreaming)
	writeBool(b, "requires_tool_result_name", c.RequiresToolResultName)
	writeBool(b, "requires_assistant_after_tool_result", c.RequiresAssistantAfterToolResult)
	writeBool(b, "requires_thinking_as_text", c.RequiresThinkingAsText)
	writeBool(b, "requires_reasoning_content_on_assistant", c.RequiresReasoningContentOnAssistant)
	if c.CacheControlFormat != "" {
		fmt.Fprintf(b, "cache_control_format = %q\n", c.CacheControlFormat)
	}
	writeBool(b, "supports_long_cache_retention", c.SupportsLongCacheRetention)
	writeBool(b, "send_session_affinity_headers", c.SendSessionAffinityHeaders)
	writeBool(b, "lock_temperature", c.LockTemperature)
	writeBool(b, "lock_top_p", c.LockTopP)
	writeBool(b, "lock_frequency_penalty", c.LockFrequencyPenalty)
	writeBool(b, "lock_presence_penalty", c.LockPresencePenalty)
	writeBool(b, "tool_choice_auto_only", c.ToolChoiceAutoOnly)
	if c.MaxStopSequences != nil {
		fmt.Fprintf(b, "max_stop_sequences = %d\n", *c.MaxStopSequences)
	}
	writeBool(b, "strip_empty_content", c.StripEmptyContent)
	writeBool(b, "no_json_schema", c.NoJSONSchema)
	if len(c.FinishReasonMap) > 0 {
		pairs := make([]string, 0, len(c.FinishReasonMap))
		for _, k := range sortedKeys(c.FinishReasonMap) {
			pairs = append(pairs, fmt.Sprintf("%q = %q", k, c.FinishReasonMap[k]))
		}
		fmt.Fprintf(b, "finish_reason_map = { %s }\n", strings.Join(pairs, ", "))
	}
	writeBool(b, "translate_max_to_xhigh", c.TranslateMaxToXHigh)
	if len(c.ChatTemplateKwargs) > 0 {
		pairs := make([]string, 0, len(c.ChatTemplateKwargs))
		for _, k := range sortedKeys(c.ChatTemplateKwargs) {
			pairs = append(pairs, fmt.Sprintf("%q = %s", k, tomlScalar(c.ChatTemplateKwargs[k])))
		}
		fmt.Fprintf(b, "chat_template_kwargs = { %s }\n", strings.Join(pairs, ", "))
	}
}

// tomlScalar renders a chat_template_kwargs value as inline-TOML. Kwargs are
// expected to be scalars (bool/int/float/string), which is what BurntSushi
// decodes an inline table of scalars to; any other kind falls back to a quoted
// string so Marshal never panics or emits invalid TOML.
func tomlScalar(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case string:
		return fmt.Sprintf("%q", x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%q", fmt.Sprint(x))
	}
}

func writeBool(b *strings.Builder, key string, v *bool) {
	if v != nil {
		fmt.Fprintf(b, "%s = %t\n", key, *v)
	}
}

// sortedKeys returns the map's keys in sorted order for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

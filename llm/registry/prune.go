package registry

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

// Pseudo-paths in the prunable tables (spec §8.2): developer_role is not a
// body path (false = system prompt sent as `system`, true = `developer`);
// max_tokens names whichever spelling Caps.MaxTokensField selects.
const (
	FieldDeveloperRole = "developer_role"
	FieldMaxTokens     = "max_tokens"
)

// prunable is the authoritative per-protocol table of optional wire fields
// and their baseline (send or not) before any layer applies (spec §8.2).
// Each protocol package's PrunablePaths() must return the same paths; a
// test in that package asserts it.
var prunable = map[string]map[string]bool{
	ProtocolOpenAIChat: {
		"temperature": true, "top_p": true, "stop": true, "stream_options": true, FieldMaxTokens: true,
		"store": false, "frequency_penalty": false, "presence_penalty": false, FieldDeveloperRole: false,
		"parallel_tool_calls": false, "prompt_cache_key": false, "prompt_cache_retention": false,
		"service_tier": false, "metadata": false, "logprobs": false, "n": false, "seed": false, "user": false,
	},
	ProtocolOpenAIResponses: {
		"temperature": true, "top_p": true, "max_output_tokens": true,
		"store": false, "include": false, "truncation": false, "safety_identifier": false, "service_tier": false,
		"prompt_cache_key": false, "prompt_cache_retention": false, "previous_response_id": false,
		"conversation": false, "metadata": false, "max_tool_calls": false, "background": false,
		"parallel_tool_calls": false, "text.verbosity": false, "reasoning.context": false,
	},
	ProtocolAnthropic: {
		"temperature": true, "top_p": true, "stop_sequences": true, FieldMaxTokens: true,
		"metadata": false, "service_tier": false, "fallbacks": false, "container": false,
	},
	ProtocolGoogle: {
		"generationConfig.temperature": true, "generationConfig.topP": true, "generationConfig.stopSequences": true,
		"toolConfig": true, "safetySettings": true, "cachedContent": false, "labels": false,
	},
}

// PrunablePaths returns the sorted prunable paths of a protocol, or nil for
// an unknown protocol.
func PrunablePaths(protocol string) []string {
	table, ok := prunable[protocol]
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(table))
	for p := range table {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// Baseline returns a copy of the protocol's path → send-by-default table.
func Baseline(protocol string) map[string]bool {
	table, ok := prunable[protocol]
	if !ok {
		return nil
	}
	return maps.Clone(table)
}

// ValidateFields is the load-time typo guard (spec §10): every key must be
// in the record's resolved protocol's prunable set.
func ValidateFields(fields map[string]bool, protocol, where string) error {
	table := prunable[protocol]
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := table[k]; !ok {
			return fmt.Errorf("%s: fields.%s is not a prunable path of protocol %s (valid: %s)", where, k, protocol, strings.Join(PrunablePaths(protocol), ", "))
		}
	}
	return nil
}

// seedFields fills every prunable path missing from c.Fields with its
// baseline, so a Resolved record always carries the full table (spec §4.4).
func seedFields(c *Caps, protocol string) {
	table := prunable[protocol]
	if c.Fields == nil {
		c.Fields = make(map[string]bool, len(table))
	}
	for p, send := range table {
		if _, ok := c.Fields[p]; !ok {
			c.Fields[p] = send
		}
	}
}

// Prune deletes from body every prunable path whose caps.Fields flag is
// false and returns the paths it removed, sorted (spec §8.2 step 2). Keys
// absent from the body are not reported; developer_role is never a body
// path; max_tokens maps to caps.MaxTokensField's spelling.
func Prune(body map[string]any, caps Caps) []string {
	var pruned []string
	for key, send := range caps.Fields {
		if send || key == FieldDeveloperRole {
			continue
		}
		path := key
		if key == FieldMaxTokens && caps.MaxTokensField != nil && *caps.MaxTokensField != "" {
			path = *caps.MaxTokensField
		}
		if deletePath(body, path) {
			pruned = append(pruned, path)
		}
	}
	sort.Strings(pruned)
	return pruned
}

// setPath sets a dotted path, creating parent objects (used by Transport.Body
// constants and tests).
func setPath(body map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

// getPath reads a dotted path.
func getPath(body map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	cur := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	v, ok := cur[parts[len(parts)-1]]
	return v, ok
}

// deletePath removes a dotted path and any parent object it leaves empty.
// It reports whether the leaf existed.
func deletePath(body map[string]any, path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		if _, ok := body[path]; !ok {
			return false
		}
		delete(body, path)
		return true
	}
	child, ok := body[parts[0]].(map[string]any)
	if !ok {
		return false
	}
	deleted := deletePath(child, strings.Join(parts[1:], "."))
	if deleted && len(child) == 0 {
		delete(body, parts[0])
	}
	return deleted
}

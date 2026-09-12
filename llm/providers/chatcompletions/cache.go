package chatcompletions

import "slices"

// anthropicCacheControl marks the request for Anthropic-style prompt caching
// through gateways that forward cache_control: the system prompt, the last
// tool definition, and the last conversation message each get an ephemeral
// marker (mirrors the placement Anthropic documents for messages API
// callers). ttl, when non-empty (from Caps.CacheTTL), rides along on the
// marker.
func anthropicCacheControl(body map[string]any, ttl string) {
	marker := map[string]any{"type": "ephemeral"}
	if ttl != "" {
		marker["ttl"] = ttl
	}
	msgs, _ := body["messages"].([]map[string]any)
	for _, m := range msgs {
		if m["role"] == "system" || m["role"] == "developer" {
			addCacheControlToTextContent(m, marker)
			break
		}
	}
	for _, m := range slices.Backward(msgs) {
		if r := m["role"]; r == "user" || r == "assistant" {
			if addCacheControlToTextContent(m, marker) {
				break
			}
		}
	}
	if tools, ok := body["tools"].([]map[string]any); ok && len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = marker
	}
}

// addCacheControlToTextContent attaches cc to the message's last text part,
// promoting a plain string content to a one-part array. Returns false when
// the message has no text to mark (empty string or partless content).
func addCacheControlToTextContent(msg map[string]any, cc map[string]any) bool {
	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return false
		}
		msg["content"] = []map[string]any{{"type": "text", "text": content, "cache_control": cc}}
		return true
	case []map[string]any:
		for _, part := range slices.Backward(content) {
			if part["type"] == "text" {
				part["cache_control"] = cc
				return true
			}
		}
	}
	return false
}

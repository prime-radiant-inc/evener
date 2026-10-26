package schema

import (
	"encoding/json"
	"strings"
)

// ToolIntent returns a tool call's stated "intent" argument, trimmed, or ""
// when none is present. Only string values count. It falls back to "purpose",
// the field's name before the 2026-08-29 rename, because transcripts recorded
// before the rename are still read back. Every write path emits "intent"
// exclusively. Intent is never inferred from commands or paths.
func ToolIntent(args json.RawMessage) string {
	var fields map[string]any
	if len(args) == 0 || json.Unmarshal(args, &fields) != nil {
		return ""
	}
	for _, name := range []string{"intent", "purpose"} {
		if value, ok := fields[name].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

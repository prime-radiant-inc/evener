package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ToolArgs is a decoded JSON args map with a convenience Str() accessor.
type ToolArgs map[string]any

// toolArgsFromJSON decodes a JSON object string into ToolArgs.
// Returns an empty map on any error or empty input.
func toolArgsFromJSON(s string) ToolArgs {
	if s == "" {
		return ToolArgs{}
	}
	var args ToolArgs
	if err := json.Unmarshal([]byte(s), &args); err != nil {
		return ToolArgs{}
	}
	return args
}

// Str returns the string value for key, or "" if absent or not a string.
func (a ToolArgs) Str(key string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return ""
}

// ToolRenderer describes how to render a single tool call in the TUI.
type ToolRenderer struct {
	// Verb returns the short action word (e.g. "read", "edit", "shell").
	Verb func(args ToolArgs) string
	// Target returns the primary subject (e.g. file path, command).
	Target func(args ToolArgs) string
	// Result returns the compact outcome text for a completed call.
	Result func(args ToolArgs, output, errStr string, dur time.Duration) string
	// Body renders an expanded multi-line body. May be nil (no body).
	Body func(args ToolArgs, output string, w int) string
	// ExpandedByDefault causes the body to show without user interaction.
	ExpandedByDefault bool
}

// toolRenderers is the registry of per-tool renderers.
var toolRenderers = map[string]ToolRenderer{}

// lookupToolRenderer returns the renderer for tool.  Falls back to an
// MCP-style renderer (provider__operation) or an unknown-tool renderer.
// Always returns ok=true — the fallback is the last resort.
func lookupToolRenderer(tool string) (ToolRenderer, bool) {
	if r, ok := toolRenderers[tool]; ok {
		return r, true
	}
	if strings.Contains(tool, "__") {
		return mcpFallbackRenderer(tool), true
	}
	return unknownToolRenderer(tool), true
}

func mcpFallbackRenderer(tool string) ToolRenderer {
	provider, op, _ := strings.Cut(tool, "__")
	return ToolRenderer{
		Verb:   func(_ ToolArgs) string { return provider },
		Target: func(_ ToolArgs) string { return op },
		Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
}

func unknownToolRenderer(tool string) ToolRenderer {
	return ToolRenderer{
		Verb:   func(_ ToolArgs) string { return tool },
		Target: func(_ ToolArgs) string { return "" },
		Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
}

// formatLineCount returns "1 line" or "N lines".
func formatLineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

// shortID returns the first 8 characters of an ID string.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// diffResultText counts +/- lines from a unified-diff output and returns a
// compact summary such as "3 +/2 -", "4 added", "2 removed", or "ok".
func diffResultText(_ ToolArgs, output, errStr string, _ time.Duration) string {
	if errStr != "" {
		return "error"
	}
	plus, minus := 0, 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			plus++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			minus++
		}
	}
	switch {
	case plus > 0 && minus > 0:
		return strconv.Itoa(plus) + " +/" + strconv.Itoa(minus) + " -"
	case plus > 0:
		return strconv.Itoa(plus) + " added"
	case minus > 0:
		return strconv.Itoa(minus) + " removed"
	default:
		return "ok"
	}
}

func init() {
	// read_file
	toolRenderers["read_file"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "read" },
		Target: func(args ToolArgs) string { return args.Str("file_path") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			lines := strings.Count(output, "\n") + 1
			return formatLineCount(lines)
		},
	}
}

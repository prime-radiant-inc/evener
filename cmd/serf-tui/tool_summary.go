package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// summarizeArgs returns a concise human-readable description of a tool call
// given the tool name and its raw argument JSON.
func summarizeArgs(toolName, argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

	str := func(key string) string {
		v, _ := args[key].(string)
		return v
	}
	num := func(key string) (int, bool) {
		v, ok := args[key].(float64)
		return int(v), ok
	}
	truncate := func(s string, n int) string {
		s = strings.TrimSpace(s)
		// Use only the first line for display.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i] + "…"
		}
		if len(s) > n {
			return s[:n] + "…"
		}
		return s
	}

	switch toolName {
	case "shell":
		if desc := str("description"); desc != "" {
			return truncate(desc, 80)
		}
		return truncate(str("command"), 80)

	case "read_file":
		path := shortPath(str("file_path"))
		offset, hasOffset := num("offset")
		limit, hasLimit := num("limit")
		switch {
		case hasOffset && hasLimit:
			return fmt.Sprintf("%s :%d+%d", path, offset, limit)
		case hasOffset:
			return fmt.Sprintf("%s :%d+", path, offset)
		case hasLimit:
			return fmt.Sprintf("%s :%d", path, limit)
		default:
			return path
		}

	case "write_file":
		path := shortPath(str("file_path"))
		content := str("content")
		lines := strings.Count(content, "\n") + 1
		if content == "" {
			lines = 0
		}
		return fmt.Sprintf("%s (%d lines)", path, lines)

	case "edit_file":
		path := shortPath(str("file_path"))
		old := truncate(str("old_string"), 40)
		if old != "" {
			return fmt.Sprintf("%s  %q", path, old)
		}
		return path

	case "glob":
		pattern := str("pattern")
		if path := str("path"); path != "" {
			return fmt.Sprintf("%s in %s", pattern, shortPath(path))
		}
		return pattern

	case "grep":
		pattern := str("pattern")
		if path := str("path"); path != "" {
			return fmt.Sprintf("%q in %s", pattern, shortPath(path))
		}
		return fmt.Sprintf("%q", pattern)

	case "task_list":
		action := str("action")
		switch action {
		case "append":
			if tasks, ok := args["tasks"].([]any); ok {
				return fmt.Sprintf("append %d tasks", len(tasks))
			}
			return "append"
		case "update":
			if updates, ok := args["updates"].([]any); ok {
				return fmt.Sprintf("update %d tasks", len(updates))
			}
			return "update"
		default:
			return action
		}

	case "web_search":
		return fmt.Sprintf("%q", str("query"))

	case "web_fetch":
		url := str("url")
		if len(url) > 60 {
			url = url[:60] + "…"
		}
		return url

	case "spawn_agent":
		return truncate(str("task"), 80)

	case "send_input":
		return str("agent_id")

	case "wait", "close_agent":
		return str("agent_id")

	case "use_skill":
		return str("skill_name")

	case "communicate":
		action := str("action")
		msg := truncate(str("message"), 60)
		if msg != "" {
			return fmt.Sprintf("%s: %s", action, msg)
		}
		return action

	default:
		return fallbackSummary(args)
	}
}

// shortPath trims a path to its last 2 components for compact display.
func shortPath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	parts := strings.Split(p, string(filepath.Separator))
	if len(parts) > 2 {
		return "…/" + strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

// fallbackSummary renders unknown tool args as compact key=value pairs,
// skipping any value longer than 40 chars.
func fallbackSummary(args map[string]any) string {
	var parts []string
	for k, v := range args {
		var s string
		switch val := v.(type) {
		case string:
			if len(val) > 40 {
				continue
			}
			s = fmt.Sprintf("%s=%q", k, val)
		case float64:
			s = fmt.Sprintf("%s=%g", k, val)
		case bool:
			s = fmt.Sprintf("%s=%v", k, val)
		default:
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "  ")
}

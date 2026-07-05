// Package toolsummary renders compact one-line descriptions and optional
// detail bodies for tool calls in the serf-tui transcript.
package toolsummary

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// SummarizeTool returns a compact one-line description and an optional
// multi-line detail body for a tool call.
//
// Callers populate toolCallInfo.Description / Detail fields from this in
// hub_types.go and message.go.
func SummarizeTool(toolName, argsJSON string) (desc, detail string) {
	if argsJSON == "" {
		return "", ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON, ""
	}

	str := func(key string) string {
		v, _ := args[key].(string)
		return v
	}
	num := func(key string) (int, bool) {
		v, ok := args[key].(float64)
		return int(v), ok
	}
	trunc := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) > n {
			return s[:n] + "…"
		}
		return s
	}

	switch toolName {
	case "shell":
		cmd := str("command")
		if d := str("purpose"); d != "" {
			desc = trunc(d, 80)
		} else if d := str("description"); d != "" {
			// Backward compatibility for older transcripts.
			desc = trunc(d, 80)
		} else {
			// Show first line as desc; full command as detail if multi-line.
			firstLine := cmd
			if i := strings.IndexByte(cmd, '\n'); i >= 0 {
				firstLine = cmd[:i]
			}
			desc = trunc(firstLine, 80)
		}
		// Always show the full command in detail.
		if strings.Contains(cmd, "\n") || len(cmd) > 80 {
			detail = cmd
		}
		return desc, detail

	case "read_file":
		path := shortPath(str("file_path"))
		offset, hasOffset := num("offset")
		limit, hasLimit := num("limit")
		switch {
		case hasOffset && hasLimit:
			desc = fmt.Sprintf("%s :%d+%d", path, offset, limit)
		case hasOffset:
			desc = fmt.Sprintf("%s :%d+", path, offset)
		case hasLimit:
			desc = fmt.Sprintf("%s :%d", path, limit)
		default:
			desc = path
		}
		return desc, detail

	case "write_file":
		path := shortPath(str("file_path"))
		content := str("content")
		lines := strings.Count(content, "\n") + 1
		if content == "" {
			lines = 0
		}
		desc = fmt.Sprintf("%s (%d lines)", path, lines)
		return desc, detail

	case "edit_file":
		path := str("file_path")
		old := str("old_string")
		new_ := str("new_string")
		desc = shortPath(path)
		detail = unifiedDiff(path, old, new_)
		return desc, detail

	case "glob":
		pattern := str("pattern")
		if path := str("path"); path != "" {
			desc = fmt.Sprintf("%s in %s", pattern, shortPath(path))
		} else {
			desc = pattern
		}
		return desc, detail

	case "grep":
		pattern := str("pattern")
		if path := str("path"); path != "" {
			desc = fmt.Sprintf("%q in %s", pattern, shortPath(path))
		} else {
			desc = fmt.Sprintf("%q", pattern)
		}
		return desc, detail

	case "task_list":
		action := str("action")
		switch action {
		case "append":
			tasks, _ := args["tasks"].([]any)
			desc = fmt.Sprintf("append %d tasks", len(tasks))
			detail = renderTaskAppend(tasks)
		case "update":
			updates, _ := args["updates"].([]any)
			desc = fmt.Sprintf("update %d tasks", len(updates))
			detail = renderTaskUpdate(updates)
		default:
			desc = action
		}
		return desc, detail

	case "web_search":
		desc = fmt.Sprintf("%q", str("query"))
		return desc, detail

	case "web_fetch":
		url := str("url")
		desc = trunc(url, 80)
		return desc, detail

	case "delegate":
		task := str("task")
		firstLine := task
		if i := strings.IndexByte(task, '\n'); i >= 0 {
			firstLine = task[:i]
		}
		desc = trunc(firstLine, 80)
		if strings.Contains(task, "\n") || len(task) > 80 {
			detail = task
		}
		return desc, detail

	case "job_send_message":
		desc = str("target")
		return desc, detail

	case "delegate_send":
		desc = str("to")
		return desc, detail

	case "job_read_output", "job_stop":
		desc = str("job_id")
		return desc, detail

	case "use_skill":
		desc = str("skill_name")
		return desc, detail

	case "communicate":
		msg := trunc(str("message"), 60)
		if msg == "" {
			if output, ok := args["output"].(map[string]any); ok {
				if outMsg, ok := output["message"].(string); ok {
					msg = trunc(outMsg, 60)
				}
			}
		}
		if msg != "" {
			desc = msg
		} else {
			desc = "(communicating)"
		}
		return desc, detail

	default:
		desc = fallbackSummary(args)
		return desc, detail
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

// unifiedDiff generates a unified diff of old→new and syntax-highlights it.
func unifiedDiff(filename, old, new_ string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new_, "\n")

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n",
		filename, filename, 1, len(oldLines), 1, len(newLines))
	for _, l := range oldLines {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range newLines {
		b.WriteString("+" + l + "\n")
	}
	raw := b.String()

	return highlightDiff(raw)
}

// highlightDiff applies chroma syntax highlighting to a unified diff string.
func highlightDiff(diff string) string {
	lexer := lexers.Get("diff")
	if lexer == nil {
		return diff
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return diff
	}

	it, err := lexer.Tokenise(nil, diff)
	if err != nil {
		return diff
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, it); err != nil {
		return diff
	}
	return buf.String()
}

// renderTaskAppend renders a list of tasks to be appended.
func renderTaskAppend(tasks []any) string {
	if len(tasks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range tasks {
		m, _ := t.(map[string]any)
		if m == nil {
			continue
		}
		desc, _ := m["description"].(string)
		fmt.Fprintf(&b, "  %d. %s\n", i+1, desc)
		if prompt, _ := m["prompt"].(string); prompt != "" {
			// Show first line of prompt indented.
			first := prompt
			if idx := strings.IndexByte(prompt, '\n'); idx >= 0 {
				first = prompt[:idx] + "…"
			}
			if len(first) > 72 {
				first = first[:72] + "…"
			}
			fmt.Fprintf(&b, "     %s\n", first)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTaskUpdate renders a list of task status updates.
func renderTaskUpdate(updates []any) string {
	if len(updates) == 0 {
		return ""
	}
	statusIcon := map[string]string{
		"done":        "✓",
		"in_progress": "→",
		"undone":      "○",
		"cancelled":   "✕",
	}
	var b strings.Builder
	for _, u := range updates {
		m, _ := u.(map[string]any)
		if m == nil {
			continue
		}
		idF, _ := m["id"].(float64)
		id := int(idF)
		status, _ := m["status"].(string)
		icon := statusIcon[status]
		if icon == "" {
			icon = "·"
		}
		fmt.Fprintf(&b, "  %s task %d → %s\n", icon, id, status)
	}
	return strings.TrimRight(b.String(), "\n")
}

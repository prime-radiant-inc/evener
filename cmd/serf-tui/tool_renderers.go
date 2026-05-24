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

	// shell + aliases
	shellRenderer := ToolRenderer{
		Verb: func(_ ToolArgs) string { return "shell" },
		Target: func(args ToolArgs) string {
			cmd := args.Str("command")
			if firstLine, _, ok := strings.Cut(cmd, "\n"); ok {
				cmd = firstLine
			}
			if len(cmd) > 80 {
				cmd = cmd[:80] + "…"
			}
			return cmd
		},
		Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
	toolRenderers["shell"] = shellRenderer
	toolRenderers["exec_command"] = shellRenderer
	toolRenderers["run_shell_command"] = shellRenderer

	// grep + aliases
	grepRenderer := ToolRenderer{
		Verb: func(_ ToolArgs) string { return "grep" },
		Target: func(args ToolArgs) string {
			pat := args.Str("pattern")
			path := args.Str("path")
			if path != "" {
				return pat + "  in  " + path
			}
			return pat
		},
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			if output == "" {
				return "0 hits"
			}
			hits := strings.Count(output, "\n") + 1
			return strconv.Itoa(hits) + " hits"
		},
	}
	toolRenderers["grep"] = grepRenderer
	toolRenderers["grep_files"] = grepRenderer
	toolRenderers["grep_search"] = grepRenderer

	// glob
	toolRenderers["glob"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "glob" },
		Target: func(args ToolArgs) string { return args.Str("pattern") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			matches := strings.Count(output, "\n")
			return strconv.Itoa(matches) + " matches"
		},
	}

	// list_dir + aliases
	listRenderer := ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "ls" },
		Target: func(args ToolArgs) string { return args.Str("path") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			entries := strings.Count(output, "\n")
			return strconv.Itoa(entries) + " entries"
		},
	}
	toolRenderers["list_dir"] = listRenderer
	toolRenderers["list_directory"] = listRenderer

	// edit_file (Body wired in Wave 6)
	editFileRenderer := ToolRenderer{
		Verb:              func(_ ToolArgs) string { return "edit" },
		Target:            func(args ToolArgs) string { return args.Str("file_path") },
		Result:            diffResultText,
		ExpandedByDefault: true,
	}
	toolRenderers["edit_file"] = editFileRenderer

	// write_file (Body wired in Wave 6)
	writeFileRenderer := ToolRenderer{
		Verb:              func(_ ToolArgs) string { return "write" },
		Target:            func(args ToolArgs) string { return args.Str("file_path") },
		Result:            diffResultText,
		ExpandedByDefault: true,
	}
	toolRenderers["write_file"] = writeFileRenderer

	// apply_patch (Body wired in Wave 6)
	applyPatchRenderer := ToolRenderer{
		Verb: func(_ ToolArgs) string { return "patch" },
		Target: func(args ToolArgs) string {
			patch := args.Str("patch")
			for _, line := range strings.Split(patch, "\n") {
				if strings.HasPrefix(line, "+++ b/") {
					return line[len("+++ b/"):]
				}
			}
			return ""
		},
		Result:            diffResultText,
		ExpandedByDefault: true,
	}
	toolRenderers["apply_patch"] = applyPatchRenderer

	// web_fetch
	toolRenderers["web_fetch"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "fetch" },
		Target: func(args ToolArgs) string { return args.Str("url") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return strconv.Itoa(len(output)) + " bytes"
		},
	}

	// web_search
	toolRenderers["web_search"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "search" },
		Target: func(args ToolArgs) string { return args.Str("query") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			results := strings.Count(output, "\n") + 1
			return strconv.Itoa(results) + " results"
		},
	}

	// spawn_agent
	toolRenderers["spawn_agent"] = ToolRenderer{
		Verb: func(_ ToolArgs) string { return "spawn" },
		Target: func(args ToolArgs) string {
			task := args.Str("task")
			if len(task) > 80 {
				task = task[:80] + "…"
			}
			return task
		},
		Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}

	// resume_agent / wait / close_agent — agent control factory
	agentControl := func(verb string) ToolRenderer {
		return ToolRenderer{
			Verb:   func(_ ToolArgs) string { return verb },
			Target: func(args ToolArgs) string { return shortID(args.Str("agent_id")) },
			Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
				if errStr != "" {
					return "error"
				}
				return "ok"
			},
		}
	}
	toolRenderers["resume_agent"] = agentControl("resume")
	toolRenderers["wait"] = agentControl("wait")
	toolRenderers["close_agent"] = agentControl("close")

	// task_list
	toolRenderers["task_list"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "tasks" },
		Target: func(_ ToolArgs) string { return "" },
		Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return ""
		},
		ExpandedByDefault: true,
	}

	// use_skill
	toolRenderers["use_skill"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "skill" },
		Target: func(args ToolArgs) string { return args.Str("name") },
		Result: func(_ ToolArgs, _ string, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}

	// Wave 6, task 6.1: wire diffBody into diff-producing renderers.
	editFileRenderer.Body = diffBody
	toolRenderers["edit_file"] = editFileRenderer

	writeFileRenderer.Body = diffBody
	toolRenderers["write_file"] = writeFileRenderer

	applyPatchRenderer.Body = func(args ToolArgs, _ string, w int) string {
		return diffBody(args, args.Str("patch"), w)
	}
	toolRenderers["apply_patch"] = applyPatchRenderer

	// Wave 6, task 6.2: wire fileBody into read_file renderer.
	readFileRenderer := toolRenderers["read_file"]
	readFileRenderer.Body = fileBody
	toolRenderers["read_file"] = readFileRenderer

	// Wave 6, task 6.3: wire taskListBody into task_list renderer.
	taskListR := toolRenderers["task_list"]
	taskListR.Body = taskListBody
	toolRenderers["task_list"] = taskListR

	// Wave 6, task 6.4: wire subagentBody into spawn_agent renderer.
	spawnR := toolRenderers["spawn_agent"]
	spawnR.Body = subagentBody
	toolRenderers["spawn_agent"] = spawnR
}

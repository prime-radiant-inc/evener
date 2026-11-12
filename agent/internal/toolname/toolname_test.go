package toolname

import "testing"

func TestClaudeToEvener(t *testing.T) {
	tests := map[string]string{
		"Read": "read_file", "Write": "write_file", "Edit": "edit_file",
		"Bash": "shell", "Grep": "grep", "Glob": "glob",
		"Task": "delegate", "WebFetch": "web_fetch", "WebSearch": "web_search",
		"NotebookEdit":      "notebook_edit",
		"AskUserQuestion":   "ask_user",
		"Skill":             "use_skill",
		"mcp__server__tool": "mcp__server__tool",
	}
	for input, want := range tests {
		if got := ClaudeToEvener(input); got != want {
			t.Errorf("ClaudeToEvener(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEvenerToClaude(t *testing.T) {
	tests := map[string]string{
		"read_file": "Read", "write_file": "Write", "edit_file": "Edit",
		"shell": "Bash", "grep": "Grep", "glob": "Glob",
		"delegate": "Task", "web_fetch": "WebFetch", "web_search": "WebSearch",
		"notebook_edit": "NotebookEdit",
		"ask_user":      "AskUserQuestion",
		"use_skill":     "Skill",
		"unknown":       "unknown",
	}
	for input, want := range tests {
		if got := EvenerToClaude(input); got != want {
			t.Errorf("EvenerToClaude(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestImportClaudeToolName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Read":                     "read_file",
		"Skill":                    "use_skill",
		"mcp__example__echo":       "example__echo",
		"mcp__my-server__run-tool": "my_server__run_tool",
		"mcp__plugin_superpowers-chrome_chrome__use_browser": "plugin_superpowers_chrome_chrome__use_browser",
		"mcp__":            "mcp__",
		"mcp__server_only": "mcp__server_only",
		"mcp____tool":      "mcp____tool",
		"mcp__server__":    "mcp__server__",
		"custom_tool":      "custom_tool",
	}
	for input, want := range tests {
		if got := ImportClaudeToolName(input); got != want {
			t.Errorf("ImportClaudeToolName(%q) = %q, want %q", input, got, want)
		}
	}
}

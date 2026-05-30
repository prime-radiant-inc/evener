package agent

import "testing"

func TestMapClaudeToolName(t *testing.T) {
	tests := map[string]string{
		"Read": "read_file", "Write": "write_file", "Edit": "edit_file",
		"Bash": "shell", "Grep": "grep", "Glob": "glob",
		"Task": "spawn_agent", "WebFetch": "web_fetch", "WebSearch": "web_search",
		"NotebookEdit":      "notebook_edit",
		"unknown_tool":      "unknown_tool",
		"mcp__server__tool": "mcp__server__tool",
	}
	for input, want := range tests {
		if got := MapClaudeToolName(input); got != want {
			t.Errorf("MapClaudeToolName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMapSerfToolNameToClaude(t *testing.T) {
	tests := map[string]string{
		"read_file": "Read", "write_file": "Write", "edit_file": "Edit",
		"shell": "Bash", "grep": "Grep", "glob": "Glob",
		"spawn_agent": "Task", "web_fetch": "WebFetch", "web_search": "WebSearch",
		"notebook_edit": "NotebookEdit",
		"unknown":       "unknown",
	}
	for input, want := range tests {
		if got := MapSerfToolNameToClaude(input); got != want {
			t.Errorf("MapSerfToolNameToClaude(%q) = %q, want %q", input, got, want)
		}
	}
}

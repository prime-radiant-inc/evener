package toolname

import "testing"

func TestClaudeToEvener(t *testing.T) {
	tests := map[string]string{
		"Read": "read_file", "Write": "write_file", "Edit": "edit_file",
		"Bash": "shell", "Grep": "grep", "Glob": "glob",
		"Task": "delegate", "WebFetch": "web_fetch", "WebSearch": "web_search",
		"NotebookEdit":      "notebook_edit",
		"AskUserQuestion":   "ask_user",
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
		"unknown":       "unknown",
	}
	for input, want := range tests {
		if got := EvenerToClaude(input); got != want {
			t.Errorf("EvenerToClaude(%q) = %q, want %q", input, got, want)
		}
	}
}

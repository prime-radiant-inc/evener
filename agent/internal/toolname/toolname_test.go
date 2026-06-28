package toolname

import "testing"

func TestClaudeToSerf(t *testing.T) {
	tests := map[string]string{
		"Read": "read_file", "Write": "write_file", "Edit": "edit_file",
		"Bash": "shell", "Grep": "grep", "Glob": "glob",
		"Task": "delegate", "WebFetch": "web_fetch", "WebSearch": "web_search",
		"NotebookEdit":      "notebook_edit",
		"mcp__server__tool": "mcp__server__tool",
	}
	for input, want := range tests {
		if got := ClaudeToSerf(input); got != want {
			t.Errorf("ClaudeToSerf(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSerfToClaude(t *testing.T) {
	tests := map[string]string{
		"read_file": "Read", "write_file": "Write", "edit_file": "Edit",
		"shell": "Bash", "grep": "Grep", "glob": "Glob",
		"delegate": "Task", "web_fetch": "WebFetch", "web_search": "WebSearch",
		"notebook_edit": "NotebookEdit",
		"unknown":       "unknown",
	}
	for input, want := range tests {
		if got := SerfToClaude(input); got != want {
			t.Errorf("SerfToClaude(%q) = %q, want %q", input, got, want)
		}
	}
}

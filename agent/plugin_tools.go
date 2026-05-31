package agent

// claudeToSerfToolNames maps Claude Code tool names to serf canonical names.
var claudeToSerfToolNames = map[string]string{
	"Read":         "read_file",
	"Write":        "write_file",
	"Edit":         "edit_file",
	"Bash":         "shell",
	"Grep":         "grep",
	"Glob":         "glob",
	"Task":         "spawn_agent",
	"WebFetch":     "web_fetch",
	"WebSearch":    "web_search",
	"NotebookEdit": "notebook_edit",
}

// serfToClaudeToolNames is the reverse mapping (built at init time).
var serfToClaudeToolNames map[string]string

func init() {
	serfToClaudeToolNames = make(map[string]string, len(claudeToSerfToolNames))
	for claude, serf := range claudeToSerfToolNames {
		serfToClaudeToolNames[serf] = claude
	}
}

// mapClaudeToolName converts a Claude Code tool name to serf's canonical name.
// Unknown names pass through unchanged.
func mapClaudeToolName(name string) string {
	if mapped, ok := claudeToSerfToolNames[name]; ok {
		return mapped
	}
	return name
}

// mapSerfToolNameToClaude converts a serf canonical tool name to Claude Code's name.
// Unknown names pass through unchanged.
func mapSerfToolNameToClaude(name string) string {
	if mapped, ok := serfToClaudeToolNames[name]; ok {
		return mapped
	}
	return name
}

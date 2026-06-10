// Package toolname translates tool names between serf's canonical vocabulary
// and Claude Code's. The two conventions meet when serf loads Claude-style
// plugins: plugin manifests and hook matchers name tools the Claude Code way
// ("Read", "Bash"), while serf's engine uses canonical names ("read_file",
// "shell").
package toolname

// claudeToSerf maps Claude Code tool names to serf canonical names.
var claudeToSerf = map[string]string{
	"Read":         "read_file",
	"Write":        "write_file",
	"Edit":         "edit_file",
	"Bash":         "shell",
	"Grep":         "grep",
	"Glob":         "glob",
	"Task":         "delegate",
	"WebFetch":     "web_fetch",
	"WebSearch":    "web_search",
	"NotebookEdit": "notebook_edit",
}

// serfToClaude is the reverse mapping (built at init time).
var serfToClaude map[string]string

func init() {
	serfToClaude = make(map[string]string, len(claudeToSerf))
	for claude, serf := range claudeToSerf {
		serfToClaude[serf] = claude
	}
}

// ClaudeToSerf converts a Claude Code tool name to serf's canonical name.
// Unknown names pass through unchanged.
func ClaudeToSerf(name string) string {
	if mapped, ok := claudeToSerf[name]; ok {
		return mapped
	}
	return name
}

// SerfToClaude converts a serf canonical tool name to Claude Code's name.
// Unknown names pass through unchanged.
func SerfToClaude(name string) string {
	if mapped, ok := serfToClaude[name]; ok {
		return mapped
	}
	return name
}

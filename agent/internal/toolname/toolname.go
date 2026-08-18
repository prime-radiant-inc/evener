// Package toolname translates tool names between evener's canonical vocabulary
// and Claude Code's. The two conventions meet when evener loads Claude-style
// plugins: plugin manifests and hook matchers name tools the Claude Code way
// ("Read", "Bash"), while evener's engine uses canonical names ("read_file",
// "shell").
package toolname

// claudeToSerf maps Claude Code tool names to evener canonical names.
var claudeToSerf = map[string]string{
	"Read":            "read_file",
	"Write":           "write_file",
	"Edit":            "edit_file",
	"Bash":            "shell",
	"Grep":            "grep",
	"Glob":            "glob",
	"Task":            "delegate",
	"WebFetch":        "web_fetch",
	"WebSearch":       "web_search",
	"NotebookEdit":    "notebook_edit",
	"AskUserQuestion": "ask_user",
}

// evenerToClaude is the reverse mapping (built at init time).
var evenerToClaude map[string]string

func init() {
	evenerToClaude = make(map[string]string, len(claudeToSerf))
	for claude, evener := range claudeToSerf {
		evenerToClaude[evener] = claude
	}
}

// ClaudeToSerf converts a Claude Code tool name to evener's canonical name.
// Unknown names pass through unchanged.
func ClaudeToSerf(name string) string {
	if mapped, ok := claudeToSerf[name]; ok {
		return mapped
	}
	return name
}

// EvenerToClaude converts a evener canonical tool name to Claude Code's name.
// Unknown names pass through unchanged.
func EvenerToClaude(name string) string {
	if mapped, ok := evenerToClaude[name]; ok {
		return mapped
	}
	return name
}

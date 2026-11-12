// Package toolname translates tool names between evener's canonical vocabulary
// and Claude Code's. The two conventions meet when evener loads Claude-style
// plugins: plugin manifests and hook matchers name tools the Claude Code way
// ("Read", "Bash"), while evener's engine uses canonical names ("read_file",
// "shell").
package toolname

import "strings"

// claudeToEvener maps Claude Code tool names to evener canonical names.
var claudeToEvener = map[string]string{
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
	"Skill":           "use_skill",
}

// evenerToClaude is the reverse mapping (built at init time).
var evenerToClaude map[string]string

func init() {
	evenerToClaude = make(map[string]string, len(claudeToEvener))
	for claude, evener := range claudeToEvener {
		evenerToClaude[evener] = claude
	}
}

// ClaudeToEvener converts a Claude Code tool name to evener's canonical name.
// Unknown names pass through unchanged.
func ClaudeToEvener(name string) string {
	if mapped, ok := claudeToEvener[name]; ok {
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

// SanitizeMCPToolName replaces characters invalid in LLM tool identifiers
// (such as hyphens) with underscores, matching mcp.Manager registration.
func SanitizeMCPToolName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// ImportClaudeToolName translates a tool name from a Claude Code manifest or
// agent frontmatter into evener's canonical registry namespace. Built-in tools
// map through ClaudeToEvener; Claude MCP names ("mcp__<server>__<tool>",
// including "mcp__plugin_<plugin>_<server>__<tool>") strip the "mcp__" prefix
// and apply SanitizeMCPToolName to match mcp.Manager registration.
func ImportClaudeToolName(name string) string {
	if mapped, ok := claudeToEvener[name]; ok {
		return mapped
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if server, tool, hasSep := strings.Cut(rest, "__"); hasSep && server != "" && tool != "" {
			return SanitizeMCPToolName(rest)
		}
	}
	return name
}

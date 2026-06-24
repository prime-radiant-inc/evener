package launchconfig

import (
	"strconv"
	"strings"
)

// ToArgs renders the Effective layer of Resolved into the argv slice
// `serf serve` understands. Order is deterministic and matches the order
// serf's flag parser sees them: scalars first, then list fields in the
// order they appear in the Layer struct.
func ToArgs(r Resolved) []string {
	var out []string
	add := func(flag, value string) {
		out = append(out, flag, value)
	}
	e := r.Effective
	if e.Model != "" {
		add("--model", e.Model)
	}
	if e.FastCheapModel != "" {
		add("--fast-cheap-model", e.FastCheapModel)
	}
	if e.Agent != "" {
		add("--agent", e.Agent)
	}
	if e.ReasoningEffort != "" {
		add("--reasoning-effort", e.ReasoningEffort)
	}
	if e.ContextStrategy != "" {
		add("--context-strategy", e.ContextStrategy)
	}
	if e.OpenAIResponsesContinuation != "" {
		add("--openai-responses-continuation", e.OpenAIResponsesContinuation)
	}
	if e.MaxRounds != nil {
		add("--max-rounds", strconv.Itoa(*e.MaxRounds))
	}
	if e.MaxSubagentDepth != nil {
		add("--max-subagent-depth", strconv.Itoa(*e.MaxSubagentDepth))
	}
	if e.NoProjectPrompts != nil && *e.NoProjectPrompts {
		out = append(out, "--no-project-prompts")
	}
	if e.NonInteractive != nil && *e.NonInteractive {
		out = append(out, "--non-interactive")
	}
	if e.AppReplaySize != nil {
		add("--app-replay-size", strconv.Itoa(*e.AppReplaySize))
	}
	if e.SystemPromptMode == "file" && e.SystemPromptFile != "" {
		add("--system-prompt", e.SystemPromptFile)
	}
	if e.SystemPromptAppendMode == "file" && e.SystemPromptAppendFile != "" {
		add("--system-prompt-append", e.SystemPromptAppendFile)
	}
	if e.Verbose != nil && *e.Verbose {
		out = append(out, "--verbose")
	}
	if e.TraceFile != "" {
		add("--trace", e.TraceFile)
	}
	if e.CPUProfile != "" {
		add("--cpu-profile", e.CPUProfile)
	}
	if e.ExportATIFPath != "" {
		add("--export-atif", e.ExportATIFPath)
	}
	for _, d := range e.SkillsDirs {
		add("--skills-dir", d)
	}
	for _, d := range e.PluginDirs {
		add("--plugin-dir", d)
	}
	for _, d := range e.MCPConfigs {
		add("--mcp-config", d)
	}
	for _, d := range e.SystemPromptAppend {
		add("--system-prompt-append", d)
	}
	for _, m := range e.ModelFallbacks {
		add("--model-fallback", m)
	}
	for _, m := range e.MCPs {
		spec := m.Name + ":" + m.Command
		if len(m.Args) > 0 {
			spec += " " + strings.Join(m.Args, " ")
		}
		add("--mcp", spec)
	}
	return out
}

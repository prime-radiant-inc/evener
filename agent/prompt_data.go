package agent

import (
	"strings"

	"primeradiant.com/serf/llm"
)

// PromptData is the template context for system prompt rendering.
// Assembled from session state; not a source of truth.
type PromptData struct {
	// Resolution context
	NonInteractive           bool
	Provider                 string // "openai", "anthropic", "gemini"
	Agent                    string // "coordinator", "implementer", "reviewer", etc.
	BaseInstructionsOverride string
	RolePromptOverride       string

	// Environment
	WorkingDir      string
	IsGitRepo       bool
	GitBranch       string
	Platform        string
	OSVersion       string
	Today           string
	Model           string // from profile, not EnvironmentInfo
	KnowledgeCutoff string

	// Git
	GitModifiedFiles      int
	GitUntrackedFiles     int
	GitRecentCommitTitles []string

	// Workspace
	WorkspaceTree string
	BuildInfo     string

	// Skills
	Skills               []SkillEntry
	HasUseSkill          bool
	ActivatedSkillBodies []string

	// Tools (three tiers)
	ProfileTools []ToolEntry
	MCPTools     []ToolEntry
	CustomTools  []ToolEntry

	// Tool availability for the current role/session
	CallableToolNames           []string
	UnavailableProfileToolNames []string

	// Available agents (for coordinator spawn_agent)
	AvailableAgents []AgentEntry

	// Project docs
	ProjectDocs []ProjectDoc

	// Result tool
	ResultToolName string // "communicate" or override

	// User instruction override (highest priority, appended last)
	UserInstructionOverride string

	// CLI appends (--system-prompt-append, applied after everything)
	CLIAppends []string
}

// SkillEntry is a skill for template rendering.
type SkillEntry struct {
	Name        string
	Description string
	Dir         string // directory path (for use_skill profiles)
	SkillFile   string // SKILL.md path (for read_file profiles)
}

// ToolEntry is a tool for template rendering.
type ToolEntry struct {
	Name        string
	Description string
}

// AgentEntry is a spawnable agent for template rendering.
type AgentEntry struct {
	Name        string
	Description string
}

func toolEntriesFromDefinitions(defs []llm.ToolDefinition) []ToolEntry {
	entries := make([]ToolEntry, 0, len(defs))
	for _, td := range defs {
		desc := strings.TrimSpace(td.Description)
		if desc == "" {
			desc = "(no description)"
		}
		entries = append(entries, ToolEntry{Name: td.Name, Description: desc})
	}
	return entries
}

func toolNamesFromDefinitions(defs []llm.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	seen := make(map[string]bool, len(defs))
	for _, td := range defs {
		if td.Name == "" || seen[td.Name] {
			continue
		}
		seen[td.Name] = true
		names = append(names, td.Name)
	}
	return names
}

func toolNameSetFromDefinitions(defs []llm.ToolDefinition) map[string]bool {
	names := make(map[string]bool, len(defs))
	for _, td := range defs {
		if td.Name == "" {
			continue
		}
		names[td.Name] = true
	}
	return names
}

func unavailableToolNames(profileDefs, actualDefs []llm.ToolDefinition) []string {
	actual := toolNameSetFromDefinitions(actualDefs)
	missing := make([]string, 0)
	for _, td := range profileDefs {
		if td.Name == "" || actual[td.Name] {
			continue
		}
		missing = append(missing, td.Name)
	}
	return missing
}

package agent

import (
	"strings"

	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// promptData is the template context for system prompt rendering.
// Assembled from session state; not a source of truth.
type promptData struct {
	// Resolution context
	NonInteractive           bool
	Provider                 string // "openai", "anthropic", "gemini"
	Agent                    string // public agent name, e.g. "default", "explorer", "coordinator"
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
	Skills               []skillEntry
	HasUseSkill          bool
	ActivatedSkillBodies []string

	// Tools (three tiers)
	ProfileTools []toolEntry
	MCPTools     []toolEntry
	CustomTools  []toolEntry

	// Tool availability for the current role/session
	CallableToolNames           []string
	UnavailableProfileToolNames []string

	// Available agents (for spawn_agent)
	AvailableAgents []agentEntry

	// Project docs
	ProjectDocs []ProjectDoc

	// Result tool
	ResultToolName string // "communicate" or override

	// User instruction override (highest priority, appended last)
	UserInstructionOverride string

	// CLI appends (--system-prompt-append, applied after everything)
	CLIAppends []string
}

// skillEntry is a skill for template rendering.
type skillEntry struct {
	Name        string
	CatalogName string // name shown in the system prompt skill catalog
	Description string
	Dir         string // directory path (for use_skill profiles)
	SkillFile   string // SKILL.md path (for read_file profiles)
}

func (s skillEntry) CatalogNameOrName() string {
	if strings.TrimSpace(s.CatalogName) != "" {
		return s.CatalogName
	}
	return s.Name
}

// toolEntry is a tool for template rendering.
type toolEntry struct {
	Name        string
	Description string
}

// agentTaskEntry is a summarized default task in a spawnable agent workflow.
type agentTaskEntry struct {
	Title                 string
	Description           string
	ReplacedByParentTasks bool
}

// agentEntry is a spawnable agent for template rendering.
type agentEntry struct {
	Name         string
	Description  string
	DefaultTools string
	TaskList     []agentTaskEntry
}

func toolEntriesFromDefinitions(defs []llm.ToolDefinition) []toolEntry {
	entries := make([]toolEntry, 0, len(defs))
	for _, td := range defs {
		desc := strings.TrimSpace(td.Description)
		if desc == "" {
			desc = "(no description)"
		}
		entries = append(entries, toolEntry{Name: td.Name, Description: desc})
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

func summarizeTaskPrompt(prompt string) string {
	text := strings.Join(strings.Fields(prompt), " ")
	if text == "" {
		return "(no description)"
	}
	for i, r := range text {
		switch r {
		case '.', '!', '?':
			return strings.TrimSpace(text[:i+1])
		}
	}
	if len(text) > 120 {
		return strings.TrimSpace(text[:117]) + "..."
	}
	return text
}

func agentTaskEntries(tasks []task.TaskTemplate) []agentTaskEntry {
	entries := make([]agentTaskEntry, 0, len(tasks))
	for _, task := range tasks {
		entries = append(entries, agentTaskEntry{
			Title:                 task.Title,
			Description:           summarizeTaskPrompt(task.Prompt),
			ReplacedByParentTasks: task.Insert == "parent_tasks",
		})
	}
	return entries
}

func formatToolNamesForPrompt(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, "`"+name+"`")
	}
	return strings.Join(parts, ", ")
}

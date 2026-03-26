package agent

// PromptData is the template context for system prompt rendering.
// Assembled from session state; not a source of truth.
type PromptData struct {
	// Resolution context
	NonInteractive bool
	Provider       string // "openai", "anthropic", "gemini"
	Agent          string // "coordinator", "implementer", "reviewer", etc.

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
	Skills      []SkillEntry
	HasUseSkill bool

	// Tools (three tiers)
	ProfileTools []ToolEntry
	MCPTools     []ToolEntry
	CustomTools  []ToolEntry

	// Available agents (for coordinator spawn_agent)
	AvailableAgents []AgentEntry

	// Project docs
	ProjectDocs []ProjectDoc

	// Root task — the original user-facing task from the root session.
	// Available to all sessions; subagent templates use it so implementers
	// see the full spec regardless of how the coordinator paraphrased.
	RootTask string

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

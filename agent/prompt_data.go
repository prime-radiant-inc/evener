package agent

import (
	"io/fs"
	"strings"

	"primeradiant.com/evener/agent/internal/frontmatter"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// promptData is the template context for system prompt rendering.
// Assembled from session state; not a source of truth.
type promptData struct {
	// Resolution context
	NonInteractive           bool
	BaseInstructionsOverride string
	// IsSubagent is true for a delegate session (isSubagentSession: a live
	// spawn, or a session whose persisted meta marks it a subagent, never a
	// forked root): delegates get their own delegation guidance and none of
	// the root-only sections.
	IsSubagent bool
	// Surface is the provider surface the session's profile speaks
	// ("openai", "anthropic", ...), for surface-specific guidance.
	Surface string
	// Role is the resolved role body: the role prompt override, or the bundled
	// agent definition's body without its frontmatter.
	Role string
	// TurnEndsProcess is true in a one-shot run (`evener run`, and every
	// delegate under it): the process exits once the turn's work drains, so a
	// background shell job still running then is stopped instead of waking the
	// session later.
	TurnEndsProcess bool
	// MemoryRead gates the memory guidance section: memory is enabled, bound,
	// and memory_read is callable. MemorySearch additionally requires
	// memory_search.
	MemoryRead   bool
	MemorySearch bool
	// MemorySaves is true when the session may be told to save memory (the
	// memory save tools are callable).
	MemorySaves bool
	// ProjectMemory is true when memory is readable and a project id is set.
	ProjectMemory bool

	// Environment
	WorkingDir      string
	IsGitRepo       bool
	GitBranch       string
	Platform        string
	OSVersion       string
	Today           string
	Model           string // from profile, not EnvironmentInfo
	KnowledgeCutoff string
	// ResourceCapsJSON is an omitted-when-empty machine payload for the environment
	// section. It is derived from the trusted structured environment snapshot.
	ResourceCapsJSON string
	// Sandbox is the pre-rendered environment-section sandbox line for a sandboxed
	// session ("<mode> (network on|off) — fixed for this session", plus the
	// scratch directory path when one has been provisioned); empty when the
	// session is unsandboxed, so the line is omitted (byte-identical to today).
	Sandbox string
	// Capabilities are the capability-preamble lines that follow the sandbox
	// line: writable roots, masked-path count, PATH source, scratch vars, cache
	// facts, and the session-start toolchain probe. Every line states a resolved
	// policy fact or a measurement; an unrunnable probe renders "unprobed". An
	// unsandboxed session gets this same block without the sandbox-derived lines
	// (session_capabilities.go).
	Capabilities []string

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

	// Tool availability for the current role/session
	CallableToolNames           []string
	UnavailableProfileToolNames []string

	// CallableTools is the set of canonical tool names this session's registry
	// actually serves. Sections ask it through HasTool so a canned instruction
	// never names a tool the session cannot call (ruled 2026-08-06) — the
	// prompt-side twin of Session.canInstructTool.
	CallableTools map[string]bool

	// HasAskUser gates the ask-user prompt section (spec §4.5): true exactly
	// when ask_user is registered, i.e. an interactive root session (spec §7).
	HasAskUser bool

	// HasEndReason gates the end_reason guidance: true exactly when the
	// session's communicate takes end_reason, i.e. a root someone can answer.
	HasEndReason bool

	// Delegation capability (spec §1, §5): CanDelegate is true when this session
	// has a grantable allowance (> 0) and the delegation tools are actually
	// callable. Drives the subagent template's conditional delegation/background-
	// jobs sections and the stated allowance.
	CanDelegate         bool
	DelegationAllowance int

	// Available agents (for delegate)
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

// HasTool reports whether the session can actually call the named tool, by its
// canonical name. Prompt sections call it — `{{ if .HasTool "read_transcript" }}`
// — so instruction text is written only for tools the session has; a typed
// agent's tools: allowlist deletes the rest, and a page of instructions for
// deleted tools is a page the model can only fail.
func (d promptData) HasTool(name string) bool {
	return d.CallableTools[name]
}

// resolveRolePrompt returns the role body and its PROMPT_LOADED source: the
// role prompt override when one is set, otherwise the bundled agent
// definition's body with its frontmatter stripped. A bundled definition
// reports its source even when its body is empty; an agent with no
// definition, or one that does not parse, has no role and no source.
func resolveRolePrompt(override, agentName string, agents fs.FS) (string, *promptSource) {
	if body := strings.TrimSpace(override); body != "" {
		return body, &promptSource{Label: "config:role_prompt_override", Size: len(body)}
	}
	raw, err := fs.ReadFile(agents, agentName+".md")
	if err != nil {
		return "", nil
	}
	doc, err := frontmatter.Parse(string(raw))
	if err != nil {
		return "", nil
	}
	body := strings.TrimSpace(doc.Body)
	return body, &promptSource{Label: "agent:" + agentName, Size: len(body)}
}

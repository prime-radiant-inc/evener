package agent

import (
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/internal/llm"
)

// embeddedBasePrompt resolves the embedded default system prompt for a provider.
// Used by profile constructors; overridden at session creation with full resolution.
func embeddedBasePrompt(provider string) string {
	prompt, err := ResolveSystemPrompt(provider, "", "", "", "", nil)
	if err != nil {
		return ""
	}
	return prompt
}

type EnvironmentInfo struct {
	WorkingDir            string   `json:"working_dir"`
	Platform              string   `json:"platform"`
	OSVersion             string   `json:"os_version"`
	Today                 string   `json:"today"`                    // YYYY-MM-DD
	KnowledgeCutoff       string   `json:"knowledge_cutoff"`         // YYYY-MM-DD
	IsGitRepo             bool     `json:"is_git_repo"`
	GitBranch             string   `json:"git_branch,omitempty"`
	GitOriginURL          string   `json:"git_origin_url,omitempty"`
	GitModifiedFiles      int      `json:"git_modified_files"`
	GitUntrackedFiles     int      `json:"git_untracked_files"`
	GitRecentCommitTitles []string `json:"git_recent_commit_titles,omitempty"`
}

type ProviderProfile interface {
	ID() string
	Model() string
	ToolDefinitions() []llm.ToolDefinition
	SupportsParallelToolCalls() bool
	ContextWindowSize() int
	ProjectDocFiles() []string
	BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc, skills []SkillMeta) string
	CheapModel() string
	WithModel(model string) ProviderProfile
	WithBasePrompt(prompt string) ProviderProfile
	ProviderOptions() map[string]any
	SupportsReasoning() bool
	SupportsStreaming() bool
	DefaultCommandTimeoutMS() int
	KnowledgeCutoff() string
	// ToolNameMap returns the canonical→provider-specific tool name mapping.
	// Returns nil for providers that use canonical names (e.g. Anthropic).
	ToolNameMap() map[string]string
}

type baseProfile struct {
	id             string
	model          string
	parallel       bool
	contextWindow  int
	basePrompt     string
	toolDefs       []llm.ToolDefinition
	toolNameMap    map[string]string // canonical → provider-specific
	docFiles       []string
	reasoning       bool
	streaming       bool
	defaultTimeout  int
	knowledgeCutoff string
	providerOpts    map[string]any
}

func (p *baseProfile) ID() string    { return p.id }
func (p *baseProfile) Model() string { return p.model }
func (p *baseProfile) ToolDefinitions() []llm.ToolDefinition {
	defs := append([]llm.ToolDefinition{}, p.toolDefs...)
	for i, d := range defs {
		if mapped, ok := p.toolNameMap[d.Name]; ok {
			defs[i].Name = mapped
		}
	}
	return defs
}
func (p *baseProfile) ToolNameMap() map[string]string {
	if len(p.toolNameMap) == 0 {
		return nil
	}
	m := make(map[string]string, len(p.toolNameMap))
	for k, v := range p.toolNameMap {
		m[k] = v
	}
	return m
}
func (p *baseProfile) SupportsParallelToolCalls() bool { return p.parallel }
func (p *baseProfile) ContextWindowSize() int          { return p.contextWindow }
func (p *baseProfile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}
func (p *baseProfile) ProviderOptions() map[string]any    { return p.providerOpts }
func (p *baseProfile) SupportsReasoning() bool            { return p.reasoning }
func (p *baseProfile) SupportsStreaming() bool             { return p.streaming }
func (p *baseProfile) DefaultCommandTimeoutMS() int       { return p.defaultTimeout }
func (p *baseProfile) KnowledgeCutoff() string            { return p.knowledgeCutoff }
func (p *baseProfile) CheapModel() string {
	switch p.id {
	case "openai":
		return "gpt-4.1-nano"
	case "anthropic":
		return "claude-haiku-4-5-20251001"
	case "gemini":
		return "gemini-2.5-flash-lite"
	default:
		return p.model
	}
}
func (p *baseProfile) WithModel(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.model
	}
	clone := *p
	clone.model = model
	return &clone
}

func (p *baseProfile) WithBasePrompt(prompt string) ProviderProfile {
	clone := *p
	clone.basePrompt = prompt
	return &clone
}

func (p *baseProfile) BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc, skills []SkillMeta) string {
	var b strings.Builder

	base := strings.TrimSpace(p.basePrompt)
	if base != "" {
		b.WriteString(base)
		if !strings.HasSuffix(base, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("<environment>\n")
	b.WriteString(fmt.Sprintf("Working directory: %s\n", env.WorkingDir))
	b.WriteString(fmt.Sprintf("Is git repository: %t\n", env.IsGitRepo))
	b.WriteString(fmt.Sprintf("Git branch: %s\n", env.GitBranch))
	b.WriteString(fmt.Sprintf("Platform: %s\n", env.Platform))
	b.WriteString(fmt.Sprintf("OS version: %s\n", env.OSVersion))
	b.WriteString(fmt.Sprintf("Today's date: %s\n", env.Today))
	b.WriteString(fmt.Sprintf("Model: %s\n", p.model))
	b.WriteString(fmt.Sprintf("Knowledge cutoff: %s\n", env.KnowledgeCutoff))
	b.WriteString("</environment>\n\n")

	if env.IsGitRepo {
		b.WriteString("<git>\n")
		b.WriteString(fmt.Sprintf("Branch: %s\n", env.GitBranch))
		b.WriteString(fmt.Sprintf("Modified files: %d\n", env.GitModifiedFiles))
		b.WriteString(fmt.Sprintf("Untracked files: %d\n", env.GitUntrackedFiles))
		if len(env.GitRecentCommitTitles) > 0 {
			b.WriteString("Recent commits:\n")
			for _, c := range env.GitRecentCommitTitles {
				b.WriteString("- " + c + "\n")
			}
		}
		b.WriteString("</git>\n\n")
	}

	if len(skills) > 0 {
		b.WriteString("<skills>\n")
		for _, s := range skills {
			b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		}
		b.WriteString("</skills>\n\n")
	}

	b.WriteString("Tools:\n")
	for _, td := range p.ToolDefinitions() {
		desc := strings.TrimSpace(td.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString(fmt.Sprintf("- %s: %s\n", td.Name, desc))
	}
	b.WriteString("\nTool usage:\n")
	b.WriteString("- Use tools to inspect the codebase before editing.\n")
	b.WriteString("- When editing code, prefer the provider-aligned edit tool for this profile.\n")
	b.WriteString("- After running commands, read errors carefully and fix them.\n")

	for _, d := range docs {
		if strings.TrimSpace(d.Path) == "" {
			continue
		}
		b.WriteString("\n----- BEGIN " + d.Path + " -----\n")
		b.WriteString(d.Content)
		if !strings.HasSuffix(d.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("----- END " + d.Path + " -----\n")
	}
	return b.String()
}

func NewOpenAIProfile(model string) ProviderProfile {
	return &baseProfile{
		id:              "openai",
		model:           strings.TrimSpace(model),
		parallel:        false,
		contextWindow:   128_000,
		basePrompt:      embeddedBasePrompt("openai"),
		docFiles:        []string{"AGENTS.md", ".codex/instructions.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  10_000,
		knowledgeCutoff: "2025-06-01",
		toolNameMap: map[string]string{
			"shell": "exec_command",
			"grep":  "grep_files",
			"glob":  "list_dir",
		},
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defApplyPatch(),
			defWriteFile(),
			defShell(),
			defGrep(),
			defGlob(),
			defSpawnAgent(),
			defSendInput(),
			defWait(),
			defCloseAgent(),
			defTaskList(),
			defWebFetch(),
			defCommunicate(),
			defUseSkill(),
		},
	}
}

func NewAnthropicProfile(model string) ProviderProfile {
	return &baseProfile{
		id:              "anthropic",
		model:           strings.TrimSpace(model),
		parallel:        true,
		contextWindow:   200_000,
		basePrompt:      embeddedBasePrompt("anthropic"),
		docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-04-01",
		providerOpts: map[string]any{
			"anthropic": map[string]any{
				"beta_headers": "extended-thinking-2025-04-11,prompt-caching-2024-07-31",
			},
		},
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defWriteFile(),
			defEditFile(),
			defShell(),
			defGrep(),
			defGlob(),
			defSpawnAgent(),
			defSendInput(),
			defWait(),
			defCloseAgent(),
			defTaskList(),
			defWebFetch(),
			defCommunicate(),
			defUseSkill(),
		},
	}
}

func NewGeminiProfile(model string) ProviderProfile {
	return &baseProfile{
		id:              "gemini",
		model:           strings.TrimSpace(model),
		parallel:        true,
		contextWindow:   128_000,
		basePrompt:      embeddedBasePrompt("gemini"),
		docFiles:        []string{"GEMINI.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  10_000,
		knowledgeCutoff: "2025-03-01",
		providerOpts: map[string]any{
			"gemini": map[string]any{
				"safetySettings": []map[string]any{
					{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"},
					{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_ONLY_HIGH"},
					{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_ONLY_HIGH"},
					{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_ONLY_HIGH"},
				},
			},
		},
		toolNameMap: map[string]string{
			"shell":    "run_shell_command",
			"grep":     "grep_search",
			"list_dir": "list_directory",
		},
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defReadManyFiles(),
			defWriteFile(),
			defEditFile(),
			defShell(),
			defGrep(),
			defGlob(),
			defListDir(),
			defSpawnAgent(),
			defSendInput(),
			defWait(),
			defCloseAgent(),
			defTaskList(),
			defWebFetch(),
			defWebSearch(),
			defCommunicate(),
			defUseSkill(),
		},
	}
}

func envInfoFromEnv(env ExecutionEnvironment) EnvironmentInfo {
	wd := ""
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
	}
	return EnvironmentInfo{
		WorkingDir: wd,
		Platform:   plat,
		OSVersion:  osv,
		Today:      time.Now().UTC().Format("2006-01-02"),
	}
}

func defReadFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_file",
		Description: "Read a file from the filesystem. Returns line-numbered content.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"offset":    map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
			},
			"required": []string{"file_path"},
		},
	}
}

func defReadManyFiles() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_many_files",
		Description: "Read multiple files from the filesystem. Returns a concatenated, line-numbered output for each file.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"offset":     map[string]any{"type": "integer"},
				"limit":      map[string]any{"type": "integer"},
			},
			"required": []string{"file_paths"},
		},
	}
}

func defWriteFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "write_file",
		Description: "Write content to a file. Creates the file and parent directories if needed.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"content":   map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

func defListDir() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "list_dir",
		Description: "List directory contents. Depth controls recursion (1 = this directory only).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"depth": map[string]any{"type": "integer"},
			},
		},
	}
}

func defEditFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace an exact string occurrence in a file.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path":   map[string]any{"type": "string"},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}
}

func defShell() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "shell",
		Description: "Execute a shell command. Returns stdout, stderr, and exit code.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"command":     map[string]any{"type": "string"},
				"timeout_ms":  map[string]any{"type": "integer"},
				"description": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		},
	}
}

func defGrep() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents using regex patterns.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern":          map[string]any{"type": "string"},
				"path":             map[string]any{"type": "string"},
				"glob_filter":      map[string]any{"type": "string"},
				"case_insensitive": map[string]any{"type": "boolean"},
				"max_results":      map[string]any{"type": "integer"},
			},
			"required": []string{"pattern"},
		},
	}
}

func defGlob() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "glob",
		Description: "Find files matching a glob pattern.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		},
	}
}

func defApplyPatch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "apply_patch",
		Description: "Apply code changes using the v4a patch format.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"patch": map[string]any{"type": "string"},
			},
			"required": []string{"patch"},
		},
	}
}

func defSpawnAgent() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "spawn_agent",
		Description: "Spawn a sub-agent to work on a scoped task.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task":        map[string]any{"type": "string"},
				"model":       map[string]any{"type": "string", "description": "Model override (default: parent model)"},
				"working_dir": map[string]any{"type": "string", "description": "Subdirectory to scope the agent to"},
				"max_turns":   map[string]any{"type": "integer", "description": "Turn limit for the subagent (default: 50)"},
			},
			"required": []string{"task"},
		},
	}
}

func defSendInput() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "send_input",
		Description: "Send input to a sub-agent.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
				"message":  map[string]any{"type": "string"},
			},
			"required": []string{"agent_id", "message"},
		},
	}
}

func defWait() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "wait",
		Description: "Wait for a sub-agent to finish and return its result.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id":   map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
			"required": []string{"agent_id"},
		},
	}
}

func defCloseAgent() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "close_agent",
		Description: "Close a sub-agent session.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
			},
			"required": []string{"agent_id"},
		},
	}
}

func defWebFetch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch a URL, convert HTML to markdown, cache the results, and answer a question about the content using a cheap model.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"url":      map[string]any{"type": "string", "description": "The URL to fetch (http or https)."},
				"question": map[string]any{"type": "string", "description": "What you want to know about the page content."},
			},
			"required": []string{"url", "question"},
		},
	}
}

func defWebSearch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web for current information. Returns grounded results from Google Search. Use when you need up-to-date facts, documentation, error messages, or API references.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query.",
				},
			},
			"required": []string{"query"},
		},
	}
}

func defCommunicate() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "communicate",
		Description: "Send a message to the user. Use action=status for progress updates, action=result for the final answer (exits the session).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"status", "result"},
				},
				"message": map[string]any{"type": "string"},
			},
			"required": []string{"action", "message"},
		},
	}
}

func defTaskList() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "task_list",
		Description: "Manage a persistent task list. Actions: view (show all tasks), append (add new tasks), update (change task status to undone/in_progress/done/cancelled).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"view", "append", "update"},
				},
				"tasks": map[string]any{
					"type":        "array",
					"description": "For append: tasks to add. Each has a brief description (<10 words) and a detailed prompt.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"description": map[string]any{"type": "string"},
							"prompt":      map[string]any{"type": "string"},
						},
						"required": []string{"description", "prompt"},
					},
				},
				"updates": map[string]any{
					"type":        "array",
					"description": "For update: list of {id, status} pairs.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":     map[string]any{"type": "integer"},
							"status": map[string]any{"type": "string", "enum": []string{"undone", "in_progress", "done", "cancelled"}},
						},
						"required": []string{"id", "status"},
					},
				},
			},
			"required": []string{"action"},
		},
	}
}

func defUseSkill() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "use_skill",
		Description: "Activate a skill to load its full instructions into context. Available skills are listed in the <skills> section of the system prompt.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"skill_name": map[string]any{"type": "string", "description": "Name of the skill to activate."},
			},
			"required": []string{"skill_name"},
		},
	}
}

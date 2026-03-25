package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
)

type EnvironmentInfo struct {
	WorkingDir            string        `json:"working_dir"`
	Platform              string        `json:"platform"`
	OSVersion             string        `json:"os_version"`
	Today                 string        `json:"today"`            // YYYY-MM-DD
	KnowledgeCutoff       string        `json:"knowledge_cutoff"` // YYYY-MM-DD
	IsGitRepo             bool          `json:"is_git_repo"`
	GitBranch             string        `json:"git_branch,omitempty"`
	GitOriginURL          string        `json:"git_origin_url,omitempty"`
	GitModifiedFiles      int           `json:"git_modified_files"`
	GitUntrackedFiles     int           `json:"git_untracked_files"`
	GitRecentCommitTitles []string      `json:"git_recent_commit_titles,omitempty"`
	Workspace             WorkspaceInfo `json:"workspace,omitempty"`
}

type ProviderProfile interface {
	ID() string
	Model() string
	ToolDefinitions() []llm.ToolDefinition
	SupportsParallelToolCalls() bool
	ContextWindowSize() int
	ProjectDocFiles() []string
	BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc, skills []SkillMeta, extraTools string) string
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
	// NewToolRegistry returns a ToolRegistry pre-populated with the profile's
	// tool definitions and placeholder executors. The Session wires real
	// executors after construction.
	NewToolRegistry() *ToolRegistry
}

type baseProfile struct {
	id              string
	model           string
	parallel        bool
	contextWindow   int
	basePrompt      string
	toolDefs        []llm.ToolDefinition
	toolNameMap     map[string]string // canonical → provider-specific
	docFiles        []string
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

func (p *baseProfile) NewToolRegistry() *ToolRegistry {
	reg := NewToolRegistry()
	for _, td := range p.toolDefs {
		_ = reg.Register(RegisteredTool{
			Tool: llm.Tool{Definition: td},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				return nil, fmt.Errorf("tool executor not wired")
			},
		})
	}
	return reg
}
func (p *baseProfile) SupportsParallelToolCalls() bool { return p.parallel }
func (p *baseProfile) ContextWindowSize() int          { return p.contextWindow }
func (p *baseProfile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}
func (p *baseProfile) ProviderOptions() map[string]any { return p.providerOpts }
func (p *baseProfile) SupportsReasoning() bool         { return p.reasoning }
func (p *baseProfile) SupportsStreaming() bool         { return p.streaming }
func (p *baseProfile) DefaultCommandTimeoutMS() int    { return p.defaultTimeout }
func (p *baseProfile) KnowledgeCutoff() string         { return p.knowledgeCutoff }
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
	// Parse "provider/model" strings (e.g. "openai/gpt-5.4-mini") into the
	// correct provider profile with the bare model name. This is the same
	// format used by harbor and the CLI (--model openai/gpt-5.4).
	if parts := strings.SplitN(model, "/", 2); len(parts) == 2 {
		provider := strings.ToLower(parts[0])
		bareModel := parts[1]
		if provider != p.id {
			// Different provider — construct the right profile type.
			switch provider {
			case "openai":
				return NewOpenAIProfile(bareModel)
			case "anthropic":
				return NewAnthropicProfile(bareModel)
			case "google", "gemini":
				return NewGeminiProfile(bareModel)
			}
		}
		// Same provider — just use the bare model name.
		model = bareModel
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

func (p *baseProfile) BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc, skills []SkillMeta, extraTools string) string {
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
	if env.Workspace.Tree != "" {
		b.WriteString("Workspace:\n" + env.Workspace.Tree + "\n")
	}
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

	// Workspace context: directory tree and build system info.
	// Injected so the model starts with full workspace awareness.
	if env.Workspace.Tree != "" || env.Workspace.BuildInfo != "" {
		b.WriteString("<workspace>\n")
		b.WriteString("This is a snapshot of the working directory taken at session start. It does not update.\n\n")
		if env.Workspace.Tree != "" {
			b.WriteString("Directory structure:\n")
			b.WriteString(env.Workspace.Tree)
			b.WriteString("\n\n")
		}
		if env.Workspace.BuildInfo != "" {
			b.WriteString("Build system:\n")
			b.WriteString(env.Workspace.BuildInfo)
			b.WriteString("\n")
		}
		b.WriteString("</workspace>\n\n")
	}

	// Skills section: always rendered when skills are available.
	// For profiles with use_skill (Anthropic, Gemini): model calls use_skill(name).
	// For profiles without use_skill (OpenAI): model reads the SKILL.md file directly.
	if len(skills) > 0 {
		hasUseSkill := false
		for _, td := range p.ToolDefinitions() {
			if td.Name == "use_skill" {
				hasUseSkill = true
				break
			}
		}
		b.WriteString("<skills>\n")
		if hasUseSkill {
			b.WriteString("Load a skill by calling use_skill with its name. The response includes the skill directory path for accessing scripts and other collateral.\n")
		} else {
			b.WriteString("Load a skill by reading its SKILL.md file path with read_file. The skill directory may contain scripts and other collateral.\n")
		}
		for _, s := range skills {
			if hasUseSkill {
				b.WriteString(fmt.Sprintf("- %s: %s [%s]\n", s.Name, s.Description, s.Dir))
			} else {
				b.WriteString(fmt.Sprintf("- %s: %s [%s]\n", s.Name, s.Description, s.SkillFile))
			}
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

	if extra := strings.TrimSpace(extraTools); extra != "" {
		b.WriteString("\n")
		b.WriteString(extra)
		if !strings.HasSuffix(extra, "\n") {
			b.WriteString("\n")
		}
	}

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
		parallel:        true,
		contextWindow:   128_000,
		basePrompt:      "", // set by initSessionState via template system
		docFiles:        []string{"AGENTS.md", ".codex/instructions.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		providerOpts: map[string]any{
			"openai": map[string]any{
				"parallel_tool_calls": true,
			},
		},
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
			defSubmitResult(),
		},
	}
}

const anthropicSuffix1M = "[1m]"
const anthropicBeta1M = "context-1m-2025-08-07"

// anthropicProviderOpts builds a fresh providerOpts map for the Anthropic
// profile. When has1M is true the 1M-context beta header is included.
func anthropicProviderOpts(has1M bool) map[string]any {
	opts := map[string]any{
		// Prevent truncated tool-call JSON on large code/test edits.
		"max_tokens": 16384,
	}
	if has1M {
		opts["beta_headers"] = anthropicBeta1M
	}
	return map[string]any{
		"anthropic": opts,
	}
}

// anthropicProfile embeds baseProfile and overrides WithModel / WithBasePrompt
// to re-derive contextWindow and providerOpts from the model string.
type anthropicProfile struct {
	baseProfile
}

func (p *anthropicProfile) WithModel(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.model
	}
	// Parse "provider/model" strings — delegate to the right profile type.
	if parts := strings.SplitN(model, "/", 2); len(parts) == 2 {
		provider := strings.ToLower(parts[0])
		bareModel := parts[1]
		if provider != "anthropic" {
			switch provider {
			case "openai":
				return NewOpenAIProfile(bareModel)
			case "google", "gemini":
				return NewGeminiProfile(bareModel)
			}
		}
		model = bareModel
	}
	clone := *p
	clone.model = model
	has1M := strings.HasSuffix(model, anthropicSuffix1M)
	if has1M {
		clone.contextWindow = 1_000_000
	} else {
		clone.contextWindow = 200_000
	}
	clone.providerOpts = anthropicProviderOpts(has1M)
	return &clone
}

func (p *anthropicProfile) WithBasePrompt(prompt string) ProviderProfile {
	clone := *p
	clone.basePrompt = prompt
	return &clone
}

func NewAnthropicProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	has1M := strings.HasSuffix(model, anthropicSuffix1M)
	ctxWindow := 200_000
	if has1M {
		ctxWindow = 1_000_000
	}
	return &anthropicProfile{
		baseProfile: baseProfile{
			id:              "anthropic",
			model:           model,
			parallel:        true,
			contextWindow:   ctxWindow,
			basePrompt:      "", // set by initSessionState via template system
			docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
			reasoning:       true,
			streaming:       true,
			defaultTimeout:  120_000,
			knowledgeCutoff: "2025-04-01",
			providerOpts:    anthropicProviderOpts(has1M),
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
				defSubmitResult(),
				defUseSkill(),
			},
		},
	}
}

func NewGeminiProfile(model string) ProviderProfile {
	return &baseProfile{
		id:              "gemini",
		model:           strings.TrimSpace(model),
		parallel:        true,
		contextWindow:   1_000_000,
		basePrompt:      "", // set by initSessionState via template system
		docFiles:        []string{"GEMINI.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  120_000,
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
			defSubmitResult(),
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
		WorkingDir:  wd,
		Platform:    plat,
		OSVersion:   osv,
		Today:       time.Now().UTC().Format("2006-01-02"),
		Workspace:  ScanWorkspace(wd),
	}
}

func defReadFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_file",
		Description: "Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. When reading an image, describe what you hope to learn — the system will provide a detailed description alongside the image.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"offset":    map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
				"purpose":   map[string]any{"type": "string", "description": "For image files: what do you hope to learn by looking at this image?"},
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
				"output_mode": map[string]any{
					"type":        "string",
					"enum":        []any{"content", "files_with_matches", "count"},
					"description": "Output format: content (default, matching lines), files_with_matches (file paths only), count (match counts per file)",
				},
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
		Name: "apply_patch",
		Description: `Apply code changes using the v4a patch format. Supports creating, deleting, and modifying files in a single operation.

The patch format is a stripped-down, file-oriented diff. The envelope is:

*** Begin Patch
[ one or more file sections ]
*** End Patch

Each section starts with one of three headers:

*** Add File: <path>    — create a new file. Every following line is a + line.
*** Delete File: <path> — remove an existing file. Nothing follows.
*** Update File: <path> — patch an existing file (optionally with a rename).

An Update may be followed by *** Move to: <new path> to rename the file.
Then one or more hunks, each introduced by @@ (optionally followed by a scope header).

Within a hunk, each line starts with:
  (space) — context line (unchanged)
  -       — line to remove
  +       — line to add

Context rules:
- Show 3 lines of context above and below each change.
- If 3 lines are not enough to uniquely locate the hunk, add @@ scope headers:
  @@ class MyClass
  @@ def my_method():
  [3 context lines]
  - old_code
  + new_code
  [3 context lines]

Example combining all operations:

*** Begin Patch
*** Add File: hello.txt
+Hello world
*** Update File: src/app.py
*** Move to: src/main.py
@@ def greet():
-print("Hi")
+print("Hello, world!")
*** Delete File: obsolete.txt
*** End Patch

Important:
- Always include a header (Add/Delete/Update) for each file.
- Prefix every new line with + even when creating a new file.
- File paths must be relative, NEVER absolute.
- Do NOT use standard unified diff format (--- a/ +++ b/). Use only the format above.
- Try to use apply_patch for single file edits. Use scripting for bulk search-and-replace.`,
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
				"max_turns":   map[string]any{"type": "integer", "description": "Turn limit for the subagent (default: 500)"},
				"agent_type":  map[string]any{"type": "string", "description": "Agent type (e.g. 'explorer' for built-in, or 'plugin-name:agent-name' for plugin agents)"},
				"blocking":          map[string]any{"type": "boolean", "description": "When true, spawns the agent and waits for completion in a single call, returning the result directly. Do NOT call wait() after a blocking spawn — the result is already in the response. Default is false (async). Use blocking=false only when you need to run multiple agents in parallel, then call wait() on each agent_id."},
				"reasoning_effort": map[string]any{"type": "string", "description": "Reasoning effort for this subagent: low, medium, high, or xhigh. Default inherits from parent. Use high/xhigh for complex tasks."},
			},
			"required": []string{"task"},
		},
	}
}

func defSendInput() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "resume_agent",
		Description: "Resume a sub-agent with new instructions. The agent keeps all its previous context (files read, analysis done, code written) and continues from where it left off. Use this instead of spawning a new agent when you want to iterate — e.g. send reviewer feedback to an implementer, or ask a planner to revise. Use blocking=true (recommended) to wait for the result in one call.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
				"message":  map[string]any{"type": "string"},
				"blocking": map[string]any{
					"type":        "boolean",
					"description": "When true, sends the message and waits for the agent to finish, returning the result directly. Do NOT call wait() after a blocking resume. Default is false.",
				},
			},
			"required": []string{"agent_id", "message"},
		},
	}
}

func defWait() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "wait",
		Description: "Wait for a non-blocking sub-agent to finish and return its result. Only use this after spawn_agent with blocking=false. Do NOT use after blocking=true — that already returned the result. Use timeout_ms of 300000 (5 minutes) or more — short timeouts waste rounds on retries.",
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

func defSubmitResult() llm.ToolDefinition {
	return defSubmitResultNamed("communicate")
}

func defSubmitResultNamed(name string) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: name,
		Description: "Submit your result and exit the session. Only call when work is complete and verified.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Human-readable summary of what was accomplished.",
				},
				"output": map[string]any{
					"type":                 "object",
					"description":          "Structured output (optional).",
					"additionalProperties": false,
					"properties": map[string]any{
						"message": map[string]any{"type": "string"},
						"data":    map[string]any{"type": "object"},
						"artifacts": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
					},
					"required": []string{"message", "data"},
				},
			},
		},
	}
}

func defTaskList() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "task_list",
		Description: "Manage a persistent task list. Actions: view (show all tasks), append (add new tasks), update (change task status to open/in_progress/done/cancelled). Implementation tasks automatically get a verification task created after them.",
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
					"description": "For append: tasks to add. Each has a type, brief description (<10 words), and a detailed prompt.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type": map[string]any{
								"type":        "string",
								"enum":        []string{"research", "implement", "verify"},
								"description": "Task type. 'implement' tasks automatically get a verification task.",
							},
							"description": map[string]any{"type": "string"},
							"prompt":      map[string]any{"type": "string"},
							"depends_on": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "IDs of tasks this one depends on. Optional.",
							},
						},
						"required": []string{"type", "description", "prompt"},
					},
				},
				"updates": map[string]any{
					"type":        "array",
					"description": "For update: list of {id, status} pairs with optional notes.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":     map[string]any{"type": "integer"},
							"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "cancelled"}},
							"notes":  map[string]any{"type": "string", "description": "Document what you tried and why it failed or succeeded. Appended to the task's notes log."},
							"depends_on": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "Set dependencies. [] clears them. Omit to leave unchanged.",
							},
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


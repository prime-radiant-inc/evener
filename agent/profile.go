package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
)

// resolveEffortLevels returns reasoning effort levels for the given model.
// It first checks the embedded model catalog for model-specific levels, then
// falls back to the provider default. This allows per-model effort vocabularies
// while maintaining backward compatibility.
func resolveEffortLevels(model string, providerDefault []string) []string {
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.GetModelInfo(model); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
			return append([]string(nil), mi.ReasoningEffortLevels...)
		}
	}
	return providerDefault
}

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
	CheapModel() string
	WithModel(model string) ProviderProfile
	ProviderOptions() map[string]any
	SupportsReasoning() bool
	// ReasoningEffortLevels returns the valid effort strings this provider
	// accepts, in ascending order. Returns an empty slice when the provider
	// does not support reasoning control.
	ReasoningEffortLevels() []string
	SupportsStreaming() bool
	SupportsWebSearch() bool
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
	toolDefs        []llm.ToolDefinition
	toolNameMap     map[string]string // canonical → provider-specific
	docFiles        []string
	reasoning       bool
	streaming       bool
	defaultTimeout  int
	knowledgeCutoff string
	providerOpts    map[string]any
	effortLevels    []string
	webSearch       bool
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
func (p *baseProfile) ReasoningEffortLevels() []string {
	return append([]string(nil), p.effortLevels...)
}
func (p *baseProfile) SupportsStreaming() bool      { return p.streaming }
func (p *baseProfile) SupportsWebSearch() bool      { return p.webSearch }
func (p *baseProfile) DefaultCommandTimeoutMS() int { return p.defaultTimeout }
func (p *baseProfile) KnowledgeCutoff() string      { return p.knowledgeCutoff }
func (p *baseProfile) CheapModel() string {
	switch p.id {
	case "openai":
		return "gpt-4.1-nano"
	case "anthropic":
		return "claude-haiku-4-5-20251001"
	case "gemini":
		return "gemini-2.5-flash-lite"
	case "glm":
		return "glm-4.7-flash"
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
			case "minimax":
				return NewMiniMaxProfile(bareModel)
			case "openrouter-anthropic":
				return NewOpenRouterAnthropicProfile(bareModel)
			case "kimi", "glm", "openrouter", "ollama":
				return NewOpenAICompatProfile(provider, bareModel, 0)
			}
		}
		// Same provider — just use the bare model name.
		model = bareModel
	}
	clone := *p
	clone.model = model
	return &clone
}

func NewOpenAIProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	defaultEfforts := []string{"low", "medium", "high", "xhigh"}
	efforts := resolveEffortLevels(model, defaultEfforts)
	return &baseProfile{
		id:              "openai",
		model:           model,
		parallel:        true,
		contextWindow:   128_000,
		docFiles:        []string{"AGENTS.md", ".codex/instructions.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		effortLevels:    efforts,
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
			defTaskList(efforts),
			defWebFetch(),
			defCommunicate(),
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

// anthropicProfile embeds baseProfile and overrides WithModel to re-derive
// contextWindow and providerOpts from the model string.
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
			case "minimax":
				return NewMiniMaxProfile(bareModel)
			case "openrouter-anthropic":
				return NewOpenRouterAnthropicProfile(bareModel)
			case "kimi", "glm", "openrouter", "ollama":
				return NewOpenAICompatProfile(provider, bareModel, 0)
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

func NewAnthropicProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	has1M := strings.HasSuffix(model, anthropicSuffix1M)
	ctxWindow := 200_000
	if has1M {
		ctxWindow = 1_000_000
	}
	defaultEfforts := []string{"low", "medium", "high", "max"}
	efforts := resolveEffortLevels(model, defaultEfforts)
	return &anthropicProfile{
		baseProfile: baseProfile{
			id:              "anthropic",
			model:           model,
			parallel:        true,
			contextWindow:   ctxWindow,
			docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
			reasoning:       true,
			streaming:       true,
			webSearch:       true,
			defaultTimeout:  120_000,
			knowledgeCutoff: "2025-04-01",
			effortLevels:    efforts,
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
				defTaskList(efforts),
				defWebFetch(),
				defCommunicate(),
				defUseSkill(),
			},
		},
	}
}

func NewGeminiProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	defaultEfforts := []string{"low", "medium", "high"}
	efforts := resolveEffortLevels(model, defaultEfforts)
	return &baseProfile{
		id:              "gemini",
		model:           model,
		parallel:        true,
		contextWindow:   1_000_000,
		docFiles:        []string{"GEMINI.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-03-01",
		effortLevels:    efforts,
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
			defTaskList(efforts),
			defWebFetch(),
			defWebSearch(),
			defCommunicate(),
			defUseSkill(),
		},
	}
}

func NewMiniMaxProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	defaultEfforts := []string{"low", "medium", "high", "max"}
	efforts := resolveEffortLevels(model, defaultEfforts)
	return &baseProfile{
		id:              "minimax",
		model:           model,
		parallel:        true,
		contextWindow:   204_800,
		docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		effortLevels:    efforts,
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
			defTaskList(efforts),
			defWebFetch(),
			defCommunicate(),
			defUseSkill(),
		},
	}
}

// NewOpenRouterAnthropicProfile creates a profile that routes any OpenRouter-
// served model through OpenRouter's Anthropic-Messages-compatible endpoint
// (https://openrouter.ai/api/v1/messages).
//
// Use this for models whose native tool-call format is Anthropic-style XML
// (notably minimax/minimax-m2.7). Routing them through OpenRouter's OpenAI
// chat/completions endpoint produces corrupt tool_calls.arguments where XML
// fragments leak into the JSON args string; the Anthropic endpoint keeps the
// model's native format end-to-end.
//
// The provider id is "openrouter-anthropic" which is served by
// llm/providers/openrouter_anthropic — it wraps the standard Anthropic
// adapter with the OpenRouter base URL and OPENROUTER_API_KEY.
func NewOpenRouterAnthropicProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	// Ducktype capabilities from the catalog for the specific model.
	contextWindow := 128_000
	ws := true // default to web search on (Anthropic models support it)
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.GetModelInfo(model); mi != nil {
			if mi.ContextWindow > 0 {
				contextWindow = mi.ContextWindow
			}
			// Only override if the catalog has an explicit entry for this model.
			// Absent models keep the default (true).
			ws = mi.SupportsWebSearch
		}
	}
	// Resolve effort levels from catalog; fall back to MiniMax defaults.
	defaultEfforts := []string{"low", "medium", "high", "max"}
	efforts := resolveEffortLevels(model, defaultEfforts)
	return &baseProfile{
		id:              "openrouter-anthropic",
		model:           model,
		parallel:        true,
		contextWindow:   contextWindow,
		docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       ws,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		effortLevels:    efforts,
		providerOpts: map[string]any{
			"anthropic": map[string]any{
				"max_tokens": 16384,
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
			defTaskList(efforts),
			defWebFetch(),
			defCommunicate(),
			defUseSkill(),
		},
	}
}

// resolveOpenAICompatCatalogModel runs the OpenAI-compatible catalog
// lookup precedence used by NewOpenAICompatProfile, returning the first
// matching ModelInfo or nil. Pulled out as a pure function so it can be
// unit-tested with a fake catalog without depending on which specific
// entries the embedded catalog ships.
//
// Precedence (first hit wins):
//  1. bare model name — covers kimi/glm (unprefixed catalog keys)
//  2. "<id>/<model>" exact — covers openrouter and untagged ollama,
//     and any tagged entry that ships in the catalog with its tag
//     (e.g. "ollama/llama3:8b")
//  3. "<id>/<base>" where base is `model` with any ":<tag>" suffix
//     stripped — covers typical Ollama tagged variants whose catalog
//     entry is the untagged family ("llama3.1:8b" -> "ollama/llama3.1")
func resolveOpenAICompatCatalogModel(lookup func(string) *llm.ModelInfo, id, model string) *llm.ModelInfo {
	if mi := lookup(model); mi != nil {
		return mi
	}
	if mi := lookup(id + "/" + model); mi != nil {
		return mi
	}
	if base, _, hasTag := strings.Cut(model, ":"); hasTag && base != "" {
		if mi := lookup(id + "/" + base); mi != nil {
			return mi
		}
	}
	return nil
}

// NewOpenAICompatProfile creates a profile for OpenAI-compatible providers
// (kimi, glm, openrouter, ollama, etc.). If contextWindow is 0, it's looked
// up from the embedded model catalog; if still unknown, defaults to 128K.
//
// The catalog lookup tries up to three forms in order, see
// resolveOpenAICompatCatalogModel for the precedence contract.
//
// The wire model name is always the bare value; only the catalog lookup
// is broadened.
func NewOpenAICompatProfile(id, model string, contextWindow int) ProviderProfile {
	model = strings.TrimSpace(model)
	var catModel *llm.ModelInfo
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		catModel = resolveOpenAICompatCatalogModel(cat.GetModelInfo, id, model)
	}
	if contextWindow == 0 && catModel != nil && catModel.ContextWindow > 0 {
		contextWindow = catModel.ContextWindow
	}
	if contextWindow == 0 {
		contextWindow = 128_000
	}
	defaultEfforts := []string{"low", "medium", "high"}
	var efforts []string
	if catModel != nil && len(catModel.ReasoningEffortLevels) > 0 {
		efforts = append([]string(nil), catModel.ReasoningEffortLevels...)
	} else {
		efforts = resolveEffortLevels(model, defaultEfforts)
	}
	// MiniMax via OpenRouter uses reasoning_details for multi-turn reasoning
	// (not OpenAI's reasoning_content). Set the provider option that tells the
	// openai-compat adapter to serialize/deserialize reasoning_details.
	var providerOpts map[string]any
	if strings.HasPrefix(model, "minimax/") {
		providerOpts = map[string]any{
			"openai-compatible": map[string]any{
				"reasoning": map[string]any{"enabled": true},
			},
		}
	}
	return &baseProfile{
		id:              id,
		model:           model,
		parallel:        true,
		contextWindow:   contextWindow,
		docFiles:        []string{"AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		effortLevels:    efforts,
		providerOpts:    providerOpts,
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
			defTaskList(efforts),
			defWebFetch(),
			defCommunicate(),
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
		Workspace:  ScanWorkspace(wd),
	}
}

func defReadFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_file",
		Description: "Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis. When reading an image or PDF, describe what you hope to learn — the system will provide a detailed description alongside the file.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"offset":    map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
				"purpose":   map[string]any{"type": "string", "description": "For image/PDF files: describe what factual data you need extracted. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate."},
			},
			"required": []string{"file_path"},
		},
	}
}

func defWriteFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "write_file",
		Description: "Write content to a file. Creates the file and parent directories if needed, and replaces the entire file contents when the file already exists. Use this for new files or intentional full rewrites; prefer the exact-edit tool for small changes to existing files.",
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
		Description: "List the contents of a directory path. Use depth to control recursion when exploring project structure (1 means this directory only).",
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
		Description: "Replace an exact string occurrence in an existing file. Always read the file first so you know the exact text to match. old_string must identify a unique location in the file, so include enough surrounding context to make it unambiguous. Keep each call small and focused. Set replace_all only for deliberate whole-file replacements such as a symbol rename.",
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
		Description: "Execute a shell command and return stdout, stderr, and exit code. Use this for build, test, git, runtime, and inspection commands. When using the shell to search text or files, prefer rg or rg --files if available.",
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
		Description: "Search file contents using regex patterns. Use this to find definitions, references, and recurring patterns across files.",
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
		Description: "Find files matching a glob pattern. Use this for pattern-based file discovery. If a provider aliases this tool to a name like list_dir, it still performs glob matching rather than a literal directory listing.",
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
		Description: "Spawn a sub-agent to work on a scoped task. Only you can call this tool; subagents never receive it. With blocking=true, the returned output is the subagent's own result JSON. Check `success`, `status`, and `output` yourself before trusting it. If the subagent reports a bounce, placeholder text, or otherwise fails to do the work, resume it with sharper instructions or spawn a better-suited agent instead of treating the delegation as complete.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task":             map[string]any{"type": "string"},
				"model":            map[string]any{"type": "string", "description": "Model override (default: parent model)"},
				"max_turns":        map[string]any{"type": "integer", "description": "Turn limit for the subagent (default: 500)"},
				"agent_type":       map[string]any{"type": "string", "description": "Agent type (e.g. 'explorer' or 'implementer' for built-in/bundled agents, or 'plugin-name:agent-name' for external plugin agents)"},
				"blocking":         map[string]any{"type": "boolean", "description": "When true, spawns the agent and waits for completion in a single call, returning the subagent result JSON directly. Do NOT call wait() after a blocking spawn — the result is already in the response. Default is false (async). Use blocking=false only when you need to run multiple agents in parallel, then call wait() on each agent_id."},
				"reasoning_effort": map[string]any{"type": "string", "description": "Reasoning effort for this subagent: low, medium, high, or xhigh. Default inherits from parent. Start with low — it auto-escalates when the agent gets stuck."},
				"grant_tools": map[string]any{
					"type":        "array",
					"description": "Extra tools to grant to the subagent beyond its default role. Use tool names exactly as shown in your current callable tool list. You may only grant tools that are currently callable in this session. `spawn_agent`, `resume_agent`, `wait`, and `close_agent` are only callable by you and cannot be granted.",
					"items":       map[string]any{"type": "string"},
				},
				"task_list": map[string]any{
					"type":        "array",
					"description": "Pre-populate the subagent's task list. Items replace the agent's 'parent_tasks' placeholder.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":            map[string]any{"type": "string", "description": "Short task title"},
							"prompt":           map[string]any{"type": "string", "description": "Detailed instructions"},
							"reasoning_effort": map[string]any{"type": "string", "description": "low|medium|high|xhigh"},
						},
						"required": []string{"title", "prompt"},
					},
				},
			},
			"required": []string{"task"},
		},
	}
}

func defSendInput() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "resume_agent",
		Description: "Resume a sub-agent with new instructions. The agent keeps all its previous context (files read, analysis done, code written) and continues from where it left off. Use this instead of spawning a new agent when you want to iterate — e.g. send reviewer feedback to an implementer, or ask a planner to revise. Use blocking=true (recommended) to wait for the result JSON in one call.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
				"message":  map[string]any{"type": "string"},
				"blocking": map[string]any{
					"type":        "boolean",
					"description": "When true, sends the message and waits for the agent to finish, returning the subagent result JSON directly. Do NOT call wait() after a blocking resume. Default is false.",
				},
				"task_list": map[string]any{
					"type":        "array",
					"description": "Append tasks to the subagent's task list. Items are added after any existing tasks.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":            map[string]any{"type": "string", "description": "Short task title"},
							"prompt":           map[string]any{"type": "string", "description": "Detailed instructions"},
							"reasoning_effort": map[string]any{"type": "string", "description": "low|medium|high|xhigh"},
						},
						"required": []string{"title", "prompt"},
					},
				},
			},
			"required": []string{"agent_id", "message"},
		},
	}
}

func defWait() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "wait",
		Description: "Wait for a non-blocking sub-agent to finish and return its result JSON. Only use this after spawn_agent with blocking=false. Do NOT use after blocking=true — that already returned the result. The result includes `success`, `status`, `output`, `turns_used`, and `transcript`; inspect `success` yourself instead of assuming the subagent solved the task. Use timeout_ms of 300000 (5 minutes) or more — short timeouts waste rounds on retries.",
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
		Description: "Close a sub-agent session, waiting for any active run to stop first. Returns the same result JSON shape as wait(), then removes the sub-agent from the active session list.",
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
	return defCommunicateNamed("communicate")
}

func defCommunicateNamed(name string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        name,
		Description: "Send a user-facing message. This is the only valid way to communicate with the user; never emit a plain assistant response. Set `message` to the exact text the user should see. Set `await_reply` to true only when you need user input before you can continue; otherwise set it to false. Always include `output` as the structured envelope. For ordinary conversational replies, leave `output.message` empty, `output.data` empty, and `output.artifacts` empty. When handing back completed work or machine-readable results, populate `output.message`, `output.data`, and `output.artifacts` accordingly. Some workflows may also require extra fields inside `output`, such as `output.decision` or specific `output.data.*` keys.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Exact user-facing message text. Prefer filling this even when output.message is also populated. Never use a placeholder like 'Done.' when the task asked for concrete findings.",
				},
				"await_reply": map[string]any{
					"type":        "boolean",
					"description": "Set to true only when you need user input before you can continue. Otherwise set to false.",
				},
				"output": map[string]any{
					"type":                 "object",
					"description":          "Structured output envelope. Keep this present on every call. For ordinary conversational replies, leave message empty, data empty, and artifacts empty.",
					"additionalProperties": false,
					"properties": map[string]any{
						"message": map[string]any{"type": "string", "description": "Human-readable structured summary for automation and orchestration. Leave empty for ordinary conversational replies."},
						"data": map[string]any{
							"type":                 "object",
							"description":          "Machine-readable result payload. Leave empty unless the workflow requires structured data.",
							"additionalProperties": true,
							"properties":           map[string]any{},
						},
						"artifacts": map[string]any{
							"type":        "array",
							"description": "Artifact identifiers such as file paths, transcript paths, or output URIs. Leave empty when there are none.",
							"items":       map[string]any{"type": "string"},
						},
					},
					"required": []string{"message", "data", "artifacts"},
				},
			},
			"required": []string{"message", "await_reply", "output"},
		},
	}
}

func defTaskList(effortLevels []string) llm.ToolDefinition {
	reasoningDesc := "Raise or lower the reasoning budget for this task. Omit to leave unchanged."
	reasoningSchema := map[string]any{
		"type":        "string",
		"description": reasoningDesc,
	}
	if len(effortLevels) > 0 {
		reasoningSchema["enum"] = append([]string(nil), effortLevels...)
	}
	return llm.ToolDefinition{
		Name:        "task_list",
		Description: "Manage your task list. Use view to inspect tasks and reasoning effort levels, append to add new tasks, and update to change status, notes, dependencies, or reasoning_effort. When you mark a task done, the next eligible task auto-starts and its prompt is injected. Use depends_on to express ordering and notes to record what happened.",
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
					"description": "For append: tasks to add. Each has a type, brief description (<10 words), a detailed prompt, and optional reasoning_effort.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type": map[string]any{
								"type":        "string",
								"enum":        []string{"research", "implement", "verify", "fix"},
								"description": "Task type. Use 'fix' for targeted remediation after a specific failure or review finding.",
							},
							"description": map[string]any{"type": "string"},
							"prompt":      map[string]any{"type": "string"},
							"depends_on": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "IDs of tasks this one depends on. Optional.",
							},
							"reasoning_effort": reasoningSchema,
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
							"reasoning_effort": reasoningSchema,
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
		Description: "Activate a skill to load its full instructions into context. Use a skill name from the skill catalog in the system prompt.",
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

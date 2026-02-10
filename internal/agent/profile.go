package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/prime-radiant/serf/internal/llm"
)

// communicateGuidance is behavioral guidance for the communicate tool, shared by all profiles.
const communicateGuidance = `
## communicate

You MUST use the communicate tool for ALL output to the user. Never respond with bare text.

- communicate(status): Progress updates while working. Use sparingly — only for meaningful milestones.
- communicate(result): Final answer when the task is complete. You must call this exactly once to finish.
- Every response includes an inbox with pending user messages. Read them and adjust your approach.
- If the inbox contains a message, acknowledge it in your next status or result.
`

// taskListGuidance is behavioral guidance for the task_list tool, shared by all profiles.
const taskListGuidance = `
## task_list

Use the task_list tool to track your plan when working on non-trivial tasks.

Create a plan when:
- The task requires multiple steps or tool calls to complete.
- There are logical phases where sequencing matters.
- The user asked you to do more than one thing.

Do not create a plan for simple, single-step queries you can answer immediately.

Each task has a description (<10 words) and a detailed prompt with work instructions.
Task statuses are: undone, done, cancelled.

Rules:
- Exactly one task should be in_progress at a time.
- Before starting work on a step, mark it in_progress (update status to undone if resuming).
- When you finish a step, mark it done before starting the next.
- If a step is no longer needed, mark it cancelled.
- Do not skip statuses: always move undone → in_progress → done.
- Keep the plan current: if your approach changes, append new tasks and cancel obsolete ones.
- When all steps are complete, every task should be done or cancelled.
`

// openAIBasePrompt is the system prompt for the OpenAI profile.
// It includes the full v4a patch format specification so the model
// knows exactly how to emit patches for the apply_patch tool.
const openAIBasePrompt = `You are serf, a non-interactive coding agent (OpenAI profile).
You persist until the task is fully resolved. Do not stop at analysis or partial fixes.

## apply_patch

Use the apply_patch tool to edit files. The patch format is a stripped-down, file-oriented
diff designed to be easy to parse and safe to apply. The envelope is:

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

Grammar:
Patch := Begin { FileOp } End
Begin := "*** Begin Patch" NEWLINE
End := "*** End Patch" NEWLINE
FileOp := AddFile | DeleteFile | UpdateFile
AddFile := "*** Add File: " path NEWLINE { "+" line NEWLINE }
DeleteFile := "*** Delete File: " path NEWLINE
UpdateFile := "*** Update File: " path NEWLINE [ MoveTo ] { Hunk }
MoveTo := "*** Move to: " newPath NEWLINE
Hunk := "@@" [ header ] NEWLINE { HunkLine } [ "*** End of File" NEWLINE ]
HunkLine := (" " | "-" | "+") text NEWLINE

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
` + taskListGuidance + communicateGuidance + `
## Workflow
- Read files before editing. Use grep and glob to explore the codebase.
- After making changes, run tests to verify correctness.
- Fix errors yourself rather than reporting them and stopping.
- Keep changes minimal and focused on the task.
`

// anthropicBasePrompt is the system prompt for the Anthropic profile.
const anthropicBasePrompt = `You are serf, a non-interactive coding agent (Anthropic profile).
You persist until the task is fully resolved. Do not stop at analysis or partial fixes.

## edit_file
Prefer edit_file with old_string/new_string for precise edits. Read files before editing
and keep diffs minimal and safe. The old_string must be an exact match of existing content.
` + taskListGuidance + communicateGuidance + `
## Workflow
- Read files before editing. Use grep and glob to explore the codebase.
- After making changes, run tests to verify correctness.
- Fix errors yourself rather than reporting them and stopping.
- Keep changes minimal and focused on the task.
`

// geminiBasePrompt is the system prompt for the Gemini profile.
const geminiBasePrompt = `You are serf, a non-interactive coding agent (Gemini profile).
You persist until the task is fully resolved. Do not stop at analysis or partial fixes.

## edit_file
Prefer edit_file with old_string/new_string for precise edits. Use tools to inspect
before changing code and validate by running tests.
` + taskListGuidance + communicateGuidance + `
## Workflow
- Read files before editing. Use grep, glob, and list_dir to explore the codebase.
- After making changes, run tests to verify correctness.
- Fix errors yourself rather than reporting them and stopping.
- Keep changes minimal and focused on the task.
`

type EnvironmentInfo struct {
	WorkingDir            string   `json:"working_dir"`
	Platform              string   `json:"platform"`
	OSVersion             string   `json:"os_version"`
	Today                 string   `json:"today"`                    // YYYY-MM-DD
	KnowledgeCutoff       string   `json:"knowledge_cutoff"`         // YYYY-MM-DD
	IsGitRepo             bool     `json:"is_git_repo"`
	GitBranch             string   `json:"git_branch,omitempty"`
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
	BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc) string
	CheapModel() string
	WithModel(model string) ProviderProfile
}

type baseProfile struct {
	id            string
	model         string
	parallel      bool
	contextWindow int
	basePrompt    string
	toolDefs      []llm.ToolDefinition
	docFiles      []string
}

func (p *baseProfile) ID() string    { return p.id }
func (p *baseProfile) Model() string { return p.model }
func (p *baseProfile) ToolDefinitions() []llm.ToolDefinition {
	return append([]llm.ToolDefinition{}, p.toolDefs...)
}
func (p *baseProfile) SupportsParallelToolCalls() bool { return p.parallel }
func (p *baseProfile) ContextWindowSize() int          { return p.contextWindow }
func (p *baseProfile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}
func (p *baseProfile) CheapModel() string {
	switch p.id {
	case "openai":
		return "gpt-4.1-nano"
	case "anthropic":
		return "claude-haiku-4-5-20251001"
	case "google":
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

func (p *baseProfile) BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc) string {
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

	b.WriteString("Tools:\n")
	for _, td := range p.toolDefs {
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
		id:            "openai",
		model:         strings.TrimSpace(model),
		parallel:      false,
		contextWindow: 128_000,
		basePrompt:    openAIBasePrompt,
		docFiles:      []string{"AGENTS.md", ".codex/instructions.md"},
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
		},
	}
}

func NewAnthropicProfile(model string) ProviderProfile {
	return &baseProfile{
		id:            "anthropic",
		model:         strings.TrimSpace(model),
		parallel:      true,
		contextWindow: 200_000,
		basePrompt:    anthropicBasePrompt,
		docFiles:      []string{"CLAUDE.md", "AGENTS.md"},
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
		},
	}
}

func NewGeminiProfile(model string) ProviderProfile {
	return &baseProfile{
		id:            "google",
		model:         strings.TrimSpace(model),
		parallel:      true,
		contextWindow: 128_000,
		basePrompt:    geminiBasePrompt,
		docFiles:      []string{"GEMINI.md", "AGENTS.md"},
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
		WorkingDir:      wd,
		Platform:        plat,
		OSVersion:       osv,
		Today:           time.Now().UTC().Format("2006-01-02"),
		KnowledgeCutoff: "2024-06-01",
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
				"task":  map[string]any{"type": "string"},
				"model": map[string]any{"type": "string", "description": "Model override (default: parent model)"},
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
				"input":    map[string]any{"type": "string"},
			},
			"required": []string{"agent_id", "input"},
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

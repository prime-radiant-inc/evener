package agent

import "primeradiant.com/serf/llm"

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
				"command":    map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
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
		Description: "Send a user-facing message. ALWAYS use this tool when sending a message to the user. Never emit a plain response. Set `message` to the exact text the user should see. Set `await_reply=true` only when you need user input before you can continue. Otherwise set `await_reply=false`. Always include the structured `output` envelope. For ordinary conversational replies, leave `output.message` empty, `output.data` empty, and `output.artifacts` empty. When handing back completed work or machine-readable results, populate `output` with the evidence and structured data the caller needs. Some workflows may also require extra fields inside `output`, such as `output.decision` or specific `output.data.*` keys.",
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

package tool

import (
	"strings"

	"primeradiant.com/serf/llm"
)

func DefReadFile() llm.ToolDefinition {
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

func DefWriteFile() llm.ToolDefinition {
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

func DefListDir() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "list_dir",
		Description: "List the contents of a directory path, like ls. Use depth to control recursion when exploring project structure (1 means this directory only). Returns one entry per line, like ls -F: a type sigil suffixes the name ('/' directory, '@' symlink, '*' executable), and files show a tab-separated size; sorted by name, followed by a count footer. The listing is a bounded page kept small enough to fit; when the footer says it is truncated, read the next page with offset (set it to the running count of entries already seen), narrow with a more specific path, or reduce depth.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"depth":  map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer", "description": "Index of the first entry to return (default 0). Use with limit to page a large directory."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum entries to return (default 500)."},
			},
		},
	}
}

func DefEditFile() llm.ToolDefinition {
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

func DefShell() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "shell",
		Description: "Run a shell command. By default it runs in the foreground and returns stdout, stderr, and exit code inline when it finishes (up to the session command timeout, ~120s; a command still running at that bound is promoted to a durable background job — you get its `job_id`, the process is not killed). Set `background: true` to start the command and return its `job_id` immediately (status `running` confirms it launched; an instant failure surfaces via the terminal notification, not here). `max_runtime_ms` separately caps total process runtime. Output is a navigable resource: output over ~8 KB returns a head+tail digest plus a `job_id` — read the rest with `job_read_output` (`head_lines`/`tail_lines`/`grep`; `total_bytes`/`dropped_bytes`/`output_status` say how much exists and whether any was evicted). A result with NO `job_id` is already complete: the inline output is the whole result, so do not call `job_read_output` for it (this includes a finished foreground command — if you only want more of its output, you piped it away yourself, e.g. through `tail`; re-run without the pipe). Serf notifies you when a background job finishes. Prefer `rg`/`rg --files` for searching.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"command":        map[string]any{"type": "string"},
				"description":    map[string]any{"type": "string"},
				"background":     map[string]any{"type": "boolean", "description": "false (default): run in the foreground and return when the command finishes (still running at the session timeout → promoted to a background job). true: start the command and return its job_id immediately."},
				"max_runtime_ms": map[string]any{"type": "integer"},
			},
			"required": []string{"command"},
		},
	}
}

// DefDelegate defines the delegate tool, which starts a NEW delegate
// conversation (independent agentic work) and returns a durable delegate_id plus
// concrete job IDs. agentTypes constrains the agent_type enum to the session's
// available roles; pass nil to omit the enum (free-form). reasoning_effort uses
// the delegate contract's portable low/medium/high enum; the handler resolves
// provider-specific details.
func DefDelegate(agentTypes []string) llm.ToolDefinition {
	strictFalse := false
	agentTypeSchema := map[string]any{
		"type":        "string",
		"description": "Role for the delegate. Choose from the enum; the roles are described in your agents section.",
	}
	if len(agentTypes) > 0 {
		agentTypeSchema["enum"] = append([]string(nil), agentTypes...)
	}
	return llm.ToolDefinition{
		Name: "delegate",
		Description: "Start a NEW delegate conversation to do independent agentic work; returns a durable `delegate_id` " +
			"for follow-up plus concrete `job_id`s for individual turns. `delegate` never resumes an existing " +
			"delegate: follow up on one with `delegate_send(to=<delegate_id>)`. Optional: `agent_type` picks a role from the " +
			"enum (described in your agents section); `model` and `reasoning_effort` override the defaults; " +
			"`result_schema` requests a validated structured result; `max_wait_ms` waits inline up to that many ms " +
			"(a timeout leaves the job running). `delegation_allowance` lets the delegate itself delegate, up to one " +
			"level shallower than your own allowance. For delegate readiness, status, findings, and final reports, ask the " +
			"delegate to call `communicate` with the exact marker/report. Use the delegate's output as the evidence for judging the work.",
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task":                 map[string]any{"type": "string"},
				"agent_type":           agentTypeSchema,
				"model":                map[string]any{"type": "string", "description": "Model override (default: parent model)."},
				"reasoning_effort":     map[string]any{"type": "string", "description": "Reasoning effort for this delegate (low, medium, or high). Default inherits from parent.", "enum": []string{"low", "medium", "high"}},
				"max_wait_ms":          map[string]any{"type": "integer", "description": "0 (default): return the delegate_id and started job_id immediately; you are notified on completion. >0: wait inline up to this many ms; a timeout leaves the job running."},
				"delegation_allowance": map[string]any{"type": "integer", "description": "0 (default): a leaf delegate that cannot itself delegate. >0: the delegate may delegate, granting onward allowances strictly smaller than this; must be strictly less than your own allowance. The allowance only takes effect if the chosen agent_type actually has the `delegate` tool: the built-in `subagent` role is a non-delegating leaf, so a >0 allowance on it is a silent no-op. For a multi-level tree, omit agent_type (the default role can delegate)."},
				"result_schema": map[string]any{
					"type":                 "object",
					"description":          "JSON-Schema-like object for structured delegate results. Serf validates it for initial and resumed turns, surfaces structured_result when valid, and reports structured_result_reason when invalid.",
					"additionalProperties": true,
				},
			},
			"required": []string{"task"},
		},
	}
}

// DefDelegateSend defines the delegate_send tool, the single follow-up surface
// for durable delegates and contextual caller commentary.
func DefDelegateSend() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "delegate_send",
		Description: "Send a message to a durable delegate by delegate_id, or from a delegate/watch-delivered context " +
			"to the contextual caller route. `to` accepts `dlg_...` or `caller`; it rejects job/turn handles, transcript " +
			"refs, and legacy runtime aliases. If the delegate is running or being driven, the message is steered and " +
			"returns on delivery. If the delegate is idle, set `on_idle=\"start\"` to start the next job; the default " +
			"`on_idle=\"fail\"` rejects idle delegates instead of starting work. In observer sidecars, " +
			"`delegate_send(to=\"caller\")` is the callback that wakes the caller with the observer's finding.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"to":      map[string]any{"type": "string", "description": "A delegate_id (`dlg_...`) or `caller` when available."},
				"message": map[string]any{"type": "string"},
				"on_idle": map[string]any{
					"type":        "string",
					"enum":        []string{"start", "fail"},
					"description": "Default fail: reject an idle delegate. start: start the delegate's next job.",
				},
				"max_wait_ms": map[string]any{"type": "integer", "description": "0 (default): deliver/start without waiting. >0: for a started job, wait inline up to this many ms for its result; steers and caller sends return on delivery regardless."},
			},
			"required": []string{"to", "message"},
		},
	}
}

// DefJobWatch defines the root-only job_watch tool. eventKinds are the
// model-facing session/job event-kind names available this session; they are
// interpolated into the description so the model can discover them (spec §5.11).
func DefJobWatch(eventKinds []string) llm.ToolDefinition {
	strictFalse := false
	kinds := strings.Join(eventKinds, ", ")
	if kinds == "" {
		kinds = "none available this session"
	}
	desc := "Create, inspect, list, or clear standing triggers on a running job or this caller session. " +
		"For a one-time \"did it print X yet\", use `job_read_output` with a positive max_wait_ms and grep " +
		"instead — watches are for recurring conditions, and completion needs no watch at all (terminal notifications are " +
		"automatic). For `operation=\"create\"`, set `target` to a `job_id` or `caller`; set only the triggers you need: " +
		"`output_match` (RE2 over the job's output; if the retained output " +
		"already contains a match the watch fires immediately, then again on new matches — a finished job gets a one-shot " +
		"catch-up scan), `progress_interval_ms` (periodic), `events` (kinds this session: " + kinds + "; `every` fires on " +
		"each Nth occurrence — 1 is the default; above 1 requires `events` to contain exactly one kind; `event_filter` can narrow " +
		"`assistant.tool` events by tool_name and ok/error status). Use `communicate` for result/status messages, " +
		"`assistant.tool` for tool calls, and `job.notification` for job lifecycle events. Delivery: omit `send` to be notified " +
		"yourself; set `send.to` to an observer `delegate_id` to push bounded trigger frames there — " +
		"this also grants that observer read access to the observed job. Observer callback flow: trigger frame reaches " +
		"the observer, the observer calls `delegate_send(to=\"caller\")`, and the caller continues from that callback. " +
		"The callback is completion evidence for the observer's task; after it arrives, produce one final result unless the user asked for audit details. " +
		"For normal callback flow, the callback-to-final-result path is complete; audit tools are for explicit audit requests or failed/missing callbacks. Choose one common sidecar shape: " +
		"communicate results/status use `target=\"caller\", events=[\"communicate\"], send.to=<delegate_id>`; " +
		"tool events use `target=\"caller\", events=[\"assistant.tool\"], event_filter={\"tool_name\":\"read_file\",\"status\":\"ok\"}, send.to=<delegate_id>`. " +
		"Assistant.tool frames include the matched `status` and the original tool `arguments_json`, so observers can usually decide from the frame itself. " +
		"For communicate content such as APPROVAL_REQUEST, the observer task checks the delivered `event.message`. Frames coalesce latest-wins while the target is " +
		"busy. Use `send.include_excerpt` only when `target` starts with `job_`; for `target=\"caller\"`, use `send.to` without an excerpt. Caller/session-target event frames already carry bounded event payloads. ONE active watch per " +
		"(target, send.to): a different configuration for the same key replaces the existing watch " +
		"(`replaced_existing:true`) — use a distinct `send.to` for an additional watch on the same target. " +
		"`operation=\"clear\"` removes a watch by `watch_id`."
	return llm.ToolDefinition{
		Name:        "job_watch",
		Description: desc,
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation":            map[string]any{"type": "string", "description": "create, list, inspect, or clear.", "enum": []string{"create", "list", "inspect", "clear"}},
				"watch_id":             map[string]any{"type": "string", "description": "watch_id returned by job_watch create/list; required for inspect and clear."},
				"target":               map[string]any{"type": "string", "description": "job_id, or caller for this session."},
				"output_match":         map[string]any{"type": "string", "description": "RE2 regex over the job's output. Case-sensitive unless (?i). Invalid regex errors at creation."},
				"progress_interval_ms": map[string]any{"type": "integer", "description": "Periodic trigger interval in ms (min 1000, max 3600000; handler clamps later). Omit for none."},
				"events": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Event kinds to watch; [\"*\"] = all visible. Available: " + kinds + ". Watch communicate for result/status messages.",
				},
				"every": map[string]any{
					"type":        "integer",
					"description": "Fire on each Nth occurrence of the single watched event kind. 1 is the default (fire on every occurrence); values above 1 require `events` to contain exactly one kind.",
				},
				"event_filter": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"description":          "Structured predicate for assistant.tool watches. With events [\"assistant.tool\"], match the emitted tool call by tool_name and/or status. Communicate content is delivered in event.message for the observer task to evaluate.",
					"properties": map[string]any{
						"tool_name": map[string]any{"type": "string", "description": "For assistant.tool events, match the canonical tool name exactly."},
						"status":    map[string]any{"type": "string", "description": "For assistant.tool events, match ok or error.", "enum": []string{"ok", "error"}},
					},
				},
				"send": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"description":          "Deliver to another target instead of notifying the caller.",
					"properties": map[string]any{
						"to":              map[string]any{"type": "string", "description": "delegate_id, or `caller`."},
						"message":         map[string]any{"type": "string"},
						"include_excerpt": map[string]any{"type": "boolean", "description": "Only for target values that start with job_; attaches a bounded output excerpt to delivered frames. For target \"caller\", omit this because session-event frames already include the event payload."},
					},
				},
			},
			"required": []string{"operation"},
		},
	}
}

func DefJobReadOutput() llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "job_read_output",
		Description: "Read a job's output (text in the `output` field) and status by `job_id` — reads never consume or acknowledge anything. By default returns a head+tail digest: the first ~100 and last ~100 lines with the middle elided (a marker states how much). To page more, pass `head_lines` and/or `tail_lines` (give both for a custom-sized head+tail digest), `from_line`+`line_count` for an arbitrary middle slice, or `grep` to search the whole log. The result reports `total_bytes` (lifetime output), `dropped_bytes` (permanently evicted past the retention cap), and `output_status`: `all_retained` (the window is the whole log), `windowed` (more is retained — read it), or `evicted` (`dropped_bytes` are gone). `grep` scans the **entire retained output**, not just the digest. Delegates return the report (and `structured_result`, when present) — to get a delegate's result, prefer this over `read_session_transcript`. `max_wait_ms > 0` waits: with `grep`, until a match exists, the job ends, or the timeout elapses; without `grep`, until new output or terminal state. For observer sidecar callbacks, the working signal is the `delegate_send(to=\"caller\")` steering message; use job output after that callback when you need audit or diagnosis evidence.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"job_id":      map[string]any{"type": "string"},
				"head_lines":  map[string]any{"type": "integer", "description": "Read this many lines from the START. Combine with tail_lines for a custom head+tail digest."},
				"tail_lines":  map[string]any{"type": "integer", "description": "Read this many lines from the END. Combine with head_lines for a custom head+tail digest."},
				"from_line":   map[string]any{"type": "integer", "description": "Read a middle slice starting at this 1-based line (with line_count). Cannot combine with head_lines/tail_lines."},
				"line_count":  map[string]any{"type": "integer", "description": "How many lines for a from_line slice (default 100)."},
				"grep":        map[string]any{"type": "string"},
				"max_wait_ms": map[string]any{"type": "integer", "description": "0 (default): snapshot now. >0: wait up to this many ms for a grep match (with grep), or for new output / terminal state."},
			},
			"required": []string{"job_id"},
		},
	}
}

func DefJobList() llm.ToolDefinition {
	strictFalse := false
	statusEnum := []any{"running", "completed", "failed", "cancelled", "stopped"}
	typeEnum := []any{"shell", "delegate"}
	return llm.ToolDefinition{
		Name:        "job_list",
		Description: "List this session's durable jobs, newest first; filter by `status` or `type`. Always current — if you have waited a long time with no notification, list jobs to re-orient instead of re-running work. The result also includes your active watches. Terminal statuses: completed, failed, cancelled, stopped. A short job can finish before a running-only filter sees it; when recency matters, list unfiltered or read the job by id. Observer sidecar callbacks are notification-driven: after the watched frame is handed to the observer, Serf resumes the caller from the observer's `delegate_send(to=\"caller\")` steering callback. Use job_list after that callback when you need audit or diagnosis evidence.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string", "enum": statusEnum},
				},
				"type": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string", "enum": typeEnum},
				},
				"include_nested":      map[string]any{"type": "boolean", "default": false},
				"include_descendants": map[string]any{"type": "boolean", "default": false, "description": "Walk the live descendant tree: include every live descendant's jobs, each annotated with owner_session_id and depth. A dead descendant contributes only its terminal record."},
				"limit":               map[string]any{"type": "integer", "default": 50, "maximum": 100},
			},
			"required": []any{},
		},
	}
}

func DefJobStop() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "job_stop",
		Description: "Request cancellation of a running job by `job_id`; stopping never deletes output or history. `max_wait_ms > 0` waits for the stop to finalize; `include_children=true` also stops the job's nested children. Stopped work normally lands as `status=cancelled`, `reason=stopped_by_parent`.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"job_id":           map[string]any{"type": "string"},
				"max_wait_ms":      map[string]any{"type": "integer", "description": "0 (default): request the stop and return. >0: wait up to this many ms for the job to reach terminal state."},
				"include_children": map[string]any{"type": "boolean", "default": false},
			},
			"required": []string{"job_id"},
		},
	}
}

func DefGrep() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents using regex patterns. This is the direct tool for requests to grep, search text, find tokens, find definitions, find references, and find recurring patterns across files.",
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

func DefGlob() llm.ToolDefinition {
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

func DefApplyPatch() llm.ToolDefinition {
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
				"patch": map[string]any{"type": "string", "description": "Complete v4a patch text, starting with *** Begin Patch and ending with *** End Patch."},
			},
			"required": []string{"patch"},
		},
	}
}

func DefWebFetch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch a URL, convert HTML to markdown, cache the results, and answer a question about the page content. When the user asks you to fetch, inspect, summarize, or answer from a URL, call web_fetch with that URL and the user's question before answering.",
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

func DefWebSearch() llm.ToolDefinition {
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

func DefCommunicate() llm.ToolDefinition {
	return DefCommunicateNamed("communicate")
}

func DefCommunicateNamed(name string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        name,
		Description: "Send a user-facing message. Use this tool for every message, readiness marker, requested status marker, request for input, and final answer the user or caller should see. A valid call has visible text in `message` or `output.message`. If you will immediately keep working after this message, set `end_turn=false` and continue by calling the next work tool in the following round. Use `end_turn=false` only for visible narration or status before immediate continued work. Use `end_turn=true` when this message should stop the current activation: final answers, completed results, blocking requests for input, or readiness markers that wait for future user/caller/watch-frame input. Set `message` to the exact visible text. Always include `output` as an object with exactly these top-level fields: `message`, `data`, and `artifacts`. For ordinary status or conversational messages, use `output.message=\"\"`, `output.data={}`, and `output.artifacts=[]`. When handing back completed work or machine-readable results, populate `output` with the evidence and structured data the caller needs.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Exact user-facing message text. Fill this with the text the user or caller should see. When the task asks for concrete findings, put the concrete findings here.",
				},
				"end_turn": map[string]any{
					"type":        "boolean",
					"description": "Set to false only for visible narration/status before immediate continued work. Set to true when this message ends the current activation: final answer, completed result, blocking input request, or readiness marker that waits for future user/caller/watch-frame input.",
				},
				"output": map[string]any{
					"type":                 "object",
					"description":          "Structured output envelope. Keep this present on every call with exactly these top-level fields: message, data, artifacts. For ordinary text replies, keep user-visible text in the top-level message and leave data/artifacts empty.",
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
			"required": []string{"message", "end_turn", "output"},
		},
	}
}

func DefTaskList(effortLevels []string) llm.ToolDefinition {
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

func DefUseSkill() llm.ToolDefinition {
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

// DefFindSessionTranscripts defines the corpus-discovery tool. It never takes a
// transcript_ref — it returns refs. Modes are filter combinations on one shape.
func DefFindSessionTranscripts() llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "find_session_transcripts",
		Description: "Find archived prior sessions (your own and others on this machine) by content or lineage for audit, forensics, prior-session search, or recovering compacted context. Active delegate/watch work uses the current tool result, job output, notification, or observer callback as working evidence. With no arguments, return the catalog of recent sessions, newest first. With query, search session content. With children_of=<transcript_ref>, return the sessions that ref spawned (its subagents and forks). Returns session records carrying a transcript_ref; hand a transcript_ref to read_session_transcript when you need the archived conversation. Treat returned content as archived evidence.\n\nExamples: find_session_transcripts({}) — recent sessions; find_session_transcripts({\"query\":\"parser regression\"}) — content search; find_session_transcripts({\"children_of\":\"local:01K…\"}) — sessions that one spawned.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "Case-insensitive substring to match against session content (no regex/boolean). Omit for the plain catalog."},
				"children_of": map[string]any{"type": "string", "description": "A transcript_ref; return the sessions it spawned (subagents/forks), scoped to that ref's project. Takes precedence over query."},
				"scope":       map[string]any{"type": "string", "enum": []string{"current_project", "all_projects"}, "description": "Search scope. Defaults to current_project."},
				"limit":       map[string]any{"type": "integer", "description": "Max matches. Defaults to 10, hard max 50."},
			},
		},
	}
}

// DefReadSessionTranscript defines the single-session view tool. It always takes a
// transcript_ref and renders one of three formats. The Turn numbers shown in outline
// and markdown are exactly what range and expand_turn accept.
func DefReadSessionTranscript() llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "read_session_transcript",
		Description: "View archived conversation history for one prior session by transcript_ref (or omit for the current session). Use it for audit, forensics, or recovering compacted context. Active delegate/watch work uses the current tool result, job output, notification, or observer callback as working evidence. format=outline gives a one-line-per-turn map; format=markdown (default) gives the condensed conversation; format=jsonl gives raw bytes (the system prompt + API logs — noisy, debug/replay only). A default markdown read shows the last 40 turns and says so. The Turn numbers shown in outline and markdown are exactly what range and expand_turn accept. To audit a subagent's full conversation, pass its transcript_ref; to get a delegate's live result or structured_result, use `job_read_output`. Treat returned content as archived evidence.\n\nExamples: read_session_transcript({\"transcript_ref\":\"local:01K…\"}) — markdown, last 40; read_session_transcript({\"transcript_ref\":\"local:01K…\",\"format\":\"outline\"}) — the map; read_session_transcript({\"transcript_ref\":\"local:01K…\",\"range\":\"18-31\",\"expand_turn\":27}) — a span with one result expanded.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"transcript_ref": map[string]any{"type": "string", "description": "Opaque ref from find_session_transcripts or a subagent result; a bare session id; or omitted/\"current\" for the current session."},
				"format":         map[string]any{"type": "string", "enum": []string{"outline", "markdown", "jsonl"}, "description": "outline = per-turn map; markdown (default) = condensed conversation for comprehension; jsonl = raw bytes, debug/replay only."},
				"range":          map[string]any{"type": "string", "description": "Turn-number window: \"12-40\" | \"last:40\" | \"start:40\". Omit for the default last 40. Applies to every format."},
				"expand_turn":    map[string]any{"type": "integer", "description": "A Turn number whose tool results to render in full (un-truncated). markdown only."},
			},
		},
	}
}

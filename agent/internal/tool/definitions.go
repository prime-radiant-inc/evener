package tool

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// MemoryIndexGenerated is the refusal of a write, edit or delete of MEMORY.md
// at a memory scope's root, which Evener generates from the pages.
const MemoryIndexGenerated = "MEMORY.md is generated from each page's frontmatter; edit a page's when line or tags instead"

// MemoryDefinition preserves the ordinary tool schema and adds trusted-scope
// selection. Its path parameter is described as relative to the scope root,
// since the ordinary tool's description invites a workspace or absolute path.
func MemoryDefinition(base llm.ToolDefinition, name string) llm.ToolDefinition {
	base.Name = name
	base.Parameters = CloneSchemaMap(base.Parameters)
	props := base.Parameters["properties"].(map[string]any)
	props["scope"] = map[string]any{"type": "string", "enum": []any{"personal", "project"}, "description": "personal or project."}
	if param, ok := props["file_path"].(map[string]any); ok {
		param["description"] = "Memory path relative to scope root (e.g. topic.md)."
	}
	if param, ok := props["path"].(map[string]any); ok {
		param["description"] = "Relative path to search (blank = whole scope)."
	}
	base.Parameters["required"] = append(base.Parameters["required"].([]string), "scope")
	if name == "memory_read" && !strings.Contains(base.Description, "Reading MEMORY.md") {
		base.Description += " Reading MEMORY.md at the scope root returns the whole generated index."
	}
	base.Description = "Memory wiki (`personal`/`project` relative path). " + base.Description
	return base
}

func DefMemoryRead() llm.ToolDefinition {
	return MemoryDefinition(DefReadFile(), "memory_read")
}

func DefMemoryDelete() llm.ToolDefinition {
	return MemoryDefinition(llm.ToolDefinition{
		Description: "Remove a memory file (not a directory; read first; update cross-links separately).",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
			"required":   []string{"file_path"},
		},
	}, "memory_delete")
}

// DefMemoryWrite is memory_write: the page's when line and body. Evener writes
// the frontmatter from when.
func DefMemoryWrite() llm.ToolDefinition {
	return MemoryDefinition(llm.ToolDefinition{
		Description: "Write or replace one memory page; Evener generates frontmatter from `when`.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"when":      map[string]any{"type": "string", "description": "When a future session needs this fact."},
				"body":      map[string]any{"type": "string", "description": "Heading, single fact, **Why:**, and **How to apply:** (no frontmatter)."},
			},
			"required": []string{"file_path", "when", "body"},
		},
	}, "memory_write")
}

// readFileCoreDescription is the description every read_file vision regime
// shares. A regime appends its own vision_prompt sentence, or none when the
// model sees images and PDFs inline.
const readFileCoreDescription = "Read numbered text, image, or PDF."

// defReadFile builds read_file's shared schema so the three vision regimes and
// memory_read cannot drift on the core description, offset, limit, or pages.
// visionSuffix is appended to the description, and visionParamDesc becomes the
// vision_prompt property; both are empty in Regime 1, which omits vision_prompt
// entirely (#6190).
func defReadFile(visionSuffix, visionParamDesc string) llm.ToolDefinition {
	props := map[string]any{
		"file_path": map[string]any{"type": "string", "description": "Path."},
		"offset":    map[string]any{"type": "integer", "description": "1-based line (default 1) or negative from EOF."},
		"limit":     map[string]any{"type": "integer", "description": "Max lines (default 2000, ~48 KB cap; 1 reads line uncut)."},
		"pages":     map[string]any{"type": "string", "description": "PDF pages (e.g. \"1-5\", max 20; required >10 pages/20 MiB)."},
	}
	if visionParamDesc != "" {
		props["vision_prompt"] = map[string]any{"type": "string", "description": visionParamDesc}
	}
	return llm.ToolDefinition{
		Name:        "read_file",
		Description: readFileCoreDescription + visionSuffix,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           props,
			"required":             []string{"file_path"},
		},
	}
}

// DefReadFile returns read_file for Regime 3, where images and PDFs both use
// the vision side-channel.
func DefReadFile() llm.ToolDefinition {
	return defReadFile(" Pass `vision_prompt` for images/PDFs.", "Image/PDF OCR or visual query.")
}

// DefReadFileNativeVision returns read_file tailored for Regime 1: neither images
// nor PDFs run the side-channel. vision_prompt is omitted from advertised
// properties and the description omits the vision_prompt sentence (#6190).
func DefReadFileNativeVision() llm.ToolDefinition {
	return defReadFile("", "")
}

// DefReadFilePDFVisionOnly returns read_file tailored for Regime 2: images are
// native inline, while PDFs use the side-channel. vision_prompt is kept and
// scoped to PDF files in both the tool description and parameter description (#6190).
func DefReadFilePDFVisionOnly() llm.ToolDefinition {
	return defReadFile(" Pass `vision_prompt` for PDFs.", "PDF OCR or visual query.")
}

func DefWriteFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "write_file",
		Description: "Create or overwrite a file and its parent dirs.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string", "description": "Path."},
				"content":   map[string]any{"type": "string", "description": "Content."},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

func DefListDir() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "list_dir",
		Description: "List directory entries with type suffixes ('/', '@', '*').",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Dir (default root)."},
				"depth":  map[string]any{"type": "integer", "description": "Depth 1-16 (default 1)."},
				"offset": map[string]any{"type": "integer", "description": "Offset (default 0)."},
				"limit":  map[string]any{"type": "integer", "description": "Limit (default 1000)."},
			},
		},
	}
}

func defEditFile(readTool, writeTool string) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "edit_file",
		Description: fmt.Sprintf(
			"Apply a non-empty `edits` array of disjoint `{old_string, new_string, replace_all?}` replacements matched atomically against the original file without overlapping text (read with %s first, omit line prefixes, merge nearby/overlapping changes into one edit; rewrite with %s).",
			readTool, writeTool,
		),
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "File path.",
				},
				"edits": map[string]any{
					"type":        "array",
					"minItems":    1,
					"description": "Non-empty array of disjoint `{old_string, new_string, replace_all?}` replacements matched against the original file.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"old_string": map[string]any{
								"type":        "string",
								"description": "Exact text from original file (unique unless `replace_all` is true).",
							},
							"new_string": map[string]any{
								"type":        "string",
								"description": "Replacement text.",
							},
							"replace_all": map[string]any{
								"type":        "boolean",
								"description": "Replace all matches (default false).",
							},
						},
						"required": []string{"old_string", "new_string"},
					},
				},
			},
			"required": []string{"file_path", "edits"},
		},
	}
}

func DefEditFile() llm.ToolDefinition {
	return defEditFile("read_file", "write_file")
}

func DefMemoryEdit() llm.ToolDefinition {
	return MemoryDefinition(defEditFile("memory_read", "memory_write"), "memory_edit")
}

// DefShell is the fully capable seed form, equivalent to
// DefShellWithDetach(true). Sessions narrow it for their live environment.
func DefShell() llm.ToolDefinition {
	return DefShellWithDetach(true)
}

// ShellModeProperty returns the shell schema's mode property for an
// environment whose execenv.DetachSupportReporter reports detachAvailable. When
// false, the enum omits "detached" and the description says this environment
// cannot disown a process, so the advertised modes match what DetachCommand
// accepts.
func ShellModeProperty(detachAvailable bool) map[string]any {
	modeEnum := []any{"foreground", "background"}
	modeDescription := "foreground (default for builds, tests, and waited commands; auto-backgrounds at 2m); background (for independent parallel work or a session-owned service that must return immediately; stops on exit)"
	if detachAvailable {
		modeEnum = append(modeEnum, "detached")
		modeDescription += "; detached (outlives exit, returns PID only, no job/notice, discards unredirected output; pass absolute paths and redirect needed logs to an absolute path)."
	} else {
		modeDescription += "; detached unavailable."
	}
	return map[string]any{
		"type":        "string",
		"enum":        modeEnum,
		"description": modeDescription,
	}
}

// DefShellWithDetach returns the shell definition for an environment whose
// execenv.DetachSupportReporter reports detachAvailable.
func DefShellWithDetach(detachAvailable bool) llm.ToolDefinition {
	modeHint := "`background`"
	if detachAvailable {
		modeHint = "`background`/`detached`"
	}
	return llm.ToolDefinition{
		Name:        "shell",
		Description: "Run Bash (pipefail). Large/background runs return a digest + `job_id` and notify on exit; use " + modeHint + ", not `&`.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"command":     map[string]any{"type": "string", "description": "Command."},
				"description": map[string]any{"type": "string", "description": "Summary."},
				"mode":        ShellModeProperty(detachAvailable),
				"cwd":         map[string]any{"type": "string", "description": "Dir."},
			},
			"required": []string{"command"},
		},
	}
}

// DelegateSandboxSchema describes the sandbox control that the current
// session can actually enforce. Available=false removes the sandbox knob;
// an explicit value is still rejected by the handler rather than ignored.
// Modes lists the mode names the parent floor permits (informational);
// SandboxEnum (or, when empty, DelegateSandboxEnumFromModes over Modes)
// becomes the sandbox property's enum: combined mode+network values such
// as "read-only+nonet" and "nonet".
type DelegateSandboxSchema struct {
	Available          bool
	Modes              []string
	SandboxDescription string
	// SandboxEnum carries the precomputed combined sandbox+sandbox_net enum
	// values (e.g. "off", "read-only", "read-only+nonet", "nonet"). Empty
	// derives the enum from Modes via DelegateSandboxEnumFromModes.
	SandboxEnum []string
	// ModelDescription is appended to the model override description when a
	// caller has captured a bounded, startup-frozen availability snapshot.
	// Empty preserves the generic string contract.
	ModelDescription string
}

const delegateModelOverrideDescription = "Model override (default: current)."

// shortTaskTitleDescription is the shared "<10 words" contract for a task
// item's short title, used by both task surfaces: the delegate brief's
// seeded task_list items (field `title`) and the task_list tool's add items
// (field `description`).
const shortTaskTitleDescription = "Title (<10 words)."

// DelegateModelDescriptionAdditionBudget reports how many bytes may be
// appended to the model parameter's fixed description without exceeding the
// caller's total schema-description budget.
func DelegateModelDescriptionAdditionBudget(maxBytes int) int {
	budget := maxBytes - len([]byte(delegateModelOverrideDescription)) - 1
	if budget < 0 {
		return 0
	}
	return budget
}

// delegateReasoningEffortLevels is the portable effort vocabulary the delegate
// tool advertises and its handler accepts: the session's canonical levels plus
// the explicit off sentinel. Deriving it from llm keeps the tool schema from
// being narrower than createDelegate's validation, which rejected a legitimate
// level at argument validation before the handler ever saw it.
func delegateReasoningEffortLevels() []string {
	return llm.AcceptedReasoningEfforts()
}

// delegateReasoningEffortDescription names the levels the schema enumerates,
// built from the same source so the prose and the enum cannot drift.
func delegateReasoningEffortDescription() string {
	return fmt.Sprintf("Effort (%s; %s=off; default inherits parent).",
		strings.Join(llm.ReasoningEffortVocabulary(), ", "), llm.ReasoningEffortNone)
}

// DefDelegate returns the default delegate tool definition with free-form
// agent_type, the full sandbox surface, and DefaultResultToolName. Session
// callers use DefDelegateWithResultTool so advertised roles, sandbox controls,
// and result tool match the live session.
func DefDelegate() llm.ToolDefinition {
	return DefDelegateWithResultTool(nil, DelegateSandboxSchema{
		Available: true,
		Modes:     []string{"off", "read-only", "workspace-write", "restricted"},
	}, DefaultResultToolName)
}

// DelegateRoleWriteNote notes that the explorer role is read-only (no edit
// tools), shared between the static delegate schema and the live per-role
// capability roster appended at request time.
const DelegateRoleWriteNote = "explorer is read-only."

// DefDelegateWithResultTool defines the delegate tool, which starts a NEW
// delegate conversation and returns its durable stable identity. agentTypes
// constrains the agent_type enum to the session's available roles; nil omits
// the enum (free-form). sandboxSchema exposes only the sandbox controls and
// model descriptions the session can enforce. resultTool names the session's
// result tool for observer reporting instructions. reasoning_effort uses the
// delegate contract's portable vocabulary (delegateReasoningEffortLevels); the
// handler clamps it to the child model's own ladder.
func DefDelegateWithResultTool(agentTypes []string, sandboxSchema DelegateSandboxSchema, resultTool string) llm.ToolDefinition {
	strictFalse := false
	effortLevels := delegateReasoningEffortLevels()
	agentTypeSchema := map[string]any{
		"type":        "string",
		"description": "Role. " + DelegateRoleWriteNote,
	}
	if len(agentTypes) > 0 {
		agentTypeSchema["enum"] = append([]string(nil), agentTypes...)
	}
	def := llm.ToolDefinition{
		Name: "delegate",
		Description: "Start a delegate (`delegate_id`; follow up: `delegate_send(to=<delegate_id>)`). " +
			"`watch_parent=true` allows it to observe you. " +
			ObserverReporting(resultTool),
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Self-contained brief: goal, facts, owned paths, check, evidence (steps in `task_list`).",
				},
				"task_list": map[string]any{
					"type":        "array",
					"description": "Ordered steps.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"title":            map[string]any{"type": "string", "description": shortTaskTitleDescription},
							"prompt":           map[string]any{"type": "string", "description": "Step instruction."},
							"reasoning_effort": map[string]any{"type": "string", "enum": append(slices.Clone(effortLevels), "inherit"), "description": "Effort (default inherit)."},
							"type":             map[string]any{"type": "string", "enum": []string{"research", "implement", "verify", "fix"}, "description": "Kind (default implement)."},
						},
						"required": []string{"title", "prompt"},
					},
				},
				"agent_type":           agentTypeSchema,
				"model":                map[string]any{"type": "string", "description": delegateModelOverrideDescription},
				"reasoning_effort":     map[string]any{"type": "string", "description": delegateReasoningEffortDescription(), "enum": effortLevels},
				"delegation_allowance": map[string]any{"type": "integer", "description": "Sub-delegation cap (0=leaf; < yours; default yours-1)."},
				"watch_parent":         map[string]any{"type": "boolean", "description": "Allow it to observe you."},
				"fork_context": map[string]any{
					"type": "boolean", "default": false,
					"description": "Copy your history (same model/provider).",
				},
				"isolation": map[string]any{
					"type":        "string",
					"enum":        []string{"worktree"},
					"description": "Git worktree isolation.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Label/worktree branch (new git ref).",
				},
				"sandbox": map[string]any{
					"type":        "string",
					"enum":        []string{"off", "read-only", "workspace-write", "restricted"},
					"description": "Sandbox (at least as confining): off, read-only (no writes), workspace-write (working tree writes), restricted (working tree only), +nonet, nonet.",
				},
				"result_schema": map[string]any{
					"type":                 "object",
					"description":          "JSON Schema on initial/resumed turns (structured_result, structured_result_reason).",
					"additionalProperties": true,
				},
			},
			"required": []string{"prompt"},
		},
	}
	props := def.Parameters["properties"].(map[string]any)
	if sandboxSchema.ModelDescription != "" {
		props["model"].(map[string]any)["description"] = strings.TrimSpace(props["model"].(map[string]any)["description"].(string) + " " + sandboxSchema.ModelDescription)
	}
	if !sandboxSchema.Available {
		delete(props, "sandbox")
		def.Description += " Host lacks per-delegate sandboxing; omit `sandbox`."
		return def
	}
	if len(sandboxSchema.SandboxEnum) > 0 {
		props["sandbox"].(map[string]any)["enum"] = append([]string(nil), sandboxSchema.SandboxEnum...)
	} else if enum := DelegateSandboxEnumFromModes(sandboxSchema.Modes, true, false); len(enum) > 0 {
		props["sandbox"].(map[string]any)["enum"] = enum
	} else {
		// No usable sandbox mode; remove the control entirely.
		delete(props, "sandbox")
	}
	if sandboxSchema.SandboxDescription != "" {
		if sandbox, ok := props["sandbox"].(map[string]any); ok {
			sandbox["description"] = strings.TrimSpace(sandbox["description"].(string) + " " + sandboxSchema.SandboxDescription)
		}
	}
	return def
}

// DelegateSandboxEnumFromModes expands sandbox mode names into the combined
// sandbox+sandbox_net enum values: each non-off mode produces a base value
// (inherit parent network) and a +nonet variant (disable network). When the
// parent network is off, only +nonet variants are emitted. Bare "nonet"
// (inherit the parent's mode, disable network) is available under any non-off
// parent with network on; when parentModeOff it is omitted, since network
// confinement is meaningless without a sandbox.
func DelegateSandboxEnumFromModes(modes []string, parentNetwork, parentModeOff bool) []string {
	var values []string
	for _, mode := range modes {
		if mode == "off" {
			values = append(values, "off")
			continue
		}
		if parentNetwork {
			// Parent has network: base value inherits network, +nonet disables it.
			values = append(values, mode, mode+"+nonet")
		} else {
			// Parent network is off: only +nonet is enforceable.
			values = append(values, mode+"+nonet")
		}
	}
	if parentNetwork && !parentModeOff {
		values = append(values, "nonet")
	}
	return values
}

// DefDelegateSend defines the delegate_send tool, the single follow-up surface
// for your own durable delegates.
func DefDelegateSend() llm.ToolDefinition {
	return DefDelegateSendNamed(DefaultResultToolName)
}

// DefDelegateSendNamed is DefDelegateSend for a session whose result tool is
// named resultTool.
func DefDelegateSendNamed(resultTool string) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "delegate_send",
		Description: "Message (and wake) a direct child (`dlg_...`; use `" + resultTool + "` for parent).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"to":          map[string]any{"type": "string", "description": "Child by `dlg_...` ID."},
				"message":     map[string]any{"type": "string", "description": "Message."},
				"max_wait_ms": map[string]any{"type": "integer", "description": "0 (default) sends/steers now; >0 waits if idle (`earlier_results`), fails `target_busy` if running."},
			},
			"required": []string{"to", "message"},
		},
	}
}

// DefModelList exposes a bounded read-only view of a startup-frozen model
// snapshot. Cursor values are opaque and snapshot-authenticated by the agent.
// Paging never re-enumerates a provider, so the description names the
// recovery paths for one whose startup enumeration failed: a delegate model
// override is checked against that provider's live listing at spawn
// (selectSubagentModel), and a resumed session captures a new snapshot.
func DefModelList() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "model_list",
		Description: "List startup-frozen models, paged by cursor, max_count, and max_bytes. `delegate` probes provider/model live if startup failed; resuming captures a fresh snapshot.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"cursor":    map[string]any{"type": "string", "description": "Opaque `next` cursor (keep matching bounds)."},
				"max_count": map[string]any{"type": "integer", "minimum": 1, "maximum": 128, "description": "Max models per page (default 128)."},
				"max_bytes": map[string]any{"type": "integer", "minimum": 512, "maximum": 4096, "description": "Max bytes per page (default 4096)."},
			},
		},
	}
}

// DefJobWatch defines the job_watch tool, available to any session that can
// run jobs; cross-session sources authorize themselves. eventKinds are the
// model-facing session/job event-kind names available this session; they are
// interpolated into the description so the model can discover them (spec §5.11).
func DefJobWatch(eventKinds []string) llm.ToolDefinition {
	return DefJobWatchNamed(eventKinds, DefaultResultToolName)
}

// DefJobWatchNamed is DefJobWatch for a session whose result tool is named
// resultTool.
func DefJobWatchNamed(eventKinds []string, resultTool string) llm.ToolDefinition {
	strictFalse := false
	kinds := strings.Join(eventKinds, ", ")
	if kinds == "" {
		kinds = "none available this session"
	}
	desc := "Wake yourself later: `after_seconds` (60 to 86400) once or `repeat_seconds` (60 to 3600) recurring (`source` defaults to `self`; timers require a persistent session, not one-shot runs). Pass `operation` (create, list, inspect, or clear); `watch_id` is required for inspect or clear. For an ordinary `create` without `hub`, provide a trigger (`output_match`, `progress_interval_ms`, `events`, or a timer), except on a granted cross-session source like `parent` where omitting triggers watches all bounded public events. Session-event watches require a running target; on a finished shell job, `output_match` scans retained output and pure `progress_interval_ms` returns a terminal catch-up. `every` > 1 requires `events` to hold one kind. Watch a source you can observe (`self`, `parent`, `dlg_...`, or `job_id`). Delivery is implicit and coalesces. Events (available: " + kinds + "). " + ObserverReporting(resultTool)
	return llm.ToolDefinition{
		Name:        "job_watch",
		Description: desc,
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation":            map[string]any{"type": "string", "description": "create, list, inspect, or clear.", "enum": []string{"create", "list", "inspect", "clear"}},
				"watch_id":             map[string]any{"type": "string", "description": "Watch ID (required for inspect or clear)."},
				"source":               map[string]any{"type": []string{"string", "null"}, "description": "`self`, `parent`, `dlg_...`, or shell `job_id`."},
				"output_match":         map[string]any{"type": []string{"string", "null"}, "description": "RE2 regex on rolling 4096-byte job output."},
				"progress_interval_ms": map[string]any{"type": []string{"integer", "null"}, "description": "Concrete shell job progress interval in ms (1000-3600000)."},
				"after_seconds":        map[string]any{"type": []string{"integer", "null"}, "description": "One-shot delay in seconds (60-86400, source self only)."},
				"repeat_seconds":       map[string]any{"type": []string{"integer", "null"}, "description": "Repeat interval in seconds (60-3600, source self only)."},
				"note":                 map[string]any{"type": []string{"string", "null"}, "description": "Note delivered on each fire."},
				"events": map[string]any{
					"type":        []string{"array", "null"},
					"items":       map[string]any{"type": "string"},
					"description": "Event kinds.",
				},
				"every": map[string]any{
					"type":        []string{"integer", "null"},
					"description": "Fire every Nth match (default 1; >1 requires `events` to hold one kind).",
				},
				"event_filter": map[string]any{
					"type":                 []string{"object", "null"},
					"additionalProperties": false,
					"description":          "`assistant.tool` filter (requires `events: [\"assistant.tool\"]`).",
					"properties": map[string]any{
						"tool_name": map[string]any{"type": "string", "description": "Canonical tool name."},
						"status":    map[string]any{"type": "string", "description": "Outcome: ok or error.", "enum": []string{"ok", "error"}},
					},
				},
			},
			"required": []string{"operation"},
		},
	}
}

func DefJobStatus() llm.ToolDefinition {
	return DefJobStatusNamed(DefaultResultToolName)
}

// EndTurnToWait is the guidance for an agent that has nothing else to do until
// its background work finishes: yield quietly and let the completion wake it.
// It is the one wording shared by the job tool descriptions and the refusals of
// wait-shaped calls.
func EndTurnToWait(resultTool string) string {
	return "When idle waiting on background work, call `" + resultTool + "` (`next=\"log_quietly_and_end_turn\"`) with a brief wait note to yield quietly until woken, `" + resultTool + "` (`next=\"send_and_continue\"`) for an update first, or `job_watch` (`output_match`)."
}

// DefJobStatusNamed is DefJobStatus for a session whose result tool is named
// resultTool.
func DefJobStatusNamed(resultTool string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "job_status",
		Description: "Inspect metadata for one shell job or delegate (status, phase, timings, transcript_ref). Repeat calls on an unchanged running target are refused. " + EndTurnToWait(resultTool) + " Read output with `read_transcript(transcript_ref)`.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "Shell `job_id` or delegate `dlg_...` ID."},
			},
			"required": []string{"target"},
		},
	}
}

func DefJobList() llm.ToolDefinition {
	return DefJobListNamed(DefaultResultToolName)
}

var jobListStatusEnum = func() []any {
	names := appwire.JobListStatusFilterNames()
	out := make([]any, len(names))
	for i, name := range names {
		out[i] = name
	}
	return out
}()

// DefJobListNamed is DefJobList for a session whose result tool is named
// resultTool.
func DefJobListNamed(resultTool string) llm.ToolDefinition {
	strictFalse := false
	statusEnum := append([]any(nil), jobListStatusEnum...)
	typeEnum := []any{"shell", "delegate"}
	return llm.ToolDefinition{
		Name:        "job_list",
		Description: "List jobs, delegates, and watches, newest first. " + EndTurnToWait(resultTool) + " " + ObserverReporting(resultTool) + " Read transcripts for audit or diagnosis context. Terminal statuses include command_exited_nonzero and command_killed.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": statusEnum},
					"description": "Filter by status.",
				},
				"type": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": typeEnum},
					"description": "Filter by type (`shell` or `delegate`).",
				},
				"include_nested":      map[string]any{"type": "boolean", "default": false, "description": "Include child-session rows."},
				"include_descendants": map[string]any{"type": "boolean", "default": false, "description": "Walk live descendant tree."},
				"limit":               map[string]any{"type": "integer", "default": 50, "maximum": 100, "description": "Max rows (default 50, max 100)."},
				"offset":              map[string]any{"type": "integer", "default": 0, "description": "Listing start offset."},
			},
			"required": []any{},
		},
	}
}

func DefJobStop() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "job_stop",
		Description: "Cancel a shell job or delegate without deleting output or history (recursively stops a delegate's jobs).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target":           map[string]any{"type": "string", "description": "Shell `job_id` or delegate `dlg_...` ID."},
				"max_wait_ms":      map[string]any{"type": "integer", "description": "0 (default) returns now; >0 waits ms for settlement."},
				"include_children": map[string]any{"type": "boolean", "default": false, "description": "Also stop nested child shell jobs."},
			},
			"required": []string{"target"},
		},
	}
}

func DefGrep() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "grep",
		Description: "Search files by regex (skips dotfiles/gitignored unless in `glob_filter`).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern":          map[string]any{"type": "string", "description": "Regex."},
				"path":             map[string]any{"type": "string", "description": "File or dir."},
				"glob_filter":      map[string]any{"type": "string", "description": "File glob (e.g. `*.go`)."},
				"case_insensitive": map[string]any{"type": "boolean", "description": "Ignore case."},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Max results (default 100).",
				},
				"context_lines": map[string]any{
					"type":        "integer",
					"description": "0-10 context lines (default 0).",
				},
				"output_mode": map[string]any{
					"type":        "string",
					"enum":        []any{"content", "files_with_matches", "count"},
					"description": "Mode (default content).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func DefGlob() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "glob",
		Description: "Find files by recursive glob (skips dotfiles/gitignored by default).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern":         map[string]any{"type": "string", "description": "Glob pattern."},
				"path":            map[string]any{"type": "string", "description": "Base dir."},
				"include_ignored": map[string]any{"type": "boolean", "description": "Include dotfiles/gitignored."},
			},
			"required": []string{"pattern"},
		},
	}
}

func DefApplyPatch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "apply_patch",
		Description: `Apply a v4a patch for creating, modifying, or deleting files (not unified diff) wrapped in *** Begin Patch ... *** End Patch.
Headers: *** Add File: <path> (every line prefixed with +), *** Delete File: <path>, *** Update File: <path> (optional *** Move to: <new path>, then @@ [scope] hunks with space context, - removed, + added; ~3 lines context, or enough context / an @@ scope header to uniquely identify the intended occurrence when context matches more than once).
Paths resolve from cwd (use absolute paths outside cwd). Use scripting for bulk search-and-replace. Always end with *** End Patch and copy exact lines and indentation from read_file.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"patch": map[string]any{"type": "string", "description": "Complete v4a patch (*** Begin Patch ... *** End Patch)."},
			},
			"required": []string{"patch"},
		},
	}
}

func DefWebFetch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch a URL as markdown and answer a question.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"url":      map[string]any{"type": "string", "description": "HTTP(S) URL."},
				"question": map[string]any{"type": "string", "description": "Question."},
			},
			"required": []string{"url", "question"},
		},
	}
}

// DefaultResultToolName is the result tool's name unless a session renames it
// (SessionConfig.ResultToolName). Tool descriptions that teach the result tool
// are built from the configured name by the Def...Named constructors.
const DefaultResultToolName = "communicate"

// CommunicateNext values select what happens after a communicate call.
const (
	CommunicateNextSendAndEndTurn       = schema.CommunicateNextSendAndEndTurn
	CommunicateNextSendAndContinue      = schema.CommunicateNextSendAndContinue
	CommunicateNextLogQuietlyAndEndTurn = schema.CommunicateNextLogQuietlyAndEndTurn
)

// communicateDescription builds the communicate tool description for either a
// human-partner session or a delegate session from one shared template so the
// turn-end, update, quiet-yield, and output contracts stay in sync. It names no
// output fields because a bound result schema replaces the default output shape
// (provider.WithCommunicateOutputSchema).
func communicateDescription(audience, finalUse string) string {
	return "Message " + audience +
		". Default next=\"send_and_end_turn\" ENDS YOUR TURN IMMEDIATELY (use only " + finalUse +
		"); pass next=\"send_and_continue\" for mid-work updates, or next=\"log_quietly_and_end_turn\" (no prose) when waiting on background work. " +
		"Include `output` only on `send_and_end_turn` (required when a result schema is bound; omit on updates and quiet yields)."
}

// communicateNextDescription builds the `next` enum parameter description for
// partner and delegate communicate definitions, leading with send_and_continue
// so models see the mid-work update option before the turn-ending default.
func communicateNextDescription(updateTarget, finalKind, quietEffect string) string {
	return "\"send_and_continue\" sends a mid-work update" + updateTarget +
		" and keeps working; \"send_and_end_turn\" (default) ENDS THE TURN immediately with " + finalKind +
		"; \"log_quietly_and_end_turn\" logs a quiet wait note" + quietEffect + " and ends the turn."
}

func DefCommunicate() llm.ToolDefinition {
	return DefCommunicateNamed(DefaultResultToolName)
}

func DefCommunicateNamed(name string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        name,
		Description: communicateDescription("your partner/caller", "when complete and verified"),
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Reply, update, or wait note.",
				},
				"next": map[string]any{
					"type":        "string",
					"enum":        slices.Clone(schema.CommunicateNextValues),
					"default":     CommunicateNextSendAndEndTurn,
					"description": communicateNextDescription("", "your final reply", ""),
				},
				"output": map[string]any{
					"type":                 "object",
					"description":          "Structured result (`send_and_end_turn` only; required when a result schema is bound).",
					"additionalProperties": false,
					"properties": map[string]any{
						"message": map[string]any{"type": "string", "description": "Summary."},
						"data": map[string]any{
							"type":                 "object",
							"description":          "JSON data.",
							"additionalProperties": true,
							"properties":           map[string]any{},
						},
						"artifacts": map[string]any{
							"type":        "array",
							"description": "Paths or URIs.",
							"items":       map[string]any{"type": "string"},
						},
					},
				},
			},
			"required": []string{"message"},
		},
	}
}

// ObserverReporting is how an observer sidecar talks to the session it
// watches through resultTool, shared by every tool description and error that
// teaches it.
func ObserverReporting(resultTool string) string {
	return "Observers: `" + resultTool + "` (`next=\"send_and_end_turn\"`) ready/findings, `next=\"send_and_continue\"` update, `next=\"log_quietly_and_end_turn\"` quiet."
}

// WithCommunicateDelegateUpdates returns a copy of a communicate definition
// worded for a delegate: next="send_and_continue" sends an update its parent
// receives, next="send_and_end_turn" (or omitted) its report or an observer
// readiness line, and next="log_quietly_and_end_turn" a quiet yield with a
// note. The output schema stays as the caller built it.
func WithCommunicateDelegateUpdates(def llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(def.Parameters)
	props, _ := params["properties"].(map[string]any)
	if message, ok := props["message"].(map[string]any); ok {
		message["description"] = "Report, update, or wait note."
	}
	if nextProp, ok := props["next"].(map[string]any); ok {
		nextProp["description"] = communicateNextDescription(" to your parent", "your report or readiness line", " without waking your parent")
	}
	def.Description = communicateDescription("your parent agent", "for your report or readiness line")
	def.Parameters = params
	return def
}

// WithCommunicateDelegatePointer returns a copy of a communicate definition
// that also points to delegate_send, for a session that can message its own
// delegates. communicate only reaches a human partner or a parent, so without
// the pointer a model answering its delegate's question reaches for it.
func WithCommunicateDelegatePointer(def llm.ToolDefinition) llm.ToolDefinition {
	def.Description += " Use `delegate_send` for delegates."
	return def
}

func DefTaskList(effortLevels []string) llm.ToolDefinition {
	addReasoningSchema := map[string]any{
		"type":        "string",
		"description": "Effort (default/\"inherit\": session).",
	}
	updateReasoningSchema := map[string]any{
		"type":        "string",
		"description": "Effort (\"inherit\": unchanged).",
	}
	if len(effortLevels) > 0 {
		// "inherit" rides along so a strict-mode provider (which force-requires
		// every property) still lets the model decline to override.
		enum := append(append([]string(nil), effortLevels...), "inherit")
		addReasoningSchema["enum"] = enum
		updateReasoningSchema["enum"] = append([]string(nil), enum...)
	}
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "task_list",
		Description: "View (`{}`) or edit (`add`/`update`); finishing one auto-starts next. Only one task may be in_progress at a time; to start a new one, complete or defer the current one in the same update array.",
		// Strict is explicitly false: the OpenAI Responses adapter defaults
		// strict=true when unset and force-requires every nested property,
		// which would force strict-mode models to emit "status": "" (enum
		// violation) or "depends_on": [] on every update item. Opting out keeps
		// optional fields genuinely omittable; task_list's NormalizeArgs still
		// drops update values that mean no change (its updatePlaceholders
		// table), since some models send them anyway.
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"add": map[string]any{
					"type":        "array",
					"description": "Tasks to add.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"type": map[string]any{
								"type":        "string",
								"enum":        []string{"research", "implement", "verify", "fix"},
								"description": "Kind (default implement).",
							},
							"description": map[string]any{"type": "string", "description": shortTaskTitleDescription},
							"prompt":      map[string]any{"type": "string", "description": "Instruction."},
							"depends_on": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "Prereq IDs.",
							},
							"reasoning_effort": addReasoningSchema,
						},
						"required": []string{"description", "prompt"},
					},
				},
				"update": map[string]any{
					"type":        "array",
					"description": "Updates by `id`.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":     map[string]any{"type": "integer", "description": "ID."},
							"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "cancelled"}, "description": "Status."},
							"notes":  map[string]any{"type": "string", "description": "Notes."},
							"depends_on": map[string]any{
								"type":        []string{"array", "null"},
								"items":       map[string]any{"type": "integer"},
								"description": "Replace prereqs ([0] clears).",
							},
							"reasoning_effort": updateReasoningSchema,
						},
						"required": []string{"id"},
					},
				},
			},
		},
	}
}

func DefUseSkill() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "use_skill",
		Description: "Load a catalog skill.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"skill_name": map[string]any{"type": "string", "description": "Name."},
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
		Description: "Search archived sessions (`{}` for recent, `query` for substring, `children_of` for direct children; repeat on child refs for deeper levels) and return `transcript_ref` handles for `read_transcript`. Snippets with `seq: -1` are display markers only.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query":          map[string]any{"type": "string", "description": "Case-insensitive substring (no regex or boolean operators)."},
				"children_of":    map[string]any{"type": "string", "description": "Parent transcript_ref for direct children (overrides query)."},
				"kind":           map[string]any{"type": "string", "enum": []string{"root", "subagent", "fork", "any"}, "description": "Lineage kind."},
				"has_children":   map[string]any{"type": "boolean", "description": "Require children (true/false)."},
				"min_turns":      map[string]any{"type": "integer", "description": "Min turns."},
				"max_turns":      map[string]any{"type": "integer", "description": "Max turns."},
				"updated_after":  map[string]any{"type": "string", "description": "RFC3339 min time."},
				"updated_before": map[string]any{"type": "string", "description": "RFC3339 max time."},
				"scope":          map[string]any{"type": "string", "enum": []string{"current_project", "all_projects"}, "description": "Scope (default current_project)."},
				"limit":          map[string]any{"type": "integer", "description": "Max results (default 10, max 50)."},
			},
		},
	}
}

// DefDoctorEvener defines the read-only in-process doctor data plane: the
// same commands and result shapes as the `evener doctor` CLI, executed against
// the session's own state root by default so inspection never depends on a
// shell, PATH, or cwd. Mirrors the doctoring-evener skill's command table.
//
// The command enum lives here as the single source: the definition's schema
// and the agent-layer dispatcher both derive from DoctorEvenerCommands, so
// the tool surface and dispatch cannot drift.
func DoctorEvenerCommands() []string {
	return []string{"locate", "transcript", "apilog", "jobs", "mutations", "watches", "tree", "turnids", "sessions", "audit", "plugins", "lifecycle"}
}

// DoctorEvenerSelectorDialect is the selector grammar phrase shared by the
// definition's prose and the agent-layer dispatcher's usage errors, so the
// schema and the errors cannot drift apart.
const DoctorEvenerSelectorDialect = "local:<id>, proj:<project-id>:<id>, or bare <id>"

// DoctorSessionsDefaultSince bounds a doctor_evener sessions census that names
// no since. A state root can hold tens of thousands of sessions; an unbounded
// census from a turn would run for the better part of an hour.
const DoctorSessionsDefaultSince = 7 * 24 * time.Hour

// doctorSessionsDefaultSinceText renders the default in the duration syntax
// the since argument accepts.
var doctorSessionsDefaultSinceText = fmt.Sprintf("%dh", int(DoctorSessionsDefaultSince.Hours()))

func DefDoctorEvener() llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "doctor_evener",
		Description: "Read-only forensic inspection of Evener state (`evener doctor`). Load `doctoring-evener` before diagnosing or running runbooks.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"enum":        DoctorEvenerCommands(),
					"description": "Subcommand.",
				},
				"selector":  map[string]any{"type": "string", "description": DoctorEvenerSelectorDialect + " (required by locate/transcript/apilog/jobs/mutations/watches/tree; rejected by sessions/audit/turnids/plugins/lifecycle)."},
				"state_dir": map[string]any{"type": "string", "description": "State root override (rejected by plugins/lifecycle)."},
				"count":     map[string]any{"type": "string", "description": "transcript: count tool calls."},
				"health":    map[string]any{"type": "boolean", "description": "transcript/apilog: health."},
				"range":     map[string]any{"type": "string", "description": "transcript: turn window."},
				"text_max":  map[string]any{"type": "integer", "description": "transcript: per-turn byte cap."},
				"full_text": map[string]any{"type": "boolean", "description": "transcript: uncapped text."},
				"empty":     map[string]any{"type": "boolean", "description": "apilog: empty replies."},
				"errors":    map[string]any{"type": "boolean", "description": "apilog: errors only."},
				"cache_spikes": map[string]any{
					"type":        "boolean",
					"description": "apilog: uncached >= threshold.",
				},
				"threshold": map[string]any{"type": "integer", "description": "apilog: cache_spikes floor."},
				"summary":   map[string]any{"type": "boolean", "description": "apilog: aggregate only."},
				"validate":  map[string]any{"type": "boolean", "description": "apilog: integrity scan."},
				"recompute": map[string]any{
					"type":        "boolean",
					"description": "apilog: recompute from bodies.",
				},
				"job_id":   map[string]any{"type": "string", "description": "jobs: filter job_id."},
				"watch_id": map[string]any{"type": "string", "description": "watches: filter watch_id."},
				"self_loops": map[string]any{
					"type":        "boolean",
					"description": "watches: loop hits only.",
				},
				"depth":     map[string]any{"type": "integer", "description": "tree: max depth."},
				"observers": map[string]any{"type": "boolean", "description": "tree: with observers."},
				"since":     map[string]any{"type": "string", "description": "sessions/audit/turnids: window (sessions default " + doctorSessionsDefaultSinceText + "; audit requires sessions XOR since)."},
				"bucket":    map[string]any{"type": "string", "description": "sessions/turnids: bucket ID."},
				"runbook":   map[string]any{"type": "string", "description": "audit: runbook name (required for audit)."},
				"sessions":  map[string]any{"type": "string", "description": "audit: CSV selectors (audit requires sessions XOR since)."},
			},
			"required": []string{"command"},
		},
	}
}

// DefManageWorktree defines the manage_worktree lifecycle tool (spec §2): a
// single tool with an operation enum, mirroring task_list's action pattern.
// Args are flattened across operations rather than split per-op in the
// schema; which args apply to which operation is documented in the
// description and enforced by the handler, not the schema. Each arg's own
// description names the operations that read it.
func DefManageWorktree() llm.ToolDefinition {
	desc := "Manage git worktrees for isolated, parallel, or risky work. `prune` removes lanes without unmerged work."
	return llm.ToolDefinition{
		Name:        "manage_worktree",
		Description: desc,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation": map[string]any{
					"type":        "string",
					"description": "Worktree operation.",
					"enum":        []string{"create", "list", "switch", "adopt", "exit", "remove", "prune", "unlock", "dispose"},
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Worktree/branch name (required for create/remove; switch needs name XOR path).",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Delegate ID (`dlg_...`, required for unlock/dispose).",
				},
				"base_ref": map[string]any{
					"type":        "string",
					"description": "Base ref for create (default HEAD).",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Worktree path (required for adopt; switch needs name XOR path).",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Override merge-safety check (keeps dirty edits safe).",
				},
				"force_dirty": map[string]any{
					"type":        "boolean",
					"description": "Discard uncommitted changes.",
				},
				"delete_branch": map[string]any{
					"type":        "boolean",
					"description": "Also delete branch on remove.",
				},
				"orphaned_delegate_days": map[string]any{
					"type":        "number",
					"description": "Prune locks for parents inactive this many days (0 default; positive min ~0.00072).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"default":     15,
					"minimum":     1,
					"maximum":     15,
					"description": "List limit (default 15, max 15).",
				},
				"offset": map[string]any{
					"type":        "integer",
					"default":     0,
					"minimum":     0,
					"description": "List start offset.",
				},
			},
			"required": []string{"operation"},
		},
	}
}

// DefManageWorktreeDisposeOnly is the dispose-only variant of manage_worktree
// (delegate-lane disposal spec §P1 "Availability"). It is served — under the
// same tool name — to a worktree-isolated coordinator: a delegate spawned with
// isolation:"worktree" that itself carries a delegation allowance. Such a
// session lives inside its own lane; letting it create/switch/remove/prune
// worktrees would let it disturb the sibling lanes its parent created (see
// rootOnlyWorktreeTools), so the schema exposes only the dispose operation —
// retiring its OWN sub-delegates' lanes. The registry can add or remove only
// whole tools, not individual operations, so the handler enforces the same
// restriction in-line (session_tools_worktree.go).
func DefManageWorktreeDisposeOnly() llm.ToolDefinition {
	desc := "Retire one of your own delegates' isolation worktree lanes by `id` (`dlg_...`): unlocks, removes worktree, deletes branch, and marks disposed. Only `dispose` is permitted inside your own isolation lane (`force` discards unmerged commits; `force_dirty` discards uncommitted changes)."
	return llm.ToolDefinition{
		Name:        "manage_worktree",
		Description: desc,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation": map[string]any{
					"type":        "string",
					"description": "Only dispose is available.",
					"enum":        []string{"dispose"},
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Delegate ID (`dlg_...`, required).",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Discard unmerged commits (does not discard uncommitted edits).",
				},
				"force_dirty": map[string]any{
					"type":        "boolean",
					"description": "Discard uncommitted changes.",
				},
			},
			"required": []string{"operation"},
		},
	}
}

// DefReadTranscript defines the retained-evidence reader for session, job, and
// artifact references. Its descriptions mirror the executor's ref-specific
// operation and compatibility rules.
func DefReadTranscript() llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name: "read_transcript",
		Description: "Read retained evidence by `transcript_ref`: a session ref, `job:<job_id>` (from `job_status`, `job_list`, a windowed shell result, or a watch frame), or `artifact:<id>`. " +
			"Session refs support `format`, `range`, and `expand_turn` (requires `format=\"markdown\"`, the default, and `offset_bytes` continues that expansion in fixed 16 KiB pages). " +
			"`job:` (with `job_status` and `retained_start_bytes`) and `artifact:` refs byte-page via `offset_bytes` or search via `output_match` with `context_lines`. " +
			"Session refs reject `output_match`; `job:` refs reject `range`, `expand_turn`, `outline`, and `jsonl`; `artifact:` refs reject explicit `format`, `range`, and `expand_turn`. " +
			"Never poll this waiting for completion.",
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"transcript_ref": map[string]any{"type": "string", "description": "Session ref, ID, `current`, `job:<job_id>`, or `artifact:<id>`."},
				"format":         map[string]any{"type": []any{"string", "null"}, "enum": []any{"outline", "markdown", "jsonl", nil}, "description": "Output format (default markdown)."},
				"range":          map[string]any{"type": []any{"string", "null"}, "description": "Turn window (`12-40`, `last:40`, or `start:40`)."},
				"expand_turn":    map[string]any{"type": []any{"integer", "null"}, "minimum": 0, "description": "Session markdown only: expand any semantic Turn N as byte-paged transcript_v2_jsonl."},
				"offset_bytes":   map[string]any{"type": "integer", "minimum": 0, "description": "Continuation byte offset for session `expand_turn`, or raw page/search start for `job:`/`artifact:`."},
				"output_match":   map[string]any{"type": []any{"string", "null"}, "maxLength": 65_536, "description": "RE2 regex for job:/artifact: search (max 65,536 chars)."},
				"context_lines": map[string]any{
					"type":        []any{"integer", "null"},
					"minimum":     0,
					"maximum":     10,
					"description": "Context lines (0-10, default 0; requires output_match).",
				},
			},
		},
	}
}

// DefAskUser defines the ask_user tool: structured, mostly multiple-choice
// questions that end the model's turn at the asking round's boundary. Strict is
// false because multi_select/why/if_unanswered/recommended are all optional.
func DefAskUser() llm.ToolDefinition {
	return DefAskUserNamed(DefaultResultToolName)
}

// DefAskUserNamed is DefAskUser for a session whose result tool is named
// resultTool.
func DefAskUserNamed(resultTool string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name: "ask_user",
		Description: "Ask structured questions. Ends your turn after the round and waits indefinitely for the answer. " +
			"First try to resolve the question yourself with tools, and finish independent work before asking. " +
			"Multiple questions? Call multiple times per round (`" + resultTool + "` in the same round still delivers). " +
			"Give 1-4 `questions` with 2-5 unique-label `options`, putting any `recommended` option first. " +
			"The harness adds an \"Other\" free-text option and \"you decide\". " +
			"Replies arrive as `[answers]` (choice, free text, \"you decide\", fallback, or skipped: use best judgment, state assumptions, and do not re-ask) or prose; read any user note, which may override a choice.",
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "1-4 questions.",
					"minItems":    1,
					"maxItems":    4,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"header":   map[string]any{"type": "string", "description": "Optional label (default Question N)."},
							"question": map[string]any{"type": "string", "description": "Question text."},
							"options": map[string]any{
								"type":        "array",
								"description": "2-5 unique-label options.",
								"minItems":    2,
								"maxItems":    5,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": "string", "description": "Unique label."},
										"detail":      map[string]any{"type": "string", "description": "Option detail."},
										"recommended": map[string]any{"type": "boolean", "description": "Recommend at most one option (put it first)."},
									},
									"required": []string{"label", "detail"},
								},
							},
							"multi_select": map[string]any{"type": "boolean", "default": false, "description": "Allow multiple selections."},
							"why":          map[string]any{"type": "string", "description": "What the answer changes."},
							"if_unanswered": map[string]any{
								"type":        "string",
								"description": "Proposed fallback the user can accept (if skipped, use judgment and state the assumption).",
							},
						},
						"required": []string{"question", "options"},
					},
				},
			},
			"required": []string{"questions"},
		},
	}
}

// DefUpdateGoal returns the tool definition for update_goal.
// The model calls this to declare the active goal complete or blocked.
func DefUpdateGoal() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "update_goal",
		Description: `Mark the active session goal complete or blocked. ` +
			`"complete" only when the objective is genuinely achieved and verified ` +
			`per the goal guidance; "blocked" only when truly stuck per that guidance. ` +
			`(Criteria live in the continuation guidance, not repeated here.)`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"complete", "blocked"},
					"description": "complete: genuinely achieved and verified; blocked: truly stuck.",
				},
			},
			"required": []string{"status"},
		},
	}
}

// DefNotesAgentSet returns the tool definition for notes_agent_set.
// The model calls this to keep its session whiteboard current: a short capsule
// of the session for a manager (mission and progress, "Now:", "Next:").
// (Agent tool, not a hub RPC: the daemon handles notes/agent/set in session.
// Tool names cannot contain slashes — see llm.ValidateToolName — so the agent
// tool uses snake_case.)
func DefNotesAgentSet() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "notes_agent_set",
		Description: `Replace your session whiteboard (under 600 characters, plain words with no paths, IDs, or jargon). ` +
			`Write three parts on separate lines: a short mission/progress summary, a "Now:" line, and one or two "Next:" lines. ` +
			`Update when a phase finishes, the plan changes, you are blocked, and before your final report.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note": map[string]any{
					"type":        "string",
					"description": "Whiteboard text in the three parts above (empty clears it).",
				},
			},
			"required": []string{"note"},
		},
	}
}

// DefUrlsAdd returns the tool definition for urls_add.
// The model calls this to add a URL to the session's shared URL list.
// (Agent tool, not a hub RPC: the daemon handles urls/add in session.)
func DefUrlsAdd() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "urls_add",
		Description: `Add an http(s), file:///, or relative path (prefix with "./" if the first segment has a colon) to the session URL list, updating any matching entry's label.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "URL or relative path.",
				},
				"label": map[string]any{
					"type":        "string",
					"description": "Optional short label.",
				},
			},
			"required": []string{"url"},
		},
	}
}

// DefUrlsRemove returns the tool definition for urls_remove.
// The model calls this to remove a URL list entry by its id.
// (Wire RPC method: urls/remove.)
func DefUrlsRemove() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "urls_remove",
		Description: `Remove a URL entry by its entry ID.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "URL entry ID.",
				},
			},
			"required": []string{"id"},
		},
	}
}

// DefNotesRead returns the tool definition for notes_read.
// The model calls this to read the current shared notes and URL list.
// (Wire read: notes/read.)
func DefNotesRead() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "notes_read",
		Description: `Read the human's whiteboard, your whiteboard, and the shared URL list.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
	}
}

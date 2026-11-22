package tool

import (
	"fmt"
	"slices"
	"strings"
	"time"

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
	props["scope"] = map[string]any{"type": "string", "enum": []any{"personal", "project"}, "description": "Which memory to use: personal or project."}
	if param, ok := props["file_path"].(map[string]any); ok {
		param["description"] = "Path of the memory file, relative to the scope root, such as topic.md or tools/vitest.md; never an absolute path or a file outside memory."
	}
	if param, ok := props["path"].(map[string]any); ok {
		param["description"] = "File or directory to search, relative to the scope root; blank searches the whole scope."
	}
	base.Parameters["required"] = append(base.Parameters["required"].([]string), "scope")
	if name == "memory_read" && !strings.Contains(base.Description, "Reading MEMORY.md") {
		base.Description += " Reading MEMORY.md at the scope root returns the whole generated index."
	}
	base.Description = "Operate on a relative path in the bound personal or project memory wiki. " + base.Description
	return base
}

func DefMemoryRead() llm.ToolDefinition {
	return MemoryDefinition(DefReadFile(), "memory_read")
}

func DefMemoryDelete() llm.ToolDefinition {
	return MemoryDefinition(llm.ToolDefinition{
		Description: "Remove one memory file, not a directory; directories it leaves empty go too. Missing files are a no-op. Its index line goes away on its own; read first, and repair links from other pages separately if needed.",
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
		Description: "Write one memory page. Evener writes the page's frontmatter from when, followed by body, and replaces the whole page when it already exists.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"when":      map[string]any{"type": "string", "description": "One line saying when a future session needs the page: the moments it would otherwise get this wrong, at the level that covers all of them (\"When a log line could include a customer's street address\"). If those moments call for different facts, the page holds several facts, so give each its own page, even facts your partner told you in one breath."},
				"body":      map[string]any{"type": "string", "description": "The page's Markdown, without frontmatter: a first heading naming what the page is about, such as \"Customer addresses in logs\" rather than the rule itself; then one durable fact, led by the fact or rule, a one-line **Why:** (with where the fact came from, when that matters) and a one-line **How to apply:** saying when it bears on work, with absolute dates."},
			},
			"required": []string{"file_path", "when", "body"},
		},
	}, "memory_write")
}

// readFileCoreDescription is the description every read_file vision regime
// shares. A regime appends its own vision_prompt sentence, or none when the
// model sees images and PDFs inline.
const readFileCoreDescription = "Read a file from the filesystem. Returns line-numbered content for text files (up to 2000 lines or ~48 KB per call; lines over 2000 chars are truncated, and passing limit=1 on a line returns it uncut up to ~48 KB). Prefer reading whole files or 300-500+ line windows over small 50-100 line slices, batch independent read_file calls in parallel, and use read_file instead of shell cat/head/tail/sed. Pass a negative offset (e.g. offset=-100) to read from the end of a file. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image (downscaled if oversized). For PDF files, returns the document (pass pages for PDFs over 10 pages or 20 MiB)."

// defReadFile builds read_file's shared schema so the three vision regimes and
// memory_read cannot drift on the core description, offset, limit, or pages.
// visionSuffix is appended to the description, and visionParamDesc becomes the
// vision_prompt property; both are empty in Regime 1, which omits vision_prompt
// entirely (#6190).
func defReadFile(visionSuffix, visionParamDesc string) llm.ToolDefinition {
	props := map[string]any{
		"file_path": map[string]any{"type": "string", "description": "Path of the file to read."},
		"offset":    map[string]any{"type": "integer", "description": "1-based start line (default 1), or a negative integer to read from the end of the file (e.g. -100 reads the last 100 lines)."},
		"limit":     map[string]any{"type": "integer", "description": "Maximum lines to return (default 2000; output is also bounded at ~48 KB). Pass 1 to read a single long line uncut."},
		"pages":     map[string]any{"type": "string", "description": "PDF reads only: 1-based page or inclusive page range (e.g. \"1-5\" or \"3\"; max 20 pages per call). Required for PDFs over 10 pages or 20 MiB."},
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
	return defReadFile(" When reading an image or PDF, put what you hope to learn in the `vision_prompt` argument — the system will provide a detailed description alongside the file.", "Image/PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate.")
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
	return defReadFile(" When reading a PDF, put what you hope to learn in the `vision_prompt` argument — the system will provide a detailed description alongside the file.", "PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate.")
}

func DefWriteFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "write_file",
		Description: "Write content to a file. Creates the file and parent directories if needed, and replaces the entire file contents when the file already exists. Use this for new files or intentional full rewrites; prefer the exact-edit tool for small changes to existing files.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string", "description": "Path of the file to write; parent directories are created."},
				"content":   map[string]any{"type": "string", "description": "Complete new contents; replaces an existing file entirely."},
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
				"path":   map[string]any{"type": "string", "description": "Directory to list; blank means the working root."},
				"depth":  map[string]any{"type": "integer", "description": "Recursion depth; 1 lists only this directory (default). Values above 16 are clamped to 16."},
				"offset": map[string]any{"type": "integer", "description": "Index of the first entry to return (default 0). Use with limit to page a large directory."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum entries to return (default 1000; a page-size budget may return fewer)."},
			},
		},
	}
}

func defEditFile(readTool, writeTool string) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "edit_file",
		Description: fmt.Sprintf(
			"Apply one or more exact replacements to an existing file atomically. "+
				"Read the file first with %s and never copy line-number prefixes (like '12\\t' or '12:') into old_string or new_string. "+
				"Batch disjoint changes in the same file into one call via edits rather than separate calls. "+
				"Every edits[].old_string matches the original file (not after earlier edits) and must not overlap any other edit; merge nearby changes into one entry. "+
				"Keep each old_string as small as possible while unique, and use %s when replacing most or all of a file. "+
				"If any edit fails to match, is ambiguous, or overlaps another edit, nothing is written.",
			readTool, writeTool,
		),
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path of the existing file to edit.",
				},
				"edits": map[string]any{
					"type":        "array",
					"minItems":    1,
					"description": "Disjoint replacements matched against the original file and applied atomically.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"old_string": map[string]any{
								"type":        "string",
								"description": "Non-empty exact text from the original file to replace (unique unless replace_all is true).",
							},
							"new_string": map[string]any{
								"type":        "string",
								"description": "Replacement text.",
							},
							"replace_all": map[string]any{
								"type":        "boolean",
								"description": "Replace every occurrence in the original file instead of requiring a unique match (default false).",
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
	modeDescription := "foreground waits for the command to finish before continuing; background runs a session-owned job in the background and Evener stops it when the session ends"
	if detachAvailable {
		modeEnum = append(modeEnum, "detached")
		modeDescription += "; detached is the only mode that lets a process survive after this Evener session stops — a detached process is not a job: its output is discarded (redirect it to an absolute path the process may write) and it sends no completion notification. A detached command gets no session scratch: $EVENER_SCRATCH_DIR is unset there, so no path it reads or writes may be built from it."
	} else {
		modeDescription += ". This environment cannot disown a process, so detached mode is unavailable; nothing can outlive the session."
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
	return llm.ToolDefinition{
		Name: "shell",
		Description: "Run a shell command and report stdout, stderr, and exit status. " +
			"The command runs in Bash with pipefail, so a failed stage fails the whole command; check the exit code. " +
			"Small foreground output comes back in full. Larger or background output is bounded: a finished command with large output returns a head-and-tail digest and a job_id under which the full output is kept, so there is no need to pipe through head or tail. " +
			"A background job reports a launch failure at once; once it is running you get its job_id and a notification when it finishes, so it needs no completion marker or redirect. " +
			"To keep a complete log beyond the retained output, tee inside the command, for example `( make test ) 2>&1 | tee \"$EVENER_SCRATCH_DIR/test.log\"` when EVENER_SCRATCH_DIR is set: pipefail keeps the command's exit status, and tee overwrites the file. " +
			"A `&` inside the command creates neither a tracked job nor an escape from teardown: when the command ends or its job is stopped, its whole process group is torn down — descendants holding an inherited output pipe get the SIGTERM/SIGKILL escalation, and any other group member, for example a child that redirected its own output, is killed with the group. Run a helper that must outlive the command as its own background job or in detached mode instead.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"command":     map[string]any{"type": "string", "description": "The shell command to run (POSIX: Bash with pipefail)."},
				"description": map[string]any{"type": "string", "description": "Optional human-facing summary of the command, shown on job surfaces."},
				"mode":        ShellModeProperty(detachAvailable),
				"cwd":         map[string]any{"type": "string", "description": "Optional working directory for the command. Relative paths resolve against your current working directory. With sandbox off, paths may be outside it; confined policies require paths inside it. Must already exist. Default: your current working directory."},
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

const delegateModelOverrideDescription = "Model override. Default captures your current model at spawn (later switches do not affect existing delegates); an explicit value pins that model."

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
	return fmt.Sprintf("Effort (%s, or %s=off); default inherits.",
		strings.Join(llm.ReasoningEffortVocabulary(), ", "), llm.ReasoningEffortNone)
}

// DefDelegate defines the delegate tool, which starts a NEW delegate
// conversation (independent agentic work) and returns its durable stable identity.
// agentTypes constrains the agent_type enum to the session's
// available roles; pass nil to omit the enum (free-form). reasoning_effort uses
// the delegate contract's portable vocabulary (delegateReasoningEffortLevels);
// the handler clamps it to the child model's own ladder. The legacy constructor
// exposes the full sandbox surface; session-owned callers should use
// DefDelegateWithSandbox so the advertised controls match the host and parent
// capabilities.
func DefDelegate(agentTypes []string) llm.ToolDefinition {
	return DefDelegateWithSandbox(agentTypes, DelegateSandboxSchema{
		Available: true,
		Modes:     []string{"off", "read-only", "workspace-write", "restricted"},
	})
}

// DefDelegateWithSandbox is DefDelegate with a capability-honest sandbox
// surface. It keeps the stable delegate contract identical for unrelated
// parameters while removing or constraining sandbox values the session cannot
// enforce.
func DefDelegateWithSandbox(agentTypes []string, sandboxSchema DelegateSandboxSchema) llm.ToolDefinition {
	return DefDelegateWithResultTool(agentTypes, sandboxSchema, DefaultResultToolName)
}

// DelegateRoleWriteNote tells the model which role may write files. The
// explorer role has no edit tools, so a seat that must write (a reviewer that
// files its report, an implementer) takes subagent.
const DelegateRoleWriteNote = "explorer is read-only; write via subagent."

// DefDelegateWithResultTool is DefDelegateWithSandbox for a session whose
// result tool is named resultTool.
func DefDelegateWithResultTool(agentTypes []string, sandboxSchema DelegateSandboxSchema, resultTool string) llm.ToolDefinition {
	strictFalse := false
	effortLevels := delegateReasoningEffortLevels()
	agentTypeSchema := map[string]any{
		"type":        "string",
		"description": "Role from enum (see agents section). " + DelegateRoleWriteNote,
	}
	if len(agentTypes) > 0 {
		agentTypeSchema["enum"] = append([]string(nil), agentTypes...)
	}
	def := llm.ToolDefinition{
		Name: "delegate",
		Description: "Start a NEW delegate; returns durable `delegate_id` immediately (follow up via `delegate_send(to=<delegate_id>)`). " +
			"Ask it to report via `" + resultTool + "`. `watch_parent=true` creates an observer (`job_watch(source=\"parent\")`). " +
			ObserverReporting(resultTool) + " Observer readiness may list `watching:true`/`watches`. " +
			"Child shell jobs end with its activation and wake the child, not you (run waits you need here); parent watches survive until cleared. Read transcript if notice omits report.",
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Self-contained brief (clean unless fork_context=true): quoted request, facts/paths/tools, owned/forbidden paths, acceptance check, required evidence. Multi-step -> task_list. Confined seats write workspace/scratch only (return report in result, not artifacts_dir).",
				},
				"task_list": map[string]any{
					"type":        "array",
					"description": "Ordered steps seeding delegate's task list (fills parent_tasks or follows defaults); each prompt must stand alone.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"title":            map[string]any{"type": "string", "description": shortTaskTitleDescription},
							"prompt":           map[string]any{"type": "string", "description": "Self-contained step prompt."},
							"reasoning_effort": map[string]any{"type": "string", "enum": append(slices.Clone(effortLevels), "inherit"), "description": "Step effort (\"inherit\" = delegate's)."},
							"type":             map[string]any{"type": "string", "enum": []string{"research", "implement", "verify", "fix"}, "description": "Work kind (default implement)."},
						},
						"required": []string{"title", "prompt"},
					},
				},
				"agent_type":           agentTypeSchema,
				"model":                map[string]any{"type": "string", "description": delegateModelOverrideDescription},
				"reasoning_effort":     map[string]any{"type": "string", "description": delegateReasoningEffortDescription(), "enum": effortLevels},
				"delegation_allowance": map[string]any{"type": "integer", "description": "Default: yours-1; 0=leaf; >0=cap (<yours) if role has delegate."},
				"watch_parent":         map[string]any{"type": "boolean", "description": "Grant job_watch(source=\"parent\") (non-transitive, no delegation)."},
				"fork_context": map[string]any{
					"type": "boolean", "default": false,
					"description": "Copy parent history (excluding active round); needs parent model/provider. Default false.",
				},
				"isolation": map[string]any{
					"type":        "string",
					"enum":        []string{"worktree"},
					"description": "\"worktree\": isolated git branch from HEAD (concurrent writers; delegating children get dispose-only manage_worktree). Default shares cwd.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Mnemonic label (addressing uses delegate_id); with isolation=\"worktree\", names git branch.",
				},
				"sandbox": map[string]any{
					"type":        "string",
					"enum":        []string{"off", "read-only", "workspace-write", "restricted"},
					"description": "Sandbox (at least as confining as yours on reads/writes): \"off\", \"read-only\" (no writes, temp dir), \"workspace-write\" (writes working tree), \"restricted\" (working tree only). \"+nonet\" disables net; \"nonet\" drops net only. Masks secrets, confines shells.",
				},
				"result_schema": map[string]any{
					"type":                 "object",
					"description":          "Draft 2020-12 JSON Schema (properties must be schema objects); validated on initial/resumed turns -> structured_result or structured_result_reason.",
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
		def.Description += " This session's host cannot enforce per-delegate sandboxing, so `sandbox` is unavailable; do not send it."
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
		Name: "delegate_send",
		Description: "Sends a message to one of your own delegates, by the durable delegate_id you got when you created it. " +
			"To reach your own parent, use `" + resultTool + "` instead: end_turn=false for an update, end_turn=true for your report, the one line naming the work you are waiting on, or a readiness line before a watch frame. " +
			"`to` accepts only a `dlg_...` delegate_id this session owns; it rejects `caller`, job/turn handles, and runtime aliases. " +
			"If the delegate is running, a send with max_wait_ms unset or 0 steers it and returns on delivery; a positive max_wait_ms is refused as target_busy, while a delegate that finishes before admission is started and the wait is honored. A delegate already being driven or otherwise busy is refused as target_busy. " +
			"Idle delegates are started/resumed automatically through the existing restore path, so follow-up messages resume them without an explicit idle-mode flag.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"to":          map[string]any{"type": "string", "description": "The delegate_id (`dlg_...`) of a delegate this session created."},
				"message":     map[string]any{"type": "string", "description": "The message to deliver to that delegate."},
				"max_wait_ms": map[string]any{"type": "integer", "description": "0 (default): deliver/start without waiting. >0: for a newly started delegate generation, wait inline up to this many ms for its result; the reply also carries, under earlier_results, any earlier result of that delegate you had not yet received. A positive value on a still-running delegate is refused as target_busy; a delegate that becomes idle before admission is started and the wait is honored. Steer a running delegate with 0."},
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
		Description: "List choices from the startup-frozen model availability snapshot. This is read-only; use the opaque cursor returned in next for the next page. Pages are bounded by both max_count and max_bytes and are never truncated. Paging never re-enumerates providers: one whose status kind is failure or timeout keeps that status for this session. To use one of its models once the problem clears, pass provider/model as the delegate model override; the spawn lists that provider again and names its current models if the requested one is not offered. A resumed session captures a new snapshot.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"cursor":    map[string]any{"type": "string", "description": "Opaque snapshot-bound continuation cursor from the next field of the previous page; omit for the first page. A cursor is bound to the max_count and max_bytes of the page that returned it: omit them or repeat those values with it."},
				"max_count": map[string]any{"type": "integer", "minimum": 1, "maximum": 128, "description": "Maximum models per page. Defaults to 128; larger is rejected."},
				"max_bytes": map[string]any{"type": "integer", "minimum": 512, "maximum": 4096, "description": "Maximum serialized page size in bytes. Defaults to 4096."},
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
	desc := "Wake yourself later: `after_seconds` (one-shot) or `repeat_seconds` (interval) on `self` for external state; never poll Evener jobs/delegates with timers (completions wake you; arm a one-shot with `note` to recheck a slow job). Clear timers before finishing. Create, inspect, list, or clear watches on a source you can observe (`self`, `parent` if `watch_parent=true`, `dlg_...`, or `job_id`). Delivery is implicit; frames coalesce while busy. Session sources: `events` (available: " + kinds + "; omitted = all public events cross-session), `event_filter`, `every`; jobs: `output_match` or `progress_interval_ms`. `assistant.tool` frames include `status` and `arguments_json`. " + ObserverReporting(resultTool) + " Stop acting on `self` loops as depth grows and clear (prefer an observer delegate)."
	return llm.ToolDefinition{
		Name:        "job_watch",
		Description: desc,
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation":            map[string]any{"type": "string", "description": "create, list, inspect, or clear.", "enum": []string{"create", "list", "inspect", "clear"}},
				"watch_id":             map[string]any{"type": "string", "description": "Watch ID (for inspect/clear)."},
				"source":               map[string]any{"type": []string{"string", "null"}, "description": "`self`, `parent` (if `watch_parent=true`), `dlg_...`, or `job_id`."},
				"output_match":         map[string]any{"type": []string{"string", "null"}, "description": "RE2 regex over rolling 4096B job output (max 4096B match, fires once/match, multiline ^/$)."},
				"progress_interval_ms": map[string]any{"type": []string{"integer", "null"}, "description": "Job progress interval ms (1000-3600000)."},
				"after_seconds":        map[string]any{"type": []string{"integer", "null"}, "description": "One-shot seconds (60 to 86400); self only."},
				"repeat_seconds":       map[string]any{"type": []string{"integer", "null"}, "description": "Interval seconds (60 to 3600); self only."},
				"note":                 map[string]any{"type": []string{"string", "null"}, "description": "Payload returned per fire (< escaped in block, raw in inspect)."},
				"events": map[string]any{
					"type":        []string{"array", "null"},
					"items":       map[string]any{"type": "string"},
					"description": "Events to watch (available: " + kinds + "; [\"*\"] = all; `communicate` for messages).",
				},
				"every": map[string]any{
					"type":        []string{"integer", "null"},
					"description": "Fire every Nth event (default 1; >1 requires one kind in events).",
				},
				"event_filter": map[string]any{
					"type":                 []string{"object", "null"},
					"additionalProperties": false,
					"description":          "Filter `assistant.tool` events (`communicate` gives `event.message`).",
					"properties": map[string]any{
						"tool_name": map[string]any{"type": "string", "description": "Exact tool name."},
						"status":    map[string]any{"type": "string", "description": "ok or error.", "enum": []string{"ok", "error"}},
					},
				},
			},
			"required": []string{"operation"},
		},
	}
}

func DefJobStatus() llm.ToolDefinition {
	return DefJobStatusNamed(DefaultResultToolName, false)
}

// EndTurnToWait is the guidance for an agent that has nothing else to do until
// its background work finishes: end the turn and let the completion wake it.
// It is the one wording shared by the job tool descriptions and the refusals of
// wait-shaped calls. hasEndReason is whether the session's result tool takes
// end_reason (only a root with a human partner does); advice to pass it
// elsewhere would fail the tool's schema.
func EndTurnToWait(resultTool string, hasEndReason bool) string {
	call := "call `" + resultTool + "` with `end_turn=true`"
	if hasEndReason {
		call += " and `end_reason=\"waiting_on_work\"`"
	}
	return "When you have nothing else to do until your background work finishes, end your turn: " + call + ". Its completion wakes you. To wake on a condition instead, set `job_watch` with `output_match`."
}

// DefJobStatusNamed is DefJobStatus for a session whose result tool is named
// resultTool and does (hasEndReason) or does not take end_reason.
func DefJobStatusNamed(resultTool string, hasEndReason bool) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "job_status",
		Description: "Inspect one durable shell job or stable delegate by target. Use this for metadata-only orientation: type, lifecycle status, observable phase, running/quiet time, and transcript_ref. Delegate status never returns terminal packet contents or acknowledges delivery. Completion is notification-driven, and a repeat call on a running target whose status and phase have not changed is refused. " + EndTurnToWait(resultTool, hasEndReason) + " Read raw evidence with read_transcript(transcript_ref).",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "A shell job_id (`job_...`) or stable delegate_id (`dlg_...`)."},
			},
			"required": []string{"target"},
		},
	}
}

func DefJobList() llm.ToolDefinition {
	return DefJobListNamed(DefaultResultToolName, false)
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
func DefJobListNamed(resultTool string, hasEndReason bool) llm.ToolDefinition {
	strictFalse := false
	statusEnum := append([]any(nil), jobListStatusEnum...)
	typeEnum := []any{"shell", "delegate"}
	return llm.ToolDefinition{
		Name:        "job_list",
		Description: "List jobs, delegates (idle once ended), and watches, newest first (`status`, `phase`, timings, `transcript_ref`). " + EndTurnToWait(resultTool, hasEndReason) + " " + ObserverReporting(resultTool) + " Check transcripts after observer reports for audit or diagnosis. Terminal statuses include `command_exited_nonzero`, `command_killed`; list unfiltered or use `job_status` when short jobs may have finished.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": statusEnum},
					"description": "Status filter.",
				},
				"type": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": typeEnum},
					"description": "Type filter.",
				},
				"include_nested":      map[string]any{"type": "boolean", "default": false, "description": "Include child-session rows."},
				"include_descendants": map[string]any{"type": "boolean", "default": false, "description": "Walk live descendant tree."},
				"limit":               map[string]any{"type": "integer", "default": 50, "maximum": 100, "description": "Max rows (default 50, max 100)."},
				"offset":              map[string]any{"type": "integer", "default": 0, "description": "Offset (default 0)."},
			},
			"required": []any{},
		},
	}
}

func DefJobStop() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "job_stop",
		Description: "Request cancellation of a shell job or stable delegate by `target`; stopping never deletes output or history. Stopping a delegate also stops its running shell jobs; a delegate resumed later restarts them rather than continuing them. `max_wait_ms > 0` waits for the stop to finalize. A delegate target is always stopped recursively, so include_children cannot weaken it; for a shell target, include_children=true also stops nested children.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target":           map[string]any{"type": "string", "description": "A shell job_id (`job_...`) or stable delegate_id (`dlg_...`)."},
				"max_wait_ms":      map[string]any{"type": "integer", "description": "0 (default): request the stop and return. >0: wait up to this many ms for shell termination or stable delegate subtree-stop settlement."},
				"include_children": map[string]any{"type": "boolean", "default": false, "description": "For a shell target, also stop its nested child jobs."},
			},
			"required": []string{"target"},
		},
	}
}

func DefGrep() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents using regex patterns. `glob_filter` accepts *, ?, [], **, and bounded brace alternatives such as *.{go,md}; malformed braces are rejected. This is the direct tool for requests to grep, search text, find tokens, find definitions, find references, and find recurring patterns across files. Dotfiles and gitignored paths are excluded from the search by default; a `glob_filter` that names such a FILE overrides that exclusion for it (matching ripgrep's -g), while hidden or gitignored directories stay pruned either way. Files larger than the read cap are left out of directory searches; name one directly as the path to search it.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern":          map[string]any{"type": "string", "description": "Regex pattern to match in file contents."},
				"path":             map[string]any{"type": "string", "description": "File or directory to search; blank searches from the working root."},
				"glob_filter":      map[string]any{"type": "string", "description": "Glob filter for searched files, e.g. *.go. Naming a dotfile or gitignored file here surfaces it (ripgrep -g override); without a filter those are excluded."},
				"case_insensitive": map[string]any{"type": "boolean", "description": "Match the pattern case-insensitively."},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum output to return. In content mode the cap counts output lines — matches plus context lines — not the number of matches; in files_with_matches and count modes it caps returned files or entries. Defaults to 100.",
				},
				"context_lines": map[string]any{
					"type":        "integer",
					"description": "Lines of context to include before and after each match, 0-10 (default 0).",
				},
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
		Description: "Find files by pattern: a recursive glob match over file paths, not a literal directory listing. Supports *, ?, [], **, and bounded nested brace alternatives such as *.{ts,tsx,css}; malformed braces are rejected. Use this for pattern-based file discovery. Some providers alias this tool to a name like find_files; it still performs glob matching regardless of the wire name. By default, dotfiles/dirs (.git, .claude/worktrees/x, ...) and gitignored paths (node_modules, build output, ...) are excluded; set include_ignored to true to search them too.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern":         map[string]any{"type": "string", "description": "Glob pattern for file paths, e.g. **/*.go or *.{ts,tsx}."},
				"path":            map[string]any{"type": "string", "description": "Base directory to match in; blank means the working root."},
				"include_ignored": map[string]any{"type": "boolean", "description": "Include dotfiles/dirs and gitignored paths in results (excluded by default)."},
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
- File paths resolve against the session's current working directory, which a worktree switch changes; prefer relative paths from that directory. An absolute path is correct when the file lives outside it, such as inside a delegate's worktree while you work from the main checkout.
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

// DefaultResultToolName is the result tool's name unless a session renames it
// (SessionConfig.ResultToolName). Tool descriptions that teach the result tool
// are built from the configured name by the Def...Named constructors.
const DefaultResultToolName = "communicate"

func DefCommunicate() llm.ToolDefinition {
	return DefCommunicateNamed(DefaultResultToolName)
}

func DefCommunicateNamed(name string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        name,
		Description: "Send a user-facing message. Use this tool for every message, readiness line, requested status marker, request for input, and final answer the user or caller should see. A valid call has visible text in `message` or `output.message`. If you will immediately keep working after this message, set `end_turn=false` and continue by calling the next work tool in the following round. Use `end_turn=false` only for visible narration or status before immediate continued work. Use `end_turn=true` when this message should stop the current activation: final answers, completed results, blocking requests for input, readiness lines that wait for future user/caller/watch-frame input, and a line naming the work you are waiting on. Set `message` to the exact visible text. Always include `output` as an object with exactly these top-level fields: `message`, `data`, and `artifacts`. For ordinary status or conversational messages, use `output.message=\"\"`, `output.data={}`, and `output.artifacts=[]`. When handing back completed work or machine-readable results, populate `output` with the evidence and structured data the caller needs.",
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
					"description": "Set to false only for visible narration/status before immediate continued work. Set to true when this message ends the current activation: final answer, completed result, blocking input request, readiness line that waits for future user/caller/watch-frame input, or a line naming the work you are waiting on.",
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

// Why a root session's communicate call ends its turn: done (nothing waits
// on the human partner), needs_response (the agent cannot go on until its
// human partner answers or acts), or waiting_on_work (work the agent started
// will wake it). Offered only where someone can answer the session; only
// needs_response rests the session awaiting.
const (
	CommunicateEndReasonDone          = "done"
	CommunicateEndReasonNeedsResponse = "needs_response"
	CommunicateEndReasonWaitingOnWork = "waiting_on_work"
)

// CommunicateEndReasons lists the end_reason values in the order the
// parameter advertises them.
var CommunicateEndReasons = []string{CommunicateEndReasonDone, CommunicateEndReasonNeedsResponse, CommunicateEndReasonWaitingOnWork}

// WithCommunicateEndReason returns a copy of a communicate definition that
// also offers end_reason. Only a root someone can answer gets it: nobody
// responds to a delegate's or a headless root's resting state.
func WithCommunicateEndReason(def llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(def.Parameters)
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return def
	}
	props["end_reason"] = map[string]any{
		"type":        "string",
		"enum":        slices.Clone(CommunicateEndReasons),
		"description": "Why this message ends your turn; read only when end_turn=true, and done when omitted. `done`: you finished or answered, and nothing waits on your human partner; the session rests idle. `needs_response`: you cannot go on until your human partner answers or acts, such as a decision, a review, or something only they can do; the session shows it is waiting on them. `waiting_on_work`: you are waiting on delegates or background jobs that will wake you; the session rests idle and asks nobody for attention. The call is refused when nothing can wake the session: use `done` if finished, `needs_response` if your partner must act, or start the work or arm a watch before ending the turn.",
	}
	def.Parameters = params
	return def
}

// ObserverReporting is how an observer sidecar talks to the session it
// watches through resultTool, shared by every tool description and error that
// teaches it.
func ObserverReporting(resultTool string) string {
	return "Observers send updates with `" + resultTool + "(end_turn=false)`, and a readiness line or their findings with `" + resultTool + "(end_turn=true)`."
}

// communicateDelegateDescription is communicate worded for a delegate, whose
// messages go to its parent agent. It names no output fields: a delegate
// started with result_schema gets that schema as its output parameter
// (provider.WithCommunicateOutputSchema), so only the schema can say the shape.
const communicateDelegateDescription = "Send a message to your parent agent; it is the only way your parent hears from you. Put the text your parent should read in `message`. " +
	"With `end_turn=false` the message is an update: your parent receives it, and it wakes a parent that is waiting on you, so send one only when your parent needs to know something or act on it, never to narrate progress, and keep working in the following round. " +
	"The result's `delivery` says whether your parent has the update (`delivered`), will get it at its next step (`queued`), or cannot get it (`not_delivered`, with a `reason`); the call succeeds either way. " +
	"With `end_turn=true` the message ends your turn with your report, your one line naming the work you are waiting on, or a readiness line before you wait for watch frames: put the complete result in a report. A `result_schema` still requires its output on every call, so when the schema has no truthful pending value, keep the turn open rather than invent one. " +
	"Always include `output` in the shape its schema gives; when you hand back completed work or machine-readable results, fill it with the evidence and structured data your parent needs."

// WithCommunicateDelegateUpdates returns a copy of a communicate definition
// worded for a delegate: end_turn=false sends an update its parent receives,
// end_turn=true its report, its wait line, or an observer readiness line. The
// output schema stays as the caller built it.
func WithCommunicateDelegateUpdates(def llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(def.Parameters)
	props, _ := params["properties"].(map[string]any)
	if message, ok := props["message"].(map[string]any); ok {
		message["description"] = "The text your parent agent receives. When the task asks for concrete findings, put them here."
	}
	if endTurn, ok := props["end_turn"].(map[string]any); ok {
		endTurn["description"] = "false: send an update and keep working; your parent receives it, and it wakes a parent that is waiting on you. true: send your report, the one line naming the work you are waiting on, or a readiness line before you wait for watch frames, and end your turn."
	}
	def.Description = communicateDelegateDescription
	def.Parameters = params
	return def
}

// WithCommunicateDelegatePointer returns a copy of a communicate definition
// that also points to delegate_send, for a session that can message its own
// delegates. communicate only reaches a human partner or a parent, so without
// the pointer a model answering its delegate's question reaches for it.
func WithCommunicateDelegatePointer(def llm.ToolDefinition) llm.ToolDefinition {
	def.Description += " To message one of your delegates, use `delegate_send`."
	return def
}

func DefTaskList(effortLevels []string) llm.ToolDefinition {
	reasoningDesc := "Effort override (\"inherit\"/omit = unchanged/default)."
	reasoningSchema := map[string]any{
		"type":        "string",
		"description": reasoningDesc,
	}
	if len(effortLevels) > 0 {
		// "inherit" rides along so a strict-mode provider (which force-requires
		// every property) still lets the model decline to override.
		reasoningSchema["enum"] = append(append([]string(nil), effortLevels...), "inherit")
	}
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "task_list",
		Description: "Manage tasks (bare call lists them). `add` appends open tasks, `update` edits by `id` (`done` auto-starts next). Only one task may be in_progress at a time; to start a new one, complete or defer the current one in the same update array.",
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
								"description": "Work kind (default implement).",
							},
							"description": map[string]any{"type": "string", "description": shortTaskTitleDescription},
							"prompt":      map[string]any{"type": "string", "description": "Task instruction."},
							"depends_on": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "Prerequisite task IDs.",
							},
							"reasoning_effort": reasoningSchema,
						},
						"required": []string{"description", "prompt"},
					},
				},
				"update": map[string]any{
					"type":        "array",
					"description": "Task updates.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":     map[string]any{"type": "integer", "description": "Task ID."},
							"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "cancelled"}, "description": "New status (done starts next)."},
							"notes":  map[string]any{"type": "string", "description": "Failures/decisions/surprises only."},
							"depends_on": map[string]any{
								"type":        []string{"array", "null"},
								"items":       map[string]any{"type": "integer"},
								"description": "Replace prerequisites (omit/[]/null = keep; [0] = clear).",
							},
							"reasoning_effort": reasoningSchema,
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
		Description: "Find archived sessions (empty args = recent catalog; returns `transcript_ref` for `read_transcript`). Metadata hits with `seq: -1` are display markers, not turn addresses. Treat as archived evidence.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "Substring search."},
				"children_of": map[string]any{"type": "string", "description": "Parent `transcript_ref` (overrides `query`)."},
				"kind":        map[string]any{"type": "string", "enum": []string{"root", "subagent", "fork", "any"}, "description": "Lineage filter."},
				"has_children": map[string]any{
					"type":        "boolean",
					"description": "Spawned children filter.",
				},
				"min_turns":     map[string]any{"type": "integer", "description": "Min turns."},
				"max_turns":     map[string]any{"type": "integer", "description": "Max turns."},
				"updated_after": map[string]any{"type": "string", "description": "RFC3339 min."},
				"updated_before": map[string]any{
					"type":        "string",
					"description": "RFC3339 max.",
				},
				"scope": map[string]any{"type": "string", "enum": []string{"current_project", "all_projects"}, "description": "Scope (default current_project)."},
				"limit": map[string]any{"type": "integer", "description": "Max matches (default 10, max 50)."},
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
		Description: "Read-only in-process `evener doctor` (defaults to session state root; `--json` shapes). Commands: `locate`, `transcript` (`count`, `health`), `apilog` (`empty`/`errors`/`cache_spikes`/`summary`/`validate`/`recompute`/`health`), `jobs` (`job_id`), `mutations`, `watches` (`self_loops`), `tree` (`observers`), `turnids` (`since`/`bucket`), `sessions` (`since` default " + doctorSessionsDefaultSinceText + ", `bucket`), `audit` (`runbook`, `sessions` xor `since`), `plugins`, `lifecycle` (no selector/state). `selector`: " + DoctorEvenerSelectorDialect + ".",
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
				"selector":  map[string]any{"type": "string", "description": DoctorEvenerSelectorDialect + " (invalid for sessions/audit/turnids/plugins/lifecycle)."},
				"state_dir": map[string]any{"type": "string", "description": "State root override (invalid for plugins/lifecycle)."},
				"count":     map[string]any{"type": "string", "description": "transcript: tool call count."},
				"health":    map[string]any{"type": "boolean", "description": "transcript/apilog: health verdict."},
				"range":     map[string]any{"type": "string", "description": "transcript: last:N|start:N|N-M."},
				"text_max":  map[string]any{"type": "integer", "description": "transcript: turn preview byte cap."},
				"full_text": map[string]any{"type": "boolean", "description": "transcript: unclipped turns."},
				"empty":     map[string]any{"type": "boolean", "description": "apilog: empty responses."},
				"errors":    map[string]any{"type": "boolean", "description": "apilog: failed calls."},
				"cache_spikes": map[string]any{
					"type":        "boolean",
					"description": "apilog: uncached >= threshold.",
				},
				"threshold": map[string]any{"type": "integer", "description": "apilog: cache_spikes floor."},
				"summary":   map[string]any{"type": "boolean", "description": "apilog: aggregate only."},
				"validate":  map[string]any{"type": "boolean", "description": "apilog: integrity scan."},
				"recompute": map[string]any{
					"type":        "boolean",
					"description": "apilog: recount empty rows.",
				},
				"job_id":   map[string]any{"type": "string", "description": "jobs: scope to job_id."},
				"watch_id": map[string]any{"type": "string", "description": "watches: scope to watch_id."},
				"self_loops": map[string]any{
					"type":        "boolean",
					"description": "watches: runaway-fuse only.",
				},
				"depth":     map[string]any{"type": "integer", "description": "tree: max depth (default 50)."},
				"observers": map[string]any{"type": "boolean", "description": "tree: include observer edges."},
				"since":     map[string]any{"type": "string", "description": "sessions/audit/turnids: duration (e.g. 120h; sessions default " + doctorSessionsDefaultSinceText + ")."},
				"bucket":    map[string]any{"type": "string", "description": "sessions/turnids: bucket id (default all)."},
				"runbook":   map[string]any{"type": "string", "description": "audit: doctoring-evener runbook name."},
				"sessions":  map[string]any{"type": "string", "description": "audit: comma-separated selectors (xor since)."},
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
	desc := "Manage git worktrees for isolated, parallel, or risky work (not ordinary branch work; use plain git commands). Ops: `create` (`name`, `base_ref` default HEAD; enters it); `list`; `switch` (`name` xor `path`); `adopt` (unmanaged `path` from `list`); `exit` (to main checkout); `remove` (`name`, `delete_branch`, `force`, `force_dirty`); `prune` (removes lanes with no unmerged work; `orphaned_delegate_days` unlocks gone/inactive parents' lanes); `unlock` (parent recovery: release `id` lock keeping lane); `dispose` (retire `id` lane/branch; `force`/`force_dirty`). `create`/`switch`/`exit` change cwd; merge by `exit`ing + plain git."
	return llm.ToolDefinition{
		Name:        "manage_worktree",
		Description: desc,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation": map[string]any{
					"type":        "string",
					"description": "Operation.",
					"enum":        []string{"create", "list", "switch", "adopt", "exit", "remove", "prune", "unlock", "dispose"},
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Worktree/branch name.",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Delegate id (`dlg_…`).",
				},
				"base_ref": map[string]any{
					"type":        "string",
					"description": "Base commit-ish (default HEAD).",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Worktree path (`switch`/`adopt`).",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Override merge/unmerged checks (keeps dirty check).",
				},
				"force_dirty": map[string]any{
					"type":        "boolean",
					"description": "Discard uncommitted changes.",
				},
				"delete_branch": map[string]any{
					"type":        "boolean",
					"description": "Also delete branch (default false).",
				},
				"orphaned_delegate_days": map[string]any{
					"type":        "number",
					"description": "Unlock orphan delegate lanes inactive >= N days (min ~0.00072; 0 keeps locks).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"default":     15,
					"minimum":     1,
					"maximum":     15,
					"description": "Max entries (default/max 15).",
				},
				"offset": map[string]any{
					"type":        "integer",
					"default":     0,
					"minimum":     0,
					"description": "Start offset (default 0).",
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
	desc := "Retire one of YOUR OWN delegates' isolation worktree lanes once you are done with its work: " +
		"unlocks the lane, removes the worktree, deletes its branch, and marks the delegate disposed. " +
		"Target it by the delegate `id` (dlg_…); `force` overrides the refusal to discard unmerged commits, " +
		"`force_dirty` overrides the refusal to discard uncommitted changes. " +
		"dispose is the ONLY worktree operation available to you: you run inside your own isolation worktree lane, " +
		"so you cannot create, switch, list, remove, or prune worktrees (that could disturb sibling lanes)."
	return llm.ToolDefinition{
		Name:        "manage_worktree",
		Description: desc,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation": map[string]any{
					"type":        "string",
					"description": "Only dispose is available on this surface.",
					"enum":        []string{"dispose"},
				},
				"id": map[string]any{
					"type":        "string",
					"description": "The delegate id (dlg_…) whose isolation worktree lane to retire.",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Override the refusal to dispose a lane with unmerged commits (they are discarded). Does NOT discard uncommitted changes — use force_dirty for that. Default false.",
				},
				"force_dirty": map[string]any{
					"type":        "boolean",
					"description": "Proceed even if the lane has uncommitted changes (they are discarded). Separate from force so overriding a merge refusal cannot silently discard an edit, and vice versa. Default false.",
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
		Name:        "read_transcript",
		Description: "Read retained evidence by `transcript_ref`. A session ref (or delegate; never `job:`): `format` (markdown/outline/jsonl), `range`, `expand_turn` (exact `transcript_v2_jsonl`, fixed 16 KiB pages via `offset_bytes`), no `output_match`. `job:<id>` (from `job_status`/`job_list`, windowed shell, or watch frame): shell output as markdown, `offset_bytes` (fixed 16 KiB raw lifetime pages), or `output_match` (RE2, `context_lines` 0–10, <=65,536 chars); pages include `job_status`, `total_bytes`/`retained_start_bytes` (`output_unavailable` if pruned); rejects `range`/`expand_turn`/non-markdown `format`. `artifact:<id>` (root tree only; `artifact_expired`/`artifact_unavailable`): raw page 0, `offset_bytes`/`output_match`/`context_lines`, `retained_start_bytes` 0, no `job_status`, rejects `format`/`range`/`expand_turn`. Do not poll.",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"transcript_ref": map[string]any{"type": "string", "description": "Session ref/id, current (default), job:<id>, or artifact:<id>."},
				"format":         map[string]any{"type": []any{"string", "null"}, "enum": []any{"outline", "markdown", "jsonl", nil}, "description": "markdown (default), outline, or jsonl (session only)."},
				"range":          map[string]any{"type": []any{"string", "null"}, "description": "Session turn window: \"12-40\", \"last:40\" (default), or \"start:40\"."},
				"expand_turn":    map[string]any{"type": []any{"integer", "null"}, "minimum": 0, "description": "Session markdown: byte-paged transcript_v2_jsonl for any semantic Turn N (`offset_bytes` continues)."},
				"offset_bytes":   map[string]any{"type": "integer", "minimum": 0, "description": "Byte offset (session expansion or job:/artifact: raw page/search)."},
				"output_match":   map[string]any{"type": []any{"string", "null"}, "maxLength": 65_536, "description": "RE2 line search for job:/artifact: (max 65,536 chars)."},
				"context_lines": map[string]any{
					"type":        []any{"integer", "null"},
					"minimum":     0,
					"maximum":     10,
					"description": "Context lines for output_match (0-10, default 0).",
				},
			},
		},
	}
}

// DefAskUser defines the ask_user tool (spec §4): structured, mostly
// multiple-choice questions that end the model's turn at the asking round's
// boundary. The Description is verbatim from spec §4.4 (the blockquote,
// leading "> " markers stripped); the schema is spec §4.2. Strict is false
// because multi_select/why/if_unanswered/recommended are all optional.
func DefAskUser() llm.ToolDefinition {
	return DefAskUserNamed(DefaultResultToolName)
}

// DefAskUserNamed is DefAskUser for a session whose result tool is named
// resultTool.
func DefAskUserNamed(resultTool string) llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name: "ask_user",
		Description: "Ask the user structured questions. Asking yields the floor: when the round containing your `ask_user` call(s) completes, your turn ends and the session waits visibly for the reply (no timeout). Do the work that does not need answers first, then batch every question this decision point needs — several `ask_user` calls may share the round, and a `" + resultTool + "` in the same round still delivers its message. The answers arrive in the user's next message: either the numbered `[answers]` form (one resolution per question: a selection, free text, \"you decide\" — choose with your judgment, honoring any stated leaning —, your stated fallback, or skipped — proceed on your best judgment, state the assumption, and do not immediately re-ask) or free prose; treat either as the reply to everything you asked. Any answer may carry a user note — read it; it can qualify or override the selection.\n\n" +
			"- `questions`: 1–4 per call, each with an optional display `header` (omitted headers display as `Question N`), the full `question`, and 2–5 `options` (`{label, detail}`, labels unique). Set `multi_select` to allow several; set `recommended: true` on at most one option and put it first.\n" +
			"- Do not add an \"Other\" or free-text option; the UI always offers one, plus \"you decide\".\n" +
			"- Optional per question: `why` (one line: what the answer changes) and `if_unanswered` (the fallback you would take; the user can accept it with one tap).\n\n" +
			"First try to resolve the question yourself with tools. Asking is how you end your turn when only the user can unblock the rest — finish the answer-independent work before you ask.",
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "1-4 questions to ask this round.",
					"minItems":    1,
					"maxItems":    4,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"header":   map[string]any{"type": "string", "description": "Display label; answer framing encodes delimiter characters when needed."},
							"question": map[string]any{"type": "string", "description": "The full question text."},
							"options": map[string]any{
								"type":        "array",
								"description": "2-5 options; labels must be unique within the question.",
								"minItems":    2,
								"maxItems":    5,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": "string", "description": "Unique option label within the question."},
										"detail":      map[string]any{"type": "string", "description": "Explanatory text shown with the option."},
										"recommended": map[string]any{"type": "boolean", "description": "At most one per question; marks the model's suggestion, shown first."},
									},
									"required": []string{"label", "detail"},
								},
							},
							"multi_select": map[string]any{"type": "boolean", "default": false, "description": "Allow selecting more than one option."},
							"why":          map[string]any{"type": "string", "description": "Optional one-line context: what the answer changes."},
							"if_unanswered": map[string]any{
								"type":        "string",
								"description": "Optional one-line fallback the user would take; the user can accept it with one tap.",
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
		Description: `Keep your session whiteboard current. ` +
			`It is a capsule a manager checking on this session reads in ten seconds ` +
			`to learn what the session is for and where it stands. ` +
			`Use plain words, with no file paths, ids or jargon. ` +
			`Write three parts, each on its own line: ` +
			`a short paragraph with the mission and what is done so far; ` +
			`one line starting "Now:" with what you are doing; ` +
			`one or two lines starting "Next:" with what is left. ` +
			`Set it once you understand the task, update it when a phase finishes, the plan changes, ` +
			`or you are blocked, and bring it up to date before your final report; ` +
			`for a job you finish in one go, one update before the report is enough. ` +
			`Each update replaces the whole whiteboard. ` +
			`Keep it under 600 characters. Example:` + "\n" +
			`Fixing the importer's rejected timestamps for the reporting team. ` +
			`The parser required seconds; I made them optional and added tests, which pass.` + "\n" +
			`Now: checking the exporter, which uses the same parser.` + "\n" +
			`Next: fix the exporter the same way, then report back.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note": map[string]any{
					"type":        "string",
					"description": "The whole whiteboard, in the three parts above. Line breaks are kept. Empty clears it.",
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
		Name: "urls_add",
		Description: `Add a URL to the session's shared URL list. ` +
			`Accepts http(s) URLs, file:/// URLs, and bare paths under the session ` +
			`working directory. Re-adding an existing URL updates its label and ` +
			`returns the existing entry. A bare path whose first segment contains a ` +
			`colon is read as a URL scheme and rejected; prefix it with "./" to add ` +
			`it as a path.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL or session-relative file path to add.",
				},
				"label": map[string]any{
					"type":        "string",
					"description": "Optional short label for the URL.",
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
		Name: "urls_remove",
		Description: `Remove a URL from the session's shared URL list by entry id ` +
			`(the id returned when the URL was added).`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The id of the URL list entry to remove.",
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
		Name: "notes_read",
		Description: `Read the session's shared notes: the human's whiteboard, ` +
			`your whiteboard, and the shared URL list.`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
	}
}

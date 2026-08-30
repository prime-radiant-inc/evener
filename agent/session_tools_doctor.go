package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/bundled"
	"primeradiant.com/evener/internal/plugins"
)

// doctorTools returns the doctor_evener tool: the read-only in-process
// equivalent of the `evener doctor` CLI, executing the same commands against
// the session's own state root by default — no shell, no PATH, no cwd
// dependence (the failure class the doctoring-evener skill hit when it
// instructed shell invocation). Read-only by library construction — every
// command reads settled durable state through the same read-only folds the
// CLI uses (pinned by TestDoctorEvener_HandlerDoesNotMutateState, extending
// stable_delegate_readonly_test.go to the tool layer); the ReadOnly flag
// itself is the registry's parallel-batching hint, not the enforcement.
// Output-limited like the transcript tools: the sessions table and audit
// findings are the large shapes.
func doctorTools(deps *toolDeps) []tool.RegisteredTool {
	return []tool.RegisteredTool{
		{
			Definition: tool.DefDoctorEvener(),
			ReadOnly:   true,
			Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				_ = ctx
				return execDoctorEvener(deps, args)
			},
			Limit: schema.ToolOutputLimit{MaxChars: transcriptToolMaxChars, Strategy: schema.TruncTail},
		},
	}
}

// doctorEvenerCommandNames is the single source for the command enum: the
// definition's schema and this dispatcher both derive from it, so the tool
// surface and dispatch cannot drift.
var doctorEvenerCommandNames = tool.DoctorEvenerCommands()

// doctorStateBase resolves the state root for one invocation: an explicit
// state_dir argument wins; otherwise the session's own state root (the
// daemon's), falling back to the doctor's default resolution only when the
// session has no state dir (state persistence off). This is the inheritance
// that removes the PATH/cwd/env failure class: a doctor delegate sees the
// daemon's state root by construction.
func doctorStateBase(deps *toolDeps, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	if deps != nil && deps.stateDir != "" {
		return deps.stateDir
	}
	return doctor.ResolveStateBase("")
}

// doctorBundledSkills is a package var (like the CLI's bundledSkills) so
// tests can substitute the runbook source FS.
var doctorBundledSkills = bundled.Skills

// execDoctorEvener dispatches one doctor_evener invocation to the
// agent/doctor package. Results are the CLI's --json struct shapes: the CLI's
// cmd functions marshal these same structs, so the tool and
// `evener doctor --json` cannot disagree on shape. Command-option
// precedence mirrors the CLI exactly (count, then health, then render;
// validate over health on apilog).
func execDoctorEvener(deps *toolDeps, args map[string]any) (any, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return nil, fmt.Errorf("doctor_evener requires a command (one of: %s)", strings.Join(doctorEvenerCommandNames, ", "))
	}
	if !slices.Contains(doctorEvenerCommandNames, command) {
		return nil, fmt.Errorf("unknown doctor command %q (one of: %s)", command, strings.Join(doctorEvenerCommandNames, ", "))
	}

	stateBase := doctorStateBase(deps, stringArg(args, "state_dir"))
	selector := stringArg(args, "selector")

	// Selector validation mirrors the CLI's own usage errors: an agent
	// passing a selector to a sweep command believing it scopes to one
	// session would otherwise get a state-root-wide result with no signal,
	// and selector-taking commands must not run with an empty selector.
	switch command {
	case "turnids", "sessions", "audit", "plugins":
		if strings.TrimSpace(selector) != "" {
			return nil, fmt.Errorf("doctor command %q takes no selector (it is a state-root-wide sweep)", command)
		}
	default:
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
	}

	switch command {
	case "locate":
		return doctor.Locate(stateBase, selector)

	case "transcript":
		if v := stringArg(args, "count"); v != "" {
			return doctor.Count(stateBase, selector, v)
		}
		if shellBoolArg(args, "health") {
			return doctor.TranscriptHealth(stateBase, selector)
		}
		// No format argument: the tool returns the CLI's --json struct shape,
		// where render format doesn't apply (the CLI's --format governs its own
		// text render).
		opts := doctor.TranscriptOpts{TextMax: doctor.DefaultTextMax}
		if v := stringArg(args, "range"); v != "" {
			opts.Range = v
		}
		if shellBoolArg(args, "full_text") {
			opts.TextMax = doctor.TextMaxFull
		} else if n, ok := shellIntArg(args, "text_max"); ok {
			if n <= 0 {
				return nil, errors.New("text_max must be positive; use full_text to render turns with no cap")
			}
			opts.TextMax = n
		}
		return doctor.Transcript(stateBase, selector, opts)

	case "apilog":
		if shellBoolArg(args, "validate") {
			return doctor.ValidateAPILog(stateBase, selector)
		}
		if shellBoolArg(args, "health") {
			return doctor.APIHealth(stateBase, selector)
		}
		opts := doctor.APILogOpts{}
		opts.EmptyOnly = shellBoolArg(args, "empty")
		opts.ErrorsOnly = shellBoolArg(args, "errors")
		opts.CacheSpikes = shellBoolArg(args, "cache_spikes")
		if n, ok := shellIntArg(args, "threshold"); ok {
			opts.SpikeThreshold = n
		}
		opts.SummaryOnly = shellBoolArg(args, "summary")
		opts.Recompute = shellBoolArg(args, "recompute")
		return doctor.APILog(stateBase, selector, opts)

	case "jobs":
		opts := doctor.JobOpts{}
		if v := stringArg(args, "job_id"); v != "" {
			opts.JobID = v
		}
		return doctor.Jobs(stateBase, selector, opts)

	case "mutations":
		return doctor.Mutations(stateBase, selector)

	case "watches":
		opts := doctor.WatchOpts{}
		if v := stringArg(args, "watch_id"); v != "" {
			opts.WatchID = v
		}
		if shellBoolArg(args, "self_loops") {
			opts.SelfLoopsOnly = true
		}
		return doctor.Watches(stateBase, selector, opts)

	case "tree":
		opts := doctor.TreeOpts{}
		if n, ok := shellIntArg(args, "depth"); ok {
			opts.Depth = n
		}
		if shellBoolArg(args, "observers") {
			opts.Observers = true
		}
		return doctor.Tree(stateBase, selector, opts)

	case "turnids":
		return doctor.ScanTurnIDs(stateBase)

	case "sessions":
		opts := doctor.SessionsOpts{}
		if v := stringArg(args, "since"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("sessions since: %w", err)
			}
			opts.Since = d
		}
		if v := stringArg(args, "bucket"); v != "" {
			opts.Bucket = v
		}
		res, err := doctor.ListSessions(stateBase, opts)
		if err != nil {
			return nil, err
		}
		return doctorCapSessionsRows(res), nil

	case "audit":
		runbookName := stringArg(args, "runbook")
		if runbookName == "" {
			return nil, errors.New("audit requires runbook (one of the bundled doctoring-evener runbooks)")
		}
		runbook, err := doctorLoadRunbook(runbookName)
		if err != nil {
			return nil, err
		}
		opts := doctor.AuditOpts{}
		if v := stringArg(args, "sessions"); v != "" {
			opts.Sessions = strings.Split(v, ",")
		}
		if v := stringArg(args, "since"); v != "" {
			if len(opts.Sessions) > 0 {
				return nil, errors.New("audit sessions and since are mutually exclusive")
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("audit since: %w", err)
			}
			opts.Since = d
		}
		res, err := doctor.RunAudit(stateBase, runbook, opts)
		return res, err

	case "plugins":
		// Manager.Doctor includes a store-writability probe (create + remove one
		// temp file) mirroring the CLI's plugins check; everything else it does is
		// read-only. The tool's store root is the default config root — the CLI's
		// --store-root override has no tool counterpart, so state_dir is
		// rejected rather than silently ignored (the stray-selector pattern).
		if strings.TrimSpace(stringArg(args, "state_dir")) != "" {
			return nil, errors.New("doctor command \"plugins\" takes no state_dir (the plugin store lives in the config root, not a state root)")
		}
		findings, err := plugins.NewManager("").Doctor()
		if err != nil {
			return nil, fmt.Errorf("plugins: %w", err)
		}
		return findings, nil
	}

	// Unreachable: the enum gate above covers every command.
	return nil, fmt.Errorf("unknown doctor command %q", command)
}

// doctorRowCap is the structural row cap for the sessions enumeration. Capping
// rows inside the result keeps the envelope valid JSON under the char limit —
// the mid-JSON truncation the default strategy would otherwise produce. 500
// rows is far under the 600k-char limit at the row sizes ListSessions emits.
const doctorRowCap = 500

// doctorSessionsEnvelope is the tool-layer result for the sessions command:
// the library enumeration plus the cap disclosure. Embedding (rather than
// putting these fields on the library type) follows the findSessionsEnvelope
// convention in session_tools_find.go — capping is a presentation concern, so
// the disclosure lives with the renderer, and agent/doctor's type stays clean.
type doctorSessionsEnvelope struct {
	doctor.SessionsResult
	// Truncated reports that the sessions list was structurally capped at
	// doctorRowCap rows.
	Truncated bool `json:"truncated,omitempty"`
	// TotalRows is the full row count before a structural cap.
	TotalRows int `json:"total_rows,omitempty"`
	// UnreadableTruncated reports that the unreadable list was structurally
	// capped at doctorRowCap rows.
	UnreadableTruncated bool `json:"unreadable_truncated,omitempty"`
	// TotalUnreadableRows is the full unreadable count before a structural cap.
	TotalUnreadableRows int `json:"total_unreadable_rows,omitempty"`
}

// doctorCapSessionsRows structurally caps a sessions enumeration and discloses
// the cut, mirroring find_session_transcripts' scan_truncated convention.
// Both lists are capped: a mass-corrupt state root is the exact scenario the
// sessions sweep exists for, and an unbounded unreadable list would recreate
// the overflow the cap prevents. (audit evidence — refs and prose — is capped
// at the producer in agent/doctor/audit.go.)
func doctorCapSessionsRows(res doctor.SessionsResult) doctorSessionsEnvelope {
	env := doctorSessionsEnvelope{SessionsResult: res}
	if len(env.Sessions) > doctorRowCap {
		env.Truncated = true
		env.TotalRows = len(env.Sessions)
		env.Sessions = env.Sessions[:doctorRowCap]
	}
	if len(env.Unreadable) > doctorRowCap {
		env.UnreadableTruncated = true
		env.TotalUnreadableRows = len(env.Unreadable)
		env.Unreadable = env.Unreadable[:doctorRowCap]
	}
	return env
}

// doctorRequireSelector rejects selector-less invocation of a
// selector-taking command — the CLI's own usage error, surfaced as a tool
// error instead.
func doctorRequireSelector(command, selector string) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("doctor command %q requires a selector (local:<id>, proj:<hash>:<id>, or bare <id>)", command)
	}
	return nil
}

// doctorLoadRunbook resolves a runbook by name from the bundled
// doctoring-evener skill's runbooks/ — the same resolution the CLI's
// loadRunbook performs, shared through doctor.ParseRunbookFromFS.
func doctorLoadRunbook(name string) (doctor.Runbook, error) {
	return doctor.ParseRunbookFromFS(doctorBundledSkills(), "doctoring-evener", name)
}

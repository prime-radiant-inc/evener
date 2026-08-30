package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
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

// doctorEvenerCommandNames is the command enum shared by the definition and
// the dispatcher — one source so the tool surface and dispatch cannot drift.
var doctorEvenerCommandNames = []string{
	"locate", "transcript", "apilog", "jobs", "mutations", "watches", "tree",
	"turnids", "sessions", "audit", "plugins",
}

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

	// Selector-less commands must reject a stray selector, not silently
	// ignore it — the CLI's own usage error. An agent passing a selector to a
	// sweep command believing it scopes to one session would otherwise get a
	// state-root-wide result with no signal.
	switch command {
	case "turnids", "sessions", "audit", "plugins":
		if strings.TrimSpace(selector) != "" {
			return nil, fmt.Errorf("doctor command %q takes no selector (it is a state-root-wide sweep)", command)
		}
	}

	switch command {
	case "locate":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		return doctor.Locate(stateBase, selector)

	case "transcript":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		if v := stringArg(args, "count"); v != "" {
			return doctor.Count(stateBase, selector, v)
		}
		if b, ok := doctorBoolArg(args, "health"); ok && b {
			return doctor.TranscriptHealth(stateBase, selector)
		}
		opts := doctor.TranscriptOpts{Format: "markdown", TextMax: doctor.DefaultTextMax}
		if v := stringArg(args, "format"); v != "" {
			opts.Format = v
		}
		if v := stringArg(args, "range"); v != "" {
			opts.Range = v
		}
		if b, ok := doctorBoolArg(args, "full_text"); ok && b {
			opts.TextMax = doctor.TextMaxFull
		} else if n, ok := doctorIntArg(args, "text_max"); ok {
			if n <= 0 {
				return nil, errors.New("text_max must be positive; use full_text to render turns with no cap")
			}
			opts.TextMax = n
		}
		return doctor.Transcript(stateBase, selector, opts)

	case "apilog":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		if b, ok := doctorBoolArg(args, "validate"); ok && b {
			return doctor.ValidateAPILog(stateBase, selector)
		}
		if b, ok := doctorBoolArg(args, "health"); ok && b {
			return doctor.APIHealth(stateBase, selector)
		}
		opts := doctor.APILogOpts{}
		if b, ok := doctorBoolArg(args, "empty"); ok {
			opts.EmptyOnly = b
		}
		if b, ok := doctorBoolArg(args, "errors"); ok {
			opts.ErrorsOnly = b
		}
		if b, ok := doctorBoolArg(args, "cache_spikes"); ok {
			opts.CacheSpikes = b
		}
		if n, ok := doctorIntArg(args, "threshold"); ok {
			opts.SpikeThreshold = n
		}
		if b, ok := doctorBoolArg(args, "summary"); ok {
			opts.SummaryOnly = b
		}
		if b, ok := doctorBoolArg(args, "recompute"); ok {
			opts.Recompute = b
		}
		return doctor.APILog(stateBase, selector, opts)

	case "jobs":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		opts := doctor.JobOpts{}
		if v := stringArg(args, "job_id"); v != "" {
			opts.JobID = v
		}
		return doctor.Jobs(stateBase, selector, opts)

	case "mutations":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		return doctor.Mutations(stateBase, selector)

	case "watches":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		opts := doctor.WatchOpts{}
		if v := stringArg(args, "watch_id"); v != "" {
			opts.WatchID = v
		}
		if b, ok := doctorBoolArg(args, "self_loops"); ok && b {
			opts.SelfLoopsOnly = true
		}
		return doctor.Watches(stateBase, selector, opts)

	case "tree":
		if err := doctorRequireSelector(command, selector); err != nil {
			return nil, err
		}
		opts := doctor.TreeOpts{}
		if n, ok := doctorIntArg(args, "depth"); ok {
			opts.Depth = n
		}
		if b, ok := doctorBoolArg(args, "observers"); ok && b {
			opts.Observers = true
		}
		return doctor.Tree(stateBase, selector, opts)

	case "turnids":
		return doctor.ScanTurnIDs(stateBase)

	case "sessions":
		opts := doctor.SessionsOpts{}
		if v := stringArg(args, "since"); v != "" {
			d, err := doctorParseDuration(v)
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
			d, err := doctorParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("audit since: %w", err)
			}
			opts.Since = d
		}
		res, err := doctor.RunAudit(stateBase, runbook, opts)
		if err != nil {
			return nil, err
		}
		return doctorCapAuditResult(res), nil

	case "plugins":
		// Manager.Doctor includes a store-writability probe (create + remove one
		// temp file) mirroring the CLI's plugins check; everything else it does is
		// read-only. The tool's store root is the default config root — the CLI's
		// --store-root override has no tool counterpart.
		findings, err := plugins.NewManager("").Doctor()
		if err != nil {
			return nil, fmt.Errorf("plugins: %w", err)
		}
		return findings, nil
	}

	// Unreachable: the enum gate above covers every command.
	return nil, fmt.Errorf("unknown doctor command %q", command)
}

// doctorRowCap is the structural row cap for the two unbounded result shapes
// (sessions enumeration and audit evidence). Capping rows inside the result
// keeps the envelope valid JSON under the char limit — the mid-JSON truncation
// the default strategy would otherwise produce. 500 rows is far under the
// 600k-char limit at the row sizes ListSessions emits.
const doctorRowCap = 500

// doctorCapSessionsRows structurally caps a sessions enumeration and discloses
// the cut, mirroring find_session_transcripts' scan_truncated convention.
func doctorCapSessionsRows(res doctor.SessionsResult) doctor.SessionsResult {
	if len(res.Sessions) <= doctorRowCap {
		return res
	}
	kept := res.Sessions[:doctorRowCap]
	return doctor.SessionsResult{
		Sessions:   kept,
		Unreadable: res.Unreadable,
		Truncated:  true,
		TotalRows:  len(res.Sessions),
	}
}

// doctorCapAuditResult caps each finding's evidence session-ref list so a
// fleet-wide trip (the 1,927-session case) cannot overflow the envelope; the
// summary counts stay true.
func doctorCapAuditResult(res doctor.AuditResult) doctor.AuditResult {
	capped := res
	for i := range capped.Findings {
		if len(capped.Findings[i].Evidence.SessionRefs) > doctorRowCap {
			capped.Findings[i].Evidence.SessionRefs = capped.Findings[i].Evidence.SessionRefs[:doctorRowCap]
		}
	}
	return capped
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

// doctorParseDuration accepts a Go duration string (h/m/s suffixes),
// matching the CLI flag package's parsing of --since.
func doctorParseDuration(v string) (time.Duration, error) {
	return time.ParseDuration(v)
}

// doctorLoadRunbook resolves a runbook by name from the bundled
// doctoring-evener skill's runbooks/ — the same resolution the CLI's
// loadRunbook performs. Name sanitization rejects path traversal before the
// FS join.
func doctorLoadRunbook(name string) (doctor.Runbook, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return doctor.Runbook{}, fmt.Errorf("invalid runbook name %q", name)
	}
	rbPath := path.Join("doctoring-evener", "runbooks", name+".md")
	content, err := fs.ReadFile(doctorBundledSkills(), rbPath)
	if err != nil {
		return doctor.Runbook{}, fmt.Errorf("load runbook %q: %w", name, err)
	}
	return doctor.ParseRunbook(name, content)
}

// doctorBoolArg reads a boolean argument; ok is false when absent or not a
// bool.
func doctorBoolArg(args map[string]any, key string) (bool, bool) {
	v, ok := args[key].(bool)
	return v, ok
}

// doctorIntArg reads an integer argument (JSON numbers decode as float64);
// ok is false when absent or not numeric.
func doctorIntArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

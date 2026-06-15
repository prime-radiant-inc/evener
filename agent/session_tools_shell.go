package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// defaultListDirLimit caps how many entries a list_dir call returns when the
// caller does not specify a limit, so a listing of a huge directory cannot blow
// past the tool-output size cap with no recovery handle.
const defaultListDirLimit = 500

// listDirResult is the paginated list_dir wire shape: a bounded page of entries
// plus the totals an agent needs to decide whether to page further or narrow the
// path/depth.
type listDirResult struct {
	Path      string             `json:"path"`
	Entries   []execenv.DirEntry `json:"entries"`
	Total     int                `json:"total"`
	Returned  int                `json:"returned"`
	Offset    int                `json:"offset"`
	Truncated bool               `json:"truncated"`
}

// paginateDirEntries slices entries into the [offset, offset+limit) page (limit
// <= 0 applies defaultListDirLimit, matching the strict-schema unset-as-zero
// convention) and reports the totals. The page is always a non-nil slice so it
// serializes as [] rather than null when empty.
func paginateDirEntries(path string, entries []execenv.DirEntry, offset, limit int) listDirResult {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultListDirLimit
	}
	total := len(entries)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	page := entries[start:end]
	if len(page) == 0 {
		page = []execenv.DirEntry{}
	}
	return listDirResult{
		Path:      path,
		Entries:   page,
		Total:     total,
		Returned:  len(page),
		Offset:    offset,
		Truncated: end < total,
	}
}

func registerShellTools(reg *tool.Registry, s *Session, deps *toolDeps) error {
	// shell
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefShell()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			shellArgs, err := parseShellToolArgs(args)
			if err != nil {
				return "", err
			}
			if se, ok := env.(execenv.StreamingExecutor); ok {
				if s == nil || s.jobManager == nil {
					return "", errors.New("shell jobs require an initialized JobManager")
				}
				shellArgs = applyShellTimeoutPolicy(deps, shellArgs)
				return marshalShellToolResult(runShell(ctx, s.jobManager, se, shellArgs), shellToolResultMaxChars(reg))
			}
			return runBufferedShell(ctx, env, deps, shellArgs)
		},
	}); err != nil {
		return err
	}

	// list_dir (Gemini-aligned)
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefListDir(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["path"])
			depth := 1
			if v, ok := args["depth"].(float64); ok && int(v) > 0 {
				depth = int(v)
			}
			offset := 0
			if v, ok := args["offset"].(float64); ok && int(v) > 0 {
				offset = int(v)
			}
			limit := 0
			if v, ok := args["limit"].(float64); ok && int(v) > 0 {
				limit = int(v)
			}
			entries, err := env.ListDirectory(path, depth)
			if err != nil {
				return nil, err
			}
			return paginateDirEntries(path, entries, offset, limit), nil
		},
	})

	// grep
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefGrep(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			glob := fmt.Sprint(args["glob_filter"])
			ci := false
			if v, ok := args["case_insensitive"].(bool); ok {
				ci = v
			}
			maxRes := 100
			if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
				maxRes = int(v)
			}
			outputMode := ""
			if v, ok := args["output_mode"].(string); ok {
				outputMode = v
			}
			return env.Grep(pat, path, glob, ci, maxRes, outputMode)
		},
	}); err != nil {
		return err
	}

	// glob
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefGlob(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			matches, err := env.Glob(pat, path)
			if err != nil {
				return "", err
			}
			return strings.Join(matches, "\n"), nil
		},
	}); err != nil {
		return err
	}

	// apply_patch (OpenAI-specific; best-effort implementation lives in this repo)
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefApplyPatch()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := fmt.Sprint(args["patch"])
			return tool.ApplyPatch(env.WorkingDirectory(), patch)
		},
	})

	return nil
}

func parseShellToolArgs(args map[string]any) (shellArgs, error) {
	parsed := shellArgs{
		Command:     fmt.Sprint(args["command"]),
		Description: stringArg(args, "description"),
		// background false (the strict-provider-forced default) runs foreground and
		// returns when the command finishes; true starts it and returns the job_id now.
		Background: shellBoolArg(args, "background"),
	}
	var ok bool
	if parsed.MaxRuntimeMS, ok = shellIntArg(args, "max_runtime_ms"); !ok {
		parsed.MaxRuntimeMS = 0
	}
	if parsed.MaxRuntimeMS < 0 {
		return shellArgs{}, errors.New("max_runtime_ms must be non-negative")
	}
	if parsed.MaxRuntimeMS > 0 && parsed.MaxRuntimeMS < minShellMaxRuntimeMS {
		parsed.MaxRuntimeMS = minShellMaxRuntimeMS
	}
	return parsed, nil
}

func shellBoolArg(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func shellIntArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

const (
	shellToolResultDefaultMaxChars = 30_000
	shellToolResultMinJSONChars    = 800
	minShellMaxRuntimeMS           = 1000
)

func enforceShellToolJSONLimit(reg *tool.Registry) {
	if reg == nil {
		return
	}
	registered := reg.Get("shell")
	if registered == nil || registered.Limit.MaxChars >= shellToolResultMinJSONChars {
		return
	}
	reg.OverrideLimits(map[string]schema.ToolOutputLimit{
		"shell": {MaxChars: shellToolResultMinJSONChars},
	})
}

func shellToolResultMaxChars(reg *tool.Registry) int {
	if reg == nil {
		return shellToolResultDefaultMaxChars
	}
	registered := reg.Get("shell")
	if registered == nil || registered.Limit.MaxChars <= 0 {
		return shellToolResultDefaultMaxChars
	}
	if registered.Limit.MaxChars < shellToolResultMinJSONChars {
		return shellToolResultMinJSONChars
	}
	return registered.Limit.MaxChars
}

func marshalShellToolResult(res shellResult, maxChars int) (tool.TextResult, error) {
	// complete-or-handle (spec §0.6): within-bound results carry a settle
	// closure. Apply both layers — ride-whole budget (shellRideWholeBytes)
	// and tool-result char bound (maxChars) — to decide keep vs discard.
	if res.settle != nil {
		return marshalCompleteOrHandleResult(res, maxChars)
	}

	out := shellToolResult{
		JobID:               res.JobID,
		Type:                res.Type,
		Status:              res.Status,
		Reason:              shellStringPtrOrNil(res.Reason),
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		ExitCode:            res.ExitCode,
	}
	if !res.RunningInBackground || res.Output != "" {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
		out.TotalBytes = res.TotalBytes
		out.DroppedBytes = res.DroppedBytes
		out.OutputStatus = outputWindowStatus(res.TotalBytes, res.DroppedBytes, res.Truncated)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return tool.TextResult{}, err
	}
	full := string(b)
	model := full
	if maxChars > 0 {
		var err error
		model, err = marshalBoundedShellToolResult(out, maxChars)
		if err != nil {
			return tool.TextResult{}, err
		}
	}
	return tool.TextResult{Output: model, FullOutput: full}, nil
}

// marshalCompleteOrHandleResult implements spec §0.6 for within-bound shell
// completions. It checks both layers:
//
//  1. Ride-whole budget (shellRideWholeBytes): output > 8KB cannot ride whole
//     in the inline result.
//  2. Tool-result char bound (maxChars): marshaled JSON > bound also exceeds.
//
// If either layer triggers → settle(true) keeps the delayed job (durable),
// returns a truncated tail + job_id. Otherwise → settle(false) discards the
// delayed job (ephemeral), returns complete output inline with no job_id.
func marshalCompleteOrHandleResult(res shellResult, maxChars int) (tool.TextResult, error) {
	falseVal := false
	out := shellToolResult{
		Type:         res.Type,
		Status:       res.Status,
		Reason:       shellStringPtrOrNil(res.Reason),
		TimedOut:     res.TimedOut,
		ExitCode:     res.ExitCode,
		Output:       &res.Output,
		Truncated:    &falseVal,
		TotalBytes:   res.TotalBytes,
		DroppedBytes: res.DroppedBytes,
	}

	// Layer 1: ride-whole budget.
	embedExceeded := len(res.Output) > shellRideWholeBytes

	// Layer 2: tool-result char bound. Marshal with full output and check.
	charBoundExceeded := false
	if !embedExceeded && maxChars > 0 {
		b, err := json.Marshal(out)
		if err != nil {
			_ = res.settle(false)
			return tool.TextResult{}, err
		}
		charBoundExceeded = jsonCharLen(b) > maxChars
	}

	if !embedExceeded && !charBoundExceeded {
		// Ephemeral: complete output fits inline. Discard the delayed job.
		_ = res.settle(false)
		out.OutputStatus = outputWindowStatus(res.TotalBytes, res.DroppedBytes, false)
		b, err := json.Marshal(out)
		if err != nil {
			return tool.TextResult{}, err
		}
		s := string(b)
		return tool.TextResult{Output: s, FullOutput: s}, nil
	}

	// Keep: output cannot ride whole inline. Commit + finalize the delayed job.
	jobID := res.settle(true)
	out.JobID = jobID
	out.OutputStatus = outputWindowStatus(res.TotalBytes, res.DroppedBytes, true)

	// FullOutput (TOOL_CALL_END, for observers/hooks) carries the complete output;
	// the durable job retains it too. The model sees only a small head+tail digest
	// + the job_id — it pages the rest via job_read_output.
	fullBytes, err := json.Marshal(out)
	if err != nil {
		return tool.TextResult{}, err
	}

	peek := out
	digest := shellInlineDigest(res.Output, res.TotalBytes, res.DroppedBytes)
	peekTruncated := true
	peek.Output = &digest
	peek.Truncated = &peekTruncated
	if maxChars > 0 {
		model, err := marshalBoundedShellToolResult(peek, maxChars)
		if err != nil {
			return tool.TextResult{}, err
		}
		return tool.TextResult{Output: model, FullOutput: string(fullBytes)}, nil
	}
	pb, err := json.Marshal(peek)
	if err != nil {
		return tool.TextResult{}, err
	}
	return tool.TextResult{Output: string(pb), FullOutput: string(fullBytes)}, nil
}

// outputWindowStatus classifies an output window for the model: "all_retained"
// when the returned window is the whole retained log, "windowed" when the full
// log is retained but only a slice was returned (page the rest via
// job_read_output), and "evicted" when the oldest bytes were permanently dropped
// past the retention cap. It describes the WINDOW, not the job lifecycle — a
// running job whose window covers everything-so-far still reports "all_retained".
func outputWindowStatus(total, dropped int64, truncated bool) string {
	if dropped > 0 {
		return "evicted"
	}
	if truncated {
		return "windowed"
	}
	return "all_retained"
}

func marshalBoundedShellToolResult(out shellToolResult, maxChars int) (string, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	if jsonCharLen(b) <= maxChars || out.Output == nil {
		return string(b), nil
	}

	original := []rune(*out.Output)
	truncated := true
	out.Truncated = &truncated

	best := ""
	lo, hi := 0, len(original)
	for lo <= hi {
		mid := lo + (hi-lo)/2
		candidate := string(original[len(original)-mid:])
		out.Output = &candidate
		b, err = json.Marshal(out)
		if err != nil {
			return "", err
		}
		if jsonCharLen(b) <= maxChars {
			best = candidate
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}

	out.Output = &best
	b, err = json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsonCharLen(b []byte) int {
	return len([]rune(string(b)))
}

type shellToolResult struct {
	JobID               string  `json:"job_id,omitempty"`
	Type                string  `json:"type"`
	Status              string  `json:"status"`
	Reason              *string `json:"reason"`
	RunningInBackground bool    `json:"running_in_background"`
	TimedOut            bool    `json:"timed_out"`
	ExitCode            *int    `json:"exit_code,omitempty"`
	Output              *string `json:"output,omitempty"`
	Truncated           *bool   `json:"truncated,omitempty"`
	TotalBytes          int64   `json:"total_bytes,omitempty"`
	DroppedBytes        int64   `json:"dropped_bytes,omitempty"`
	OutputStatus        string  `json:"output_status,omitempty"`
}

func shellStringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func applyShellTimeoutPolicy(deps *toolDeps, args shellArgs) shellArgs {
	if deps == nil || deps.cmdTimeouts == nil {
		return args
	}
	defTimeout, maxTimeout := deps.cmdTimeouts()
	timeout := defTimeout
	if args.BlockTimeoutMS > 0 {
		timeout = args.BlockTimeoutMS
	}
	if maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
	}
	args.BlockTimeoutMS = timeout
	return args
}

func runBufferedShell(ctx context.Context, env execenv.ExecutionEnvironment, deps *toolDeps, args shellArgs) (string, error) {
	// A non-streaming environment has no job manager, so it cannot background a
	// command; making that explicit beats silently running it in the foreground.
	if args.Background {
		return "", errors.New("invalid_request: background requires a streaming execution environment")
	}
	args = applyShellTimeoutPolicy(deps, args)
	timeout := args.BlockTimeoutMS
	timeoutParam := "max_runtime_ms"
	if args.MaxRuntimeMS > 0 && (timeout == 0 || args.MaxRuntimeMS < timeout) {
		timeout = args.MaxRuntimeMS
	}
	res, err := env.ExecCommand(ctx, args.Command, timeout, "", nil)

	// Return a line-oriented tool output so line truncation works as intended for shell output.
	var b strings.Builder
	if strings.TrimSpace(res.Stdout) != "" {
		b.WriteString(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(res.Stderr) != "" {
		b.WriteString(res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	if errors.Is(err, context.Canceled) && !res.TimedOut {
		b.WriteString("[ERROR: Command was canceled before completion. Partial output is shown above.]\n")
	} else if res.TimedOut {
		b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the %s parameter.]\n", timeout, timeoutParam))
	}
	b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
	return b.String(), err
}

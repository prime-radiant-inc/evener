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
				// Validate a launch-time watch before the job is created, so a
				// malformed watch is a synchronous error with no durable job.
				if shellArgs.Watch != nil {
					if err := s.jobManager.validateLaunchWatch(*shellArgs.Watch); err != nil {
						return "", err
					}
				}
				shellArgs = applyShellTimeoutPolicy(deps, shellArgs)
				return marshalShellToolResult(runShell(ctx, s.jobManager, se, shellArgs), shellToolResultMaxChars(reg))
			}
			if shellArgs.Watch != nil {
				return "", errors.New("invalid_request: watch requires a background-capable shell")
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
			return env.ListDirectory(path, depth)
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
	}
	var ok bool
	// max_wait_ms: 0/absent = session default (unset); positive = explicit bound;
	// negative = invalid_request. Zero reads as unset so strict-mode providers
	// (OpenAI Responses) that force every parameter on every call still work.
	if parsed.BlockTimeoutMS, ok = shellIntArg(args, "max_wait_ms"); !ok {
		parsed.BlockTimeoutMS = 0
	}
	if parsed.BlockTimeoutMS < 0 {
		return shellArgs{}, errors.New("invalid_request: max_wait_ms must be non-negative")
	}
	if parsed.MaxRuntimeMS, ok = shellIntArg(args, "max_runtime_ms"); !ok {
		parsed.MaxRuntimeMS = 0
	}
	if parsed.MaxRuntimeMS < 0 {
		return shellArgs{}, errors.New("max_runtime_ms must be non-negative")
	}
	if parsed.MaxRuntimeMS > 0 && parsed.MaxRuntimeMS < minShellMaxRuntimeMS {
		parsed.MaxRuntimeMS = minShellMaxRuntimeMS
	}
	watch, err := parseLaunchWatchArg(args)
	if err != nil {
		return shellArgs{}, err
	}
	parsed.Watch = watch
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
	// closure. Apply both layers — inline embed budget (shellInlineOutputBytes)
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
//  1. Inline embed budget (shellInlineOutputBytes): output > 64KB cannot ride
//     in the inline result.
//  2. Tool-result char bound (maxChars): marshaled JSON > bound also exceeds.
//
// If either layer triggers → settle(true) keeps the delayed job (durable),
// returns a truncated tail + job_id. Otherwise → settle(false) discards the
// delayed job (ephemeral), returns complete output inline with no job_id.
func marshalCompleteOrHandleResult(res shellResult, maxChars int) (tool.TextResult, error) {
	falseVal := false
	out := shellToolResult{
		Type:      res.Type,
		Status:    res.Status,
		Reason:    shellStringPtrOrNil(res.Reason),
		TimedOut:  res.TimedOut,
		ExitCode:  res.ExitCode,
		Output:    &res.Output,
		Truncated: &falseVal,
	}

	// Layer 1: inline embed budget.
	embedExceeded := len(res.Output) > shellInlineOutputBytes

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
		b, err := json.Marshal(out)
		if err != nil {
			return tool.TextResult{}, err
		}
		s := string(b)
		return tool.TextResult{Output: s, FullOutput: s}, nil
	}

	// Keep: output cannot ride inline in full. Commit + finalize the delayed job.
	jobID := res.settle(true)

	// Build the kept result with a bounded tail and job_id.
	out.JobID = jobID
	if maxChars > 0 {
		model, err := marshalBoundedShellToolResult(out, maxChars)
		if err != nil {
			return tool.TextResult{}, err
		}
		b, err := json.Marshal(out)
		if err != nil {
			return tool.TextResult{}, err
		}
		return tool.TextResult{Output: model, FullOutput: string(b)}, nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return tool.TextResult{}, err
	}
	s := string(b)
	return tool.TextResult{Output: s, FullOutput: s}, nil
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
	args = applyShellTimeoutPolicy(deps, args)
	timeout := args.BlockTimeoutMS
	timeoutParam := "max_wait_ms"
	if args.MaxRuntimeMS > 0 && (timeout == 0 || args.MaxRuntimeMS < timeout) {
		timeout = args.MaxRuntimeMS
		timeoutParam = "max_runtime_ms"
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

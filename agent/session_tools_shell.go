package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
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
				return marshalShellToolResult(runShell(ctx, s.jobManager, se, shellArgs))
			}
			if shellArgs.Background {
				return "background shell jobs require streaming execution support", errors.New("background shell jobs require streaming execution support")
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
		Background:  shellBoolArg(args, "background"),
	}
	var ok bool
	if parsed.BlockTimeoutMS, ok = shellIntArg(args, "block_timeout_ms"); !ok {
		parsed.BlockTimeoutMS = 0
	}
	if parsed.MaxRuntimeMS, ok = shellIntArg(args, "max_runtime_ms"); !ok {
		parsed.MaxRuntimeMS = 0
	}
	if parsed.BlockTimeoutMS < 0 {
		return shellArgs{}, errors.New("block_timeout_ms must be non-negative")
	}
	if parsed.MaxRuntimeMS < 0 {
		return shellArgs{}, errors.New("max_runtime_ms must be non-negative")
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

func marshalShellToolResult(res shellResult) (string, error) {
	var b strings.Builder
	if res.JobID != "" {
		b.WriteString("job_id=")
		b.WriteString(res.JobID)
		b.WriteString(" ")
	}
	b.WriteString("type=")
	b.WriteString(res.Type)
	b.WriteString(" status=")
	b.WriteString(res.Status)
	if res.Reason != "" {
		b.WriteString(" reason=")
		b.WriteString(res.Reason)
	} else {
		b.WriteString(" reason=null")
	}
	b.WriteString(fmt.Sprintf(" running_in_background=%t timed_out=%t", res.RunningInBackground, res.TimedOut))
	if res.ExitCode != nil {
		b.WriteString(fmt.Sprintf(" exit_code=%d", *res.ExitCode))
	}
	if !res.RunningInBackground || res.Output != "" {
		b.WriteString(fmt.Sprintf(" truncated=%t", res.Truncated))
		b.WriteString("\noutput:\n")
		b.WriteString(res.Output)
		if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func runBufferedShell(ctx context.Context, env execenv.ExecutionEnvironment, deps *toolDeps, args shellArgs) (string, error) {
	defTimeout, maxTimeout := deps.cmdTimeouts()
	timeout := defTimeout
	if args.BlockTimeoutMS > 0 {
		timeout = args.BlockTimeoutMS
	}
	if maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
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
		b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the block_timeout_ms parameter.]\n", timeout))
	}
	b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
	return b.String(), err
}

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

func registerShellTools(reg *tool.Registry, deps *toolDeps) error {
	// shell
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefShell()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			cmd := fmt.Sprint(args["command"])
			defTimeout, maxTimeout := deps.cmdTimeouts()
			timeout := defTimeout
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			if maxTimeout > 0 && timeout > maxTimeout {
				timeout = maxTimeout
			}
			res, err := env.ExecCommand(ctx, cmd, timeout, "", nil)

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
				b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]\n", timeout))
			}
			b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
			return b.String(), err
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

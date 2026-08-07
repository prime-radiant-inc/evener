package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// registerGoalTools registers the update_goal tool into reg, mirroring registerTaskTools.
func registerGoalTools(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefUpdateGoal()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env

			store := deps.goalGuard.Store()

			statusStr := fmt.Sprint(args["status"])
			var st goal.Status
			switch statusStr {
			case "complete":
				st = goal.StatusComplete
			case "blocked":
				st = goal.StatusBlocked
			default:
				return nil, fmt.Errorf("update_goal: invalid status %q (must be \"complete\" or \"blocked\")", statusStr)
			}

			if !store.SetTerminal(st, "", deps.now()) {
				return tool.StateResult{Output: "No goal is active for this session (none was set at launch); nothing recorded — this tool only updates a goal the harness registered."}, nil
			}

			snap, _ := store.Snapshot()
			return tool.StateResult{
				Output: "Goal marked " + statusStr + ".",
				State:  goalStateView(snap),
			}, nil
		},
	})
}

// goalStateView converts a goal snapshot into a small serializable map for the
// tool_state side-channel on TOOL_CALL_END events.
func goalStateView(snap goal.Snapshot) map[string]any {
	return map[string]any{
		"objective":  snap.Objective,
		"status":     string(snap.Status),
		"iterations": snap.Iterations,
		"stopReason": snap.StopReason,
	}
}

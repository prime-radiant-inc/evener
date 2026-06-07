package agent

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// defUpdateGoal returns the tool definition for update_goal.
// The model calls this to declare the active goal complete or blocked.
func defUpdateGoal() llm.ToolDefinition {
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
					"type": "string",
					"enum": []string{"complete", "blocked"},
				},
			},
			"required": []string{"status"},
		},
	}
}

// registerGoalTools registers the update_goal tool into reg, mirroring registerTaskTools.
func registerGoalTools(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: defUpdateGoal()},
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

			if !store.SetTerminal(st, "", time.Now()) {
				return tool.StateResult{Output: "No active goal to update."}, nil
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

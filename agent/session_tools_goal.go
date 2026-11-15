package agent

import (
	"context"
	"fmt"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/tool"
)

// registerGoalTools registers the update_goal tool into reg, mirroring registerTaskTools.
func registerGoalTools(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefUpdateGoal(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env

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

			snap, res := deps.goalGuard.TransitionTerminal(st, "", deps.now())
			switch res {
			case goal.SetTerminalSuccess:
				return tool.StateResult{
					Output: "Goal marked " + statusStr + ".",
					State:  goalStateView(snap),
				}, nil
			case goal.SetTerminalAlreadyTerminal:
				return tool.StateResult{
					Output: "The goal for this session is already terminal (" + string(snap.Status) + "); nothing recorded — this tool only updates an active goal.",
					State:  goalStateView(snap),
				}, nil
			default:
				return tool.StateResult{Output: "No goal is active for this session (none was set at launch); nothing recorded — this tool only updates a goal the harness registered."}, nil
			}
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

// getOrCreateGoalStore returns the session's goal store, initializing it lazily
// on first call. The store has its own mutex and is goroutine-safe.
func (s *Session) getOrCreateGoalStore() *goal.Store {
	s.goalStoreOnce.Do(func() {
		s.goalStore = goal.NewStore()
	})
	return s.goalStore
}

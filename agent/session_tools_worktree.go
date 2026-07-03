package agent

import (
	"context"
	"errors"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// registerWorktreeTool registers the manage_worktree lifecycle tool (spec §2)
// directly on the registry, mirroring registerGoalTools/registerTaskTools:
// registry-only, not part of any provider profile's own tool definitions.
// Register with ReadOnly unset (false) — even the list operation is part of a
// stateful lifecycle tool and must serialize with env-changing operations.
//
// This is the Task-12 registration skeleton only. The six operations
// (create/list/switch/exit/remove/prune) are implemented in Tasks 13-16; the
// handler below is a placeholder that reports it isn't wired up yet.
func registerWorktreeTool(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefManageWorktree()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			_ = deps
			return nil, errors.New("manage_worktree: not yet implemented")
		},
	})
}

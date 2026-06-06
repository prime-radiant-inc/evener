package agent

import (
	"context"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func findSessionTranscriptsTool(deps *toolDeps) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefFindSessionTranscripts(), ReadOnly: true},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return execFindSessionTranscripts(deps, args)
		},
	}
}

// execFindSessionTranscripts discovers prior sessions (catalog/query/children_of).
// The discovery executor and trimmed records are implemented in the find task.
func execFindSessionTranscripts(deps *toolDeps, args map[string]any) (any, error) {
	_ = deps
	_ = args
	return map[string]any{"todo": "find_session_transcripts not yet implemented"}, nil
}

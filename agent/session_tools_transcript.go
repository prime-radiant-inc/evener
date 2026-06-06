package agent

import (
	"context"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// transcriptToolMaxChars is the explicit registry output limit applied to every
// transcript tool. The render/outline layer is the single source of truncation; this
// ceiling only stops the registry from re-truncating a render-bounded envelope into
// invalid JSON. It is a backstop, not a participant.
const transcriptToolMaxChars = 600_000

// transcriptTools returns the read-only transcript inspection tools. It is wired into
// the session's tool assembly only when state persistence is enabled (a non-empty
// StateDir), since the tools resolve and read on-disk transcript files.
func transcriptTools(deps *toolDeps) []tool.RegisteredTool {
	tools := []tool.RegisteredTool{
		readSessionTranscriptTool(deps),
		findSessionTranscriptsTool(deps),
	}
	for i := range tools {
		tools[i].Limit = schema.ToolOutputLimit{MaxChars: transcriptToolMaxChars}
	}
	return tools
}

func readSessionTranscriptTool(deps *toolDeps) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefReadSessionTranscript(), ReadOnly: true},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return execReadSessionTranscript(deps, args)
		},
	}
}

// execReadSessionTranscript renders one session by ref. The format dispatch
// (outline/markdown/jsonl), range, and expand_turn are implemented in later tasks.
func execReadSessionTranscript(deps *toolDeps, args map[string]any) (any, error) {
	_ = deps
	_ = args
	return map[string]any{"todo": "read_session_transcript not yet implemented"}, nil
}

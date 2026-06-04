package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func registerWebTools(reg *tool.Registry, deps *toolDeps) {
	// Web fetch.
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefWebFetch()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			rawURL := fmt.Sprint(args["url"])
			question := fmt.Sprint(args["question"])
			return deps.web.fetch(ctx, rawURL, question)
		},
	})

	// Web search (Gemini only — see tool_web_search.go for why).
	// OpenAI and Anthropic handle web search natively via req.WebSearch;
	// registering a function tool named "web_search" for those providers
	// causes a duplicate name collision with the adapter-injected server tool.
	if deps.webSearchEnabled {
		_ = reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: tool.DefWebSearch()},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				query := fmt.Sprint(args["query"])
				return deps.web.search(ctx, query)
			},
		})
	}
}

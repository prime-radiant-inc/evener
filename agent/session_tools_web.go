package agent

import (
	"context"
	"fmt"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
)

// egressDeniedByNet returns a typed DeniedError when env is a sandboxed session
// whose network egress is off, else nil. evener's own web egress (web_fetch) is part of the tool plane that --sandbox-net off governs; the LLM
// inference connection is not. In a non-sandboxed session (the wrapper is nil —
// every session until the flag goes live) this is always a no-op, so the web
// tools behave exactly as before.
func egressDeniedByNet(env execenv.ExecutionEnvironment, toolName string) error {
	p, ok := env.(interface {
		KernelWrapper() *sandbox.Wrapper
	})
	if !ok {
		return nil
	}
	w := p.KernelWrapper()
	if w == nil || w.Policy().Network {
		return nil
	}
	return &sandbox.DeniedError{
		Mode:   w.Policy().Mode,
		Tool:   toolName,
		Reason: "network egress is disabled in this sandbox; this sandbox policy is fixed for the session",
	}
}

func registerWebTools(reg *tool.Registry, deps *toolDeps) {
	// Web fetch.
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefWebFetch(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			if err := egressDeniedByNet(env, "web_fetch"); err != nil {
				return nil, err
			}
			rawURL := fmt.Sprint(args["url"])
			question := fmt.Sprint(args["question"])
			return deps.web.fetch(ctx, rawURL, question)
		},
	})
}

package agent

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/internal/tool"
)

func registerModelListTool(reg *tool.Registry, s *Session) error {
	return reg.Register(tool.RegisteredTool{
		Definition: tool.DefModelList(), ReadOnly: true,
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			cursor, _ := args["cursor"].(string)
			count := modelavailability.DefaultInlineMaxCount
			bytes := modelavailability.DefaultInlineMaxBytes
			if n, ok := args["max_count"].(float64); ok && n > 0 {
				count = int(n)
			}
			if n, ok := args["max_bytes"].(float64); ok && n > 0 {
				bytes = int(n)
			}
			if count > modelavailability.DefaultInlineMaxCount || bytes > modelavailability.DefaultInlineMaxBytes {
				return nil, errors.New("page bounds exceed contract")
			}
			return s.modelSnapshot.Page(cursor, count, bytes)
		},
	})
}

package agent

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
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

func enforceModelListJSONLimits(reg *tool.Registry) {
	if reg == nil {
		return
	}
	registered := reg.Get("model_list")
	if registered == nil {
		return
	}

	override := schema.ToolOutputLimit{Strategy: registered.Limit.Strategy}
	if registered.Limit.MaxChars < modelavailability.DefaultInlineMaxBytes {
		override.MaxChars = modelavailability.DefaultInlineMaxBytes
	}
	if registered.Limit.MaxLines > 0 && registered.Limit.MaxLines <= modelavailability.DefaultInlineMaxBytes {
		override.MaxLines = modelavailability.DefaultInlineMaxBytes + 1
	}
	if override.MaxChars == 0 && override.MaxLines == 0 {
		return
	}
	reg.OverrideLimits(map[string]schema.ToolOutputLimit{"model_list": override})
}

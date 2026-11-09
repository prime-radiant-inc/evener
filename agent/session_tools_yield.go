package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
)

// registerYieldTool wires the quiet-yield tool (issue #832). It is a terminal,
// non-user-visible tool: its executor records the request, and processOneInput
// ends the activation once the round's tool results are durable.
func registerYieldTool(reg *tool.Registry, s *Session) error {
	return reg.Register(tool.RegisteredTool{
		Definition: tool.DefYield(),
		ReadOnly:   true,
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return quietYieldTool(s, ctx, args)
		},
	})
}

// quietYieldTool validates one yield call and arms its optional fallback.
func quietYieldTool(s *Session, ctx context.Context, args map[string]any) (any, error) {
	if s.cfg.TurnEndsProcess {
		return nil, errors.New("invalid_request: yield needs a session that outlives this turn; call communicate with end_turn=true instead")
	}
	timeoutSeconds, hasTimeout, err := quietYieldTimeoutArg(args)
	if err != nil {
		return nil, err
	}
	if !hasTimeout && !s.hasKnownRunningWork() {
		return nil, errors.New("invalid_request: a yield without timeout_seconds needs a running shell job or stable delegate that can wake the session; pass timeout_seconds or start the work first")
	}
	y := s.requestQuietYield(tool.IntentFromContext(ctx), time.Duration(timeoutSeconds)*time.Second)
	result := map[string]any{
		"status":   "yielding",
		"yield_id": y.ID,
	}
	if hasTimeout {
		result["timeout_seconds"] = timeoutSeconds
		result["deadline"] = y.Deadline.UTC().Format(time.RFC3339)
	}
	return result, nil
}

// quietYieldTimeoutArg decodes the optional timeout strictly: the registry
// schema already bounds it at dispatch, and this repeats the same checks so a
// direct executor call (and a provider that ignored the schema) cannot arm an
// unbounded or zero-length wait.
func quietYieldTimeoutArg(args map[string]any) (seconds int, present bool, err error) {
	raw, ok := args["timeout_seconds"]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case float64:
		if value != float64(int(value)) {
			return 0, false, errors.New("invalid_request: timeout_seconds must be an integer")
		}
		seconds = int(value)
	case int:
		seconds = value
	default:
		return 0, false, errors.New("invalid_request: timeout_seconds must be an integer")
	}
	if seconds < 1 || seconds > quietYieldTimeoutMaxSeconds {
		return 0, false, fmt.Errorf("invalid_request: timeout_seconds must be between 1 and %d", quietYieldTimeoutMaxSeconds)
	}
	return seconds, true, nil
}

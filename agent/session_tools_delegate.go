package agent

import (
	"context"
	"errors"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

type delegateCreateResult struct {
	DelegateID     string                      `json:"delegate_id"`
	ChildSessionID string                      `json:"child_session_id"`
	Type           string                      `json:"type"`
	Status         string                      `json:"status"`
	Name           string                      `json:"name,omitempty"`
	AgentType      string                      `json:"agent_type,omitempty"`
	Tools          []string                    `json:"tools,omitempty"`
	Reason         string                      `json:"reason,omitempty"`
	Resumable      *bool                       `json:"resumable,omitempty"`
	TranscriptRef  string                      `json:"transcript_ref"`
	Model          string                      `json:"model,omitempty"`
	Sandbox        *delegateSandboxToolResult  `json:"sandbox,omitempty"`
	Worktree       *delegateWorktreeToolResult `json:"worktree,omitempty"`
	Warnings       []string                    `json:"warnings,omitempty"`
	StartError     string                      `json:"error,omitempty"`
	// ArtifactsDir names the delegation's durable per-delegation artifacts
	// directory so the controller can reference it in briefs and hand it to
	// later seats. It dies with the delegate's own session state.
	ArtifactsDir string `json:"artifacts_dir,omitempty"`
}

func registerDelegateTool(reg *tool.Registry, s *Session) error {
	reg.Remove("delegate")
	if err := reg.Register(tool.RegisteredTool{
		Definition: s.delegateToolDefinition(),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return delegateCreateTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate"))
		},
	}); err != nil {
		return err
	}
	reg.Remove("delegate_send")
	return reg.Register(tool.RegisteredTool{
		Definition: tool.DefDelegateSendNamed(s.resultToolName()),
		Limit:      schema.ToolOutputLimit{MaxChars: delegateReportWholeMaxChars, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return delegateSendTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate_send"))
		},
	})
}

func delegateSendTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	target := strings.TrimSpace(stringArg(args, "to"))
	if target == runtimeMessageAliasCaller {
		return nil, errors.New("invalid_request: delegate_send reaches only your own delegates; to message your parent, call communicate with end_turn=false")
	}
	message := strings.TrimSpace(stringArg(args, "message"))
	wait := 0
	if n, ok := shellIntArg(args, "max_wait_ms"); ok {
		if n < 0 {
			return nil, errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		wait = n
	}
	requestedWait := wait
	if wait > 0 {
		wait = int(clampJobBlockTimeout(wait).Milliseconds())
	}
	outcome := (delegateRuntime{owner: s}).send(ctx, target, message, wait)
	outcome.result.WaitIgnoredReason = liveSteerWaitIgnoredReason(requestedWait, outcome.result.Status, outcome.result.Action)
	if outcome.result.Err != nil && outcome.result.DelegateID == "" {
		return nil, outcome.result.Err
	}
	if outcome.commit != nil {
		commits := append(slices.Clone(outcome.earlierCommits), outcome.commit)
		callID, _ := ctx.Value(ctxToolCallID).(string)
		if callID == "" {
			for _, commit := range commits {
				_, _ = commit.Complete(false)
			}
			return nil, errors.New("delegate result cannot be committed outside a tool round")
		}
		for _, commit := range commits {
			s.queueDelegateDeliveryCommit(callID, commit)
		}
	}
	return marshalDelegateSendResult(outcome.result, maxChars)
}

func delegateCreateTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (string, error) {
	if _, exists := args["max_wait_ms"]; exists {
		return "", errors.New("invalid_request: delegate creation does not accept max_wait_ms")
	}
	decoded, err := decodeDelegateArgs(args)
	if err != nil {
		return "", err
	}
	result := s.createDelegate(ctx, decoded)
	if result.Err != nil && result.DelegateID == "" {
		return "", result.Err
	}
	out := delegateCreateResult{
		DelegateID:     result.DelegateID,
		ChildSessionID: result.ChildSessionID,
		Type:           result.Type,
		Status:         string(result.Status),
		Name:           result.Name,
		AgentType:      result.AgentType,
		Tools:          append([]string(nil), result.Tools...),
		Reason:         result.Reason,
		Resumable:      result.Resumable,
		TranscriptRef:  result.TranscriptRef,
		Model:          result.Model,
		Sandbox:        delegateSandboxToolResultFrom(result.Sandbox),
		Worktree:       delegateWorktreeToolResultFrom(result.Worktree),
		ArtifactsDir:   result.ArtifactsDir,
		Warnings:       append([]string(nil), result.Warnings...),
	}
	if result.Err != nil {
		out.StartError = result.Err.Error()
	}
	return marshalDelegateCreateResult(out, maxChars)
}

func marshalDelegateCreateResult(out delegateCreateResult, maxChars int) (string, error) {
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	// Preserve the durable identity and outcome by dropping optional diagnostics
	// from least to most useful before falling back to the bounded core.
	// ArtifactsDir is the delegation's routing contract, so it is dropped only
	// after the large supplementary diagnostics (StartError, Warnings, Worktree)
	// and only when the core plus the path still cannot fit. It is dropped before
	// Model and Sandbox because the minimum-bound contract
	// (TestDelegateResourceCreate_RegisteredPostCommitFailureRetainsStableIdentityWithinMinimumLimit)
	// pins the model diagnostic as retained at the floor.
	out.StartError = ""
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Warnings = nil
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Worktree = nil
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.ArtifactsDir = ""
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Model = ""
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Sandbox = nil
	return marshalBoundedJSON(out, maxChars)
}

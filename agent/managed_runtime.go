package agent

import (
	"context"
	"encoding/json"
	"errors"

	"primeradiant.com/evener/llm"
)

// ManagedRuntimeProvider supplies an application-owned local catalog and binding.
// Catalog and Bind must be nonblocking and must not acquire a service. Runtime
// authority is established only by Prepare or Authorize for an actual call.
type ManagedRuntimeProvider interface {
	Catalog() ([]ManagedTool, error)
	Bind(ManagedSession) (ManagedBinding, error)
}

// ManagedTool declares a strict tool. Validate must reject malformed or ambiguous
// JSON and enforce the complete application schema before any generic decoding.
type ManagedTool struct {
	Definition llm.ToolDefinition
	Operation  string
	ReadOnly   bool
	Validate   func(json.RawMessage) error
}

// ManagedSession is the actual engine identity and final narrowed tool catalog.
// Parent is the current live parent binding, never a persisted authority claim.
type ManagedSession struct {
	SessionID, ParentSessionID string
	Parent                     ManagedBinding
	Tools                      []string
}

// ManagedBinding owns only one session's authority, not a shared service process.
// Prepare must return without submitting an operation. Execute receives exactly
// the prepared request on first dispatch and recovery. A transport error from
// Execute means outcome unknown; domain failures belong in ManagedResult.Host.
// Authorize checks current policy, including for cached-result settlement.
type ManagedBinding interface {
	Prepare(context.Context, ManagedCall) (ManagedRequest, error)
	Authorize(context.Context, ManagedRequest) error
	Execute(context.Context, ManagedRequest) (ManagedResult, error)
	Close(context.Context) error
}

// ManagedCall is the accepted hook-prepared input and actual tool occurrence.
// InvocationID is empty for ephemeral reads; durable reads keep it local. Only
// fixed-catalog mutation operations may inject it into service mutationId.
type ManagedCall struct {
	ToolName, ToolCallID, Operation, InvocationID string
	Arguments                                     json.RawMessage
}

// ManagedIdentity contains stable logical authority, never a lease or transport.
type ManagedIdentity struct {
	BindingID   string `json:"binding_id"`
	ServiceID   string `json:"service_id"`
	RealmID     string `json:"realm_id"`
	PrincipalID string `json:"principal_id"`
	NamespaceID string `json:"namespace_id"`
}

// ManagedRequest contains immutable service input and stable authority IDs only.
// The session journal stores Arguments as bytes so JSON formatting is preserved.
type ManagedRequest struct {
	ToolName     string          `json:"tool_name"`
	ToolCallID   string          `json:"tool_call_id"`
	Identity     ManagedIdentity `json:"identity"`
	Operation    string          `json:"operation"`
	InvocationID string          `json:"invocation_id"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
}

// ManagedResult separates original model text from the trusted host envelope.
// Its complete default encoding/json representation must fit 112 MiB; the
// separately authored ModelText JSON string must fit 8 MiB, including escaping.
// A trusted adapter retains the whole service payload and bounds its additional
// encoded origin/tool metadata to 1 MiB. A 16 MiB unescaped wire response can
// become 96 MiB under durable HTML escaping. These limits leave room for both
// original and provider-facing text in a single 128 MiB transcript entry.
// ModelText is a deliberate allowlisted projection, never the host envelope.
type ManagedResult struct {
	ModelText string         `json:"model_text"`
	Host      *llm.MCPResult `json:"host,omitempty"`
}

// ErrManagedUnavailable identifies transient runtime or service unavailability.
var ErrManagedUnavailable = errors.New("managed runtime unavailable")

// ErrManagedAuthorityDenied identifies current policy refusal, not a retry hint.
var ErrManagedAuthorityDenied = errors.New("managed authority denied")

// ErrManagedRecoveryPending marks an unresolved unpaired durable invocation.
var ErrManagedRecoveryPending = errors.New("managed invocation recovery pending")

// ManagedRecoveryPendingError refuses runnable history with an unpaired durable
// invocation. Denied is a policy refusal, not a promise of automatic retry.
type ManagedRecoveryPendingError struct {
	Denied bool
	Cause  error
}

func (e *ManagedRecoveryPendingError) Error() string {
	if e.Denied {
		return "managed invocation recovery pending: authority denied"
	}
	return "managed invocation recovery pending: outcome unavailable"
}
func (e *ManagedRecoveryPendingError) Unwrap() error { return e.Cause }

// Is identifies pending recovery without discarding the underlying cause.
func (e *ManagedRecoveryPendingError) Is(target error) bool {
	return target == ErrManagedRecoveryPending
}

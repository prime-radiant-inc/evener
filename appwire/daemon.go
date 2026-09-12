package appwire

import "context"

// Daemon lifecycle wire contracts. Timestamps are RFC3339 strings (UTC,
// fractional seconds preserved); durations are integer milliseconds with the
// unit in the field name. A zero TimeoutMillis means the retirement timer is
// disabled; a nil *DaemonLifecycle means the capability is unknown (the peer
// cannot answer evener/daemon/status). The two are never interchangeable.

type DaemonIdentity struct {
	Ref        string `json:"ref"`
	PID        int    `json:"pid"`
	StartedAt  string `json:"startedAt"`
	Generation string `json:"generation"` // opaque, non-secret fingerprint of exact ownership
}

type DaemonBlocker struct {
	Category   string `json:"category"`
	SessionID  string `json:"sessionId,omitempty"`
	DelegateID string `json:"delegateId,omitempty"`
}

type DaemonLifecycle struct {
	Phase         string          `json:"phase"`
	TimeoutMillis int64           `json:"timeoutMillis"`
	EligibleSince string          `json:"eligibleSince,omitempty"`
	Deadline      string          `json:"deadline,omitempty"`
	Blockers      []DaemonBlocker `json:"blockers"`
	Failure       string          `json:"failure,omitempty"`
}

type DaemonResident struct {
	Identity      DaemonIdentity   `json:"identity"`
	Name          string           `json:"name"`
	Protocol      string           `json:"protocol"`
	Compatibility string           `json:"compatibility"` // compatible, incompatible, unknown
	Archived      bool             `json:"archived"`
	ProbeState    string           `json:"probeState"` // current, stale, unknown
	Lifecycle     *DaemonLifecycle `json:"lifecycle,omitempty"`
	CanRetire     bool             `json:"canRetire"`
	CanForceStop  bool             `json:"canForceStop"`
}

type DaemonListParams struct{}

type DaemonListResponse struct {
	DefaultTimeoutMillis int64            `json:"defaultTimeoutMillis"`
	Daemons              []DaemonResident `json:"daemons"`
}

type DaemonRetireParams struct {
	Identity DaemonIdentity `json:"identity"`
}

type DaemonRetireResponse struct {
	Accepted  bool            `json:"accepted"`
	Lifecycle DaemonLifecycle `json:"lifecycle"`
}

type DaemonStatusParams struct{}

type DaemonStatusResponse struct {
	Lifecycle DaemonLifecycle `json:"lifecycle"`
}

// LifecycleErrorData composes the existing mutation error data with the
// lifecycle race detail instead of overwriting it: a raced request was not
// durably accepted and an automatic retry is safe once the phase settles.
type LifecycleErrorData struct {
	ErrorData
	LifecycleReason string `json:"lifecycleReason"`
	Retryable       bool   `json:"retryable"`
}

// LifecycleUnavailable is the typed CodeUnavailable a peer receives when a
// request loses the race with a lifecycle transition (preparing/retiring). The
// phase is the reason; the caller must not treat a lost response as proof of
// exit — evener/daemon/status remains the lifecycle authority.
func LifecycleUnavailable(phase string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: "daemon lifecycle unavailable while " + phase,
		Data: LifecycleErrorData{
			ErrorData: ErrorData{
				EvenerErrorInfo:  ErrorActionUnavailable,
				MutationOutcome:  MutationOutcomeNotAccepted,
				RetryDisposition: RetryDispositionAutomatic,
			},
			LifecycleReason: phase,
			Retryable:       true,
		},
	}
}

func (c *Client) DaemonList(ctx context.Context, params DaemonListParams) (DaemonListResponse, error) {
	var out DaemonListResponse
	err := c.request(ctx, MethodEvenerDaemonList, params, &out)
	return out, err
}

func (c *Client) DaemonRetire(ctx context.Context, params DaemonRetireParams) (DaemonRetireResponse, error) {
	var out DaemonRetireResponse
	err := c.request(ctx, MethodEvenerDaemonRetire, params, &out)
	return out, err
}

func (c *Client) DaemonStatus(ctx context.Context, params DaemonStatusParams) (DaemonStatusResponse, error) {
	var out DaemonStatusResponse
	err := c.request(ctx, MethodEvenerDaemonStatus, params, &out)
	return out, err
}

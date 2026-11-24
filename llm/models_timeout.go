package llm

import (
	"context"
	"time"

	"primeradiant.com/evener/llm/registry"
)

const (
	// LiveModelListTimeout bounds one non-Copilot instance's live model
	// listing. The launch check, the hub's prefetch and model picker, a Codex
	// roster retry at launch, and a model switch all use this budget for
	// those routes. Two stalled endpoints then cost 16s of the hub's 30s
	// launch-check child budget, so the healthy providers after them still
	// answer.
	LiveModelListTimeout = 8 * time.Second

	// CopilotLiveModelListTimeout bounds one Copilot listing. It contains the
	// session exchange a listing may need before it fetches (the auth service
	// bounds one exchange at 10s) with headroom, so a slow exchange does not
	// kill a still-valid stored session's fallback.
	CopilotLiveModelListTimeout = 15 * time.Second
)

// LiveModelListTimeoutFor returns the live model-listing budget for one
// instance's route. A Copilot listing may resolve the stored session before it
// fetches, so that route gets CopilotLiveModelListTimeout; every other route
// keeps LiveModelListTimeout. A nil registry, an unknown instance, or a route
// whose transport cannot be resolved keeps the short budget: nothing proves it
// needs the Copilot exchange. An earlier caller deadline still cancels the
// listing through the context, and a session past its actual expiry is still
// renewed.
func LiveModelListTimeoutFor(reg *registry.Registry, instance string) time.Duration {
	if reg != nil {
		if res, err := reg.ResolveInstanceTransport(instance); err == nil &&
			res.Transport.Auth == registry.AuthOAuthGitHubCopilot {
			return CopilotLiveModelListTimeout
		}
	}
	return LiveModelListTimeout
}

type modelListingTimeoutKey struct{}

// WithModelListingTimeout configures model-listing HTTP timeouts without changing
// the Protocol or LiveModelLister interfaces. An explicit zero policy disables
// adapter timeouts; caller context deadlines and HTTP client policy still apply.
// Override listers should pass ModelListingTimeout(ctx) to their HTTP requests.
func WithModelListingTimeout(ctx context.Context, timeout AdapterTimeout) context.Context {
	return context.WithValue(ctx, modelListingTimeoutKey{}, timeout)
}

// ModelListingTimeout returns the listing policy, or DefaultAdapterTimeout when
// none was supplied. The default bounds header/response-byte idle, not total time.
func ModelListingTimeout(ctx context.Context) *AdapterTimeout {
	timeout, ok := ctx.Value(modelListingTimeoutKey{}).(AdapterTimeout)
	if !ok {
		timeout = DefaultAdapterTimeout()
	}
	return &timeout
}

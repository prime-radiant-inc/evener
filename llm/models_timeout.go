package llm

import (
	"context"
	"time"
)

// LiveModelListTimeout bounds one instance's live model listing on every
// provider route. The launch check, the hub's prefetch and model picker, a
// Codex roster retry at launch, and a model switch all use this budget. Two
// stalled endpoints then cost 16s of the hub's 30s launch-check child budget,
// so the healthy providers after them still answer. A Copilot listing may
// resolve its stored session before it fetches, but the auth service bounds
// that opportunistic exchange itself, so the route needs no longer budget.
const LiveModelListTimeout = 8 * time.Second

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

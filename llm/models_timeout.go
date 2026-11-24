package llm

import (
	"context"
	"time"
)

// LiveModelListTimeout bounds one instance's live model listing. The launch
// check, the hub's prefetch and model picker, a Codex roster retry at launch,
// and a model switch all use this one per-instance budget so they cannot
// drift apart. It contains the Copilot session exchange a listing may need
// before it fetches (the auth service bounds one exchange at 10s) with
// headroom, so a slow exchange does not kill a still-valid stored session's
// fallback. An earlier caller deadline still cancels the listing through the
// context, and a session past its actual expiry is still renewed.
const LiveModelListTimeout = 15 * time.Second

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

package llm

import "context"

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

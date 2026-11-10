package llm

import "context"

type requestSentObserverKey struct{}

// WithRequestSentObserver attaches observe to ctx. Client.Complete and
// Client.Stream call it each time they hand a request to the provider adapter
// or protocol: after validation, middleware and local token admission have all
// let it through, and while ctx is still live. A request refused or stopped
// before that point never calls it. It may run more than once per context
// (retries, fallbacks) and from whichever goroutine dispatches.
func WithRequestSentObserver(ctx context.Context, observe func()) context.Context {
	return context.WithValue(ctx, requestSentObserverKey{}, observe)
}

// observeRequestSent reports a request leaving for the provider to the
// observer attached to ctx, if any.
func observeRequestSent(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if observe, ok := ctx.Value(requestSentObserverKey{}).(func()); ok {
		observe()
	}
}

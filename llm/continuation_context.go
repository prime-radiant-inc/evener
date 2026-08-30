package llm

import "context"

type continuationHasherKey struct{}

// ContextWithContinuationHasher attaches the client's continuation hasher so
// a protocol can stamp resp.Raw["id_hash"] (spec §7.6) without holding
// per-client state; the protocols are process singletons.
func ContextWithContinuationHasher(ctx context.Context, h *ContinuationHasher) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, continuationHasherKey{}, h)
}

// ContinuationHasherFromContext returns the hasher attached by
// ContextWithContinuationHasher, or nil.
func ContinuationHasherFromContext(ctx context.Context) *ContinuationHasher {
	h, _ := ctx.Value(continuationHasherKey{}).(*ContinuationHasher)
	return h
}

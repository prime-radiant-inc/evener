package llm

import "context"

// CompleteFunc performs a non-streaming completion for req and returns the Response.
type CompleteFunc func(ctx context.Context, req Request) (Response, error)

// StreamFunc performs a streaming completion for req and returns the Stream.
type StreamFunc func(ctx context.Context, req Request) (Stream, error)

// Middleware wraps provider calls for cross-cutting concerns. Middleware is applied in
// registration order for the request phase and in reverse order for the response/event phase.
type Middleware interface {
	WrapComplete(next CompleteFunc) CompleteFunc
	WrapStream(next StreamFunc) StreamFunc
}

// MiddlewareFunc adapts ordinary functions to the Middleware interface. A nil Complete
// or Stream field means that phase is passed through to next unchanged.
type MiddlewareFunc struct {
	Complete func(ctx context.Context, req Request, next CompleteFunc) (Response, error)
	Stream   func(ctx context.Context, req Request, next StreamFunc) (Stream, error)
}

// WrapComplete returns next unchanged when Complete is nil; otherwise it returns a
// CompleteFunc that invokes Complete with next.
func (m MiddlewareFunc) WrapComplete(next CompleteFunc) CompleteFunc {
	if m.Complete == nil {
		return next
	}
	return func(ctx context.Context, req Request) (Response, error) {
		return m.Complete(ctx, req, next)
	}
}

// WrapStream returns next unchanged when Stream is nil; otherwise it returns a
// StreamFunc that invokes Stream with next.
func (m MiddlewareFunc) WrapStream(next StreamFunc) StreamFunc {
	if m.Stream == nil {
		return next
	}
	return func(ctx context.Context, req Request) (Stream, error) {
		return m.Stream(ctx, req, next)
	}
}

func applyMiddlewareComplete(base CompleteFunc, mw []Middleware) CompleteFunc {
	h := base
	for i := len(mw) - 1; i >= 0; i-- {
		if mw[i] == nil {
			continue
		}
		h = mw[i].WrapComplete(h)
	}
	return h
}

func applyMiddlewareStream(base StreamFunc, mw []Middleware) StreamFunc {
	h := base
	for i := len(mw) - 1; i >= 0; i-- {
		if mw[i] == nil {
			continue
		}
		h = mw[i].WrapStream(h)
	}
	return h
}

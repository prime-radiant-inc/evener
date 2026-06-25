package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/serf/appwire"
)

type HandlerFunc func(context.Context, json.RawMessage) (any, error)

type Router struct {
	handlers map[string]HandlerFunc
}

func NewRouter() *Router {
	return &Router{handlers: map[string]HandlerFunc{}}
}

func (r *Router) Handle(method string, fn HandlerFunc) {
	r.handlers[method] = fn
}

// Methods returns the registered method names, unordered. Used by the appwire
// catalog cross-check tests to verify the generated protocol doc matches what
// is actually wired.
func (r *Router) Methods() []string {
	out := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		out = append(out, m)
	}
	return out
}

func HandleTyped[P any, R any](r *Router, method string, fn func(context.Context, P) (R, error)) {
	r.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
		}
		return fn(ctx, params)
	})
}

func (r *Router) Dispatch(ctx context.Context, req appwire.Request) (any, error) {
	fn, ok := r.handlers[req.Method]
	if !ok {
		return nil, appwire.MethodNotFound(req.Method)
	}
	out, err := fn(ctx, req.Params)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return appwire.EmptyResponse{}, nil
	}
	return out, nil
}

func WireError(err error) appwire.WireError {
	var wire appwire.WireError
	if errors.As(err, &wire) {
		return wire
	}
	return appwire.InternalError(fmt.Sprint(err))
}

package llm

import (
	"context"
	"errors"
	"testing"
)

func TestMiddlewareFunc_NilPhasesPassThrough(t *testing.T) {
	ctx := context.Background()

	baseResp := Response{Model: "base"}
	baseComplete := CompleteFunc(func(ctx context.Context, req Request) (Response, error) {
		return baseResp, nil
	})
	errStream := errors.New("stream-base")
	baseStream := StreamFunc(func(ctx context.Context, req Request) (Stream, error) {
		return nil, errStream
	})

	// A zero MiddlewareFunc has nil Complete and Stream: both phases pass through.
	m := MiddlewareFunc{}

	resp, err := m.WrapComplete(baseComplete)(ctx, Request{})
	if err != nil || resp.Model != "base" {
		t.Fatalf("WrapComplete passthrough = (%+v, %v), want base", resp, err)
	}

	_, err = m.WrapStream(baseStream)(ctx, Request{})
	if !errors.Is(err, errStream) {
		t.Fatalf("WrapStream passthrough err = %v, want %v", err, errStream)
	}
}

func TestApplyMiddleware_SkipsNilEntries(t *testing.T) {
	ctx := context.Background()

	completeCalled := false
	streamCalled := false

	base := CompleteFunc(func(ctx context.Context, req Request) (Response, error) {
		return Response{Model: "ok"}, nil
	})
	sbase := StreamFunc(func(ctx context.Context, req Request) (Stream, error) {
		return nil, errors.New("no stream")
	})

	mw := []Middleware{
		nil, // must be skipped without panicking
		MiddlewareFunc{
			Complete: func(ctx context.Context, req Request, next CompleteFunc) (Response, error) {
				completeCalled = true
				return next(ctx, req)
			},
			Stream: func(ctx context.Context, req Request, next StreamFunc) (Stream, error) {
				streamCalled = true
				return next(ctx, req)
			},
		},
	}

	h := applyMiddlewareComplete(base, mw)
	resp, err := h(ctx, Request{})
	if err != nil || resp.Model != "ok" {
		t.Fatalf("complete chain = (%+v, %v)", resp, err)
	}
	if !completeCalled {
		t.Fatal("complete middleware was not invoked")
	}

	s := applyMiddlewareStream(sbase, mw)
	if _, err := s(ctx, Request{}); err == nil {
		t.Fatal("stream chain err = nil, want base error")
	}
	if !streamCalled {
		t.Fatal("stream middleware was not invoked")
	}
}

func TestCloneProviderOptions_DeepIndependence(t *testing.T) {
	if got := cloneProviderOptions(nil); got != nil {
		t.Fatalf("cloneProviderOptions(nil) = %v, want nil", got)
	}

	in := map[string]any{
		"nested":   map[string]any{"inner": "v"},
		"strmap":   map[string]string{"a": "b"},
		"anyslice": []any{"x", map[string]any{"deep": "y"}},
		"strslice": []string{"one", "two"},
		"scalar":   42,
	}

	out := cloneProviderOptions(in)

	// Mutate the clone's nested containers; the source must stay intact.
	out["nested"].(map[string]any)["inner"] = "MUTATED"
	out["strmap"].(map[string]string)["a"] = "MUTATED"
	out["anyslice"].([]any)[0] = "MUTATED"
	out["anyslice"].([]any)[1].(map[string]any)["deep"] = "MUTATED"
	out["strslice"].([]string)[0] = "MUTATED"

	if in["nested"].(map[string]any)["inner"] != "v" {
		t.Error("nested map aliased between source and clone")
	}
	if in["strmap"].(map[string]string)["a"] != "b" {
		t.Error("string map aliased between source and clone")
	}
	if in["anyslice"].([]any)[0] != "x" {
		t.Error("any slice aliased between source and clone")
	}
	if in["anyslice"].([]any)[1].(map[string]any)["deep"] != "y" {
		t.Error("nested map inside slice aliased between source and clone")
	}
	if in["strslice"].([]string)[0] != "one" {
		t.Error("string slice aliased between source and clone")
	}
	if out["scalar"] != 42 {
		t.Errorf("scalar clone = %v, want 42", out["scalar"])
	}
}

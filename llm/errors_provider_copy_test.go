package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// providerOf reads an error's provider attribution the way callers do.
func providerOf(err error) string {
	if e, ok := errors.AsType[Error](err); ok {
		return e.Provider()
	}
	return ""
}

// sharedErrAdapter is a scripted adapter that serves the SAME error instance
// to every caller, which is what a recorded script or a cached failure does.
type sharedErrAdapter struct {
	name string
	err  error
}

func (a *sharedErrAdapter) Name() string { return a.name }
func (a *sharedErrAdapter) Complete(context.Context, Request) (Response, error) {
	return Response{}, a.err
}
func (a *sharedErrAdapter) Stream(context.Context, Request) (Stream, error) {
	return nil, a.err
}

// Two dispatches on different instances must be able to restamp one shared
// error concurrently. The session's own turn and its namer goroutine do
// exactly this against a scripted adapter, and restamping in place raced:
// one Complete read the provider while the other wrote it.
func TestRewriteErrorProviderIsSafeForConcurrentDispatch(t *testing.T) {
	shared := ErrorFromHTTPStatus("scripted", 429, "slow down", nil, nil)
	c := NewClient()
	c.Register(&sharedErrAdapter{name: "one", err: shared})
	c.Register(&sharedErrAdapter{name: "two", err: shared})

	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		wg.Go(func() {
			for range 50 {
				_, err := c.Complete(context.Background(), Request{
					Provider: name, Model: "m", Messages: []Message{User("hi")},
				})
				if err == nil {
					t.Errorf("%s: want the scripted error", name)
					return
				}
				if got := providerOf(err); got != name {
					t.Errorf("provider = %q, want %q on the returned error", got, name)
					return
				}
			}
		})
	}
	wg.Wait()

	// The instance every caller shares is untouched by any of them.
	if got := providerOf(shared); got != "scripted" {
		t.Fatalf("shared error provider = %q, want scripted: rewriting mutated the caller's value", got)
	}
}

// The restamp is a copy: the returned error carries the new provider in
// Provider() and in its message, the original keeps its own, and everything
// else about the error survives — concrete type for errors.As, the Unwrap
// chain for errors.Is, kind, retryability, hint, and retry-after.
func TestRewriteErrorProviderCopiesInsteadOfMutating(t *testing.T) {
	after := 3 * time.Second
	orig := ErrorFromHTTPStatus("openai", 429, "rate limited", map[string]any{
		"error": map[string]any{"code": "rate_limit_exceeded"},
	}, &after)

	got := RewriteErrorProvider(orig, "work-gw")
	//nolint:errorlint // identity is the assertion: the rewrite must not hand back its input
	if got == orig {
		t.Fatal("RewriteErrorProvider returned the same instance; it must copy")
	}
	if p := providerOf(got); p != "work-gw" {
		t.Fatalf("returned provider = %q, want work-gw", p)
	}
	if p := providerOf(orig); p != "openai" {
		t.Fatalf("original provider = %q, want openai (untouched)", p)
	}
	if !strings.HasPrefix(got.Error(), "work-gw error") {
		t.Fatalf("returned message = %q, want the new provider rendered", got.Error())
	}
	if !strings.HasPrefix(orig.Error(), "openai error") {
		t.Fatalf("original message = %q, want the old provider rendered", orig.Error())
	}

	// Everything else is preserved.
	var rl *rateLimitError
	if !errors.As(got, &rl) {
		t.Fatalf("errors.As lost the concrete type: %T", got)
	}
	if rl.Provider() != "work-gw" {
		t.Fatalf("errors.As found %q, want the restamped provider", rl.Provider())
	}
	if Kind(got) != Kind(orig) || Classify(got) != Classify(orig) {
		t.Fatalf("kind/classify drifted: %v/%v vs %v/%v", Kind(got), Classify(got), Kind(orig), Classify(orig))
	}
	var gotE, origE Error
	if !errors.As(got, &gotE) || !errors.As(orig, &origE) {
		t.Fatal("the copy is no longer an llm.Error")
	}
	if gotE.Retryable() != origE.Retryable() || !gotE.Retryable() {
		t.Fatalf("retryable drifted: %v vs %v", gotE.Retryable(), origE.Retryable())
	}
	if gotE.StatusCode() != origE.StatusCode() || gotE.ErrorCode() != origE.ErrorCode() {
		t.Fatalf("status/code drifted: %d/%q vs %d/%q", gotE.StatusCode(), gotE.ErrorCode(), origE.StatusCode(), origE.ErrorCode())
	}
	if ErrorHint(got) != ErrorHint(orig) {
		t.Fatalf("hint drifted: %q vs %q", ErrorHint(got), ErrorHint(orig))
	}
	if gotE.RetryAfter() == nil || *gotE.RetryAfter() != after {
		t.Fatalf("retry-after lost: %+v", gotE.RetryAfter())
	}
	if gotE.ErrorCode() != "rate_limit_exceeded" {
		t.Fatalf("error code lost: %q", gotE.ErrorCode())
	}
}

// The Unwrap chain survives the copy, so errors.Is still reaches the cause.
func TestRewriteErrorProviderKeepsTheUnwrapChain(t *testing.T) {
	cause := errors.New("underlying")
	orig := NewStreamError("openai", "stream died", cause)
	got := RewriteErrorProvider(orig, "work-gw")
	if !errors.Is(got, cause) {
		t.Fatal("errors.Is lost the wrapped cause")
	}
	if !errors.Is(orig, cause) {
		t.Fatal("the original lost its cause")
	}
	// The copy unwraps to the error it was made from, so a caller holding
	// that one still recognizes the re-attributed error as the same failure.
	if !errors.Is(got, orig) {
		t.Fatal("errors.Is(copy, original) must hold: callers compare against the error they scripted")
	}
	var se *StreamError
	if !errors.As(got, &se) || se.Provider() != "work-gw" {
		t.Fatalf("errors.As(*StreamError) = %v, provider %q", errors.As(got, &se), se.Provider())
	}
	if providerOf(orig) != "openai" {
		t.Fatalf("original provider = %q, want openai", providerOf(orig))
	}
}

// An error that never had a provider keeps having none: restamping would turn
// "context canceled" into "<instance> error: context canceled".
func TestRewriteErrorProviderLeavesBlankProviderAlone(t *testing.T) {
	abort := NewAbortError("context canceled", context.Canceled)
	//nolint:errorlint // identity is the assertion: a blank-provider error is returned untouched
	if got := RewriteErrorProvider(abort, "work-gw"); got != abort {
		t.Fatalf("blank-provider error was rewritten: %v", got)
	}
	if got := providerOf(abort); got != "" {
		t.Fatalf("provider = %q, want empty", got)
	}
	if RewriteErrorProvider(nil, "work-gw") != nil {
		t.Fatal("nil stays nil")
	}
	// A plain error the package does not own is returned as-is.
	plain := errors.New("boom")
	//nolint:errorlint // identity is the assertion: an error this package does not own is untouched
	if got := RewriteErrorProvider(plain, "work-gw"); got != plain {
		t.Fatalf("plain error was rewritten: %v", got)
	}
}

// Every error type this package attributes to a provider must be copyable,
// or RewriteErrorProvider silently skips it and the attribution is lost.
func TestEveryProviderAttributedErrorCopies(t *testing.T) {
	after := time.Second
	raw := map[string]any{"error": map[string]any{"code": "c"}}
	cases := []error{}
	for _, status := range []int{400, 401, 403, 404, 408, 413, 429, 500, 599} {
		cases = append(cases, ErrorFromHTTPStatus("p", status, "m", raw, &after))
	}
	cases = append(cases,
		ErrorFromHTTPStatus("p", 400, "content policy violation", raw, nil),
		ErrorFromHTTPStatus("p", 400, "context length exceeded", raw, nil),
		ErrorFromHTTPStatus("p", 429, "quota exceeded", raw, nil),
		NewStreamError("p", "m", nil),
		NewUnsupportedEndpointError("p", "m", nil),
		NewUnsupportedToolChoiceError("p", "auto"),
		NewRequestTimeoutError("p", "m", nil),
	)
	for _, err := range cases {
		if providerOf(err) == "" {
			t.Fatalf("fixture %T has no provider to rewrite", err)
		}
		got := RewriteErrorProvider(err, "other")
		//nolint:errorlint // identity is the assertion: every attributed type must copy
		if got == err {
			t.Errorf("%T was not copied: RewriteErrorProvider returned the same instance", err)
			continue
		}
		if providerOf(got) != "other" || providerOf(err) != "p" {
			t.Errorf("%T: got %q / original %q, want other / p", err, providerOf(got), providerOf(err))
		}
	}
}

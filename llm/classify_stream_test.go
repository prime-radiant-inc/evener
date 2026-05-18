package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestRetryChainShortCircuitsOnPermanent verifies that a permanent provider
// error (e.g. 403 access denied) burns exactly one attempt of the retry
// budget rather than draining the full MaxRetries.
func TestRetryChainShortCircuitsOnPermanent(t *testing.T) {
	c := NewClient()
	err403 := ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil)
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			// Repeat the 403 response far more times than MaxRetries so any
			// extra retry attempts are observable as extra scripted calls.
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err403 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err403 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err403 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err403 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err403 },
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var sleepCalls int
	var sleepMu sync.Mutex
	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
		RetryPolicy: &RetryPolicy{
			MaxRetries:        4,
			BaseDelay:         1 * time.Millisecond,
			MaxDelay:          10 * time.Millisecond,
			BackoffMultiplier: 2.0,
			Jitter:            false,
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			_ = ctx
			_ = d
			sleepMu.Lock()
			sleepCalls++
			sleepMu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	// Drain events; we expect an ERROR event surfacing the 403.
	for range res.Events() {
	}
	_, rerr := res.Response()
	if rerr == nil {
		t.Fatalf("expected Response() error")
	}
	var le Error
	if !errors.As(rerr, &le) || le.StatusCode() != 403 {
		t.Fatalf("expected 403 llm.Error, got %T (%v)", rerr, rerr)
	}

	if got := len(a.Requests()); got != 1 {
		t.Fatalf("expected exactly 1 stream attempt (permanent error short-circuits); got %d", got)
	}
	sleepMu.Lock()
	defer sleepMu.Unlock()
	if sleepCalls != 0 {
		t.Fatalf("expected 0 backoff sleeps on permanent error; got %d", sleepCalls)
	}
}

// TestRetryChainExhaustsOnRetryable verifies that a retryable provider error
// (e.g. 429 rate-limit) burns the full MaxRetries budget before surfacing.
func TestRetryChainExhaustsOnRetryable(t *testing.T) {
	c := NewClient()
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err429 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err429 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err429 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err429 },
			func(ctx context.Context, req Request) (Stream, error) { _ = ctx; _ = req; return nil, err429 },
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var sleepCalls int
	var sleepMu sync.Mutex
	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
		RetryPolicy: &RetryPolicy{
			MaxRetries:        3, // 1 initial + 3 retries = 4 attempts
			BaseDelay:         1 * time.Millisecond,
			MaxDelay:          10 * time.Millisecond,
			BackoffMultiplier: 2.0,
			Jitter:            false,
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			_ = ctx
			_ = d
			sleepMu.Lock()
			sleepCalls++
			sleepMu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	for range res.Events() {
	}
	_, rerr := res.Response()
	if rerr == nil {
		t.Fatalf("expected Response() error")
	}
	var le Error
	if !errors.As(rerr, &le) || le.StatusCode() != 429 {
		t.Fatalf("expected 429 llm.Error, got %T (%v)", rerr, rerr)
	}

	if got := len(a.Requests()); got != 4 {
		t.Fatalf("expected 4 stream attempts (initial + 3 retries); got %d", got)
	}
	sleepMu.Lock()
	defer sleepMu.Unlock()
	if sleepCalls != 3 {
		t.Fatalf("expected 3 backoff sleeps before giving up; got %d", sleepCalls)
	}
}

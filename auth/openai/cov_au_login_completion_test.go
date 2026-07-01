package openai

import (
	"context"
	"errors"
	"testing"
)

// sendResult returns a buffered channel already carrying r, so
// waitForLoginCompletion can receive it without a racing producer.
func sendResult(r callbackResult) chan callbackResult {
	ch := make(chan callbackResult, 1)
	ch <- r
	return ch
}

func TestWaitForLoginCompletionCallbackSuccess(t *testing.T) {
	callback := sendResult(callbackResult{result: CallbackResult{Code: "cb", State: "s"}})
	manual := make(chan callbackResult, 1)

	got, err := waitForLoginCompletion(context.Background(), callback, manual)
	if err != nil {
		t.Fatalf("waitForLoginCompletion() error = %v", err)
	}
	if got.Code != "cb" {
		t.Fatalf("Code = %q, want cb", got.Code)
	}
}

func TestWaitForLoginCompletionManualSuccess(t *testing.T) {
	callback := make(chan callbackResult, 1)
	manual := sendResult(callbackResult{result: CallbackResult{Code: "manual", State: "s"}})

	got, err := waitForLoginCompletion(context.Background(), callback, manual)
	if err != nil {
		t.Fatalf("waitForLoginCompletion() error = %v", err)
	}
	if got.Code != "manual" {
		t.Fatalf("Code = %q, want manual", got.Code)
	}
}

// TestWaitForLoginCompletionCallbackTransientThenManual verifies that a
// callback channel yielding a benign context error (Canceled/DeadlineExceeded)
// is retired without failing the flow, letting the manual paste win.
func TestWaitForLoginCompletionCallbackTransientThenManual(t *testing.T) {
	for _, benign := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(benign.Error(), func(t *testing.T) {
			callback := sendResult(callbackResult{err: benign})
			manual := sendResult(callbackResult{result: CallbackResult{Code: "manual"}})

			got, err := waitForLoginCompletion(context.Background(), callback, manual)
			if err != nil {
				t.Fatalf("waitForLoginCompletion() error = %v", err)
			}
			if got.Code != "manual" {
				t.Fatalf("Code = %q, want manual after benign callback error", got.Code)
			}
		})
	}
}

func TestWaitForLoginCompletionManualTransientThenCallback(t *testing.T) {
	for _, benign := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(benign.Error(), func(t *testing.T) {
			callback := sendResult(callbackResult{result: CallbackResult{Code: "cb"}})
			manual := sendResult(callbackResult{err: benign})

			got, err := waitForLoginCompletion(context.Background(), callback, manual)
			if err != nil {
				t.Fatalf("waitForLoginCompletion() error = %v", err)
			}
			if got.Code != "cb" {
				t.Fatalf("Code = %q, want cb after benign manual error", got.Code)
			}
		})
	}
}

func TestWaitForLoginCompletionCallbackFatalError(t *testing.T) {
	fatal := errors.New("serve OpenAI callback: boom")
	callback := sendResult(callbackResult{err: fatal})
	manual := make(chan callbackResult, 1)

	if _, err := waitForLoginCompletion(context.Background(), callback, manual); !errors.Is(err, fatal) {
		t.Fatalf("waitForLoginCompletion() error = %v, want %v", err, fatal)
	}
}

func TestWaitForLoginCompletionManualFatalError(t *testing.T) {
	fatal := errors.New("state mismatch")
	callback := make(chan callbackResult, 1)
	manual := sendResult(callbackResult{err: fatal})

	if _, err := waitForLoginCompletion(context.Background(), callback, manual); !errors.Is(err, fatal) {
		t.Fatalf("waitForLoginCompletion() error = %v, want %v", err, fatal)
	}
}

func TestWaitForLoginCompletionContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Both channels stay empty so the ctx.Done() arm is the only ready case.
	callback := make(chan callbackResult, 1)
	manual := make(chan callbackResult, 1)

	if _, err := waitForLoginCompletion(ctx, callback, manual); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForLoginCompletion() error = %v, want context.Canceled", err)
	}
}

// TestWaitForLoginCompletionBothChannelsExhausted covers the loop-exit arm:
// once both sources have reported benign context errors there is nothing left
// to wait on, and the function reports the context's error.
func TestWaitForLoginCompletionBothChannelsExhausted(t *testing.T) {
	callback := sendResult(callbackResult{err: context.Canceled})
	manual := sendResult(callbackResult{err: context.DeadlineExceeded})

	_, err := waitForLoginCompletion(context.Background(), callback, manual)
	if err == nil {
		t.Fatal("waitForLoginCompletion() error = nil, want deadline exceeded after both retired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForLoginCompletion() error = %v, want context.DeadlineExceeded", err)
	}
}

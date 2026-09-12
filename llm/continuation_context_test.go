package llm

import (
	"context"
	"errors"
	"testing"
)

func TestContinuationHasherContextRoundTrip(t *testing.T) {
	if got := ContinuationHasherFromContext(context.Background()); got != nil {
		t.Fatalf("a bare context carries no hasher: %v", got)
	}
	h := NewContinuationHasher([]byte("task-2-secret"))
	ctx := ContextWithContinuationHasher(context.Background(), h)
	if got := ContinuationHasherFromContext(ctx); got != h {
		t.Fatalf("hasher = %p, want %p", got, h)
	}
	if got := ContextWithContinuationHasher(ctx, nil); got != ctx {
		t.Fatal("attaching a nil hasher must leave the context alone")
	}
}

func TestClientContinuationHasherRequiresStateDir(t *testing.T) {
	bare := NewClient()
	if _, err := bare.ContinuationHasher(); !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("a client with no state directory: %v", err)
	}
	if got := ContinuationHasherFromContext(bare.withHasher(context.Background())); got != nil {
		t.Fatalf("such a client attaches no hasher to a dispatch: %v", got)
	}

	c := NewClient(WithClientStateDir(t.TempDir()))
	h, err := c.ContinuationHasher()
	if err != nil || h == nil {
		t.Fatalf("ContinuationHasher = %v, %v", h, err)
	}
	again, err := c.ContinuationHasher()
	if err != nil || again != h {
		t.Fatalf("the secret is loaded once per client: %p vs %p, %v", again, h, err)
	}
	if got := ContinuationHasherFromContext(c.withHasher(context.Background())); got != h {
		t.Fatalf("the client attaches its own hasher to a dispatch: %p, want %p", got, h)
	}
}

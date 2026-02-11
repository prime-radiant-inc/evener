package llm

import (
	"context"
	"testing"
	"time"
)

func TestApplyAdapterTimeout_Request(t *testing.T) {
	timeout := &AdapterTimeout{
		Connect:    1 * time.Second,
		Request:    5 * time.Second,
		StreamRead: 2 * time.Second,
	}
	ctx := context.Background()
	ctx, cancel := ApplyAdapterTimeout(ctx, timeout, false)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on context")
	}
	remaining := time.Until(deadline)
	if remaining < 4*time.Second || remaining > 6*time.Second {
		t.Errorf("expected ~5s remaining, got %v", remaining)
	}
}

func TestApplyAdapterTimeout_Nil(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := ApplyAdapterTimeout(ctx, nil, false)
	defer cancel()

	_, ok := ctx.Deadline()
	if ok {
		t.Error("expected no deadline for nil timeout")
	}
}

func TestApplyAdapterTimeout_Streaming(t *testing.T) {
	timeout := &AdapterTimeout{
		Request:    5 * time.Second,
		StreamRead: 2 * time.Second,
	}
	ctx := context.Background()
	ctx, cancel := ApplyAdapterTimeout(ctx, timeout, true)
	defer cancel()

	_, ok := ctx.Deadline()
	if ok {
		t.Error("expected no deadline for streaming (stream_read is per-event)")
	}
}

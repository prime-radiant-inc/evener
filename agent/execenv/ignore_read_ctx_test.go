package execenv

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestReadIgnoreFileBytesObservesCancellation pins the ctx entry check the
// ignore read gains by routing through readBounded: a cancelled ctx must answer
// with the cancellation, not read the stream (the bespoke wiring armed only the
// deadline, so a cancelled read surfaced as a stall).
func TestReadIgnoreFileBytesObservesCancellation(t *testing.T) {
	t.Parallel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &grepCtxFile{File: pr, ctx: ctx}
	if _, err := readIgnoreFileBytes(f, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("readIgnoreFileBytes(cancelled) = %v, want context.Canceled", err)
	}
}

package apptranscript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestTurnCacheLatestItemWindowFromFileHonorsContextDuringUncachedIndexScan(t *testing.T) {
	path := writeCancellationTranscript(t, 512)
	ctx := &countingCancelContext{threshold: 32, done: make(chan struct{})}

	_, _, err := NewTurnCache().LatestItemWindowFromFileContext(ctx, path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:scan",
		Limit:     1,
	}, boundedTestProjector)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("latest item window error = %v, want context.Canceled", err)
	}
	if ctx.calls < ctx.threshold {
		t.Fatalf("context checks = %d, want at least %d during scan", ctx.calls, ctx.threshold)
	}
}

func TestTurnCachePageFromFileHonorsContextDuringSelectedRangeProjection(t *testing.T) {
	path := writeCancellationTranscript(t, 256)
	cache := NewTurnCache()
	if _, _, err := cache.LatestItemWindowFromFileContext(context.Background(), path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:projection",
		Limit:     1,
	}, boundedTestProjector); err != nil {
		t.Fatalf("prime item index: %v", err)
	}

	ctx := &countingCancelContext{threshold: 8, done: make(chan struct{})}
	_, err := cache.PageFromFileContext(ctx, path, testMaxLineBytes, "128", 64, boundedTestProjector)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("selected-range page error = %v, want context.Canceled", err)
	}
	if ctx.calls < ctx.threshold {
		t.Fatalf("context checks = %d, want at least %d during projection", ctx.calls, ctx.threshold)
	}
}

type countingCancelContext struct {
	threshold int
	calls     int
	done      chan struct{}
}

func (c *countingCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *countingCancelContext) Done() <-chan struct{} {
	if c.done == nil {
		c.done = make(chan struct{})
	}
	return c.done
}

func (c *countingCancelContext) Err() error {
	c.calls++
	if c.threshold > 0 && c.calls >= c.threshold {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
		return context.Canceled
	}
	return nil
}

func (c *countingCancelContext) Value(key any) any { return nil }

func writeCancellationTranscript(t testing.TB, count int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("cancellation-%d.transcript.jsonl", count))
	data := transcriptHeaderLine(t)
	for i := 1; i <= count; i++ {
		data = append(data, marshalEntryLine(t, transcript.Entry{
			Kind: "entry",
			Seq:  i,
			Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("entry-%d", i)), Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC()},
		})...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

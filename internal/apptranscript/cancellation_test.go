package apptranscript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	_, err := pageFromFileForTestContext(ctx, cache, path, testMaxLineBytes, "128", 64, boundedTestProjector)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("selected-range page error = %v, want context.Canceled", err)
	}
	if ctx.calls < ctx.threshold {
		t.Fatalf("context checks = %d, want at least %d during projection", ctx.calls, ctx.threshold)
	}
}

func TestTurnCacheCanceledWarmValidationPreservesPublishedState(t *testing.T) {
	t.Run("append_prefix", func(t *testing.T) {
		path := writeCancellationTranscript(t, 512)
		cache := NewTurnCache()
		options := ItemWindowOptions{ThreadRef: "local:warm-prefix", Limit: 1}
		_, beforeIdentity, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, boundedTestProjector)
		if err != nil {
			t.Fatalf("prime item index: %v", err)
		}
		before := cache.entries[path]
		beforeOrder := append([]string(nil), cache.order...)
		beforeSidecar, err := os.ReadFile(path + ".appwire-index.json")
		if err != nil {
			t.Fatalf("read sidecar: %v", err)
		}
		appendFile(t, path, []byte("{not-json}\n"))

		ctx := &countingCancelContext{threshold: 8, done: make(chan struct{})}
		_, _, err = cache.LatestItemWindowFromFileContext(ctx, path, testMaxLineBytes, options, boundedTestProjector)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("warm append validation error = %v, want context.Canceled", err)
		}
		if ctx.calls < ctx.threshold {
			t.Fatalf("context checks = %d, want cancellation during prefix validation", ctx.calls)
		}
		after := cache.entries[path]
		if after.turnIndex == nil || !reflect.DeepEqual(before.turnIndex, after.turnIndex) || !reflect.DeepEqual(before.toolResolver, after.toolResolver) {
			t.Error("canceled warm append changed published index or resolver")
		}
		if !reflect.DeepEqual(beforeOrder, cache.order) {
			t.Error("canceled warm append changed cache LRU")
		}
		afterSidecar, err := os.ReadFile(path + ".appwire-index.json")
		if err != nil || !reflect.DeepEqual(beforeSidecar, afterSidecar) {
			t.Errorf("canceled warm append changed sidecar: %v", err)
		}
		if cache.entries[path].turnIndex.Incarnation != beforeIdentity.Incarnation {
			t.Fatalf("cancellation rotated incarnation: before=%q after=%q", beforeIdentity.Incarnation, cache.entries[path].turnIndex.Incarnation)
		}
	})

	t.Run("record_validation", func(t *testing.T) {
		path := writeCancellationTranscript(t, 512)
		prime := NewTurnCache()
		if _, _, err := prime.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:record", Limit: 1}, boundedTestProjector); err != nil {
			t.Fatalf("prime item index: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		index, err := readTurnIndexWithJournal(path+".appwire-index.json", info.Size())
		if err != nil {
			t.Fatalf("read index: %v", err)
		}
		if len(index.Records) == 0 {
			t.Fatal("prime index has no records")
		}
		index.Records[len(index.Records)-1].Index++
		if err := writeTurnIndex(path+".appwire-index.json", index, nil); err != nil {
			t.Fatalf("write invalid index: %v", err)
		}
		beforeSidecar, err := os.ReadFile(path + ".appwire-index.json")
		if err != nil {
			t.Fatalf("read invalid sidecar: %v", err)
		}

		cache := NewTurnCache()
		ctx := &countingCancelContext{threshold: 10, done: make(chan struct{})}
		_, stats, err := cache.loadTurnIndexInternal(ctx, path, testMaxLineBytes, boundedTestProjector, false, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("record validation error = %v, want context.Canceled", err)
		}
		if stats.rebuilt {
			t.Error("canceled record validation fell through to index rebuild")
		}
		if _, ok := cache.entries[path]; ok {
			t.Error("canceled record validation published cache")
		}
		afterSidecar, err := os.ReadFile(path + ".appwire-index.json")
		if err != nil || !reflect.DeepEqual(beforeSidecar, afterSidecar) {
			t.Errorf("canceled record validation changed sidecar: %v", err)
		}
	})

	t.Run("item_count_validation", func(t *testing.T) {
		path := writeCancellationTranscript(t, 512)
		prime := NewTurnCache()
		if _, _, err := prime.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:item-count", Limit: 1}, boundedTestProjector); err != nil {
			t.Fatalf("prime item index: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		index, err := readTurnIndexWithJournal(path+".appwire-index.json", info.Size())
		if err != nil {
			t.Fatalf("read index: %v", err)
		}
		if len(index.Records) == 0 {
			t.Fatal("prime index has no records")
		}
		index.Records[len(index.Records)-1].ItemCount = 0
		index.Records[len(index.Records)-1].Visible = true
		if err := writeTurnIndex(path+".appwire-index.json", index, nil); err != nil {
			t.Fatalf("write invalid item-count index: %v", err)
		}
		beforeSidecar, err := os.ReadFile(path + ".appwire-index.json")
		if err != nil {
			t.Fatalf("read invalid sidecar: %v", err)
		}

		cache := NewTurnCache()
		ctx := &countingCancelContext{threshold: 520, done: make(chan struct{})}
		_, stats, err := cache.loadTurnIndexInternal(ctx, path, testMaxLineBytes, boundedTestProjector, false, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("item-count validation error = %v, want context.Canceled", err)
		}
		if stats.rebuilt {
			t.Error("canceled item-count validation fell through to index rebuild")
		}
		if _, ok := cache.entries[path]; ok {
			t.Error("canceled item-count validation published cache")
		}
		afterSidecar, err := os.ReadFile(path + ".appwire-index.json")
		if err != nil || !reflect.DeepEqual(beforeSidecar, afterSidecar) {
			t.Errorf("canceled item-count validation changed sidecar: %v", err)
		}
	})
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

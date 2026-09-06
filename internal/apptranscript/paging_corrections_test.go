package apptranscript

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestLogicalTurnLimitsUseOneCoordinateSystem(t *testing.T) {
	path := writeEntries(t, userEntry(1, "question"), assistantTextEntry(2, "answer"), userEntry(3, "next"))
	cache := NewTurnCache()
	grouped, err := cache.ItemTurnsFromFile(path, testMaxLineBytes, fullProjector(boundedTestProjector))
	if err != nil {
		t.Fatal(err)
	}
	cached, err := cache.ItemTurnsFromFile(path, testMaxLineBytes, fullProjector(boundedTestProjector))
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped) != 2 || !reflect.DeepEqual(grouped, cached) {
		t.Fatalf("grouped=%d cached=%d, want two logical turns and cache equivalence", len(grouped), len(cached))
	}
	for _, limit := range []int{-1, 0, 40} {
		latest, _, err := latestFromFileForTestContext(t.Context(), cache, path, testMaxLineBytes, limit, boundedTestProjector)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(turnIDs(latest), turnIDs(grouped)) {
			t.Errorf("limit %d latest ids=%v want=%v", limit, turnIDs(latest), turnIDs(grouped))
		}
		page, err := pageFromFileForTestContext(t.Context(), cache, path, testMaxLineBytes, "2", limit, boundedTestProjector)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(turnIDs(page.Turns), turnIDs(grouped)) {
			t.Errorf("limit %d page ids=%v want=%v", limit, turnIDs(page.Turns), turnIDs(grouped))
		}
	}
}

func TestCanceledLogicalReadsPreserveCache(t *testing.T) {
	for _, limit := range []int{0, 1} {
		t.Run(map[int]string{0: "full", 1: "bounded_warm"}[limit], func(t *testing.T) {
			path := writeEntries(t, userEntry(1, "one"))
			other := writeEntries(t, userEntry(1, "other"))
			cache := NewTurnCache()
			if limit > 0 {
				if _, _, err := latestFromFileForTest(cache, path, testMaxLineBytes, 1, boundedTestProjector); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := latestFromFileForTest(cache, other, testMaxLineBytes, 1, boundedTestProjector); err != nil {
				t.Fatal(err)
			}
			beforeOrder := append([]string(nil), cache.order...)
			before := cache.entries[path]
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, _, err := latestFromFileForTestContext(ctx, cache, path, testMaxLineBytes, limit, boundedTestProjector)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled read error=%v", err)
			}
			if _, err := pageFromFileForTestContext(ctx, cache, path, testMaxLineBytes, "1", limit, boundedTestProjector); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled page error=%v", err)
			}
			if !reflect.DeepEqual(before, cache.entries[path]) || !reflect.DeepEqual(beforeOrder, cache.order) {
				t.Error("canceled read changed cache or LRU")
			}
		})
	}
}

func TestCanceledSelectedItemProjectionDoesNotPublish(t *testing.T) {
	path := writeEntries(t, userEntry(1, "selected"))
	cache := NewTurnCache()
	cache.max = 1
	other := writeEntries(t, userEntry(1, "retained"))
	if _, _, err := latestFromFileForTest(cache, other, testMaxLineBytes, 1, boundedTestProjector); err != nil {
		t.Fatal(err)
	}
	before := cache.entries[other]
	beforeOrder := append([]string(nil), cache.order...)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	project := func(turn schema.Turn, id string, i int, names map[string]string) []appwire.ThreadItem {
		calls++
		items := boundedTestProjector(turn, id, i, names)
		if calls == 2 {
			cancel()
		} // one index projection followed by selected projection
		return items
	}
	_, _, err := cache.LatestItemWindowFromFileContext(ctx, path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:thread", Limit: 1}, project)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	if _, ok := cache.entries[path]; ok {
		t.Error("canceled selected projection published cache")
	}
	if !reflect.DeepEqual(beforeOrder, cache.order) || !reflect.DeepEqual(before, cache.entries[other]) {
		t.Error("canceled selected projection changed LRU")
	}
	for _, suffix := range []string{".appwire-index.json", ".appwire-index.json.journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("canceled projection published %s: %v", suffix, err)
		}
	}
}

func TestCanceledGroupedFullProjectionStopsScanning(t *testing.T) {
	path := writeEntries(t, userEntry(1, "one"), userEntry(2, "two"))
	cache := NewTurnCache()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	project := func(turn schema.Turn, id string, i int, names map[string]string) []appwire.ThreadItem {
		calls++
		cancel()
		return boundedTestProjector(turn, id, i, names)
	}
	_, _, err := latestFromFileForTestContext(ctx, cache, path, testMaxLineBytes, 0, project)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error=%v projected=%d want cancellation after first entry", err, calls)
	}
	if len(cache.entries) != 0 || len(cache.order) != 0 {
		t.Fatal("canceled full projection published cache")
	}
}

func TestCanceledSelectedAppendPreservesIndexAndJournal(t *testing.T) {
	path := writeEntries(t, assistantToolCallEntry(1, "call", "communicate", `{}`))
	cache := NewTurnCache()
	armed := false
	calls := 0
	var cancel context.CancelFunc
	project := func(turn schema.Turn, id string, i int, names map[string]string) []appwire.ThreadItem {
		items := boundedTestProjector(turn, id, i, names)
		if armed && i == 3 {
			calls++
			if calls == 2 {
				cancel()
			}
		}
		return items
	}
	options := ItemWindowOptions{ThreadRef: "local:thread", Limit: 1}
	if _, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, project); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, marshalEntryLine(t, userEntry(2, "next")))
	if _, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, project); err != nil {
		t.Fatal(err)
	}
	other := writeEntries(t, userEntry(1, "other"))
	if _, _, err := latestFromFileForTest(cache, other, testMaxLineBytes, 1, boundedTestProjector); err != nil {
		t.Fatal(err)
	}
	before := *cache.entries[path].turnIndex
	resolverBefore := cloneToolNames(cache.entries[path].toolResolver)
	beforeOrder := append([]string(nil), cache.order...)
	persisted := map[string][]byte{}
	for _, suffix := range []string{".appwire-index.json", ".appwire-index.json.journal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		persisted[suffix] = data
	}
	appendFile(t, path, marshalEntryLine(t, toolResultEntry(3, "call", "", "done")))
	ctx, cancelContext := context.WithCancel(t.Context())
	cancel = cancelContext
	defer cancel()
	armed = true
	_, _, err := cache.LatestItemWindowFromFileContext(ctx, path, testMaxLineBytes, options, project)
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("error=%v selected calls=%d", err, calls)
	}
	after := cache.entries[path]
	if !reflect.DeepEqual(before, *after.turnIndex) || !reflect.DeepEqual(resolverBefore, after.toolResolver) || !reflect.DeepEqual(beforeOrder, cache.order) {
		t.Error("canceled selected append changed index, resolver, or LRU")
	}
	for suffix, want := range persisted {
		got, err := os.ReadFile(path + suffix)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("canceled selected append changed %s: %v", suffix, err)
		}
	}
	armed = false
	if _, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, project); err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
}

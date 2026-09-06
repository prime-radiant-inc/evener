package apptranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestTurnCacheReusesParseUntilFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := NewTurnCache()
	parses := 0
	parse := func() ([]appwire.Turn, error) {
		parses++
		return []appwire.Turn{{ID: "turn_1"}}, nil
	}

	cache.loadItemProjection(path, parse)
	cache.loadItemProjection(path, parse)
	cache.loadItemProjection(path, parse)
	if parses != 1 {
		t.Fatalf("parses=%d, want 1 (cache should reuse the unchanged file)", parses)
	}

	// Change the file (size + modtime) → re-parse.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache.loadItemProjection(path, parse)
	cache.loadItemProjection(path, parse)
	if parses != 2 {
		t.Fatalf("parses=%d, want 2 (a changed file must re-parse once)", parses)
	}
}

func TestTurnCacheMissingFileParsesUncached(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "nope.jsonl")
	cache := NewTurnCache()
	parses := 0
	parse := func() ([]appwire.Turn, error) { parses++; return nil, nil }
	cache.loadItemProjection(missingPath, parse)
	cache.loadItemProjection(missingPath, parse)
	if parses != 2 {
		t.Fatalf("parses=%d, want 2 (no stable identity → never cache)", parses)
	}
}

func TestTurnCacheEvictsBeyondBound(t *testing.T) {
	dir := t.TempDir()
	cache := NewTurnCache()
	paths := make([]string, defaultTurnCacheSize+1)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("%02d.jsonl", i))
		paths[i] = p
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cache.loadItemProjection(p, func() ([]appwire.Turn, error) { return nil, nil })
	}
	if len(cache.entries) != defaultTurnCacheSize {
		t.Fatalf("cache holds %d entries, want %d (bounded)", len(cache.entries), defaultTurnCacheSize)
	}
	// LRU policy: the first path was loaded first and must be evicted, while
	// the remaining defaultTurnCacheSize paths survive.
	if _, ok := cache.entries[paths[0]]; ok {
		t.Fatalf("oldest path should have been evicted but is still cached")
	}
	if _, ok := cache.entries[paths[1]]; !ok {
		t.Fatalf("second-oldest path should still be cached but was evicted")
	}
	if _, ok := cache.entries[paths[len(paths)-1]]; !ok {
		t.Fatalf("newest path should still be cached but was evicted")
	}
}

func TestTurnCacheReusesNativeItemProjection(t *testing.T) {
	path := writeEntries(t,
		userEntry(1, "question"),
		transcript.Entry{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")}},
	)
	cache := NewTurnCache()
	first, err := cache.ItemTurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.ItemTurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("native item turns=%d/%d, want one logical turn", len(first), len(second))
	}
}

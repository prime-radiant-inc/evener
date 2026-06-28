package apptranscript

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

func TestTurnCacheReusesParseUntilFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := NewTurnCache()
	parses := 0
	parse := func() []appwire.Turn {
		parses++
		return []appwire.Turn{{ID: "turn_1"}}
	}

	cache.load(path, parse)
	cache.load(path, parse)
	cache.load(path, parse)
	if parses != 1 {
		t.Fatalf("parses=%d, want 1 (cache should reuse the unchanged file)", parses)
	}

	// Change the file (size + modtime) → re-parse.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache.load(path, parse)
	cache.load(path, parse)
	if parses != 2 {
		t.Fatalf("parses=%d, want 2 (a changed file must re-parse once)", parses)
	}
}

func TestTurnCacheMissingFileParsesUncached(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "nope.jsonl")
	cache := NewTurnCache()
	parses := 0
	parse := func() []appwire.Turn { parses++; return nil }
	cache.load(missingPath, parse)
	cache.load(missingPath, parse)
	if parses != 2 {
		t.Fatalf("parses=%d, want 2 (no stable identity → never cache)", parses)
	}
}

func TestTurnCacheEvictsBeyondBound(t *testing.T) {
	dir := t.TempDir()
	cache := NewTurnCache()
	cache.max = 2
	paths := make([]string, 3)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, string(rune('a'+i))+".jsonl")
		paths[i] = p
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cache.load(p, func() []appwire.Turn { return nil })
	}
	if len(cache.entries) != 2 {
		t.Fatalf("cache holds %d entries, want 2 (bounded)", len(cache.entries))
	}
	// LRU policy: a.jsonl was loaded first and must be the evicted entry;
	// b.jsonl and c.jsonl are the two most-recently-used and must survive.
	if _, ok := cache.entries[paths[0]]; ok {
		t.Fatalf("a.jsonl should have been evicted (LRU oldest) but is still cached")
	}
	if _, ok := cache.entries[paths[1]]; !ok {
		t.Fatalf("b.jsonl should still be cached (second-most-recent) but was evicted")
	}
	if _, ok := cache.entries[paths[2]]; !ok {
		t.Fatalf("c.jsonl should still be cached (most recent) but was evicted")
	}
}

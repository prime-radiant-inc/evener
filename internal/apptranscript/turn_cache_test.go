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
	cache := NewTurnCache()
	parses := 0
	parse := func() []appwire.Turn { parses++; return nil }
	cache.load(filepath.Join(t.TempDir(), "nope.jsonl"), parse)
	cache.load(filepath.Join(t.TempDir(), "nope.jsonl"), parse)
	if parses != 2 {
		t.Fatalf("parses=%d, want 2 (no stable identity → never cache)", parses)
	}
}

func TestTurnCacheEvictsBeyondBound(t *testing.T) {
	dir := t.TempDir()
	cache := NewTurnCache()
	cache.max = 2
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, string(rune('a'+i))+".jsonl")
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cache.load(p, func() []appwire.Turn { return nil })
	}
	if len(cache.entries) != 2 {
		t.Fatalf("cache holds %d entries, want 2 (bounded)", len(cache.entries))
	}
}

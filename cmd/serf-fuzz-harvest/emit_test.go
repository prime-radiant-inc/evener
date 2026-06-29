package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEncodeBytesSeedRoundTrips(t *testing.T) {
	data := []byte("data:\x07\nweird\xff")
	enc := string(encodeBytesSeed(data))
	lines := strings.SplitN(enc, "\n", 2)
	if lines[0] != "go test fuzz v1" {
		t.Fatalf("missing header: %q", enc)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(lines[1]), "[]byte("), ")")
	unq, err := strconv.Unquote(body)
	if err != nil {
		t.Fatalf("unquote %q: %v", body, err)
	}
	if unq != string(data) {
		t.Fatalf("round-trip mismatch: got %q want %q", unq, string(data))
	}
}

func TestEmitWritesDedupsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	e := NewEmitter(false, 32768)

	if _, err := e.EmitBytes(dir, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// Same content again -> deduped, not rewritten.
	if _, err := e.EmitBytes(dir, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if e.written != 1 || e.deduped != 1 {
		t.Fatalf("written=%d deduped=%d, want 1/1", e.written, e.deduped)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	// A fresh emitter over the same dir must skip the existing file (no diff).
	e2 := NewEmitter(false, 32768)
	if _, err := e2.EmitBytes(dir, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if e2.written != 0 || e2.deduped != 1 {
		t.Fatalf("re-run not idempotent: written=%d deduped=%d", e2.written, e2.deduped)
	}
}

func TestEmitDropsOversize(t *testing.T) {
	dir := t.TempDir()
	e := NewEmitter(false, 16)
	if _, err := e.EmitBytes(dir, []byte(strings.Repeat("A", 100))); err != nil {
		t.Fatal(err)
	}
	if e.oversized != 1 || e.written != 0 {
		t.Fatalf("oversize not dropped: oversized=%d written=%d", e.oversized, e.written)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("oversize seed was written: %d files", len(files))
	}
}

func TestEmitDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	e := NewEmitter(true, 32768)
	if _, err := e.EmitIntBytes(dir, 3, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if e.wouldWrite != 1 || e.written != 0 {
		t.Fatalf("dry-run wrote: wouldWrite=%d written=%d", e.wouldWrite, e.written)
	}
	if _, err := os.Stat(dir); err == nil {
		if files, _ := os.ReadDir(dir); len(files) != 0 {
			t.Fatalf("dry-run created files: %d", len(files))
		}
	}
}

// The written file must be loadable by `go test -run '^Fuzz'`. We assert the
// exact shape here; the harvester acceptance test runs the real loader.
func TestEmitIntBytesShape(t *testing.T) {
	dir := t.TempDir()
	e := NewEmitter(false, 32768)
	if _, err := e.EmitIntBytes(dir, 5, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	raw, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))
	want := "go test fuzz v1\nint(5)\n[]byte(\"{\\\"a\\\":1}\")\n"
	if string(raw) != want {
		t.Fatalf("seed shape mismatch:\n got %q\n want %q", raw, want)
	}
}

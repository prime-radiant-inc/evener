package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunNewServerError covers the path where fakellm.NewOn fails.
func TestRunNewServerError(t *testing.T) {
	// An invalid address that cannot be bound.
	err := run("not-a-valid-addr:bad", 0, 1, "")
	if err == nil {
		t.Fatal("run with invalid address should return error")
	}
}

// TestStageNoteCreatesFile exercises stageNote() which writes a numbered
// note file into a directory.
func TestStageNoteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path, err := stageNote(dir)
	if err != nil {
		t.Fatalf("stageNote: %v", err)
	}
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, dir) {
		t.Fatalf("path %q should be in dir %q", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged note: %v", err)
	}
	if !strings.HasPrefix(string(data), "note ") {
		t.Fatalf("note content should start with 'note ', got %q", data)
	}
}

// TestStageNoteMultipleCalls verifies the sequence counter increments.
func TestStageNoteMultipleCalls(t *testing.T) {
	dir := t.TempDir()
	p1, err := stageNote(dir)
	if err != nil {
		t.Fatalf("stageNote 1: %v", err)
	}
	p2, err := stageNote(dir)
	if err != nil {
		t.Fatalf("stageNote 2: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("two calls should produce different paths: %q == %q", p1, p2)
	}
}

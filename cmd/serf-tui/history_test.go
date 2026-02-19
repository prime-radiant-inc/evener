package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHistory_Empty(t *testing.T) {
	h := loadHistory("")
	if h != nil {
		t.Fatalf("expected nil, got %v", h)
	}
}

func TestLoadHistory_NoFile(t *testing.T) {
	dir := t.TempDir()
	h := loadHistory(dir)
	if h != nil {
		t.Fatalf("expected nil for nonexistent file, got %v", h)
	}
}

func TestAppendAndLoadHistory(t *testing.T) {
	dir := t.TempDir()

	appendHistory(dir, "hello world")
	appendHistory(dir, "second entry")

	h := loadHistory(dir)
	if len(h) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h))
	}
	if h[0] != "hello world" {
		t.Errorf("entry 0: got %q, want %q", h[0], "hello world")
	}
	if h[1] != "second entry" {
		t.Errorf("entry 1: got %q, want %q", h[1], "second entry")
	}
}

func TestAppendHistory_MultiLine(t *testing.T) {
	dir := t.TempDir()

	appendHistory(dir, "line one\nline two\nline three")

	h := loadHistory(dir)
	if len(h) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(h), h)
	}
	unescaped := unescapeHistory(h[0])
	if unescaped != "line one\nline two\nline three" {
		t.Errorf("got %q", unescaped)
	}
}

func TestLoadHistory_TruncatesOldEntries(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 0; i < maxHistoryEntries+50; i++ {
		lines = append(lines, "entry")
	}
	os.WriteFile(filepath.Join(dir, historyFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	h := loadHistory(dir)
	if len(h) != maxHistoryEntries {
		t.Fatalf("expected %d entries, got %d", maxHistoryEntries, len(h))
	}
}

func TestAppendHistory_EmptyNoop(t *testing.T) {
	dir := t.TempDir()
	appendHistory(dir, "")
	appendHistory("", "something")

	// Neither should have created the file.
	if _, err := os.Stat(filepath.Join(dir, historyFile)); !os.IsNotExist(err) {
		t.Fatal("expected no file for empty text")
	}
}

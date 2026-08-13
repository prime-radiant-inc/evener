package delegatestore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadEventsMissingFileDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "delegates.jsonl")
	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if events != nil {
		t.Fatalf("events = %#v, want nil", events)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat error = %v, want missing file", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("parent Stat error = %v, want missing directory", err)
	}
}

func TestReadEventsPreservesBytesModeAndMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, _, err = store.AppendBatch(make(State), []Event{
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerInitial),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("AppendBatch: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("OpenFile append: %v", err)
	}
	if _, err := file.WriteString(`{"events":[`); err != nil {
		_ = file.Close()
		t.Fatalf("append unterminated batch: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close append: %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	fixedTime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	beforeBytes := mustReadFile(t, path)
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v, want committed create/start only", events)
	}
	afterBytes := mustReadFile(t, path)
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) {
		t.Fatalf("ReadEvents changed bytes:\n got %q\nwant %q", afterBytes, beforeBytes)
	}
	if afterInfo.Mode() != beforeInfo.Mode() {
		t.Fatalf("mode = %v, want %v", afterInfo.Mode(), beforeInfo.Mode())
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("mtime = %v, want %v", afterInfo.ModTime(), beforeInfo.ModTime())
	}
}

func TestReadEventsRejectsTerminatedMalformedBatchWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	raw := []byte("{\"version\":1}\n{\"events\":[}\n")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fixedTime := time.Date(2002, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	if _, err := ReadEvents(path); err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("ReadEvents error = %v, want terminated corruption", err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, raw) {
		t.Fatalf("ReadEvents changed malformed bytes:\n got %q\nwant %q", got, raw)
	}
	if afterInfo.Mode() != beforeInfo.Mode() || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("metadata changed: got mode=%v mtime=%v want mode=%v mtime=%v", afterInfo.Mode(), afterInfo.ModTime(), beforeInfo.Mode(), beforeInfo.ModTime())
	}
}

func TestReadEventsRejectsUnknownVersionWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	raw := []byte("{\"version\":99}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := ReadEvents(path); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("ReadEvents error = %v, want unknown version", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, raw) {
		t.Fatalf("ReadEvents changed bytes:\n got %q\nwant %q", got, raw)
	}
}

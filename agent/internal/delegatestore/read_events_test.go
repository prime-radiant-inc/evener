package delegatestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// countdownContext reports itself canceled once its Err method has been
// called more times than allow, so a test can deterministically stop a scan
// partway through a journal without depending on real time or file size.
type countdownContext struct {
	context.Context
	allow int32
}

func (c *countdownContext) Err() error {
	if atomic.AddInt32(&c.allow, -1) < 0 {
		return context.Canceled
	}
	return nil
}

// writeDelegateJournal writes a valid version header followed by n batch
// lines, each holding one distinct top-level delegate-created event, so
// scan-limit and cancellation tests can control the exact line count.
func writeDelegateJournal(t *testing.T, path string, n int) {
	t.Helper()
	var buf bytes.Buffer
	header, err := json.Marshal(versionRecord{Version: CurrentVersion})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(header)
	buf.WriteByte('\n')
	for i := range n {
		event := createdEvent(fmt.Sprintf("dlg_%d", i), "")
		event.Seq = uint64(i + 1)
		batch, err := json.Marshal(batchRecord{Events: []Event{event}})
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(batch)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanEvents_WithinLimitsDecodesNormally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 5)

	events, diagnostics, err := ScanEvents(context.Background(), path, ScanLimits{MaxBytes: 1 << 20, MaxEvents: 100})
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if diagnostics.TornTail {
		t.Errorf("diagnostics.TornTail = true, want false for a terminated journal")
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
}

func TestScanEvents_Missing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "delegates.jsonl")
	events, _, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if events != nil {
		t.Fatalf("events = %#v, want nil", events)
	}
}

// TestScanEvents_ChecksCancellationBetweenRecords covers #448's acceptance
// criterion that a large-journal scan checks ctx between records (here, one
// event per batch line), not just once per file.
func TestScanEvents_ChecksCancellationBetweenRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	const total = 200
	writeDelegateJournal(t, path, total)

	ctx := &countdownContext{Context: context.Background(), allow: 10}
	events, _, err := ScanEvents(ctx, path, ScanLimits{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEvents error = %v, want context.Canceled", err)
	}
	if events != nil {
		t.Fatalf("ScanEvents returned %d events despite cancellation, want none retained", len(events))
	}
}

// TestScanEvents_RefusesRawEventLimit covers the raw-limit-refusal
// acceptance test on the event-count dimension: a journal over the event
// ceiling is refused before Fold ever sees it.
func TestScanEvents_RefusesRawEventLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 50)

	_, _, err := ScanEvents(context.Background(), path, ScanLimits{MaxEvents: 10})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
}

// TestScanEvents_MaxEventsReturnsPartialEventsAlongsideError covers the
// degrade-to-partial contract: hitting MaxEvents must not discard everything
// already decoded — a legitimately large delegates.jsonl (its Descriptor
// embeds full skill bodies and role prompts, see historicalDelegateScanLimits
// in jobs_activity_past.go) should still show its first N delegates rather
// than losing the whole activity tree.
func TestScanEvents_MaxEventsReturnsPartialEventsAlongsideError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 50)

	events, _, err := ScanEvents(context.Background(), path, ScanLimits{MaxEvents: 10})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
	if len(events) != 10 {
		t.Fatalf("got %d partial events, want exactly the 10 decoded before the limit fired", len(events))
	}
}

// TestScanEvents_MaxBytesReturnsPartialEventsAlongsideError is the byte-
// ceiling counterpart: the byte-limited read almost always lands mid-batch-
// line, so this also exercises the torn-tail-style trim back to the last
// complete line before Fold sees it.
func TestScanEvents_MaxBytesReturnsPartialEventsAlongsideError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 200)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	events, _, err := ScanEvents(context.Background(), path, ScanLimits{MaxBytes: info.Size() / 2})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
	if len(events) == 0 || len(events) >= 200 {
		t.Fatalf("got %d partial events, want a nonzero prefix short of the full 200", len(events))
	}
}

// TestScanEvents_RefusesRawByteLimit covers the raw-limit-refusal acceptance
// test on the byte dimension: a journal over the byte ceiling is refused
// before being retained.
func TestScanEvents_RefusesRawByteLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 200)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ScanEvents(context.Background(), path, ScanLimits{MaxBytes: info.Size() / 2})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
}

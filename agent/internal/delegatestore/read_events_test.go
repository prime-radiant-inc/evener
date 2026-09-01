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

// appendDelegateJournalEvents appends count more batch lines to an existing
// file written by writeDelegateJournal(t, path, startIndex), indexed
// startIndex..startIndex+count-1 via the same dlg_%d/Seq convention, so
// incrementality tests can grow a journal after an earlier scan already ran
// against it.
func appendDelegateJournalEvents(t *testing.T, path string, startIndex, count int) {
	t.Helper()
	var buf bytes.Buffer
	for i := startIndex; i < startIndex+count; i++ {
		event := createdEvent(fmt.Sprintf("dlg_%d", i), "")
		event.Seq = uint64(i + 1)
		batch, err := json.Marshal(batchRecord{Events: []Event{event}})
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(batch)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(buf.Bytes()); err != nil {
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

// TestScanEvents_MissingVersionHeaderOnEmptyFile pins existing behavior for
// an actually-empty (0-byte) file, distinct from a missing file: ScanEvents
// streams the header line itself now, so this regression-pins that an empty
// read is still reported as "missing version header", not misread as EOF-
// with-nothing-wrong.
func TestScanEvents_MissingVersionHeaderOnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err == nil || !strings.Contains(err.Error(), "missing version header") {
		t.Fatalf("ScanEvents error = %v, want missing version header", err)
	}
}

// TestScanEvents_UnterminatedVersionHeaderOnEntireFileWithoutNewline pins
// existing behavior: a file with no newline anywhere (not even the header
// line completes) is always a hard error, regardless of scan limits — there
// is no complete line to trim back to.
func TestScanEvents_UnterminatedVersionHeaderOnEntireFileWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err == nil || !strings.Contains(err.Error(), "unterminated version header") {
		t.Fatalf("ScanEvents error = %v, want unterminated version header", err)
	}
}

// TestScanEvents_CancellationTakesPriorityOverCoincidentByteLimit covers
// roborev's finding on #448 (jobstore's counterpart is
// TestScanEvents_CancellationTakesPriorityOverCoincidentByteLimit in
// agent/internal/jobstore): if ctx is canceled during the read whose chunk
// ALSO happens to push totalBytes past MaxBytes, that must still be
// reported as context.Canceled, not silently swallowed by
// ErrScanLimitExceeded. MaxBytes is set to exactly the header+line-1 size:
// both fit (totalBytes == MaxBytes, not over), so the limit fires on line
// 2's byte check — deterministically, not by guessing a fraction of the
// whole file's size.
func TestScanEvents_CancellationTakesPriorityOverCoincidentByteLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 1)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	headerPlusLine1Size := info.Size()
	writeDelegateJournal(t, path, 5)
	// allow:3 lets exactly three ctx.Err() calls through as nil — the
	// top-of-read checks for the header, line 1, and line 2 — then reports
	// canceled on every call after, including the check placed right before
	// the byte-limit return on line 2's iteration.
	ctx := &countdownContext{Context: context.Background(), allow: 3}

	_, _, err = ScanEvents(ctx, path, ScanLimits{MaxBytes: headerPlusLine1Size})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEvents error = %v, want context.Canceled (not ErrScanLimitExceeded) when cancellation coincides with the byte limit", err)
	}
}

// TestScanEvents_CancellationTakesPriorityOverCoincidentEventLimit is the
// MaxEvents counterpart: with MaxEvents=1, the limit fires on line 2's
// iteration (line 1's single event fits; line 2's would exceed it) — three
// top-of-read ctx checks (header, line 1, line 2) must see nil before the
// fourth, the one guarding the limit-triggered return itself, reports
// canceled.
func TestScanEvents_CancellationTakesPriorityOverCoincidentEventLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 5)
	ctx := &countdownContext{Context: context.Background(), allow: 3}

	_, _, err := ScanEvents(ctx, path, ScanLimits{MaxEvents: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEvents error = %v, want context.Canceled (not ErrScanLimitExceeded) when cancellation coincides with the event limit", err)
	}
}

// TestScanEvents_RefusesSingleOversizedUnterminatedLine mirrors jobstore's
// test of the same name: a single batch line with no trailing newline at
// all, longer than MaxBytes, must be refused rather than tolerated as an
// in-flight partial write — and the underlying reader must itself be
// bounded via io.LimitReader so this refusal doesn't require buffering the
// whole oversized line first (bufio.Reader.ReadBytes has no size cap of its
// own, unlike Scanner's MaxScanTokenSize).
// TestScanEvents_RefusesSingleOversizedUnterminatedLine covers the
// unterminated-final-chunk path: a single batch line with no trailing
// newline at all, longer than MaxLineBytes, must be refused rather than
// tolerated as an in-flight partial write. This is now MaxLineBytes'
// responsibility, not MaxBytes' — #448's incremental-fold round removes
// MaxBytes as a truncating per-file ceiling (a legitimate large journal must
// fold in full, not get cut off), but a single pathological line is still
// corruption, not Tuesday, so it keeps its own independent, always-on cap.
func TestScanEvents_RefusesSingleOversizedUnterminatedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 0)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"events":[` + strings.Repeat("x", 5_000_000)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err = ScanEvents(context.Background(), path, ScanLimits{MaxLineBytes: 100})
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("ScanEvents error = %v, want ErrLineTooLong", err)
	}
}

// TestScanEvents_RefusesOversizedTerminatedLineViaMaxLineBytes covers the
// terminated-line path for the same cap: MaxLineBytes refuses a single
// pathological batch line even when it IS newline-terminated, independently
// of any MaxBytes setting.
func TestScanEvents_RefusesOversizedTerminatedLineViaMaxLineBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 0)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"events":[` + strings.Repeat("x", 5_000_000) + "]}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err = ScanEvents(context.Background(), path, ScanLimits{MaxLineBytes: 100})
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("ScanEvents error = %v, want ErrLineTooLong", err)
	}
}

// TestScanEventsFrom_ReadsOnlyTheDeltaSinceOffsetSkippingTheHeader is the
// incrementality contract ScanEventsFrom exists for: a resumed scan starting
// past the header must decode ONLY the batch lines appended since the
// earlier call's reported offset -- not re-read the header (which does not
// recur past byte zero) and not re-decode anything already seen.
func TestScanEventsFrom_ReadsOnlyTheDeltaSinceOffsetSkippingTheHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 3)

	first, firstOffset, _, err := ScanEventsFrom(context.Background(), path, 0, ScanLimits{})
	if err != nil {
		t.Fatalf("first ScanEventsFrom: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first scan got %d events, want 3", len(first))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if firstOffset != info.Size() {
		t.Fatalf("firstOffset = %d, want the file's full size %d", firstOffset, info.Size())
	}

	appendDelegateJournalEvents(t, path, 3, 2)

	second, secondOffset, _, err := ScanEventsFrom(context.Background(), path, firstOffset, ScanLimits{})
	if err != nil {
		t.Fatalf("second ScanEventsFrom: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second scan got %d events, want exactly the 2 appended since firstOffset, not all 5", len(second))
	}
	if second[0].DelegateID != "dlg_3" || second[1].DelegateID != "dlg_4" {
		t.Fatalf("second scan events = %+v, want dlg_3 then dlg_4", second)
	}
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if secondOffset != info2.Size() {
		t.Fatalf("secondOffset = %d, want the file's new full size %d", secondOffset, info2.Size())
	}
}

// TestScanEvents_IsScanEventsFromAtOffsetZero pins ScanEvents as a thin
// wrapper: identical behavior to calling ScanEventsFrom with fromOffset 0
// and discarding the returned offset.
func TestScanEvents_IsScanEventsFromAtOffsetZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 4)

	viaWrapper, viaWrapperDiag, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	viaFrom, _, viaFromDiag, err := ScanEventsFrom(context.Background(), path, 0, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEventsFrom: %v", err)
	}
	if len(viaWrapper) != len(viaFrom) {
		t.Fatalf("ScanEvents returned %d events, ScanEventsFrom(0) returned %d, want equal", len(viaWrapper), len(viaFrom))
	}
	for i := range viaWrapper {
		if viaWrapper[i].DelegateID != viaFrom[i].DelegateID {
			t.Fatalf("event %d differs: ScanEvents=%+v ScanEventsFrom=%+v", i, viaWrapper[i], viaFrom[i])
		}
	}
	if viaWrapperDiag != viaFromDiag {
		t.Fatalf("diagnostics differ: ScanEvents=%+v ScanEventsFrom=%+v", viaWrapperDiag, viaFromDiag)
	}
}

// TestScanEvents_TornTailTrueOnGenuineUnterminatedFile pins existing,
// correct behavior: an actually-incomplete trailing batch line (an in-flight
// append racing the read, with no scan limit involved) is a genuine torn
// tail.
func TestScanEvents_TornTailTrueOnGenuineUnterminatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 3)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"events":[`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	events, diagnostics, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if !diagnostics.TornTail {
		t.Errorf("diagnostics.TornTail = false, want true for a genuinely incomplete trailing batch line")
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want the 3 complete ones (the torn trailing line contributes none)", len(events))
	}
}

// TestScanEvents_TornTailFalseOnArtificialByteCutoff covers roborev's
// finding on #448: TornTail must reflect whether the journal genuinely ends
// without a terminating newline, not whether an artificial MaxBytes cutoff
// happened to land mid-line. The journal here is cleanly terminated — an
// unbounded read would show TornTail=false — so a MaxBytes cutoff reporting
// TornTail=true would be reporting corruption that isn't there.
func TestScanEvents_TornTailFalseOnArtificialByteCutoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	writeDelegateJournal(t, path, 200)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	_, diagnostics, err := ScanEvents(context.Background(), path, ScanLimits{MaxBytes: info.Size() / 2})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
	if diagnostics.TornTail {
		t.Errorf("diagnostics.TornTail = true, want false: the cutoff is an artificial MaxBytes ceiling, not a genuine torn tail")
	}
}

// TestScanEvents_StopsBeforeDecodingOnceEventBudgetExhausted covers
// roborev's finding on #807: MaxEvents must be checked BEFORE attempting to
// decode the next batch line, not after — otherwise a MaxEvents: 1 scan can
// still fully decode an oversized (or, as here, malformed) later batch just
// to discover afterward that the budget was already spent. Line 1 holds
// exactly one valid event (bringing the running count to MaxEvents); line 2
// is malformed JSON. If the fix checks the budget first, ScanEvents never
// attempts to decode line 2 at all and reports ErrScanLimitExceeded; the
// pre-fix behavior decodes line 2 first and reports a decode error instead.
func TestScanEvents_StopsBeforeDecodingOnceEventBudgetExhausted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	var buf bytes.Buffer
	header, err := json.Marshal(versionRecord{Version: CurrentVersion})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(header)
	buf.WriteByte('\n')
	event := createdEvent("dlg_0", "")
	event.Seq = 1
	firstBatch, err := json.Marshal(batchRecord{Events: []Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(firstBatch)
	buf.WriteByte('\n')
	buf.WriteString(`{"events":[}` + "\n") // malformed -- must never be reached
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = ScanEvents(context.Background(), path, ScanLimits{MaxEvents: 1})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded (a decode error means the malformed line 2 was reached despite the budget already being spent)", err)
	}
}

// TestScanEvents_DoesNotSwallowScanLimitExceededBehindAFoldError covers
// roborev's finding on #807: when the partial prefix ScanEvents degrades to
// on hitting a limit would NOT fold cleanly on its own (e.g. it ends
// mid-relationship — a RunStarted for a delegate whose own Created event is
// in a later, never-read batch), ScanEvents must still report
// ErrScanLimitExceeded, not a Fold error. Folding is the caller's job
// (scanRootDelegateState already does it); ScanEvents degrading to partial
// only to have its own internal validation immediately reject that same
// partial prefix would silently defeat the documented contract in exactly
// the cases it exists for.
func TestScanEvents_DoesNotSwallowScanLimitExceededBehindAFoldError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	var buf bytes.Buffer
	header, err := json.Marshal(versionRecord{Version: CurrentVersion})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(header)
	buf.WriteByte('\n')
	orphan := startedEvent("dlg_orphan", 1, TriggerInitial)
	orphan.Seq = 1
	firstBatch, err := json.Marshal(batchRecord{Events: []Event{orphan}})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(firstBatch)
	buf.WriteByte('\n')
	second := createdEvent("dlg_second", "")
	second.Seq = 2
	secondBatch, err := json.Marshal(batchRecord{Events: []Event{second}})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(secondBatch)
	buf.WriteByte('\n')
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// MaxEvents: 1 stops the scan after the orphan RunStarted alone -- a
	// partial prefix that, folded by itself, is invalid (no Created event
	// for dlg_orphan ever appears in it).
	events, _, err := ScanEvents(context.Background(), path, ScanLimits{MaxEvents: 1})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded (a fold error here means it was silently swallowed)", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want the 1 decoded before the limit fired", len(events))
	}
}

package jobstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadEvents_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Kind: EventWatchRegistered, WatchID: "w1", Watch: &WatchEvent{Generation: "g1", OwnerSessionID: "o", VisibleSessionID: "v", Target: "job:x", ConfigHash: "h"}},
		{Kind: EventWatchSendPending, WatchID: "w1", WatchSend: &WatchSendState{Key: WatchSendKey{WatchID: "w1"}, DeliveryID: "d1", UpdateSeq: 1}},
		{Kind: EventWatchSendDelivered, WatchID: "w1", WatchSend: &WatchSendState{Key: WatchSendKey{WatchID: "w1"}, DeliveryID: "d1", UpdateSeq: 1}},
	}
	for _, e := range want {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadEvents got %d events, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Kind != want[i].Kind {
			t.Errorf("event %d kind = %q, want %q", i, got[i].Kind, want[i].Kind)
		}
	}
	// The real folds run over the read-back events.
	if w := FoldWatches(got); w["w1"] == nil || w["w1"].Generation != "g1" {
		t.Errorf("FoldWatches over ReadEvents lost the watch registration")
	}
}

func TestReadEvents_Missing(t *testing.T) {
	got, err := ReadEvents(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("missing file should yield nil events, got %d", len(got))
	}
}

func TestReadEvents_TolerateTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"watch_registered","seq":1,"job_id":"","watch_id":"w1","watch":{"generation":"g1"}}` + "\n" +
		`{"kind":"watch_send_pending","seq":2,` // truncated trailing line, no newline
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("trailing partial should be tolerated: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (partial trailing skipped)", len(got))
	}
}

func TestReadEvents_ErrorsOnDefinitiveTrailingCorruption(t *testing.T) {
	for _, trailing := range []string{
		"not-json",
		`{"kind":}`,
		`{"seq":1e2x`,
		`{"resumable":trx`,
		`{"resumable":tru `,
		`{"seq":1e `,
	} {
		t.Run(trailing, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "jobs.jsonl")
			content := `{"kind":"job_started","seq":1,"job_id":"job_A"}` + "\n" + trailing
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadEvents(path); err == nil {
				t.Fatalf("definitively malformed trailing record %q was tolerated", trailing)
			}
		})
	}
}

func TestReadEvents_ErrorsOnNewlineTerminatedTrailingCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"job_started","seq":1,"job_id":"job_A"}` + "\n" + `{"kind":"job_finished"` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(path); err == nil {
		t.Fatal("newline-terminated trailing corruption was tolerated")
	}
}

func TestReadEvents_ErrorsOnMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"watch_registered","seq":1}` + "\n" + `NOT JSON` + "\n" + `{"kind":"watch_cleared","seq":3}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(path); err == nil {
		t.Fatal("a malformed non-trailing line should error")
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

// writeScanEvents writes n newline-delimited events built by line to path,
// for tests that need a journal larger than the few hand-written lines above.
func writeScanEvents(t *testing.T, path string, n int, line func(i int) Event) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := range n {
		if err := enc.Encode(line(i)); err != nil {
			t.Fatal(err)
		}
	}
}

// appendScanEvents appends count more lines to an existing file written by
// writeScanEvents, indexed startIndex..startIndex+count-1 via the same line
// callback shape, so incrementality tests can grow a journal after an
// earlier scan already ran against it.
func appendScanEvents(t *testing.T, path string, startIndex, count int, line func(i int) Event) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := startIndex; i < startIndex+count; i++ {
		if err := enc.Encode(line(i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanEvents_WithinLimitsDecodesNormally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 5, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	events, err := ScanEvents(context.Background(), path, ScanLimits{MaxBytes: 1 << 20, MaxEvents: 100})
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
}

func TestScanEvents_Missing(t *testing.T) {
	got, err := ScanEvents(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"), ScanLimits{})
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("missing file should yield nil events, got %d", len(got))
	}
}

func TestScanEvents_TolerateTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"watch_registered","seq":1,"job_id":"","watch_id":"w1","watch":{"generation":"g1"}}` + "\n" +
		`{"kind":"watch_send_pending","seq":2,` // truncated trailing line, no newline
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("trailing partial should be tolerated: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (partial trailing skipped)", len(got))
	}
}

func TestScanEvents_ErrorsOnMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"watch_registered","seq":1}` + "\n" + `NOT JSON` + "\n" + `{"kind":"watch_cleared","seq":3}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanEvents(context.Background(), path, ScanLimits{}); err == nil {
		t.Fatal("a malformed non-trailing line should error")
	}
}

// TestScanEvents_ChecksCancellationBetweenRecords covers #448's acceptance
// criterion that a large-journal scan checks ctx between records (not just
// once per file), so a canceled request stops before decoding the rest of a
// large journal.
func TestScanEvents_ChecksCancellationBetweenRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	const total = 500
	writeScanEvents(t, path, total, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	ctx := &countdownContext{Context: context.Background(), allow: 10}
	events, err := ScanEvents(ctx, path, ScanLimits{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEvents error = %v, want context.Canceled", err)
	}
	if len(events) >= total {
		t.Fatalf("ScanEvents decoded all %d events despite cancellation, got %d", total, len(events))
	}
}

// TestScanEvents_ManyNonJobEventsExceedEventLimit covers #448's evidence that
// journals include non-job lifecycle events (notify/watch) that consume scan
// work but produce no job row — the raw event ceiling must count them too,
// not only job_started/job_finished records.
func TestScanEvents_ManyNonJobEventsExceedEventLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 50, func(i int) Event {
		return Event{Kind: EventWatchSendPending, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	_, err := ScanEvents(context.Background(), path, ScanLimits{MaxEvents: 10})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
}

// TestScanEvents_CancellationTakesPriorityOverCoincidentEventLimit is the
// MaxEvents counterpart of the byte-limit priority test above: the same
// coincidence risk applies to the event-count check. With MaxEvents=1, the
// limit fires on line 2's iteration (line 1 fits, incrementing len(events)
// to 1; line 2's check then sees len(events)>=MaxEvents) — two top-of-loop
// ctx checks (line 1's and line 2's) must see nil before the third call, the
// one guarding the limit-triggered return itself, is the one that reports
// canceled.
func TestScanEvents_CancellationTakesPriorityOverCoincidentEventLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 5, func(i int) Event {
		return Event{Kind: EventWatchSendPending, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})
	ctx := &countdownContext{Context: context.Background(), allow: 2}

	_, err := ScanEvents(ctx, path, ScanLimits{MaxEvents: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEvents error = %v, want context.Canceled (not ErrScanLimitExceeded) when cancellation coincides with the event limit", err)
	}
}

// TestScanEvents_MaxEventsReturnsPartialEventsAlongsideError covers the
// degrade-to-partial contract a caller needs to truncate gracefully instead
// of hard-failing: when MaxEvents is hit, ScanEvents returns both the
// successfully decoded prefix AND ErrScanLimitExceeded, so a caller that
// understands this specific error can keep what was read rather than
// discarding it. (Callers that don't recognize the error still fail safe:
// checking err first, as every existing caller does, ignores the events.)
func TestScanEvents_MaxEventsReturnsPartialEventsAlongsideError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 50, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	events, err := ScanEvents(context.Background(), path, ScanLimits{MaxEvents: 10})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
	if len(events) != 10 {
		t.Fatalf("got %d partial events, want exactly the 10 decoded before the limit fired", len(events))
	}
	for i, e := range events {
		if e.WatchID != fmt.Sprintf("w%d", i) {
			t.Fatalf("partial events out of order: event %d = %+v", i, e)
		}
	}
}

// TestScanEvents_MaxBytesReturnsPartialEventsAlongsideError is the byte-
// ceiling counterpart: events decoded before the byte limit fired are still
// returned alongside the error.
func TestScanEvents_MaxBytesReturnsPartialEventsAlongsideError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 200, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	events, err := ScanEvents(context.Background(), path, ScanLimits{MaxBytes: info.Size() / 2})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
	if len(events) == 0 || len(events) >= 200 {
		t.Fatalf("got %d partial events, want a nonzero prefix short of the full 200", len(events))
	}
}

// TestScanEvents_CancellationTakesPriorityOverCoincidentByteLimit covers
// roborev's finding on #448: if ctx is canceled during the ReadBytes call
// whose chunk ALSO happens to push totalBytes past MaxBytes, the next
// iteration's ctx.Err() check never gets a chance to run — ScanEvents would
// return ErrScanLimitExceeded, silently swallowing the fact that the caller
// had already asked to stop. A second ctx check belongs right at the
// byte-limit return itself, not only at the top of the next iteration.
func TestScanEvents_CancellationTakesPriorityOverCoincidentByteLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	event := func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	}
	// MaxBytes set to exactly line 1's size: line 1 fits (totalBytes ==
	// MaxBytes, not over), so the limit fires on line 2's byte check —
	// deterministically, not by guessing a fraction of the whole file's size.
	writeScanEvents(t, path, 1, event)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	oneLineSize := info.Size()
	writeScanEvents(t, path, 5, event)
	// allow:2 lets exactly two ctx.Err() calls through as nil — the
	// top-of-iteration checks for lines 1 and 2 — then reports canceled on
	// every call after, including a check placed right before the
	// byte-limit return on line 2's iteration.
	ctx := &countdownContext{Context: context.Background(), allow: 2}

	_, err = ScanEvents(ctx, path, ScanLimits{MaxBytes: oneLineSize})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEvents error = %v, want context.Canceled (not ErrScanLimitExceeded) when cancellation coincides with the byte limit", err)
	}
}

// TestScanEvents_RefusesRawByteLimit covers the raw-limit-refusal acceptance
// test: a journal over the byte ceiling is refused before being retained.
func TestScanEvents_RefusesRawByteLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 200, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ScanEvents(context.Background(), path, ScanLimits{MaxBytes: info.Size() / 2})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
}

// TestScanEvents_RefusesSingleOversizedUnterminatedLine covers the
// unterminated-final-chunk path specifically: a single line with no trailing
// newline at all, longer than MaxBytes, must be refused rather than
// tolerated as an in-flight partial write. 5 MB matches the scale the
// adversarial review empirically probed bufio.Reader.ReadBytes at (it has no
// internal cap, unlike Scanner's MaxScanTokenSize, so it will buffer a line
// of any length before returning it) — the underlying reader must itself be
// bounded via io.LimitReader so this refusal doesn't require buffering the
// whole 5 MB line first.
// TestScanEvents_RefusesSingleOversizedUnterminatedLine covers the
// unterminated-final-chunk path specifically: a single line with no trailing
// newline at all, longer than MaxLineBytes, must be refused rather than
// tolerated as an in-flight partial write. This is now MaxLineBytes'
// responsibility, not MaxBytes' — #448's incremental-fold round removes
// MaxBytes as a truncating per-file ceiling (a legitimate large journal must
// fold in full, not get cut off), but a single pathological line is still
// corruption, not Tuesday, so it keeps its own independent, always-on cap.
func TestScanEvents_RefusesSingleOversizedUnterminatedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	// One line, no trailing newline, far longer than the line cap below.
	content := []byte(`{"kind":"watch_registered","seq":1,"watch_id":"` + strings.Repeat("w", 5_000_000) + `"}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ScanEvents(context.Background(), path, ScanLimits{MaxLineBytes: 100})
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("ScanEvents error = %v, want ErrLineTooLong", err)
	}
}

// TestScanEvents_RefusesOversizedTerminatedLineViaMaxLineBytes covers the
// terminated-line path for the same cap: MaxLineBytes refuses a single
// pathological line even when it IS newline-terminated (not just an
// in-flight partial write), and independently of any MaxBytes setting.
func TestScanEvents_RefusesOversizedTerminatedLineViaMaxLineBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := []byte(`{"kind":"watch_registered","seq":1,"watch_id":"` + strings.Repeat("w", 5_000_000) + `"}` + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ScanEvents(context.Background(), path, ScanLimits{MaxLineBytes: 100})
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("ScanEvents error = %v, want ErrLineTooLong", err)
	}
}

// TestScanEventsFrom_ReadsOnlyTheDeltaSinceOffset is the incrementality
// contract ScanEventsFrom exists for: given the offset ScanEventsFrom
// reported after an earlier call, a later call starting there must decode
// ONLY the events appended since — not re-decode anything already seen —
// and the offset it returns must land exactly at the new end of file.
func TestScanEventsFrom_ReadsOnlyTheDeltaSinceOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 3, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	first, firstOffset, err := ScanEventsFrom(context.Background(), path, 0, ScanLimits{})
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
		t.Fatalf("firstOffset = %d, want the file's full size %d (a clean file with no torn tail)", firstOffset, info.Size())
	}

	appendScanEvents(t, path, 3, 2, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	second, secondOffset, err := ScanEventsFrom(context.Background(), path, firstOffset, ScanLimits{})
	if err != nil {
		t.Fatalf("second ScanEventsFrom: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second scan got %d events, want exactly the 2 appended since firstOffset, not all 5", len(second))
	}
	if second[0].WatchID != "w3" || second[1].WatchID != "w4" {
		t.Fatalf("second scan events = %+v, want w3 then w4", second)
	}
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if secondOffset != info2.Size() {
		t.Fatalf("secondOffset = %d, want the file's new full size %d", secondOffset, info2.Size())
	}
}

// TestScanEventsFrom_ToleratesUnterminatedTrailingLineAcrossOffsets proves
// the offset-tracking discipline that makes repeated ScanEventsFrom calls
// safe against an in-flight write: the returned offset never advances past
// an unterminated final line, so a LATER call from that same offset picks
// the same content back up once it is completed, rather than silently
// skipping or duplicating it.
func TestScanEventsFrom_ToleratesUnterminatedTrailingLineAcrossOffsets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	complete := []byte(`{"kind":"watch_registered","seq":1,"watch_id":"w0"}` + "\n")
	if err := os.WriteFile(path, complete, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"watch_registered","seq":2,"watch_id":"w1"`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	events, offset, err := ScanEventsFrom(context.Background(), path, 0, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEventsFrom: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (the torn trailing line contributes none)", len(events))
	}
	if offset != int64(len(complete)) {
		t.Fatalf("offset = %d, want %d (just past the one complete line, not into the torn tail)", offset, len(complete))
	}

	// Finish the second event and append a third.
	f2, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f2.WriteString(`}` + "\n" + `{"kind":"watch_registered","seq":3,"watch_id":"w2"}` + "\n"); err != nil {
		_ = f2.Close()
		t.Fatal(err)
	}
	if err := f2.Close(); err != nil {
		t.Fatal(err)
	}

	more, _, err := ScanEventsFrom(context.Background(), path, offset, ScanLimits{})
	if err != nil {
		t.Fatalf("second ScanEventsFrom: %v", err)
	}
	if len(more) != 2 || more[0].WatchID != "w1" || more[1].WatchID != "w2" {
		t.Fatalf("second scan events = %+v, want w1 (now complete) then w2 -- resuming from offset must not have skipped the completed line", more)
	}
}

// TestScanEvents_IsScanEventsFromAtOffsetZero pins ScanEvents as a thin
// wrapper: identical behavior to calling ScanEventsFrom with fromOffset 0
// and discarding the returned offset.
func TestScanEvents_IsScanEventsFromAtOffsetZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	writeScanEvents(t, path, 4, func(i int) Event {
		return Event{Kind: EventWatchRegistered, Seq: int64(i + 1), WatchID: fmt.Sprintf("w%d", i)}
	})

	viaWrapper, err := ScanEvents(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	viaFrom, _, err := ScanEventsFrom(context.Background(), path, 0, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEventsFrom: %v", err)
	}
	if len(viaWrapper) != len(viaFrom) {
		t.Fatalf("ScanEvents returned %d events, ScanEventsFrom(0) returned %d, want equal", len(viaWrapper), len(viaFrom))
	}
	for i := range viaWrapper {
		if viaWrapper[i] != viaFrom[i] {
			t.Fatalf("event %d differs: ScanEvents=%+v ScanEventsFrom=%+v", i, viaWrapper[i], viaFrom[i])
		}
	}
}

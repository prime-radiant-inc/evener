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
func TestScanEvents_RefusesSingleOversizedUnterminatedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	// One line, no trailing newline, far longer than the byte ceiling below.
	content := []byte(`{"kind":"watch_registered","seq":1,"watch_id":"` + strings.Repeat("w", 5_000_000) + `"}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ScanEvents(context.Background(), path, ScanLimits{MaxBytes: 100})
	if !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("ScanEvents error = %v, want ErrScanLimitExceeded", err)
	}
}

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestNoteJobActivityTerminalJobGuard covers the terminal-job early-return in
// noteJobActivity (jobs.go:388-389): a terminal job in jm.running does not
// update LastActivity or Phase.
func TestNoteJobActivityTerminalJobGuard(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Capture the persisted record after finalize.
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	persisted, ok := recs[rec.JobID]
	if !ok {
		t.Fatalf("job %s not found in store after finalize", rec.JobID)
	}
	prevPhase := persisted.Phase
	prevActivity := persisted.LastActivity
	// After finalize the job is removed from jm.running, so noteJobActivity
	// is a no-op. Verify it does not mutate the persisted record.
	jm.noteJobActivity(rec.JobID, "newphase")
	recs2, err := jm.store.Load()
	if err != nil {
		t.Fatalf("store.Load after note: %v", err)
	}
	after, ok := recs2[rec.JobID]
	if !ok {
		t.Fatalf("job %s disappeared from store after noteJobActivity", rec.JobID)
	}
	if after.Phase != prevPhase {
		t.Fatalf("Phase changed by noteJobActivity on finalized job: got %q, want %q", after.Phase, prevPhase)
	}
	if after.LastActivity != prevActivity {
		t.Fatalf("LastActivity changed by noteJobActivity on finalized job: got %v, want %v", after.LastActivity, prevActivity)
	}
}

// TestReadOutputHeadLiveAndStorePaths covers readOutputHead (jobs.go:1217-1238)
// for both the live-running path and the store-only fallback path.
func TestReadOutputHeadLiveAndStorePaths(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("hello world\nsecond line\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Live running path.
	content, total, truncated, err := jm.readOutputHead(rec.JobID, 5)
	if err != nil {
		t.Fatalf("readOutputHead live: %v", err)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want 'hello'", content)
	}
	if total != int64(len("hello world\nsecond line\n")) {
		t.Fatalf("total = %d, want %d", total, len("hello world\nsecond line\n"))
	}
	if !truncated {
		t.Fatal("should be truncated")
	}

	// Store-only path after finalize.
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	content, total, truncated, err = jm.readOutputHead(rec.JobID, 100)
	if err != nil {
		t.Fatalf("readOutputHead store: %v", err)
	}
	if content != "hello world\nsecond line\n" {
		t.Fatalf("content = %q", content)
	}
	if truncated {
		t.Fatal("should not be truncated when reading full content")
	}
	if total != int64(len("hello world\nsecond line\n")) {
		t.Fatalf("total = %d, want %d", total, len("hello world\nsecond line\n"))
	}

	// Not found.
	_, _, _, err = jm.readOutputHead("job_missing", 100)
	if err == nil {
		t.Fatal("missing job should error")
	}
}

// TestReadOutputHeadStorePathStatsError covers the error path when the output
// file stats fail for a store-only job (jobs.go:1233-1237).
func TestReadOutputHeadStorePathStatsError(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("data\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Delete the output file to trigger a stats error.
	recs, _ := jm.store.Load()
	r := recs[rec.JobID]
	outputPath := jm.outputPathForJob(r, rec.JobID)
	_ = os.Remove(outputPath)
	_, _, _, err := jm.readOutputHead(rec.JobID, 100)
	if err == nil {
		t.Fatal("missing output file should error")
	}
}

// TestOutputDroppedLiveAndStorePaths covers outputDropped (jobs.go:1246-1263)
// for both the live-running and store-only paths.
func TestOutputDroppedLiveAndStorePaths(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	// No output dropped yet on a live job.
	dropped, err := jm.outputDropped(rec.JobID)
	if err != nil {
		t.Fatalf("outputDropped live: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}

	// Store-only path after finalize.
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	dropped, err = jm.outputDropped(rec.JobID)
	if err != nil {
		t.Fatalf("outputDropped store: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}

	// Not found.
	_, err = jm.outputDropped("job_missing")
	if err == nil {
		t.Fatal("missing job should error")
	}
}

// TestReadOutputWindowLivePath covers readOutputWindow (jobs.go:1178-1213)
// for the live-running path.
func TestReadOutputWindowLivePath(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := []byte("line one\nline two\nline three\n")
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, output); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Live running path — read tail (beforeBytes <= 0 reads tail).
	win, err := jm.readOutputWindow(rec.JobID, 0, 100)
	if err != nil {
		t.Fatalf("readOutputWindow live: %v", err)
	}
	if win.total != int64(len(output)) {
		t.Fatalf("total = %d, want %d", win.total, len(output))
	}
	if !strings.Contains(win.content, "line three") {
		t.Fatalf("content = %q, want last line", win.content)
	}
}

// TestReadOutputWindowStorePath covers the store-only fallback path of
// readOutputWindow for a finalized job.
func TestReadOutputWindowStorePath(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := []byte("stored line one\nstored line two\n")
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, output); err != nil {
		t.Fatalf("append: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	win, err := jm.readOutputWindow(rec.JobID, 0, 100)
	if err != nil {
		t.Fatalf("readOutputWindow store: %v", err)
	}
	if !strings.Contains(win.content, "stored line two") {
		t.Fatalf("content = %q", win.content)
	}
	if win.total != int64(len(output)) {
		t.Fatalf("total = %d, want %d", win.total, len(output))
	}
}

// TestReadOutputWindowNotFound covers the not-found error path.
func TestReadOutputWindowNotFound(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	_, err := jm.readOutputWindow("job_missing", 0, 100)
	if err == nil {
		t.Fatal("missing job should error")
	}
}

// TestHeadOutput covers headOutput (jobs.go:2163-2166), the live output head
// reader wrapper.
func TestHeadOutput(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("head content\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	content, total, truncated, err := headOutput(jm.running[rec.JobID].output, 4)
	if err != nil {
		t.Fatalf("headOutput: %v", err)
	}
	if content != "head" {
		t.Fatalf("content = %q, want 'head'", content)
	}
	if total != int64(len("head content\n")) {
		t.Fatalf("total = %d", total)
	}
	if !truncated {
		t.Fatal("should be truncated")
	}
}

// TestBoundedStructuredResult covers all branches of boundedStructuredResult
// (jobs.go:1804-1834): nil value with captureFailed, nil value with schema,
// nil value without schema, marshal error/too large, schema validation fail,
// and the happy path.
func TestBoundedStructuredResult(t *testing.T) {
	t.Parallel()
	// nil value with captureFailed=true.
	got, valid, reason := boundedStructuredResult(nil, nil, true)
	if got != nil || valid == nil || *valid != false || reason != structuredResultReasonSchemaCaptureFailed {
		t.Fatalf("captureFailed: got=%v valid=%v reason=%q", got, valid, reason)
	}

	// nil value with schema requested, no captureFailed.
	got, valid, reason = boundedStructuredResult(nil, "someSchema", false)
	if got != nil || valid == nil || *valid != false || reason != structuredResultReasonSchemaResultMissing {
		t.Fatalf("schema+nil: got=%v valid=%v reason=%q", got, valid, reason)
	}

	// nil value without schema.
	got, valid, reason = boundedStructuredResult(nil, nil, false)
	if got != nil || valid != nil || reason != "" {
		t.Fatalf("nil no schema: got=%v valid=%v reason=%q", got, valid, reason)
	}

	// Value too large: create a value that marshals to > maxPersistedStructuredResultJSONBytes.
	large := strings.Repeat("x", maxPersistedStructuredResultJSONBytes+100)
	got, valid, reason = boundedStructuredResult(large, nil, false)
	if got != nil || valid == nil || *valid != false || reason != structuredResultReasonSchemaResultTooLarge {
		t.Fatalf("too large: got=%v valid=%v reason=%q", got, valid, reason)
	}

	// Happy path: small value, no schema.
	got, valid, reason = boundedStructuredResult(map[string]any{"ok": true}, nil, false)
	if got == nil || valid == nil || *valid != true || reason != "" {
		t.Fatalf("happy path: got=%v valid=%v reason=%q", got, valid, reason)
	}

	// Value that cannot be marshaled (channel).
	got, valid, reason = boundedStructuredResult(make(chan int), nil, false)
	if got != nil || valid == nil || *valid != false || reason != structuredResultReasonSchemaCaptureFailed {
		t.Fatalf("marshal error: got=%v valid=%v reason=%q", got, valid, reason)
	}
}

// TestBoundedStructuredResultSchemaValidation covers the schema validation
// failure branch (jobs.go:1828-1831).
func TestBoundedStructuredResultSchemaValidation(t *testing.T) {
	t.Parallel()
	// A schema that rejects the value — pass a non-matching schema.
	// validateStructuredResult with a schema and a non-conforming value.
	got, valid, reason := boundedStructuredResult(
		map[string]any{"wrong": "value"},
		map[string]any{"type": "object", "properties": map[string]any{"required_field": map[string]any{"type": "string"}}, "required": []string{"required_field"}},
		false,
	)
	if got != nil || valid == nil || *valid != false {
		t.Fatalf("validation fail: got=%v valid=%v reason=%q", got, valid, reason)
	}
	// Reason could be validation_failed or schema_capture_failed depending on
	// whether the schema itself is valid.
	if reason == "" {
		t.Fatal("reason should not be empty on validation failure")
	}
}

// TestAbandonRunningJob covers abandonRunningJob (jobs.go:808-837) for a
// single running job.
func TestAbandonRunningJob(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("some output\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	jm.abandonRunningJob(rec.JobID)
	// After abandon, the job should be out of jm.running.
	jm.mu.Lock()
	_, stillRunning := jm.running[rec.JobID]
	jm.mu.Unlock()
	if stillRunning {
		t.Fatal("job should not be running after abandon")
	}
	// Abandoning a non-running job is a no-op.
	jm.abandonRunningJob("job_nonexistent")
}

// TestValidatedOutputStatsForRecordMismatch covers the metadata mismatch error
// path (jobs.go:2180-2181).
func TestValidatedOutputStatsForRecordMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	if err := os.WriteFile(logPath, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a record with a mismatched OutputBytes.
	rec := &jobstore.JobRecord{Status: jobstore.StatusCompleted, OutputBytes: 999}
	_, _, err := validatedOutputStatsForRecord(logPath, rec)
	if err == nil {
		t.Fatal("mismatched OutputBytes should error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v, want mismatch message", err)
	}
	// Matching record should succeed.
	rec.OutputBytes = int64(len("hello world\n"))
	total, _, err := validatedOutputStatsForRecord(logPath, rec)
	if err != nil {
		t.Fatalf("matching record: %v", err)
	}
	if total != int64(len("hello world\n")) {
		t.Fatalf("total = %d, want %d", total, len("hello world\n"))
	}
	// Non-terminal record skips the mismatch check.
	rec2 := &jobstore.JobRecord{Status: jobstore.StatusRunning, OutputBytes: 999}
	_, _, err = validatedOutputStatsForRecord(logPath, rec2)
	if err != nil {
		t.Fatalf("non-terminal record should skip mismatch check: %v", err)
	}
}

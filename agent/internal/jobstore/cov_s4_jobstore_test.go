package jobstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/fuzz/fault"
)

// openMemStore opens a Store on a fresh in-memory filesystem. The closed-store
// and parse/blank-line branches don't touch the durable disk contract the OS-fs
// tests exercise, so MemMapFs keeps them fast and hermetic.
func openMemStore(t *testing.T) *Store {
	t.Helper()
	s, err := openFs(afero.NewMemMapFs(), "/jobs.jsonl")
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	return s
}

// TestClosedStoreLoadOperationsReturnErrStoreClosed pins the ErrStoreClosed arm
// of every Load* / AppendBatch reader that store_test.go's close test does not
// already cover, so a closed store never silently reads stale state.
func TestClosedStoreLoadOperationsReturnErrStoreClosed(t *testing.T) {
	s := openMemStore(t)
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_A"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Append(Event{Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := s.AppendBatch([]Event{{Kind: EventJobStarted, JobID: "job_B"}}); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("AppendBatch after close = %v, want ErrStoreClosed", err)
	}
	if _, err := s.LoadOrdered(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("LoadOrdered after close = %v, want ErrStoreClosed", err)
	}
	if _, err := s.LoadDelegates(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("LoadDelegates after close = %v, want ErrStoreClosed", err)
	}
	if _, err := s.LoadWatches(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("LoadWatches after close = %v, want ErrStoreClosed", err)
	}
	if _, err := s.LoadWatchSends(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("LoadWatchSends after close = %v, want ErrStoreClosed", err)
	}
	if _, err := s.LoadGrants(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("LoadGrants after close = %v, want ErrStoreClosed", err)
	}
	if _, err := s.LoadEvents(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("LoadEvents after close = %v, want ErrStoreClosed", err)
	}
}

// TestAppendBatchEmptyIsNoop pins the empty-batch fast path: it must return nil
// without advancing seq or touching the file, even before any real append.
func TestAppendBatchEmptyIsNoop(t *testing.T) {
	s := openMemStore(t)
	if err := s.AppendBatch(nil); err != nil {
		t.Fatalf("AppendBatch(nil) = %v, want nil", err)
	}
	if s.seq != 0 {
		t.Fatalf("seq after empty batch = %d, want 0", s.seq)
	}
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("empty batch wrote events: %+v", events)
	}
}

// TestReadAllSkipsBlankLines pins the blank-line-continue arm of readAllLocked:
// a blank interior line is skipped, not parsed, and the surrounding events load.
func TestReadAllSkipsBlankLines(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/jobs.jsonl"
	content := `{"kind":"job_started","seq":1,"job_id":"job_A"}` + "\n\n" +
		`{"kind":"job_finished","seq":2,"job_id":"job_A","status":"completed"}` + "\n"
	if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 || events[0].JobID != "job_A" || events[1].Status != StatusCompleted {
		t.Fatalf("events = %+v, want two events around a skipped blank line", events)
	}
}

// TestOpenReportsParseErrorForNonTrailingLine pins the parse-error arm: a
// malformed line that is NOT the trailing line (the trailing line is complete
// and newline-terminated, so trailing recovery leaves it alone) is reported as
// corruption with its line number.
func TestOpenReportsParseErrorForNonTrailingLine(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/jobs.jsonl"
	content := `{oops not json}` + "\n" +
		`{"kind":"job_started","seq":2,"job_id":"job_A"}` + "\n"
	if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}
	_, err := openFs(fs, path)
	if err == nil {
		t.Fatal("openFs on mid-file corruption succeeded, want parse error")
	}
	if !strings.Contains(err.Error(), "parse event line 1") {
		t.Fatalf("error = %q, want parse-event-line with line number", err)
	}
}

// TestIsIncompleteTrailingJSON pins the classifier that decides whether a
// trailing partial line is a torn in-flight append (truncate it) or genuine
// corruption (preserve it and error). Each error is the REAL json.Unmarshal
// error for the crafted bytes, so the branches match production exactly.
func TestIsIncompleteTrailingJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		{name: "empty", line: "", want: false},
		{name: "whitespace", line: "   \t ", want: false},
		{name: "unexpected_end", line: `{"seq":5,"kind":`, want: true},
		{name: "incomplete_literal", line: `{"resumable":trx`, want: true},
		{name: "syntax_offset_before_end", line: `}garbage`, want: false},
		{name: "complete_but_invalid_object", line: `{"kind":}`, want: false},
		{name: "type_error_not_syntax", line: `{"seq":"not-a-number"}`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e Event
			err := json.Unmarshal([]byte(tc.line), &e)
			if err == nil {
				t.Fatalf("crafted line %q unmarshalled cleanly; test needs a real error", tc.line)
			}
			if got := isIncompleteTrailingJSON([]byte(tc.line), err); got != tc.want {
				t.Fatalf("isIncompleteTrailingJSON(%q, %v) = %v, want %v", tc.line, err, got, tc.want)
			}
		})
	}
}

// TestFoldDelegatesSkipsMalformedDelegateEvents pins the guards that drop a
// delegate_created / delegate_stop_gate_closed event carrying no delegate id or
// payload, so a torn event never fabricates a phantom delegate record.
func TestFoldDelegatesSkipsMalformedDelegateEvents(t *testing.T) {
	events := []Event{
		{Kind: EventDelegateCreated, Seq: 1, DelegateID: "dlg_A"},                                 // Delegate nil
		{Kind: EventDelegateCreated, Seq: 2, Delegate: &DelegateEvent{Generation: "dg_1"}},        // id ""
		{Kind: EventDelegateStopGateClosed, Seq: 3, DelegateID: "dlg_B"},                          // Delegate nil
		{Kind: EventDelegateStopGateClosed, Seq: 4, Delegate: &DelegateEvent{StopJobID: "job_1"}}, // id ""
	}
	if d := FoldDelegates(events); len(d) != 0 {
		t.Fatalf("malformed delegate events folded into records: %+v", d)
	}
}

// TestFoldDelegatesSessionAssignedKeepsIdleResumable pins the resumable->idle
// transition inside job_session_assigned: when the delegate is already idle and
// a session-assigned event reasserts resumability, the status stays idle.
func TestFoldDelegatesSessionAssignedKeepsIdleResumable(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	resumable := true
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_1", Resumable: true}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCompleted
			e.EndedAt = &end
		}),
		ev(EventJobSessionAssigned, 4, "job_1", func(e *Event) {
			e.TranscriptRef = "local:child_A"
			e.Resumable = &resumable
		}),
	}
	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if !d.Resumable || d.Status != DelegateIdle {
		t.Fatalf("delegate = %+v, want idle+resumable after session-assigned", d)
	}
}

// TestFoldDelegatesStopGateIgnoresStaleFinishedJob pins the stop-gate guard for
// a delegate with no current job: a stop-gate closure that names a job other
// than the delegate's latest job is stale and must not close the gate.
func TestFoldDelegatesStopGateIgnoresStaleFinishedJob(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_1", Resumable: true}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCompleted
			e.EndedAt = &end
		}),
		ev(EventDelegateStopGateClosed, 4, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{Generation: "dg_stale", StopJobID: "job_other"}
		}),
	}
	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.StopGateClosed || d.Generation != "dg_1" || d.Status != DelegateIdle || d.LatestJobID != "job_1" {
		t.Fatalf("delegate = %+v, want stale stop-gate ignored (idle, gate open, generation dg_1)", d)
	}
}

// TestOutputRetainedStartReportsPrunedPrefix pins RetainedStart: after the cap
// evicts the head, it reports the lifetime offset of the first retained byte.
func TestOutputRetainedStartReportsPrunedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 6)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "abcdef")
	appendOutput(t, o, "ghij")
	if got := o.RetainedStart(); got != 4 {
		t.Fatalf("RetainedStart = %d, want 4 evicted head bytes", got)
	}
}

// TestGrepFileLimitScansClosedFile pins the package-level grep over a closed
// output file, including offset shifting when the file holds only a retained
// tail, plus the limit-validation and open-failure arms.
func TestGrepFileLimitScansClosedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "prefix\nserver ready\ndone\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	re := regexp.MustCompile(`ready`)
	wantOffset := int64(len("prefix\n"))

	matches, err := GrepFileLimit(path, re, 1<<16, 0, 1<<16)
	if err != nil {
		t.Fatalf("GrepFileLimit: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "server ready" || matches[0].ByteOffset != wantOffset {
		t.Fatalf("GrepFileLimit matches = %+v, want server-ready at %d", matches, wantOffset)
	}

	shifted, err := GrepFileLimitAt(path, re, 1<<16, 0, 1<<16, 100)
	if err != nil {
		t.Fatalf("GrepFileLimitAt: %v", err)
	}
	if len(shifted) != 1 || shifted[0].ByteOffset != wantOffset+100 {
		t.Fatalf("GrepFileLimitAt matches = %+v, want offset shifted by retainedStart", shifted)
	}

	if _, err := GrepFileLimit(path, re, -1, 0, 1<<16); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("GrepFileLimit(-1) err = %v, want ErrInvalidLimit", err)
	}
	if matches, err := GrepFileLimit(path, re, 0, 0, 1<<16); err != nil || matches != nil {
		t.Fatalf("GrepFileLimit(0) = %+v, %v, want nil,nil", matches, err)
	}
	if _, err := GrepFileLimit(filepath.Join(t.TempDir(), "missing.log"), re, 1<<16, 0, 1<<16); err == nil {
		t.Fatal("GrepFileLimit on missing file succeeded, want open error")
	}
}

// TestRemoveOutputArtifactsIsIdempotent pins RemoveOutputArtifacts: it deletes
// the log and its metadata sidecar, and a second call over the now-missing
// artifacts is a clean no-op.
func TestRemoveOutputArtifactsIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "some output\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(outputMetaPath(path)); err != nil {
		t.Fatalf("expected metadata sidecar present: %v", err)
	}

	if err := RemoveOutputArtifacts(path); err != nil {
		t.Fatalf("RemoveOutputArtifacts: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log stat after remove = %v, want removed", err)
	}
	if _, err := os.Stat(outputMetaPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata stat after remove = %v, want removed", err)
	}

	if err := RemoveOutputArtifacts(path); err != nil {
		t.Fatalf("second RemoveOutputArtifacts over missing artifacts = %v, want nil", err)
	}
}

// TestOutputReadsFailAfterUnderlyingFileRemoved pins the stat/open error arms of
// Tail, Head, and Grep: once the backing file is unlinked, each read surfaces
// the filesystem error rather than returning stale bytes.
func TestOutputReadsFailAfterUnderlyingFileRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "abc\n")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove backing file: %v", err)
	}

	if _, _, _, err := o.Tail(1024); err == nil {
		t.Fatal("Tail after file removal succeeded, want stat error")
	}
	if _, _, _, err := o.Head(1024); err == nil {
		t.Fatal("Head after file removal succeeded, want stat error")
	}
	if _, err := o.Grep(regexp.MustCompile(`abc`), 1024); err == nil {
		t.Fatal("Grep after file removal succeeded, want open error")
	}
}

// TestOutputFileStatsMissingFileErrors pins the stat-failure arm of the closed
// forensic stats helper.
func TestOutputFileStatsMissingFileErrors(t *testing.T) {
	if _, _, err := OutputFileStats(filepath.Join(t.TempDir(), "nope.log")); err == nil {
		t.Fatal("OutputFileStats on missing file succeeded, want stat error")
	}
}

// TestOutputMetaRejectsNegativeRetainedStart pins the metadata sanity arm: a
// sidecar claiming a negative retained-start is rejected as a mismatch, not
// used to compute a bogus lifetime length.
func TestOutputMetaRejectsNegativeRetainedStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if err := writeOutputMetaFile(outputMetaPath(path), outputMeta{
		TotalBytes:     3,
		RetainedStart:  -1,
		RetainedSHA256: outputBytesSHA256([]byte("abc")),
	}); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if _, _, err := OutputFileStats(path); err == nil {
		t.Fatal("OutputFileStats with negative retained_start succeeded, want mismatch")
	}
}

// TestWriteOutputMetaFileEmptyPathNoop pins the empty-path fast return of the
// metadata writer: no path means no metadata to persist.
func TestWriteOutputMetaFileEmptyPathNoop(t *testing.T) {
	if err := writeOutputMetaFileFs(afero.NewMemMapFs(), "", outputMeta{TotalBytes: 1}); err != nil {
		t.Fatalf("writeOutputMetaFileFs empty path = %v, want nil", err)
	}
}

// planFaultAt builds a fault plan long enough that every filesystem op succeeds
// (byte 0x01 => b%4 != 0 => no fault) except the named op indices, which fault
// (byte 0x00 => b%4 == 0). The op index is the sequential fs-operation counter
// the Schedule advances, so a single index trips exactly one operation while the
// rest of the durable path runs normally.
func planFaultAt(idx ...int) *fault.Schedule {
	plan := bytes.Repeat([]byte{0x01}, 64)
	for _, i := range idx {
		plan[i] = 0x00
	}
	return fault.FromBytes(plan)
}

// faultStore opens a Store on a fresh MemMapFs wrapped in the given fault
// schedule. A fresh (nonexistent) log consumes fs ops 0..3 during open
// (OpenFile, Stat, Open, scan-read), so faults scheduled at op index >= 4 land
// on the first append instead of the open.
func faultStore(t *testing.T, s *fault.Schedule) *Store {
	t.Helper()
	fs := fault.FS(afero.NewMemMapFs(), s)
	st, err := openFs(fs, "/jobs.jsonl")
	if err != nil {
		t.Fatalf("openFs under fault: %v", err)
	}
	return st
}

// TestStoreAppendSurfacesFilesystemFaults pins the seek/write/sync error arms of
// Append: an injected failure at each durable step surfaces as a wrapped error
// and leaves the store's seq unadvanced (the rollback restores the tail).
func TestStoreAppendSurfacesFilesystemFaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   int
		want string
	}{
		{name: "seek", op: 4, want: "seek append start"},
		{name: "write", op: 5, want: "write event"},
		{name: "sync", op: 6, want: "sync event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := faultStore(t, planFaultAt(tc.op))
			err := s.Append(Event{Kind: EventJobStarted, JobID: "job_A"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("append fault error = %v, want %q", err, tc.want)
			}
			if s.seq != 0 {
				t.Fatalf("seq after failed append = %d, want 0", s.seq)
			}
		})
	}
}

// TestStoreAppendRollbackFailure pins the append-failure path where the rollback
// itself faults: the returned error names both the original write failure and
// the specific rollback step (truncate/seek/sync) that could not complete.
func TestStoreAppendRollbackFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rollbackOp int
		want       string
	}{
		{name: "truncate", rollbackOp: 6, want: "rollback failed: truncate to 0"},
		{name: "seek", rollbackOp: 7, want: "rollback failed: seek eof"},
		{name: "sync", rollbackOp: 8, want: "rollback failed: sync rollback truncate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// op 5 is the append write (fails), the rollback op then also faults.
			s := faultStore(t, planFaultAt(5, tc.rollbackOp))
			err := s.Append(Event{Kind: EventJobStarted, JobID: "job_A"})
			if err == nil || !strings.Contains(err.Error(), "write event") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rollback-failure error = %v, want write event + %q", err, tc.want)
			}
		})
	}
}

// TestStoreAppendBatchSurfacesFilesystemFaults pins the seek/write/sync error
// arms of AppendBatch and confirms the whole batch rolls back (seq unchanged).
func TestStoreAppendBatchSurfacesFilesystemFaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   int
		want string
	}{
		{name: "seek", op: 4, want: "seek append start"},
		{name: "write", op: 5, want: "write event"},
		{name: "sync", op: 7, want: "sync event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := faultStore(t, planFaultAt(tc.op))
			err := s.AppendBatch([]Event{
				{Kind: EventJobStarted, JobID: "job_A"},
				{Kind: EventJobStarted, JobID: "job_B"},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("batch fault error = %v, want %q", err, tc.want)
			}
			if s.seq != 0 {
				t.Fatalf("seq after failed batch = %d, want 0", s.seq)
			}
		})
	}
}

// openFsUnderFault writes content to a base MemMapFs, wraps it in the schedule,
// and returns the openFs error (the store is discarded on success).
func openFsUnderFault(t *testing.T, content string, s *fault.Schedule) error {
	t.Helper()
	base := afero.NewMemMapFs()
	if err := afero.WriteFile(base, "/jobs.jsonl", []byte(content), 0o644); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	st, err := openFs(fault.FS(base, s), "/jobs.jsonl")
	if err == nil {
		_ = st.Close()
	}
	return err
}

// TestStoreOpenTrailingRecoveryFaults pins the error arms of the trailing-line
// recovery a store runs at open when the last line is a COMPLETE event missing
// its newline: the inspect (stat/open/seek/read/read-whole) probes, the finish
// (seek/terminate/sync) writes, and the subsequent full read (open/scan) each
// fault at a known step and surface as a wrapped open error.
func TestStoreOpenTrailingRecoveryFaults(t *testing.T) {
	// event1 terminated; event2 complete but WITHOUT a trailing newline.
	content := `{"kind":"job_started","seq":1,"job_id":"job_A"}` + "\n" +
		`{"kind":"job_started","seq":2,"job_id":"job_B"}`
	for _, tc := range []struct {
		name string
		op   int
		want string
	}{
		{name: "inspect_stat", op: 1, want: "stat /jobs.jsonl"},
		{name: "inspect_open", op: 2, want: "inspect /jobs.jsonl"},
		{name: "inspect_seek", op: 3, want: "inspect trailing byte"},
		{name: "inspect_read", op: 4, want: "read trailing byte"},
		{name: "read_whole", op: 5, want: "read /jobs.jsonl"},
		{name: "finish_seek", op: 8, want: "seek after trailing recovery"},
		{name: "finish_terminate", op: 9, want: "terminate trailing event"},
		{name: "finish_sync", op: 10, want: "sync trailing recovery"},
		{name: "readall_open", op: 11, want: "read /jobs.jsonl"},
		{name: "readall_scan", op: 12, want: "scan /jobs.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := openFsUnderFault(t, content, planFaultAt(tc.op))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("open fault error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestStoreLoadReadFaultsPropagate pins the readAllLocked-error propagation arm
// of every Load* reader: an fs fault while a reader re-reads the log surfaces as
// a wrapped error rather than a partial or empty fold. Each reader gets a fresh
// store seeded with one event; a clean open consumes fs ops 0..7, so faulting op
// 8 trips the reader's first read.
func TestStoreLoadReadFaultsPropagate(t *testing.T) {
	const seeded = `{"kind":"job_started","seq":1,"job_id":"job_A"}` + "\n"
	newReaderStore := func(t *testing.T) *Store {
		t.Helper()
		base := afero.NewMemMapFs()
		if err := afero.WriteFile(base, "/jobs.jsonl", []byte(seeded), 0o644); err != nil {
			t.Fatalf("seed store: %v", err)
		}
		st, err := openFs(fault.FS(base, planFaultAt(8)), "/jobs.jsonl")
		if err != nil {
			t.Fatalf("openFs: %v", err)
		}
		return st
	}
	for _, tc := range []struct {
		name string
		read func(*Store) error
	}{
		{name: "Load", read: func(s *Store) error { _, err := s.Load(); return err }},
		{name: "LoadOrdered", read: func(s *Store) error { _, err := s.LoadOrdered(); return err }},
		{name: "LoadDelegates", read: func(s *Store) error { _, err := s.LoadDelegates(); return err }},
		{name: "LoadWatches", read: func(s *Store) error { _, err := s.LoadWatches(); return err }},
		{name: "LoadWatchSends", read: func(s *Store) error { _, err := s.LoadWatchSends(); return err }},
		{name: "LoadGrants", read: func(s *Store) error { _, err := s.LoadGrants(); return err }},
		{name: "LoadEvents", read: func(s *Store) error { _, err := s.LoadEvents(); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newReaderStore(t)
			if err := tc.read(s); err == nil || !strings.Contains(err.Error(), "stat /jobs.jsonl") {
				t.Fatalf("%s read fault = %v, want wrapped stat error", tc.name, err)
			}
		})
	}
}

// TestStoreOpenTruncateRecoveryFaults pins the error arms of the destructive
// trailing-recovery a store runs when the last line is an INCOMPLETE (torn)
// event: truncating the partial away, seeking back to the end, and fsyncing.
func TestStoreOpenTruncateRecoveryFaults(t *testing.T) {
	// event1 terminated; event2 truncated mid-object (torn in-flight append).
	content := `{"kind":"job_started","seq":1,"job_id":"job_A"}` + "\n" +
		`{"kind":"job_finished","seq":2,`
	for _, tc := range []struct {
		name string
		op   int
		want string
	}{
		{name: "truncate", op: 8, want: "truncate trailing partial line"},
		{name: "seek", op: 9, want: "seek after trailing recovery"},
		{name: "sync", op: 10, want: "sync trailing recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := openFsUnderFault(t, content, planFaultAt(tc.op))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("truncate-recovery fault error = %v, want %q", err, tc.want)
			}
		})
	}
}

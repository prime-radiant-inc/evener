package jobstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

var errFailedSync = errors.New("jobstore: injected fsync failure")

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// syncFailFS wraps a filesystem and fails the next n Sync calls, so a test can
// drive the store's failed-append rollback: one failure for the append being
// rolled back, and none for the rollback's own fsync or the reads after it.
//
// beforeFail, when set, runs inside the failing Sync before it returns. A test
// that holds it there keeps the append's bytes on disk, uncommitted and with
// the store's lock still held, which is the exact window a reader of the
// journal itself has to respect.
type syncFailFS struct {
	afero.Fs
	failures   int
	beforeFail func()
	// opens counts Open calls, the one read the store performs outside its own
	// file handle. A test uses it to prove the truncation repair is confined to
	// the truncation path: an ordinary boundary read opens nothing.
	opens atomic.Int32
}

func (fs *syncFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &syncFailFile{File: f, fs: fs}, nil
}

func (fs *syncFailFS) Open(name string) (afero.File, error) {
	fs.opens.Add(1)
	f, err := fs.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return &syncFailFile{File: f, fs: fs}, nil
}

type syncFailFile struct {
	afero.File
	fs *syncFailFS
}

func (f *syncFailFile) Sync() error {
	if f.fs.failures > 0 {
		f.fs.failures--
		if f.fs.beforeFail != nil {
			f.fs.beforeFail()
		}
		return errFailedSync
	}
	return f.File.Sync()
}

// pauseSyncFail returns the channel that closes when the failing fsync is
// entered, and an idempotent release, and arms the filesystem to fail exactly
// that one sync.
//
// The release must be reachable from cleanup: a paused append holds the store's
// lock, so a failing assertion that leaves it paused deadlocks on the store's
// own Close and the failure never reports. Every test that pauses a sync
// registers the release as cleanup.
func pauseSyncFail(fs *syncFailFS) (entered <-chan struct{}, release func()) {
	inSync := make(chan struct{})
	done := make(chan struct{})
	var enterOnce, doneOnce sync.Once
	fs.beforeFail = func() {
		enterOnce.Do(func() { close(inSync) })
		<-done
	}
	fs.failures = 1
	return inSync, func() { doneOnce.Do(func() { close(done) }) }
}

// TestCommittedJournalExcludesARolledBackAppend pins the boundary
// CommittedJournal reports: the length it returns is always a length the store
// acknowledged, so an append whose fsync fails — and which the store therefore
// truncates back — can never be observed past it.
//
// This is the property an incremental reader of a live journal depends on. Its
// scan reads the file itself, so without a boundary it would fold a start whose
// append had not committed; a failed fsync then removes the bytes and emits no
// revision, leaving a page that showed a job no committed state contains.
//
// The fixture is a real file under t.TempDir(), because the scan opens the path
// through the os package: an in-memory filesystem here would leave the scan
// reading a host path that holds nothing, and the assertion below would pass
// without ever decoding the uncommitted bytes it exists to exclude.
func TestCommittedJournalExcludesARolledBackAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &syncFailFS{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Registered before the pause so the pause's release (registered later,
	// therefore run earlier) has already unblocked any in-flight append by the
	// time the store closes.
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_committed", Type: JobShell}); err != nil {
		t.Fatalf("append committed event: %v", err)
	}
	snapshot, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	committed := snapshot.Size
	if committed == 0 {
		t.Fatal("CommittedJournal reported an empty journal after a committed append")
	}

	// The next append writes its line and then waits in its failing fsync, so
	// the uncommitted bytes are on disk while the scan below runs.
	inSync, releaseSync := pauseSyncFail(fs)
	t.Cleanup(releaseSync)
	appendErr := make(chan error, 1)
	go func() {
		appendErr <- s.Append(Event{Kind: EventJobStarted, JobID: "job_uncommitted"})
	}()
	<-inSync
	if size := int64(len(mustReadFile(t, path))); size <= committed {
		t.Fatalf("journal is %d bytes while an append waits in fsync, want more than the committed %d", size, committed)
	}

	// Bounded at the committed length, the scan sees exactly the committed job
	// even though the uncommitted line is present and complete on disk. The
	// unbounded scan of the same file does decode it, which is what proves this
	// fixture is reading the file at all.
	assertCommittedOnly := func(when string) {
		t.Helper()
		events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: committed, Bounded: true})
		if err != nil {
			t.Fatalf("bounded scan %s: %v", when, err)
		}
		if len(events) != 1 || events[0].JobID != "job_committed" {
			t.Fatalf("bounded scan %s = %+v, want exactly job_committed", when, events)
		}
	}
	assertCommittedOnly("while the append waits in fsync")
	unbounded, err := ScanEvents(t.Context(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("unbounded scan: %v", err)
	}
	if len(unbounded) != 2 {
		t.Fatalf("unbounded scan = %d events, want the in-flight bytes visible at 2", len(unbounded))
	}

	releaseSync()
	if err := <-appendErr; err == nil {
		t.Fatal("append with a failing fsync succeeded")
	}

	afterSnapshot, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after failed append: %v", err)
	}
	after := afterSnapshot.Size
	if after != committed {
		t.Fatalf("committed length = %d after a rolled-back append, want the unchanged %d", after, committed)
	}
	if size := int64(len(mustReadFile(t, path))); size != committed {
		t.Fatalf("journal is %d bytes after the rollback, want the committed %d", size, committed)
	}
	assertCommittedOnly("after the rollback")

	records, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if records["job_uncommitted"] != nil {
		t.Fatal("a rolled-back append reached the store's fold")
	}
	if records["job_committed"] == nil {
		t.Fatal("the committed job is missing from the store's fold")
	}
}

// TestCommittedJournalAtAnEmptyPrefixIsEmpty pins the empty-prefix half of the
// same boundary: a committed length of zero is EOF, not "read the file". The
// first append to a journal opened empty is precisely the append a cold reader
// must not fold, and a boundary that a cold read reported as zero would
// otherwise admit it and cache a row that rolls back.
func TestCommittedJournalAtAnEmptyPrefixIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &syncFailFS{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	snapshot, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	committed := snapshot.Size
	if committed != 0 {
		t.Fatalf("committed length of a fresh journal = %d, want 0", committed)
	}

	inSync, releaseSync := pauseSyncFail(fs)
	t.Cleanup(releaseSync)
	appendErr := make(chan error, 1)
	go func() {
		appendErr <- s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell})
	}()
	<-inSync

	// The zero boundary is authoritative: the first append's bytes are on disk
	// and must not be folded.
	assertEmpty := func(when string) {
		t.Helper()
		events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: committed, Bounded: true})
		if err != nil {
			t.Fatalf("bounded scan %s: %v", when, err)
		}
		if len(events) != 0 {
			t.Fatalf("bounded scan %s = %+v, want no events", when, events)
		}
	}
	assertEmpty("while the first append waits in fsync")
	// Without Bounded, zero is the historical unbounded read, and it does see
	// the in-flight line: the distinction is the fix, not an artifact.
	unbounded, err := ScanEvents(t.Context(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("unbounded scan: %v", err)
	}
	if len(unbounded) != 1 || unbounded[0].JobID != "job_first" {
		t.Fatalf("unbounded scan = %+v, want the in-flight job_first", unbounded)
	}

	releaseSync()
	if err := <-appendErr; err == nil {
		t.Fatal("append with a failing fsync succeeded")
	}
	assertEmpty("after the rollback")

	// A committed append moves the boundary and is folded, so the empty prefix
	// is not a permanent refusal.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_real", Type: JobShell}); err != nil {
		t.Fatalf("append after rollback: %v", err)
	}
	movedSnapshot, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after append: %v", err)
	}
	moved := movedSnapshot.Size
	events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: moved, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan at the moved boundary: %v", err)
	}
	if len(events) != 1 || events[0].JobID != "job_real" {
		t.Fatalf("bounded scan = %+v, want the committed job_real alone", events)
	}
}

// TestCommittedJournalNoticesARewriteBeforeAnAppend pins the ordering the
// boundary cannot recover after the fact. A same-length rewrite followed by a
// successful append leaves a file that is longer than the last sample with a
// moved mtime — indistinguishable from this store's own growth, because the
// append is what moved both numbers. The content check therefore runs BEFORE
// the store writes: by the time the next activity read looks, the evidence is
// gone.
//
// Without it, a warm fold resumes at its old offset over the replaced prefix:
// it keeps a job the rewrite removed and never sees the one that replaced it.
func TestCommittedJournalNoticesARewriteBeforeAnAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_original", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Rewrite in place to the same length, then append through the store. Both
	// steps happen before any activity read.
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before rewrite: %v", err)
	}
	raw := mustReadFile(t, path)
	was := []byte("job_original")
	now := []byte("job_replaced")
	if len(was) != len(now) || !bytes.Contains(raw, was) {
		t.Fatalf("fixture does not contain %q", was)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(raw, was, now), 0o644); err != nil {
		t.Fatalf("rewrite in place: %v", err)
	}
	// Filesystem timestamps are coarser than this test is fast: a rewrite landing
	// in the same clock tick as the store's last append leaves the length and the
	// modification time untouched, which is the documented same-size-same-mtime
	// boundary rather than the out-of-band rewrite this test stages. Give the
	// rewrite a time the store can resolve.
	bumpMTimePast(t, path, beforeInfo.ModTime())
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_appended", Type: JobShell}); err != nil {
		t.Fatalf("append after rewrite: %v", err)
	}

	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after rewrite and append: %v", err)
	}
	if after.Generation == before.Generation {
		t.Fatalf("generation = %d after a rewrite then append, want it to advance from %d", after.Generation, before.Generation)
	}
	// A fold that resumed at before.Size would miss the replacement entirely,
	// so the whole journal is what this generation forces the reader to fold.
	events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: after.Size, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan: %v", err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.JobID)
	}
	want := []string{"job_replaced", "job_appended"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("bounded scan = %v, want %v", ids, want)
	}

	// The same rewrite, but LONGER than the file the fold consumed. Growth on
	// its own reads as this store's own append, so a fold would resume at the
	// recorded offset: it would keep the rows the rewrite removed and decode the
	// rest from the middle of a record, which is worse than a wrong answer —
	// it is a decode of bytes that are not a record at all.
	longer := append(mustReadFile(t, path), []byte(`{"kind":"job_started","seq":900,"ts":"2026-01-01T00:00:00Z","job_id":"job_grown","type":"shell"}`+"\n")...)
	if err := os.WriteFile(path, longer, 0o644); err != nil {
		t.Fatalf("rewrite longer: %v", err)
	}
	grown, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after a longer rewrite: %v", err)
	}
	if grown.Generation == after.Generation {
		t.Fatalf("generation = %d after a longer rewrite, want it to advance from %d: growth the store did not write is not an append", grown.Generation, after.Generation)
	}
	grownEvents, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: grown.Size, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan after a longer rewrite: %v", err)
	}
	if len(grownEvents) != len(ids)+1 {
		t.Fatalf("bounded scan after a longer rewrite decoded %d records, want the %d the file holds", len(grownEvents), len(ids)+1)
	}
}

// TestCommittedJournalNoticesAReplacedJournalPath pins the other identity: the
// store holds an open handle, but the activity reader scans the PATH. Replacing
// the path (remove, then create) leaves the handle describing the old file
// while the path names a new one, so a stat of the handle reports the wrong
// length for the bytes a scan will read, and a warm fold keeps serving rows the
// path no longer has.
func TestCommittedJournalNoticesAReplacedJournalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// The replacement is SHORTER, so a reader bounded by the old handle would
	// fold past the end of what the path holds.
	line := []byte(`{"kind":"job_started","seq":1,"ts":"2026-01-01T00:00:00Z","job_id":"job_new","type":"shell"}` + "\n")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(path, line, 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after a replaced path: %v", err)
	}
	if after.Generation == before.Generation {
		t.Fatalf("generation = %d after the path was replaced, want it to advance from %d", after.Generation, before.Generation)
	}
	if after.Size > before.Size {
		t.Fatalf("committed length = %d for a %d-byte replacement, want the named journal's own length", after.Size, len(line))
	}
	events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: after.Size, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan: %v", err)
	}
	if len(events) != 1 || events[0].JobID != "job_new" {
		t.Fatalf("bounded scan = %+v, want exactly job_new", events)
	}
}

// TestCommittedJournalKeepsTheGenerationAcrossACleanRollback pins what a
// rollback is: a restoration, not a change. A failed append is truncated back
// to the committed prefix the reader was already shown, so the rows, their
// order and the paging positions are exactly what they were. The file's
// modification time moves anyway, and treating that as a rewrite would make
// every failed append rebuild the fold and refuse a continuation that nothing
// invalidated.
func TestCommittedJournalKeepsTheGenerationAcrossACleanRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &syncFailFS{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_committed", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if before.Size == 0 {
		t.Fatal("fixture committed nothing")
	}

	inSync, releaseSync := pauseSyncFail(fs)
	t.Cleanup(releaseSync)
	appendErr := make(chan error, 1)
	go func() {
		appendErr <- s.Append(Event{Kind: EventJobStarted, JobID: "job_rolled_back", Type: JobShell})
	}()
	<-inSync
	releaseSync()
	if err := <-appendErr; err == nil {
		t.Fatal("append with a failing fsync succeeded")
	}

	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after a clean rollback: %v", err)
	}
	if after.Size != before.Size {
		t.Fatalf("committed length = %d after a rollback, want the restored %d", after.Size, before.Size)
	}
	if after.Generation != before.Generation {
		t.Fatalf("generation = %d after a rollback that restored the committed prefix, want it to stay %d", after.Generation, before.Generation)
	}
}

// TestCommittedJournalRepairsATornTailBeforeReportingIt pins the truncation half
// of the committed boundary: a journal cut mid-record still has the partial
// record's bytes, and a scanner omits a line it cannot decode while leaving
// those bytes on disk. Reporting that length as committed hands a reader a
// boundary past a record nobody can decode, and the next Append joins a new
// event onto the fragment and fsyncs one malformed line — after which every
// read of this journal fails on it.
//
// The repair is the store's existing locked tail recovery, run before the
// boundary is reported. It belongs on this path and nowhere else, so the test
// also counts opens: an ordinary boundary read over an untouched journal opens
// the file zero times, which is the incremental path this boundary exists to
// keep cheap.
// TestCommittedJournalLeavesTheWriterObligationOnAnUnsettledTail pins High 6.
//
// A foreign writer tears the tail. A boundary read notices the change and runs
// the repair — and in that moment the name answers ENOENT, because a
// rename-and-replace is between its two steps. The repair returns without an
// error and without having examined anything, and the read still answers with
// the bounded view it can decode, which is what an activity page needs.
//
// Proceeding is not the same as being settled. The baseline this call records
// is the store's claim that it knows where its tail is, and the next append
// consults it to decide whether to inspect the tail at all: it compares the file
// against that baseline, finds it unchanged, skips the repair, and joins its
// event onto the fragment. One malformed line is fsynced where two records
// should be, and every later read and reopen fails on it.
//
// So a tail this call could not settle leaves the baseline unsampled. The read
// still answers; the writer still owes the repair.
func TestCommittedJournalLeavesTheWriterObligationOnAnUnsettledTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &vanishOnNthStatFs{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_before", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	partial := []byte(`{"kind":"job_started","seq":`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("tear the tail: %v", err)
	}
	torn, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the torn journal: %v", err)
	}
	// The boundary read's first stat of the name notices the change; the second
	// is the repair's own recheck, and vanishing there is the case that returns
	// settled=false with no error. Faulting the first would take a different
	// branch — the journal is simply absent — and the append below would then
	// repair for the wrong reason.
	fs.vanishOnNthStat(2)

	state, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("boundary read across a vanished tail recheck: %v", err)
	}
	if state.Size != torn.Size() {
		t.Fatalf("boundary size = %d, want the %d bytes the read could decode", state.Size, torn.Size())
	}

	// The corruption is the assertion. An unsampled baseline makes this append
	// examine the tail and cut the fragment first, so the write lands on a clean
	// end and the journal stays readable.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_torn", Type: JobShell}); err != nil {
		t.Fatalf("append after an unsettled boundary read: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the journal after that append: %v (the boundary read recorded a baseline it never examined, so the append joined the fragment)", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("fold the reopened journal: %v (the boundary read recorded a baseline it never examined, so the append joined the fragment)", err)
	}
	if records["job_before"] == nil {
		t.Fatal("the record appended before the tear is missing from the reopened journal")
	}
	if records["job_after_torn"] == nil {
		t.Fatal("the append after the tear is missing from the reopened journal")
	}
}

func TestCommittedJournalRepairsATornTailBeforeReportingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &syncFailFS{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_kept", Type: JobShell}); err != nil {
		t.Fatalf("append kept job: %v", err)
	}

	// The ordinary read path: the file is exactly what the store left, so the
	// boundary comes from a stat and the repair never runs.
	fs.opens.Store(0)
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if got := fs.opens.Load(); got != 0 {
		t.Fatalf("an ordinary boundary read opened the journal %d times, want 0: recovery must not run on this path", got)
	}

	// Cut the journal mid-record, the way a foreign truncation of a live file
	// can leave it, and let the store observe the shorter file.
	raw := mustReadFile(t, path)
	torn := int64(len(raw) - 12)
	if torn <= 0 {
		t.Fatalf("fixture journal is %d bytes, too short to cut a record from", len(raw))
	}
	if err := os.Truncate(path, torn); err != nil {
		t.Fatalf("truncate mid-record: %v", err)
	}
	repaired, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal over a torn tail: %v", err)
	}
	// The repair must happen WITHOUT opening the path. This is the stricter
	// half of the same property, not a relaxation of it: the evidence for a cut
	// and the cut itself have to be the same file, and a repair that reads the
	// name and then truncates the held handle can compute an offset from one
	// file and apply it to another — erasing a moved-aside journal that had
	// committed every byte. Repairing through the store's own handle makes that
	// unrepresentable, and the count below is what proves it did.
	openedOnTruncation := fs.opens.Load() > 0
	if repaired.Size >= torn {
		t.Fatalf("committed length = %d after a truncation to %d, want the torn record removed", repaired.Size, torn)
	}
	if openedOnTruncation {
		t.Fatalf("the truncation path opened the journal %d times through the path; recovery must inspect and repair the store's own handle, or it can cut a different file than the one it measured", fs.opens.Load())
	}
	if size := int64(len(mustReadFile(t, path))); size != repaired.Size {
		t.Fatalf("journal is %d bytes, want the reported committed %d", size, repaired.Size)
	}

	// The point of the repair: the next append stands on its own, so both
	// records read back and the journal is still appendable at all.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_repair", Type: JobShell}); err != nil {
		t.Fatalf("append after torn-tail repair: %v", err)
	}
	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after append: %v", err)
	}
	// An ordinary append after the repair is an ordinary read again: the
	// generation the truncation advanced is not advanced a second time, so a
	// fold built on the repaired journal keeps being extended instead of being
	// rebuilt on every later read.
	if after.Generation != repaired.Generation {
		t.Fatalf("generation = %d after an append to the repaired journal, want it to stay %d", after.Generation, repaired.Generation)
	}
	events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: after.Size, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan after repair and append: %v", err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.JobID)
	}
	if len(ids) != 1 || ids[0] != "job_after_repair" {
		t.Fatalf("bounded scan after repair = %v, want exactly the standalone job_after_repair", ids)
	}
	records, err := s.Load()
	if err != nil {
		t.Fatalf("Load after repair: %v", err)
	}
	if records["job_after_repair"] == nil {
		t.Fatal("the appended job is missing from the store's fold")
	}
	// Confinement, checked last so the damage above is what a regression reports
	// first: the repair runs on the truncation path and never on an ordinary
	// read, which the steady-state read at the top of this test counted.

	// The same repair on a journal that GREW without this store writing the
	// growth. A writer that dies mid-record leaves the file longer than the last
	// length the store accounted for and ends in a partial record, which is
	// indistinguishable from an append by length and time alone. Reporting that
	// length as committed hands the reader a boundary past a record nobody can
	// decode, and the next Append joins its event onto the fragment.
	partial := []byte(`{"kind":"job_started","seq":`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("append a partial record: %v", err)
	}
	grown, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal over a grown torn tail: %v", err)
	}
	if size := int64(len(mustReadFile(t, path))); size != grown.Size {
		t.Fatalf("journal is %d bytes after a boundary read, want the reported committed %d: the partial record must be repaired", size, grown.Size)
	}
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_growth", Type: JobShell}); err != nil {
		t.Fatalf("append after grown torn-tail repair: %v", err)
	}

	// The same torn tail with NO boundary read in between. Repairing only when
	// something read the file first would leave the worst ordering unguarded: a
	// foreign write tears the tail, and the store's own next append is what
	// arrives next. Appending at EOF would join the event onto the fragment and
	// fsync one malformed line, reporting success for an event no reader can
	// decode. Nothing reads this journal before the append below.
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("tear the tail again: %v", err)
	}
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_appended_to_torn", Type: JobShell}); err != nil {
		t.Fatalf("append onto a torn tail before any read repaired it: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The damage this repair prevents is durable, so a reopen is the assertion
	// that matters: without the repair the appended event is joined onto the
	// fragment, and this open fails on the malformed line it produced.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after appending to a grown torn tail: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	finalRecords, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	if finalRecords["job_after_growth"] == nil {
		t.Fatal("the append after the grown torn tail is missing from the reopened store's fold")
	}

	// The append that arrived on an unrepaired tail is readable after the same
	// reopen: it stands alone rather than sharing a line with the fragment.
	if finalRecords["job_appended_to_torn"] == nil {
		t.Fatal("the append that arrived on a torn tail is missing from the reopened store's fold")
	}

}

// TestStoreReadsANonemptyReplacementOverAnEmptyHandle pins the case where a live
// store holds an empty journal and the name is then replaced by a valid,
// nonempty one.
//
// Recovery inspects the tail of the file it HOLDS, so it has to ask what that
// file's length is. Asking the PATH instead reports the replacement's positive
// length, and seeking one byte before the end of the empty handle fails — the
// reader then refuses a healthy journal it never needed to repair, and keeps
// refusing until a writer happens to adopt the replacement.
func TestStoreReadsANonemptyReplacementOverAnEmptyHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed empty journal: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Another writer moves the empty journal aside and installs a valid one.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	replacement := []byte(`{"kind":"job_started","seq":1,"job_id":"job_replacement"}` + "\n")
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents over a valid replacement of an empty journal: %v", err)
	}
	if len(events) != 1 || events[0].JobID != "job_replacement" {
		t.Fatalf("LoadEvents = %#v, want the replacement's single event", events)
	}
}

// TestCommittedJournalLeavesAReplacedJournalAlone pins what a replacement is:
// a different file. The store's handle still refers to the journal that was
// moved aside, and its recovery truncates that handle, so repairing on this
// path edits the saved copy while leaving the replacement torn. The identity a
// boundary read has to judge first is therefore whether the name still refers
// to the file the store holds.
func TestCommittedJournalLeavesAReplacedJournalAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_saved", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Another writer leaves a partial record on the journal, then the journal is
	// moved aside and a shorter, torn replacement is created under the name.
	partial := []byte(`{"kind":"job_started","seq":`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("append a partial record: %v", err)
	}
	saved := filepath.Join(dir, "jobs.jsonl.saved")
	if err := os.Rename(path, saved); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	savedBytes := mustReadFile(t, saved)
	replacement := []byte(`{"kind":"job_started","seq":1,"ts":"2026-01-01T00:00:00Z","job_id":"job_torn"`)
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal over a replaced journal: %v", err)
	}
	if after.Generation == before.Generation {
		t.Fatalf("generation = %d after the name was replaced, want it to advance from %d", after.Generation, before.Generation)
	}
	if got := mustReadFile(t, saved); !bytes.Equal(got, savedBytes) {
		t.Fatalf("the moved-aside journal is %d bytes, want the %d it was left with: recovery must not truncate a file the name no longer reaches", len(got), len(savedBytes))
	}
	// One replacement is one generation: a second read of the same replacement
	// must not look like a second change, or every poll rebuilds the fold.
	again, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("second CommittedJournal: %v", err)
	}
	if again.Generation != after.Generation {
		t.Fatalf("generation = %d on a second read of the same replacement, want it to stay %d", again.Generation, after.Generation)
	}

	// And an append follows the NAME. The store held the file that was moved
	// aside; writing there would put the event where no reader looks. The
	// replacement is a journal, so it is adopted, its sequence continues, and
	// the record must be readable from the named path and absent from the copy.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_replacement", Type: JobShell}); err != nil {
		t.Fatalf("append to a replaced journal: %v", err)
	}
	named := mustReadFile(t, path)
	if !bytes.Contains(named, []byte("job_after_replacement")) {
		t.Fatalf("the named journal does not hold the appended record: %q", named)
	}
	if bytes.Contains(mustReadFile(t, saved), []byte("job_after_replacement")) {
		t.Fatal("the append landed in the moved-aside journal, which no reader reads")
	}
	// The sequences continue from the replacement rather than restarting, so the
	// adopted journal is a well-formed log and reopening it folds every record.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the adopted journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	folded, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load the adopted journal: %v", err)
	}
	if folded["job_after_replacement"] == nil {
		t.Fatal("the appended record is missing from the adopted journal's fold")
	}
}

// TestCommittedJournalRefusesAnUnreadableReplacement pins the backstop. A
// replacement that is not a journal cannot be adopted: appending after a record
// no reader can parse would corrupt the file the name now reaches, and writing
// to the file the store holds instead would put the event where no reader looks.
// The append must fail with the typed error, and neither file may gain bytes.
func TestCommittedJournalRefusesAnUnreadableReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_saved", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	saved := filepath.Join(dir, "jobs.jsonl.saved")
	if err := os.Rename(path, saved); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	savedBytes := mustReadFile(t, saved)
	garbage := []byte("this is not a journal\n")
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatalf("write garbage replacement: %v", err)
	}

	err = s.Append(Event{Kind: EventJobStarted, JobID: "job_refused", Type: JobShell})
	if !errors.Is(err, ErrJournalReplaced) {
		t.Fatalf("append to an unreadable replacement = %v, want ErrJournalReplaced", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, garbage) {
		t.Fatalf("the replacement is %q, want it unchanged: a refused append must write nothing", got)
	}
	if got := mustReadFile(t, saved); !bytes.Equal(got, savedBytes) {
		t.Fatalf("the moved-aside journal is %d bytes, want the %d it was left with", len(got), len(savedBytes))
	}

	// A replacement may be unreadable in the middle AND end in a fragment. The
	// fragment is recoverable and the bad record is not, so the refusal must be
	// decided by the bad record — and decided BEFORE anything cuts the fragment.
	// Otherwise refusing an unreadable journal has already rewritten it, and the
	// caller cannot tell a rejected candidate from a repaired one.
	badThenTorn := []byte(
		`{"kind":"job_started","seq":1,"job_id":"job_ok"}` + "\n" +
			`this record is not json` + "\n" +
			`{"kind":"job_started","seq":3,"job_id":"job_cut`)
	if err := os.WriteFile(path, badThenTorn, 0o644); err != nil {
		t.Fatalf("write bad-then-torn replacement: %v", err)
	}
	err = s.Append(Event{Kind: EventJobStarted, JobID: "job_refused", Type: JobShell})
	if !errors.Is(err, ErrJournalReplaced) {
		t.Fatalf("append over an unreadable record before a torn fragment = %v, want ErrJournalReplaced", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, badThenTorn) {
		t.Fatalf("the refused replacement is %d bytes, want the %d it was written with: validation must not repair a file it rejects", len(got), len(badThenTorn))
	}
}

// TestStoreAppendRefusesWhileTheJournalNameIsMissing pins what a successful
// append promises: the event is readable from the journal's name.
//
// A rename-and-replace is not atomic. Between the rename and the creation of
// the replacement the name reaches nothing, and an append that took the handle
// it still holds would write to the file that was moved aside — reporting
// success for a job completion that never reaches the journal activity and
// reopen read. That window is transient, so the append must fail with a
// retryable error and leave the event writable, not succeed somewhere no
// reader looks.
func TestStoreAppendRefusesWhileTheJournalNameIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_before", Type: JobShell}); err != nil {
		t.Fatalf("append before the gap: %v", err)
	}

	// Open the gap: the file is renamed aside and the replacement is not yet
	// installed, so the name reaches nothing.
	saved := filepath.Join(dir, "jobs.jsonl.saved")
	if err := os.Rename(path, saved); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	savedBytes := mustReadFile(t, saved)
	err = s.Append(Event{Kind: EventJobStarted, JobID: "job_in_gap", Type: JobShell})
	if !errors.Is(err, ErrJournalNameUnavailable) {
		t.Fatalf("append while the name is missing = %v, want ErrJournalNameUnavailable", err)
	}
	// The handle the store still holds must gain nothing: the event went
	// nowhere, which is why the caller may retry it.
	if got := mustReadFile(t, saved); !bytes.Equal(got, savedBytes) {
		t.Fatalf("the moved-aside journal is %d bytes, want the %d it was left with: an unnameable append must write nothing", len(got), len(savedBytes))
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("stat of the missing name = %v, want the file to still be absent: the refused append must not create it", statErr)
	}

	// The replacement lands and the retry succeeds into the named journal.
	replacement := []byte(`{"kind":"job_started","seq":1,"ts":"2026-01-01T00:00:00Z","job_id":"job_replacement","type":"shell"}` + "\n")
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatalf("install replacement: %v", err)
	}
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_in_gap", Type: JobShell}); err != nil {
		t.Fatalf("retry after the name returned: %v", err)
	}
	named := mustReadFile(t, path)
	if !bytes.Contains(named, []byte("job_in_gap")) {
		t.Fatalf("the named journal does not hold the retried record: %q", named)
	}
	if got := mustReadFile(t, saved); !bytes.Equal(got, savedBytes) {
		t.Fatal("the retry landed in the moved-aside journal, which no reader reads")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the adopted journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	folded, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load the adopted journal: %v", err)
	}
	if folded["job_in_gap"] == nil {
		t.Fatal("the retried record is missing from the adopted journal's fold")
	}
}

// TestStoreAppendKeepsTheHandleItWasGiven is the fuzz seed
// FuzzJobstoreLifecycleProgram/seed#3 reduced to a named case.
//
// A store can be constructed around a handle rather than opened from a path:
func TestStoreAppendKeepsTheHandleItWasGiven(t *testing.T) {
	const path = "/held.jsonl"
	base := afero.NewMemMapFs()
	held, err := base.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open the held handle: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	s := &Store{path: path, fs: base, f: held}
	// No Open, no sample: s.named is nil, which is the state this test is about.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_held", Type: JobShell}); err != nil {
		t.Fatalf("append through the held handle: %v", err)
	}
	if got, readErr := afero.ReadFile(base, path); readErr != nil {
		t.Fatalf("read the journal: %v", readErr)
	} else if !bytes.Contains(got, []byte("job_held")) {
		t.Fatalf("the journal the store was given holds %q, want the appended record", got)
	}
	if s.f != held {
		t.Fatal("the append replaced the handle it was given with a fresh open of the path")
	}
}

// TestCommittedJournalReportsAConfirmedAppendPastLaggingMetadata pins what a
// boundary owes a reader when the filesystem's attributes trail its data.
//
// An append fsyncs the bytes and then stats the path. A filesystem under
// attribute caching — NFS is the everyday case — can answer that stat with the
// length the file had BEFORE the write. The append knows its own committed end
// from the bytes it wrote, so the boundary must report that end. Reporting the
// lagging length instead makes a job that just committed invisible to live
// activity until the attributes catch up, which is exactly the window a hot
// path is read in.
//
// The reconciliation is one-directional: the append's end may raise the bound
// it just wrote, never lower a longer length, and never survive a replacement.
func TestCommittedJournalReportsAConfirmedAppendPastLaggingMetadata(t *testing.T) {
	// The store's own filesystem may be anything, but the bounded scan under
	// test opens the journal itself, so the journal has to be a real file: an
	// in-memory filesystem would leave the scan reading a path the OS never
	// sees, and the assertion below would pass without reading the fixture.
	base := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// Freeze the metadata at the length the file has now, so every later stat
	// answers with it however much the file grows.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_committed", Type: JobShell}); err != nil {
		t.Fatalf("append under lagging metadata: %v", err)
	}
	committed, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	actual, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if committed.Size != int64(len(actual)) {
		t.Fatalf("committed length = %d with %d bytes on disk: an append the store confirmed must not be hidden by lagging metadata", committed.Size, len(actual))
	}
	// The record the boundary now covers is the one that just committed.
	events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: committed.Size, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan: %v", err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.JobID)
	}
	if len(ids) != 2 {
		t.Fatalf("bounded scan returned %v, want both committed jobs", ids)
	}
}

// TestStoreAppendKeepsOneGenerationUnderSustainedLag pins the append half of the
// lagging-metadata rule.
//
// A confirmed append records its own end, and a filesystem whose attributes lag
// keeps reporting a smaller length for as long as the lag lasts. The next
// append's pre-write check compares the reported length against the recorded
// end, so a raw comparison reads "the file changed" on every append during the
// lag — and each one advances the content generation, which invalidates the
// fold. A session that appends and refreshes repeatedly therefore refolds its
// whole history after every append even though its prefix is intact.
//
// The boundary read already settles the lag before comparing. The pre-write
// check has to do the same thing for the same reason: the store's own bytes are
// not a rewrite.
// TestStoreBoundaryStopsVouchingForMetadataThatUnderReports pins medium 9.
//
// A confirmed append knows exactly where its bytes end. The stat taken after it
// asks a filesystem whose attributes may lag the data, and when that sample
// reports LESS than the append wrote, the store has direct evidence that
// metadata cannot resolve its writes.
//
// That evidence has to outlive the cursor. Bookkeeping drops the cursor for
// exactly this sample, and a rule that infers metadata reliability from the
// cursor's survival therefore never records the finding: the boundary keeps
// advertising trustworthy metadata while a same-length rewrite during the same
// lag leaves the reconciled size and timestamp unchanged, so repeated activity
// reads serve rows the rewrite removed.
//
// The assertion is the claim the boundary makes to its callers.
func TestStoreBoundaryStopsVouchingForMetadataThatUnderReports(t *testing.T) {
	base := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	// Pin the empty journal's timestamp into the past before the store samples
	// it. A file created and written inside one filesystem tick reports the same
	// modification time on both sides of the first append, which is genuine
	// "length moved without the timestamp" evidence; this fixture is about a
	// LAGGING sample, so the write it settles on has to be one the filesystem
	// timestamped.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed empty journal: %v", err)
	}
	if err := os.Chtimes(path, time.Time{}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("pin the seeded journal's mtime: %v", err)
	}
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if !before.MetadataResolvesWrites {
		t.Fatal("the first boundary read already reports untrustworthy metadata, so this fixture cannot show the append's evidence being recorded")
	}
	// Freeze the reported attributes: the next append's bytes are durable and
	// the path keeps reporting the length it had before them.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_under_reported", Type: JobShell}); err != nil {
		t.Fatalf("append under lagging metadata: %v", err)
	}

	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after the under-reported append: %v", err)
	}
	actual, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	if after.Size != int64(len(actual)) {
		t.Fatalf("committed length = %d with %d bytes on disk, so this append did not under-report and the fixture cannot show the claim", after.Size, len(actual))
	}
	if after.MetadataResolvesWrites {
		t.Fatal("the boundary still advertises metadata that resolves writes, though this append's own stat reported less than the bytes it confirmed: a same-length rewrite under the same lag would then go unnoticed and reads would serve the old rows")
	}
}

func TestStoreAppendKeepsOneGenerationUnderSustainedLag(t *testing.T) {
	base := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	first, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// Freeze the reported attributes where they are: every later append's bytes
	// are durable, but the path keeps reporting the length it had here.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)

	for i := range 3 {
		jobID := "job_lag_" + strconv.Itoa(i)
		if err := s.Append(Event{Kind: EventJobStarted, JobID: jobID, Type: JobShell}); err != nil {
			t.Fatalf("append %d under lagging metadata: %v", i, err)
		}
		state, err := s.CommittedJournal()
		if err != nil {
			t.Fatalf("CommittedJournal after append %d: %v", i, err)
		}
		actual, err := afero.ReadFile(base, path)
		if err != nil {
			t.Fatalf("read journal after append %d: %v", i, err)
		}
		if state.Size != int64(len(actual)) {
			t.Fatalf("append %d committed length = %d with %d bytes on disk", i, state.Size, len(actual))
		}
		if state.Generation != first.Generation {
			t.Fatalf("append %d generation = %d, want the %d it had: the store appended its own bytes, and a lagging length is not a rewrite", i, state.Generation, first.Generation)
		}
	}
}

// TestStoreAppendRepairsAFragmentHiddenByALaggingLength pins High 3.
//
// A store appends its own bytes and records where they end. The filesystem's
// attributes lag, reporting a shorter length than the bytes on disk, and an
// unrelated writer then appends a torn fragment. Two facts now hold at once:
// the attributes still trail the store's own recorded end, and the file's real
// length is PAST that end by bytes the store never wrote.
//
// Reconciling the first fact by substituting the recorded end feeds the change
// test a length that matches its recorded one, so nothing looks changed, the
// tail is never examined, and this append joins the fragment. The journal ends
// up with one malformed line and reports success for it.
//
// The recorded end explains a length that TRAILS it. Growth PAST it is
// unexplained, and unexplained bytes are exactly what the change test is for.
func TestStoreAppendRepairsAFragmentHiddenByALaggingLength(t *testing.T) {
	base := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Freeze what the attributes report. The store's own appended end is past
	// this number, which is the lag it records.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_lagged", Type: JobShell}); err != nil {
		t.Fatalf("append under lagging metadata: %v", err)
	}

	// A foreign writer leaves a torn record at the end. The attributes above
	// still report the frozen length, so the store sees a length that trails its
	// recorded end while the file itself has grown past it.
	if err := os.WriteFile(path, append(mustReadFile(t, path), `{"kind":"job_started","seq":`...), 0o644); err != nil {
		t.Fatalf("tear the tail out of band: %v", err)
	}

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_torn", Type: JobShell}); err != nil {
		t.Fatalf("append across a fragment hidden by a lagging length: %v", err)
	}

	// The fragment must have been repaired, not joined: a join produces one
	// unreadable line, and the repaired journal must be readable and hold every
	// record the store believes it wrote.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the journal: %v (the append joined the fragment and fsynced one malformed line)", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("fold the reopened journal: %v", err)
	}
	for _, want := range []string{"job_first", "job_lagged", "job_after_torn"} {
		if records[want] == nil {
			t.Fatalf("%s is missing from the journal after the fragment was repaired", want)
		}
	}
}

// TestCommittedJournalKeepsOneGenerationAcrossLaggingReads pins M2.
//
// An append records its own end, and a filesystem whose attributes lag reports
// less than that for as long as the lag lasts. The generation is what a live
// fold caches against, so a boundary that advances it on every read of the same
// bytes makes each poll look like new content: the fold is refused, the whole
// history is rebuilt, and the session pays for the journal on every refresh
// until the filesystem catches up.
//
// The property is that two boundaries over the same bytes agree about the
// content, whatever the metadata says about it. The size they report is the
// append's, because the store confirmed those bytes; the generation is one.
func TestCommittedJournalKeepsOneGenerationAcrossLaggingReads(t *testing.T) {
	base := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_committed", Type: JobShell}); err != nil {
		t.Fatalf("append under lagging metadata: %v", err)
	}
	first, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("first CommittedJournal: %v", err)
	}
	actual, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if first.Size != int64(len(actual)) {
		t.Fatalf("first committed length = %d with %d bytes on disk", first.Size, len(actual))
	}

	// Second and third reads of the same unmoved file. Nothing about the
	// content changed between them, so neither may be reported as a change.
	for read := 2; read <= 3; read++ {
		next, err := s.CommittedJournal()
		if err != nil {
			t.Fatalf("CommittedJournal read %d: %v", read, err)
		}
		if next.Size != first.Size {
			t.Fatalf("read %d committed length = %d, want the %d the store already confirmed", read, next.Size, first.Size)
		}
		if next.Generation != first.Generation {
			t.Fatalf("read %d generation = %d, want the %d it reported for the same bytes: a lagging length is not a content change", read, next.Generation, first.Generation)
		}
	}
}

// bumpMTimePast moves path's modification time far enough past base that a
// store comparing a stat with base can resolve the change, however coarse the
// filesystem's timestamps are.
func bumpMTimePast(t *testing.T, path string, base time.Time) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.ModTime().Equal(base) {
		return
	}
	if err := os.Chtimes(path, time.Time{}, base.Add(time.Second)); err != nil {
		t.Fatalf("bump mtime of %s: %v", path, err)
	}
}

// TestScanEventsFromEndOffsetStopsAtTheBoundary pins the scan's half of the
// same contract: EndOffset is EOF, so bytes past it are neither decoded nor
// counted toward the returned offset, and reaching it is a clean completion
// rather than a limit error.
func TestScanEventsFromEndOffsetStopsAtTheBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	for _, id := range []string{"job_a", "job_b"} {
		if err := s.Append(Event{Kind: EventJobStarted, JobID: id, Type: JobShell}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}
	snapshot, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	committed := snapshot.Size

	// Cut the boundary back to exactly the first line: the second record is
	// past it and must not appear. The first line's own length is what a store
	// would have committed had it stopped there.
	raw := mustReadFile(t, path)
	lineEnd := int64(bytes.IndexByte(raw, '\n') + 1)
	first, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: lineEnd})
	if err != nil {
		t.Fatalf("bounded scan: %v", err)
	}
	if len(first) != 1 || first[0].JobID != "job_a" {
		t.Fatalf("bounded scan = %+v, want only job_a", first)
	}

	// A boundary landing INSIDE a line cuts it short, so the line is withheld
	// rather than decoded whole: the scan returns the records before it and
	// reports where it stopped, and the caller resumes once those bytes commit.
	midLine := lineEnd + 4
	inside, toOffset, err := ScanEventsFrom(t.Context(), path, 0, ScanLimits{EndOffset: midLine})
	if err != nil {
		t.Fatalf("scan at a mid-line boundary: %v", err)
	}
	if len(inside) != 1 || inside[0].JobID != "job_a" {
		t.Fatalf("scan at a mid-line boundary = %+v, want only job_a", inside)
	}
	if toOffset != lineEnd {
		t.Fatalf("scan at a mid-line boundary stopped at %d, want the last complete line's end %d", toOffset, lineEnd)
	}

	// A boundary one byte short of a line's newline is the same case, and the one
	// an in-flight append actually produces: every byte of the record has landed,
	// the newline that commits it has not. The record decodes, so without an
	// explicit check the scan includes it and advances past a byte the store has
	// not acknowledged. It must be withheld, exactly like a line cut mid-body.
	beforeNewline := committed - 1
	var secondLineStart int64
	if idx := bytes.LastIndexByte(mustReadFile(t, path)[:beforeNewline], '\n'); idx >= 0 {
		secondLineStart = int64(idx + 1)
	}
	short, shortOffset, err := ScanEventsFrom(t.Context(), path, 0, ScanLimits{EndOffset: beforeNewline})
	if err != nil {
		t.Fatalf("scan with the boundary before a newline: %v", err)
	}
	if len(short) != 1 || short[0].JobID != "job_a" {
		t.Fatalf("scan with the boundary before a newline = %+v, want only job_a", short)
	}
	if shortOffset != secondLineStart {
		t.Fatalf("scan with the boundary before a newline stopped at %d, want the last committed line's end %d", shortOffset, secondLineStart)
	}

	// The boundary is a clean stop: a scan that reaches it exactly reports no
	// error and returns every record up to it.
	all, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: committed})
	if err != nil {
		t.Fatalf("scan to the committed boundary: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("scan to the committed boundary = %d events, want 2", len(all))
	}

	// And past the boundary the file is unchanged: reading it whole matches.
	whole, err := ScanEvents(t.Context(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("unbounded scan: %v", err)
	}
	if len(whole) != len(all) {
		t.Fatalf("unbounded scan = %d events, want the same %d", len(whole), len(all))
	}
}

// renameOnSyncFs renames the journal aside the first time the store syncs, so
// the replacement is installed in the window A1 describes: after the append's
// bytes are written and before the store acknowledges them.
type renameOnSyncFs struct {
	afero.Fs
	once  sync.Once
	from  string
	to    string
	armed atomic.Bool
	// leaveAbsent renames the journal aside during the sync and installs
	// nothing under the name. That is the window a rename-and-replace leaves
	// open, and it is the one where the name answers ENOENT after the write.
	leaveAbsent bool
}

func (r *renameOnSyncFs) arm() { r.armed.Store(true) }

func (r *renameOnSyncFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := r.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &renameOnSyncFile{File: f, fs: r}, nil
}

type renameOnSyncFile struct {
	afero.File
	fs *renameOnSyncFs
}

func (f *renameOnSyncFile) Sync() error {
	if f.fs.armed.CompareAndSwap(true, false) {
		f.fs.once.Do(func() {
			if err := f.fs.Rename(f.fs.from, f.fs.to); err != nil {
				panic(err)
			}
			if f.fs.leaveAbsent {
				return
			}
			// The replacement is a fresh, empty journal under the name the store
			// was opened with.
			replace, err := f.fs.OpenFile(f.fs.from, os.O_CREATE|os.O_RDWR, 0o644)
			if err != nil {
				panic(err)
			}
			_ = replace.Close()
		})
	}
	return f.File.Sync()
}

// statFailsOnNthFs fails one Stat by ordinal among the Stats made after it was
// armed. The ordinal is what makes a fixture able to aim at a specific stat:
// the append path stats the name more than once, and a fault that lands on the
// first answers a different question than the one under test.
// TestStoreAppendRetryAllocatesAboveTheAdoptedSequences pins High 4.
//
// A write is refused during a rotation, so its sequence is reserved and the
// event is left with the caller. The replacement that follows is adopted, and
// adoption seeds the store's sequence from that journal's own records.
//
// Rewinding to the refused write's number is correct only while nothing else
// claims it. When the adopted journal already holds a record at or beyond that
// number, the rewind hands the retry a sequence the journal uses — two records
// share one number, and the fold keeps whichever it meets first. A retried
// finish assigned BELOW an existing finish becomes the first finish in the fold
// and changes the recorded outcome of the job.
//
// The assertion is the journal's own ordering: every record keeps its own
// number and the retry's is above all of them.
func TestStoreAppendRetryAllocatesAboveTheAdoptedSequences(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	saved := filepath.Join(dir, "jobs.jsonl.saved")
	fs := &renameOnSyncFs{Fs: afero.NewOsFs(), from: path, to: saved}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// The name is rotated away while this append syncs, so the event is durable
	// only in the file that was moved aside. The append reports a retryable
	// replacement and reserves the sequence it used.
	fs.arm()
	if err := s.Append(Event{Kind: EventJobFinished, JobID: "job_first", Status: StatusCompleted}); !errors.Is(err, ErrJournalReplaced) {
		t.Fatalf("append across a rotation = %v, want a retryable ErrJournalReplaced", err)
	}

	// The replacement is a journal of its own, already carrying sequence numbers
	// past the refused write's. Adoption seeds the store from them.
	const adoptedSeq = 7
	replacement := []byte(`{"seq":` + strconv.FormatInt(adoptedSeq, 10) + `,"kind":"job_finished","job_id":"job_replacement"}` + "\n")
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatalf("write the replacement: %v", err)
	}

	if err := s.Append(Event{Kind: EventJobFinished, JobID: "job_first", Status: StatusCompleted}); err != nil {
		t.Fatalf("retry after adoption: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the adopted journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	raw := mustReadFile(t, path)
	for _, want := range []string{"job_replacement", "job_first"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("the adopted journal does not hold %q: %q", want, raw)
		}
	}
	// Every line keeps its own number, and the retry's is the highest: a rewind
	// would have put it at or below the number the replacement already uses.
	var seqs []int64
	for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		var rec struct {
			Seq   int64  `json:"seq"`
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		seqs = append(seqs, rec.Seq)
	}
	if len(seqs) != 2 {
		t.Fatalf("the adopted journal holds %d records, want the replacement's one plus the retry: %q", len(seqs), raw)
	}
	if seqs[0] != adoptedSeq {
		t.Fatalf("the replacement's record moved to sequence %d, want the %d it arrived with", seqs[0], adoptedSeq)
	}
	if seqs[1] <= adoptedSeq {
		t.Fatalf("the retry took sequence %d, want one above the %d the adopted journal already uses: two records sharing or inverting a number makes the fold keep whichever it meets first", seqs[1], adoptedSeq)
	}
}

type statFailsOnceBeforeAppendFs struct {
	afero.Fs
	remaining atomic.Int64
}

// failNthStat arms the fixture to fail the nth Stat from now, counting from 1.
func (f *statFailsOnceBeforeAppendFs) failNthStat(n int64) { f.remaining.Store(n) }

func (f *statFailsOnceBeforeAppendFs) Stat(name string) (os.FileInfo, error) {
	if n := f.remaining.Load(); n > 0 {
		if f.remaining.CompareAndSwap(n, n-1) && n == 1 {
			return nil, errors.New("transient stat failure")
		}
	}
	return f.Fs.Stat(name)
}

// TestStoreAppendDoesNotReuseTheSequenceOfARestoredWrite pins High 1.
//
// The journal is renamed aside during this append's fsync with no replacement
// installed, so the post-sync checks answer ENOENT and the append is correctly
// refused. Then the SAME file is put back under the name — which is what a
// rename-and-replace looks like when the replacement turns out to be the
// original — and the caller retries.
//
// The refused attempt's bytes are in that file with a sequence already
// assigned to them. A store that simply hands the next number to the retry puts
// two records with one sequence in one journal, and the fold, which orders by
// Seq, keeps whichever it met first. The retry therefore has to reach the
// journal's own answer about whether the first write arrived.
//
// The earlier fixture could not see this: it retried with a DIFFERENT job ID and
// asserted only that the new ID was readable, so a reused sequence looked
// exactly like a correct append.
func TestStoreAppendDoesNotReuseTheSequenceOfARestoredWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	saved := filepath.Join(dir, "jobs.jsonl.saved")

	fs := &renameOnSyncFs{Fs: afero.NewOsFs(), from: path, to: saved, leaveAbsent: true}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	event := Event{Kind: EventJobStarted, JobID: "job_completion", Type: JobShell}
	fs.arm()
	if err := s.Append(event); err == nil {
		t.Fatal("append acknowledged success although the name it should have reached was absent")
	}

	// The same file comes back under the name, carrying the refused write.
	if err := os.Rename(saved, path); err != nil {
		t.Fatalf("restore the name: %v", err)
	}
	if err := s.Append(event); err != nil {
		t.Fatalf("retry once the name is back: %v", err)
	}

	// Every sequence in the journal must be distinct. One number on two records
	// is the damage: the fold keeps one and silently drops the other.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.LoadEvents()
	if err != nil {
		t.Fatalf("read the journal's events: %v", err)
	}
	seen := make(map[int64]int, len(events))
	for _, got := range events {
		seen[got.Seq]++
	}
	for seq, count := range seen {
		if count > 1 {
			t.Fatalf("sequence %d appears on %d records, want one apiece: the retry reused the number the refused write had already spent", seq, count)
		}
	}
	// And the completion must be foldable: exactly one terminal read of it.
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if records["job_completion"] == nil {
		t.Fatal("the retried completion is missing from the reopened journal's fold")
	}
}

// TestStoreAppendFailsWhenTheTailRecheckCannotResolve pins H2.
//
// A foreign writer leaves a partial record at the end of a live journal. The
// store's next append notices the change and repairs the tail before writing —
// and the repair ends with an identity recheck of the name against the handle.
// When that recheck fails transiently, the function used to return nil, and the
// append caller read nil as "repaired".
//
// It was not. The fragment is still there, so the append joins its JSON onto it
// and fsyncs one malformed line, reports success, and every later read and
// reopen fails on that line. An indeterminate recheck is not a confirmation, and
// it must reach the caller as a retryable error instead.
func TestStoreAppendFailsWhenTheTailRecheckCannotResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &statFailsOnceBeforeAppendFs{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// The foreign writer tears the tail. Nothing reads the journal first: the
	// store's own next append is what arrives next, which is the ordering where
	// a silent repair failure poisons the file.
	partial := []byte(`{"kind":"job_started","seq":`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("tear the tail: %v", err)
	}
	// Aim at the repair's own identity recheck, which is the SECOND stat this
	// append makes of the name: the first is the pre-write sample every append
	// takes, and faulting that one answers a different question — it would
	// refuse the append before the tail was ever examined. Counting from the
	// append keeps the fixture pinned to the operation it names.
	fs.failNthStat(2)

	// This append's fsync arms the fault, so the repair's identity recheck is
	// the stat that fails.
	err = s.Append(Event{Kind: EventJobStarted, JobID: "job_after_torn", Type: JobShell})
	if err == nil {
		t.Fatal("append succeeded although the tail repair's identity recheck failed, so the fragment was left in place for the append to join")
	}
	if !errors.Is(err, ErrJournalNameUnavailable) {
		t.Fatalf("append across an unresolved tail recheck = %v, want a retryable ErrJournalNameUnavailable", err)
	}

	// The journal must be reopenable: the failed append must not have written
	// onto the fragment.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after the refused append: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Load(); err != nil {
		t.Fatalf("fold the reopened journal: %v", err)
	}
}

// vanishOnNthStatFs makes the journal's name answer ENOENT for exactly one
// Stat, at an ordinal chosen by the fixture, and answers normally either side.
//
// That is the rename half of a rename-and-replace with the replacement already
// installed: the name is briefly absent while one check looks at it, and by the
// time the write happens it reaches the same file as before. An append cannot
// tell from its own successful write that the check in between saw nothing.
type vanishOnNthStatFs struct {
	afero.Fs
	remaining atomic.Int64
}

func (f *vanishOnNthStatFs) vanishOnNthStat(n int64) { f.remaining.Store(n) }

func (f *vanishOnNthStatFs) Stat(name string) (os.FileInfo, error) {
	if n := f.remaining.Load(); n > 0 {
		if f.remaining.CompareAndSwap(n, n-1) && n == 1 {
			return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
		}
	}
	return f.Fs.Stat(name)
}

// TestStoreAppendRefusesWhenTheNameVanishesDuringTheTailCheck pins High 2.
//
// A torn fragment is at the end of a live journal. This append notices the
// change and examines the tail — and in that exact moment the name answers
// ENOENT, because a rename-and-replace is between its two steps. By the time the
// append writes, the name is back and reaches the same file as before, so every
// post-sync check agrees the write landed.
//
// The fragment was never examined. Joining a new record onto it fsyncs one
// malformed line, the append reports success, and every later read and reopen
// fails on that line. An ENOENT during the tail check is not a finding that the
// tail is clean, and a writer cannot treat it as one.
//
// This is the same defect class as the generic stat failure above, arriving
// through absence rather than an error: the repair returns without an error and
// without having established anything.
func TestStoreAppendRefusesWhenTheNameVanishesDuringTheTailCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")

	fs := &vanishOnNthStatFs{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_before", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	partial := []byte(`{"kind":"job_started","seq":`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("tear the tail: %v", err)
	}
	// The repair's identity recheck is the SECOND stat this append makes of the
	// name; the first is the pre-write sample, and faulting that one refuses the
	// append before the tail is examined at all — a different question.
	fs.vanishOnNthStat(2)

	appendErr := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_torn", Type: JobShell})
	if appendErr != nil && !errors.Is(appendErr, ErrJournalNameUnavailable) {
		t.Fatalf("append across a vanished tail recheck = %v, want a retryable ErrJournalNameUnavailable", appendErr)
	}

	// The corruption is the assertion. If the append reported success it wrote
	// onto the fragment, and the journal it left behind cannot be folded — so
	// this fails on the unreadable journal rather than on the success alone,
	// which is the user-visible damage: every later read and every reopen.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the journal after this append: %v (append reported success = %v; it joined the unexamined fragment and fsynced one malformed line)", err, appendErr == nil)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("fold the reopened journal: %v (append reported success = %v; it joined the unexamined fragment and fsynced one malformed line)", err, appendErr == nil)
	}
	if records["job_before"] == nil {
		t.Fatal("the record appended before the tear is missing from the reopened journal")
	}
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_retry", Type: JobShell}); err != nil {
		t.Fatalf("retry once the name is readable again: %v", err)
	}
}

// TestStoreAppendRetriesWhenTheNameIsAbsentAfterSync pins H1.
//
// A rename-and-replace is two steps, and between them the name reaches nothing.
// An append whose fsync lands in that window has made its bytes durable in the
// file that was moved aside, and both post-sync stats answer ENOENT.
//
// Absence is a definite answer and it points at where the bytes went: nowhere a
// reader of the named journal will look. Reporting success there loses the event
// silently — a shell completion told "written" stops retrying, and the session
// never shows it. The append must therefore be RETRYABLE, and once the name is
// back the retry must land a readable record.
func TestStoreAppendRetriesWhenTheNameIsAbsentAfterSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	saved := filepath.Join(dir, "jobs.jsonl.saved")

	fs := &renameOnSyncFs{Fs: afero.NewOsFs(), from: path, to: saved, leaveAbsent: true}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// The name is renamed aside during this append's fsync and no replacement
	// is installed, so it answers ENOENT for the rest of the call.
	fs.arm()
	err = s.Append(Event{Kind: EventJobStarted, JobID: "job_retry_me", Type: JobShell})
	if err == nil {
		t.Fatal("append acknowledged success although the name it should have reached was absent and the bytes went into the journal moved aside")
	}
	if !errors.Is(err, ErrJournalNameUnavailable) {
		t.Fatalf("append across an absent name = %v, want a retryable ErrJournalNameUnavailable", err)
	}
	if bytes.Contains(mustReadFile(t, saved), []byte("job_retry_me")) {
		// The bytes may or may not have landed in the moved-aside file; what
		// matters is that success was not claimed for them.
		t.Log("the refused append wrote into the moved-aside journal, which the retry below supersedes")
	}

	// The path comes back — the replacement half of the rename-and-replace —
	// carrying the journal that was moved aside. The retry must now succeed and
	// its record must be readable from the named journal.
	if err := os.Rename(saved, path); err != nil {
		t.Fatalf("restore the name: %v", err)
	}
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_retried", Type: JobShell}); err != nil {
		t.Fatalf("retry once the name is back: %v", err)
	}
	if !bytes.Contains(mustReadFile(t, path), []byte("job_retried")) {
		t.Fatal("the retried append is not in the named journal, so success did not mean the bytes landed where a reader looks")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the named journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("fold the reopened journal: %v", err)
	}
	found := false
	for _, rec := range records {
		if rec.JobID == "job_retried" {
			found = true
		}
	}
	if !found {
		t.Fatal("the retried append is not in the reopened journal's fold")
	}
}

// TestStoreAppendRefusesWhenTheNameWasReplacedDuringSync pins A1: an append that
// reports success has to have reached the journal a reader will open.
//
// The journal is renamed aside and re-created while the append's own fsync is in
// flight. The bytes are durable — in the file that was moved aside. Readers
// follow the name, so the event is invisible to activity and to reopen, and
// reporting success loses it silently. The remaining requirement is on the
// generation: a boundary that later reports the replacement must not be mistaken
// for more of the file the fold already holds, or every cached page keeps
// serving the old file's rows.
func TestStoreAppendRefusesWhenTheNameWasReplacedDuringSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	saved := filepath.Join(dir, "jobs.jsonl.saved")

	fs := &renameOnSyncFs{Fs: afero.NewOsFs(), from: path, to: saved}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal before the replacement: %v", err)
	}
	savedBytes := mustReadFile(t, path)

	fs.arm()
	err = s.Append(Event{Kind: EventJobStarted, JobID: "job_lost", Type: JobShell})
	if err == nil {
		t.Fatal("append acknowledged success although its record went into the journal that was moved aside")
	}
	if !errors.Is(err, ErrJournalReplaced) {
		t.Fatalf("append across a replacement = %v, want a retryable ErrJournalReplaced", err)
	}
	// The record is where it was written, and NOT in the file the name reaches.
	if !bytes.Contains(mustReadFile(t, saved), []byte("job_lost")) {
		t.Fatal("the fixture did not land the append in the moved-aside journal")
	}
	if bytes.Contains(mustReadFile(t, path), []byte("job_lost")) {
		t.Fatal("the acknowledged-or-refused append reached the named journal, so this fixture is not measuring the replacement")
	}

	// The generation the boundary reports for the replacement has to have moved
	// past the one a fold built from the moved-aside journal carries.
	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after the replacement: %v", err)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation = %d after the name was replaced, want it above the %d a fold built from the old file carries", after.Generation, before.Generation)
	}
	// The moved-aside journal still begins with every byte it had committed, so
	// the failed append did not truncate content that was already durable.
	if got := mustReadFile(t, saved); !bytes.HasPrefix(got, savedBytes) {
		t.Fatalf("the moved-aside journal no longer starts with the %d bytes it had committed", len(savedBytes))
	}
}

// TestStoreAppendAcknowledgesTheJournalAdoptionSelected pins H2.
//
// Adoption can install a file the pre-adoption sample never described: the name
// moves once, and again while that candidate is being examined, so the retry
// adopts the second replacement. Verifying the append against the FIRST sample
// then compares a file that was moved aside with a name that reaches the second
// one, reports a replacement, and leaves the store's sequence unadvanced — so
// the next append reuses the number this one wrote.
//
// The event is durable in the named journal, which is the whole test: success
// means it landed there, and the sequence continues from it.
func TestStoreAppendAcknowledgesTheJournalAdoptionSelected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &replaceOnAdoptOpenFs{Fs: afero.NewOsFs(), path: path, dir: dir}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_original", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Move the journal aside and put a first replacement under the name, so the
	// append below adopts. The fixture then swaps in a SECOND replacement as the
	// adoption opens it.
	if err := os.Rename(path, filepath.Join(dir, "jobs.jsonl.saved")); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	first := []byte(`{"seq":1,"kind":"job_started","job_id":"job_first_replacement"}` + "\n")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatalf("write first replacement: %v", err)
	}
	second := []byte(`{"seq":1,"kind":"job_started","job_id":"job_second_replacement"}` + "\n")
	fs.arm(second)

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_adopted", Type: JobShell}); err != nil {
		t.Fatalf("append after adoption: %v: success means the record reached the named journal", err)
	}
	named := mustReadFile(t, path)
	if !bytes.Contains(named, []byte("job_adopted")) {
		t.Fatalf("the named journal does not hold the acknowledged record: %q", named)
	}
	// The sequence continued from the adopted journal rather than restarting, so
	// the adopted log is well-formed and folds every record it holds.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the adopted journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	folded, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load the adopted journal: %v", err)
	}
	if folded["job_adopted"] == nil {
		t.Fatal("the acknowledged record is missing from the adopted journal's fold")
	}
}

// replaceOnAdoptOpenFs swaps the journal's contents once, at the moment a second
// adoption attempt opens the path, and only while armed.
// TestStoreAppendAdoptsTheFileTheNameReaches pins High 2.
//
// A rename-and-replace is not atomic, and two of them can interleave with one
// adoption. The name is sampled, then the candidate is opened, then the name is
// sampled again: rotate to B before the open and back to A after it, and both
// samples identify A while the handle in hand is B's.
//
// Comparing the two NAME samples therefore agrees with itself and says nothing
// about which file was opened. Installing that handle while recording A's
// identity makes the store append through a file the name does not reach, and
// every post-sync check then vouches for an event that exists only in B — the
// append reports success, and a job completion stops retrying while never
// appearing in the journal a reader opens.
//
// The assertion is where the record landed, not that the append succeeded.
func TestStoreAppendAdoptsTheFileTheNameReaches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &rotateTwiceOnAdoptFs{Fs: afero.NewOsFs(), path: path, dir: dir}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_original", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Move the store's own file aside and install replacement A under the name,
	// so the append below adopts. Replacement B waits off to the side; the
	// fixture rotates it into the name for the duration of the open and rotates
	// A back afterwards.
	if err := os.Rename(path, filepath.Join(dir, "jobs.jsonl.original")); err != nil {
		t.Fatalf("rename the original aside: %v", err)
	}
	first := []byte(`{"seq":1,"kind":"job_started","job_id":"job_first_replacement"}` + "\n")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatalf("write replacement A: %v", err)
	}
	second := []byte(`{"seq":1,"kind":"job_started","job_id":"job_second_replacement"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl.b"), second, 0o644); err != nil {
		t.Fatalf("write replacement B: %v", err)
	}
	fs.arm()

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_adopted", Type: JobShell}); err != nil {
		t.Fatalf("append after a double rotation: %v", err)
	}
	// The named journal is A, and that is where a reader looks.
	named := mustReadFile(t, path)
	if !bytes.Contains(named, []byte("job_adopted")) {
		t.Fatalf("the acknowledged record is not in the named journal: %q: the store adopted the file it opened, not the file the name reaches", named)
	}
	if !bytes.Contains(named, []byte("job_first_replacement")) {
		t.Fatalf("the named journal lost replacement A's own record: %q", named)
	}
	if bytes.Contains(named, []byte("job_second_replacement")) {
		t.Fatalf("the named journal holds replacement B's record, so B was installed as the store's journal: %q", named)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the adopted journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	folded, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load the adopted journal: %v", err)
	}
	if folded["job_adopted"] == nil {
		t.Fatal("the acknowledged record is missing from the named journal's fold")
	}
}

// rotateTwiceOnAdoptFs performs the two-step interleaving above: it swaps
// replacement B into the name for the duration of one OpenFile, then restores
// replacement A before the next Stat, so both name samples identify A while the
// opened handle is B.
type rotateTwiceOnAdoptFs struct {
	afero.Fs
	path   string
	dir    string
	armed  atomic.Bool
	opened atomic.Bool
}

func (r *rotateTwiceOnAdoptFs) arm() { r.armed.Store(true) }

func (r *rotateTwiceOnAdoptFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if r.armed.CompareAndSwap(true, false) {
		if err := r.Rename(r.path, filepath.Join(r.dir, "jobs.jsonl.a")); err != nil {
			panic(err)
		}
		if err := r.Rename(filepath.Join(r.dir, "jobs.jsonl.b"), r.path); err != nil {
			panic(err)
		}
		r.opened.Store(true)
	}
	return r.Fs.OpenFile(name, flag, perm)
}

func (r *rotateTwiceOnAdoptFs) Stat(name string) (os.FileInfo, error) {
	if r.opened.CompareAndSwap(true, false) {
		if err := r.Rename(r.path, filepath.Join(r.dir, "jobs.jsonl.b-done")); err != nil {
			panic(err)
		}
		if err := r.Rename(filepath.Join(r.dir, "jobs.jsonl.a"), r.path); err != nil {
			panic(err)
		}
	}
	return r.Fs.Stat(name)
}

type replaceOnAdoptOpenFs struct {
	afero.Fs
	path  string
	dir   string
	once  sync.Once
	body  []byte
	armed atomic.Bool
}

func (r *replaceOnAdoptOpenFs) arm(body []byte) {
	r.body = body
	r.armed.Store(true)
}

func (r *replaceOnAdoptOpenFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if r.armed.CompareAndSwap(true, false) {
		r.once.Do(func() {
			if err := r.Rename(r.path, filepath.Join(r.dir, "jobs.jsonl.first")); err != nil {
				panic(err)
			}
			if err := afero.WriteFile(r.Fs, r.path, r.body, 0o644); err != nil {
				panic(err)
			}
		})
	}
	return r.Fs.OpenFile(name, flag, perm)
}

// TestStoreAppendAcknowledgesADurableEventWhenThePostSyncStatFails pins H4.
//
// The bytes are durable. The store's own handle was opened, the pre-write check
// established that the name reaches it, and only the stat taken AFTER the sync
// fails. Reporting that as retryable is not a harmless failure: a caller that
// retries re-derives the terminal generation of the event it is writing, so the
// fold keeps the first terminal record and ignores the retry — the job commits,
// the append is told it failed, and the completion notification is dropped.
//
// So a stat failure after the bytes are durable is reconciled, not escalated:
// the append is acknowledged, and the record is where a reader will find it.
func TestStoreAppendAcknowledgesADurableEventWhenThePostSyncStatFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &statFailsAfterSyncFs{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	fs.failNextStat()
	if err := s.Append(Event{Kind: EventJobFinished, JobID: "job_first", Status: StatusCompleted}); err != nil {
		t.Fatalf("append whose post-sync stat failed = %v, want it acknowledged: the bytes are durable and the notification is lost if the caller retries", err)
	}
	// It is not merely acknowledged: it is readable where a reader looks.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	folded, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := folded["job_first"]
	if rec == nil {
		t.Fatal("the acknowledged record is missing from the reopened journal")
	}
	if rec.Status != StatusCompleted {
		t.Fatalf("job_first status = %q after the acknowledged completion, want it completed", rec.Status)
	}
}

// statFailsAfterSyncFs fails one Stat immediately after a Sync, which is the
// window the post-sync verification reads in.
type statFailsAfterSyncFs struct {
	afero.Fs
	synced   atomic.Bool
	failOnce atomic.Bool
}

func (f *statFailsAfterSyncFs) failNextStat() {
	// Counted from the NEXT sync, so the failure cannot land on this append's
	// pre-write stat — a different question with a different correct answer.
	f.synced.Store(false)
	f.failOnce.Store(true)
}

func (f *statFailsAfterSyncFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &statFailsAfterSyncFile{File: file, fs: f}, nil
}

func (f *statFailsAfterSyncFs) Stat(name string) (os.FileInfo, error) {
	// Only after a Sync has completed: before the write, a failing stat is a
	// different question with a different answer (the append must not proceed
	// on a handle the name may not reach).
	if f.synced.Load() && f.failOnce.CompareAndSwap(true, false) {
		return nil, errors.New("transient stat failure")
	}
	return f.Fs.Stat(name)
}

type statFailsAfterSyncFile struct {
	afero.File
	fs *statFailsAfterSyncFs
}

func (f *statFailsAfterSyncFile) Sync() error {
	err := f.File.Sync()
	f.fs.synced.Store(true)
	return err
}

// TestStoreCursorDoesNotTrustAReplacementOfTheSameLength pins the cursor half of
// the replacement question.
//
// A warm cursor keeps the events it decoded and rereads only what was appended
// since. It decides whether that prefix is still the file's prefix from the
// sample it recorded, and a replacement that reproduces the recorded length and
// modification time is indistinguishable from no write at all by those numbers.
// The prefix it holds was decoded from the file that was moved aside, so serving
// it returns rows the replacement does not have and omits the ones it does.
//
// Identity is what answers this, exactly as it answers the fold's generation.
// The replacement below is the same length and keeps the original timestamp, so
// nothing but identity can tell the two files apart.
func TestStoreCursorDoesNotTrustAReplacementOfTheSameLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	first := []byte(`{"kind":"job_started","seq":1,"job_id":"job_original"}` + "\n")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Prime the cursor: this read decodes the original and records its sample.
	if _, err := s.Load(); err != nil {
		t.Fatalf("prime the cursor: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the seeded journal: %v", err)
	}

	// Replace the journal with a different row of EXACTLY the same length and
	// restore the original timestamp, so only identity distinguishes them.
	replacement := []byte(`{"kind":"job_started","seq":1,"job_id":"job_replaced"}` + "\n")
	if len(replacement) != len(first) {
		t.Fatalf("fixture replacement is %d bytes, want the %d that keeps the length unchanged", len(replacement), len(first))
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("restore the timestamp: %v", err)
	}

	records, err := s.Load()
	if err != nil {
		t.Fatalf("Load over a same-length replacement: %v", err)
	}
	if records["job_replaced"] == nil {
		t.Fatalf("the fold has no job_replaced: the cursor served a prefix decoded from the journal that was moved aside")
	}
	if records["job_original"] != nil {
		t.Fatalf("the fold still has job_original, which the replacement does not contain: the cursor trusted the old file's prefix")
	}
}

// fragAfterSyncFs appends a torn fragment to the journal the first time the
// store syncs, so the fragment lands after this append's own bytes are durable
// and before the post-sync sample is taken.
//
// That window is what makes the sample the store resolves longer than the end
// it just wrote. The identity is correct — the name still reaches the file the
// store appended through — so nothing about the file's identity says anything is
// wrong; the only evidence is that the length is PAST the store's own write.
type fragAfterSyncFs struct {
	afero.Fs
	path string
	once sync.Once
	arm  atomic.Bool
}

func (f *fragAfterSyncFs) armFragment() { f.arm.Store(true) }

func (f *fragAfterSyncFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &fragAfterSyncFile{File: file, fs: f}, nil
}

type fragAfterSyncFile struct {
	afero.File
	fs *fragAfterSyncFs
}

func (f *fragAfterSyncFile) Sync() error {
	err := f.File.Sync()
	if f.fs.arm.CompareAndSwap(true, false) {
		f.fs.once.Do(func() {
			body, readErr := afero.ReadFile(f.fs.Fs, f.fs.path)
			if readErr != nil {
				panic(readErr)
			}
			if writeErr := afero.WriteFile(f.fs.Fs, f.fs.path, append(body, `{"kind":"job_started","seq":`...), 0o644); writeErr != nil {
				panic(writeErr)
			}
		})
	}
	return err
}

// TestStoreAppendDoesNotBookAGrowthItDidNotWrite pins High 5.
//
// A foreign writer appends a torn fragment after this append's fsync and before
// the post-sync sample. The sample has the right file identity and a length past
// the append's own end. Recording that length as the append's baseline makes the
// NEXT append see an unchanged file: it compares, finds nothing changed, skips
// the tail inspection, and writes its event onto the fragment — one malformed
// line, reported as success, and the journal no longer reopens.
//
// Bytes past the store's own confirmed end are not its write, so they are not a
// baseline it may book.
func TestStoreAppendDoesNotBookAGrowthItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &fragAfterSyncFs{Fs: afero.NewOsFs(), path: path}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fs.armFragment()
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("append with a fragment arriving after its fsync: %v", err)
	}

	// The next append must inspect the tail it did not write before adding to it.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_second", Type: JobShell}); err != nil {
		t.Fatalf("append after foreign growth: %v", err)
	}

	// The corruption is the assertion: a baseline booked over the fragment leaves
	// a journal that cannot be reopened.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the journal: %v (an append booked a length it never wrote, so the fragment was joined rather than repaired)", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("fold the reopened journal: %v", err)
	}
	for _, want := range []string{"job_first", "job_second"} {
		if records[want] == nil {
			t.Fatalf("%s is missing from the reopened journal", want)
		}
	}
}

// TestStoreAppendBooksTheBaselineItVerified pins M4.
//
// The append verifies, after its sync, that the name still reaches the journal it
// wrote, and then records the baseline a later boundary read compares against.
// Recording is a separate step, and a replacement landing between the two would
// be installed as the append's own baseline.
//
// The replacement here is the SAME LENGTH as the journal it replaces, which is
// what makes the damage observable rather than incidentally masked. A boundary
// read decides "did the content change" from identity, length and modification
// time together; a replacement that differs in length is caught by length alone,
// so it proves nothing about which file the baseline named. At an identical
// length the only thing that can still report the change is the identity the
// baseline recorded — so a store that recorded the replacement sees no change at
// all, and extends a fold built on the old bytes from an offset into the new
// file.
//
// The assertion is therefore a generation advance after the append: the bytes a
// reader now gets are not the bytes the fold holds.
func TestStoreAppendBooksTheBaselineItVerified(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &sameLengthReplaceOnBookkeepingStatFs{Fs: afero.NewOsFs(), path: path, dir: dir}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	before, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// The replacement is built from the journal the append leaves behind, with
	// one job ID rewritten in place: same length, different content.
	fs.armRewriteJobID()
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_second", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	actual, err := afero.ReadFile(afero.NewOsFs(), path)
	if err != nil {
		t.Fatalf("read named journal: %v", err)
	}
	if !bytes.Contains(actual, []byte("job_rewrit")) &&
		!bytes.Contains(actual, []byte("job_second")) {
		t.Fatalf("the named journal holds neither the appended record nor the replacement, so the fixture did not land where it claims: %q", actual)
	}
	// Whatever the fixture managed to install, the boundary must not describe the
	// previous file as though it were the current one.
	if after.Size != int64(len(actual)) {
		t.Fatalf("committed length = %d with %d bytes in the named journal: the baseline describes a different file", after.Size, len(actual))
	}
	if after.Generation == before.Generation {
		t.Fatalf("generation = %d across an append whose journal was replaced at the baseline sample, want it to advance from %d: the next read extends a fold built on the old file from an offset into the new one", after.Generation, before.Generation)
	}
}

// sameLengthReplaceOnBookkeepingStatFs replaces the journal with a same-length
// rewrite at the stat that would record the append's baseline.
//
// It cannot identify that stat by position, because the fix removes it. It acts
// on the first stat after a completed Sync whose answer would otherwise be
// recorded — which is exactly the sample the append books when it re-reads the
// path — and it performs the rewrite so that the replacement is the same length
// as the journal it moves aside.
type sameLengthReplaceOnBookkeepingStatFs struct {
	afero.Fs
	path string
	dir  string
	once sync.Once
	// synced reports a Sync has completed; armed reports this fixture should act.
	synced atomic.Bool
	armed  atomic.Bool
	// verified marks the append's own verification stat, which must see the
	// journal it wrote and must be let through.
	verified atomic.Bool
}

func (r *sameLengthReplaceOnBookkeepingStatFs) armRewriteJobID() {
	r.synced.Store(false)
	r.verified.Store(false)
	r.armed.Store(true)
}

func (r *sameLengthReplaceOnBookkeepingStatFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := r.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &sameLengthReplaceFile{File: f, fs: r}, nil
}

func (r *sameLengthReplaceOnBookkeepingStatFs) Stat(name string) (os.FileInfo, error) {
	if r.synced.Load() && r.armed.Load() {
		// The first stat after the sync is the append verifying the name still
		// reaches what it wrote. The replacement belongs to the one after it.
		if r.verified.CompareAndSwap(false, true) {
			return r.Fs.Stat(name)
		}
		r.armed.Store(false)
		r.once.Do(func() {
			saved := filepath.Join(r.dir, "jobs.jsonl.saved")
			if err := r.Rename(r.path, saved); err != nil {
				panic(err)
			}
			body, err := afero.ReadFile(r.Fs, saved)
			if err != nil {
				panic(err)
			}
			// Same length, different content: rewrite a job ID in place.
			replacement := bytes.Replace(body, []byte(`"job_second"`), []byte(`"job_rewrit"`), 1)
			if len(replacement) != len(body) {
				panic("same-length rewrite changed the length")
			}
			if err := afero.WriteFile(r.Fs, r.path, replacement, 0o644); err != nil {
				panic(err)
			}
			// Force a distinct timestamp, so the length is the only thing this
			// fixture holds constant and the identity is what must catch it.
			if err := os.Chtimes(r.path, time.Time{}, time.Now().Add(2*time.Second)); err != nil {
				panic(err)
			}
		})
	}
	return r.Fs.Stat(name)
}

type sameLengthReplaceFile struct {
	afero.File
	fs *sameLengthReplaceOnBookkeepingStatFs
}

func (f *sameLengthReplaceFile) Sync() error {
	err := f.File.Sync()
	f.fs.synced.Store(true)
	return err
}

// TestStoreAppendRepairsATornTailNobodyReadFirst pins A2: the repair belongs to
// the append, not only to the reader that happens to run before it.
//
// A foreign writer leaves a partial record at the end of a live journal after the
// store opened and before any activity read ran. Reading is what used to repair
// that tail, so a store that has not been read since has no excuse to append
// onto it — and appending onto it joins the new event to the fragment and syncs
// one malformed line. Success there is worse than a failure: the record is
// reported written, and the journal cannot be reopened afterwards.
//
// The assertion is the reopen. A successful append must leave a journal a fresh
// store can fold, which is what a session's next start does.
func TestStoreAppendRepairsATornTailNobodyReadFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_before", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// A foreign writer leaves an incomplete record. Nothing reads the journal
	// between this write and the append below.
	fragment := []byte(`{"kind":"job_started","seq":`)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open for the partial write: %v", err)
	}
	if _, err := f.Write(fragment); err != nil {
		_ = f.Close()
		t.Fatalf("write the fragment: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after the fragment: %v", err)
	}

	if err := s.Append(Event{Kind: EventJobFinished, JobID: "job_after", Type: JobShell}); err != nil {
		t.Fatalf("append after an unread torn tail: %v", err)
	}

	// The append's own record has to be readable, and so does everything that
	// was committed before the fragment. A reopen is the whole assertion: it is
	// what a session's next start does, and it fails outright on a malformed
	// line.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after an append across a torn tail: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	folded, err := reopened.Load()
	if err != nil {
		t.Fatalf("the journal a successful append left cannot be folded: %v", err)
	}
	if folded["job_after"] == nil {
		t.Fatalf("the appended record is not in the reopened journal, which folded %d records", len(folded))
	}
	if folded["job_before"] == nil {
		t.Fatal("the record committed before the fragment is missing from the reopened journal")
	}
	// The fragment is gone rather than buried inside a line: every line in the
	// journal is a complete record. Checking the bytes for the fragment's text
	// would prove nothing — that prefix occurs inside the complete record too —
	// so decode what is on disk instead.
	raw := mustReadFile(t, path)
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatalf("the journal does not end with a committed newline: %q", raw)
	}
	for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("journal line %d is not a complete record: %v (%q)", i+1, err, line)
		}
	}
}

// replaceOnOpenFs installs a second replacement the moment adoption opens the
// first one, which is the window A3 describes.
//
// Adoption examines the candidate it opened. If that examination repairs the
// file — a trailing fragment is the recoverable case — while the name has already
// moved on to a different file, the truncation lands in the journal that was just
// moved aside. And the store would then begin appending through a handle the name
// no longer reaches.
type replaceOnOpenFs struct {
	afero.Fs
	dir     string
	path    string
	armed   atomic.Bool
	instals atomic.Int64
}

func (r *replaceOnOpenFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if r.armed.CompareAndSwap(true, false) && name == r.path && flag&os.O_RDWR != 0 {
		// Move the candidate aside and install the second replacement, exactly as
		// a concurrent rename-and-replace would.
		if err := r.Rename(r.path, filepath.Join(r.dir, "jobs.jsonl.moved")); err != nil {
			return nil, err
		}
		// The replacement the name ends up reaching is torn: an incomplete
		// first record, which is the shape A3 says recovery truncates at.
		second, err := r.Fs.OpenFile(r.path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		if _, err := second.WriteString(`{"kind":"job_started","seq":9`); err != nil {
			_ = second.Close()
			return nil, err
		}
		if err := second.Close(); err != nil {
			return nil, err
		}
		r.instals.Add(1)
		// The handle adoption receives is the file that WAS named when it
		// resolved the path, not the one that replaced it.
		return r.Fs.OpenFile(filepath.Join(r.dir, "jobs.jsonl.moved"), flag, perm)
	}
	return r.Fs.OpenFile(name, flag, perm)
}

// TestStoreAdoptionNeverRepairsAJournalTheNameLeft pins A3's ordering.
//
// The candidate validation has to be a statement about the file the store will
// append to. Inspecting one file and repairing another — or repairing the file
// that was moved aside because the name moved while it was being examined — is
// the one way adoption can lose committed bytes: the moved-aside journal had a
// recoverable fragment, and the repair that was meant for the named journal cuts
// it. Nothing the store reports afterwards describes either file.
//
// So a name that reaches a different file than the candidate is refused rather
// than appended through, and the file that was moved aside keeps its bytes.
func TestStoreAdoptionNeverRepairsAJournalTheNameLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &replaceOnOpenFs{Fs: afero.NewOsFs(), dir: dir, path: path}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_original", Type: JobShell}); err != nil {
		t.Fatalf("append: %v", err)
	}
	committed := mustReadFile(t, path)

	// A torn fragment on the live journal, then a rename-and-replace. Adoption
	// will open the replacement; the fixture installs a SECOND replacement as it
	// does so.
	torn := append(bytes.Clone(committed), []byte(`{"kind":"job_started","seq":`)...)
	if err := os.WriteFile(path, torn, 0o644); err != nil {
		t.Fatalf("write the torn journal: %v", err)
	}
	moved := filepath.Join(dir, "jobs.jsonl.moved")
	// Rename the torn journal aside and put a first replacement in its place, so
	// the append has to adopt rather than write to the handle it holds.
	if err := os.Rename(path, filepath.Join(dir, "jobs.jsonl.orphan")); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	orphan := filepath.Join(dir, "jobs.jsonl.orphan")
	orphanBytes := mustReadFile(t, orphan)
	first := `{"kind":"job_started","seq":5,"ts":"0001-01-01T00:00:00Z","job_id":"job_first","type":"shell"}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatalf("install the first replacement: %v", err)
	}

	fs.armed.Store(true)
	appendErr := s.Append(Event{Kind: EventJobStarted, JobID: "job_after", Type: JobShell})
	if got := fs.instals.Load(); got != 1 {
		t.Fatalf("the fixture installed %d second replacements, want exactly 1: this test cannot reach A3 without it", got)
	}
	// A name that kept moving is not a journal this append can promise anything
	// about. Refusing is the answer that preserves the contract; succeeding is
	// only correct if it appended to what the name reaches NOW.
	if appendErr != nil {
		if !errors.Is(appendErr, ErrJournalReplaced) && !errors.Is(appendErr, ErrJournalNameUnavailable) {
			t.Fatalf("append across a replacement that moved again = %v, want a retryable replacement error", appendErr)
		}
	}
	// Whether it refused or adopted the newest file, the append may not have
	// landed anywhere the name does not reach.
	for _, p := range []string{orphan, moved} {
		if data, readErr := os.ReadFile(p); readErr == nil && bytes.Contains(data, []byte("job_after")) {
			t.Fatalf("the append landed in %s, a file the name no longer reaches", filepath.Base(p))
		}
	}
	// The torn journal that was moved aside keeps every byte it had committed.
	if got := mustReadFile(t, orphan); !bytes.HasPrefix(got, orphanBytes) {
		t.Fatal("recovery truncated the journal that had been moved aside")
	}
	// And the journal the name reaches is still a readable journal.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen the named journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Load(); err != nil {
		t.Fatalf("the named journal cannot be folded after adoption: %v", err)
	}
}

// TestScanEventsFromIncludesACompleteRecordAtPhysicalEOF pins M4: "unterminated"
// and "past the boundary" are different states, and only the second is withheld.
//
// A replacement journal can end in a complete JSON record with no trailing
// newline: an out-of-band writer, or a file rotated in from another tool. There
// is nothing more coming, so the boundary reported for it IS the file's end. A
// scan that withholds that record on every refresh hides the last completed job
// forever — a stable file never grows the newline that would release it.
//
// The other direction must not move: a record whose terminating newline has not
// committed, with the boundary genuinely short of the file, is still withheld,
// because those bytes are an append in flight. That case is covered above
// (TestScanEventsFromEndOffsetStopsAtTheBoundary), and this test asserts the
// distinction directly by scanning the same record twice: once with the boundary
// at EOF, once one byte short of it.
func TestScanEventsFromIncludesACompleteRecordAtPhysicalEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	complete := `{"kind":"job_started","seq":1,"ts":"0001-01-01T00:00:00Z","job_id":"job_last","type":"shell"}`
	if err := os.WriteFile(path, []byte(complete), 0o644); err != nil {
		t.Fatalf("write a journal with no trailing newline: %v", err)
	}
	eof := int64(len(complete))

	got, toOffset, err := ScanEventsFrom(t.Context(), path, 0, ScanLimits{EndOffset: eof})
	if err != nil {
		t.Fatalf("bounded scan at physical EOF: %v", err)
	}
	if len(got) != 1 || got[0].JobID != "job_last" {
		t.Fatalf("bounded scan at physical EOF = %+v, want the complete final record: an unterminated record that IS the file's end is finished, not in flight", got)
	}
	if toOffset != eof {
		t.Fatalf("bounded scan at physical EOF stopped at %d, want %d", toOffset, eof)
	}

	// The same bytes, with the boundary short of the file: the newline may yet
	// land, so the record stays out until it commits.
	withMore := []byte(complete + "\n")
	if err := os.WriteFile(path, withMore, 0o644); err != nil {
		t.Fatalf("rewrite with a newline: %v", err)
	}
	short, shortOffset, err := ScanEventsFrom(t.Context(), path, 0, ScanLimits{EndOffset: eof})
	if err != nil {
		t.Fatalf("bounded scan with the boundary short of EOF: %v", err)
	}
	if len(short) != 0 {
		t.Fatalf("bounded scan with the boundary short of EOF = %+v, want the record withheld until its newline commits", short)
	}
	if shortOffset != 0 {
		t.Fatalf("bounded scan with the boundary short of EOF advanced to %d, want 0", shortOffset)
	}

	// And the unbounded scan's long-standing behaviour is untouched: complete
	// JSON at a natural EOF is still included.
	whole, err := ScanEvents(t.Context(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("unbounded scan: %v", err)
	}
	if len(whole) != 1 || whole[0].JobID != "job_last" {
		t.Fatalf("unbounded scan = %+v, want the complete final record", whole)
	}
}

// handleStatFailsFs fails the held HANDLE's Stat once, on demand.
//
// Every question this store asks about the file it appends through is asked
// through the handle; the path is stat'd separately. A fixture that fails only
// the path's Stat cannot reach the boundary read's identity question at all.
type handleStatFailsFs struct {
	afero.Fs
	armed atomic.Bool
}

func (f *handleStatFailsFs) armHandleStatFailure() { f.armed.Store(true) }

func (f *handleStatFailsFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &handleStatFailsFile{File: file, fs: f}, nil
}

type handleStatFailsFile struct {
	afero.File
	fs *handleStatFailsFs
}

func (f *handleStatFailsFile) Stat() (os.FileInfo, error) {
	if f.fs.armed.CompareAndSwap(true, false) {
		return nil, errors.New("transient handle stat failure")
	}
	return f.File.Stat()
}

// TestCommittedJournalDoesNotBookATornTailItNeverExamined pins High 1.
//
// A foreign writer tears the tail of a live journal. The next boundary read is
// the one that would repair it — and its FIRST question is whether the name
// still reaches the file this store appends through, asked by statting the
// handle. When that stat fails, the repair is skipped, which is correct: the
// question was not answered.
//
// What must not happen is what used to: the read booked the torn length and
// timestamp as "what this store knows about its tail". A failed stat is not
// evidence that nothing changed, and the booking is a claim that something did
// examine it. Once the handle's stat recovers, the next append compares the file
// against that baseline, finds nothing changed, skips the tail inspection, and
// writes its event onto the fragment — one malformed line, reported as a
// successful append, and the journal no longer reopens.
//
// The assertion is the reopen: the bytes this test cares about are only visible
// after the store is closed and opened again.
func TestCommittedJournalDoesNotBookATornTailItNeverExamined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &handleStatFailsFs{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// A foreign writer tears the tail mid-record.
	partial := []byte(`{"kind":"job_started","seq":`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), partial...), 0o644); err != nil {
		t.Fatalf("tear the tail: %v", err)
	}

	// The boundary read cannot answer whether the name still reaches the handle
	// it appends through. It must still answer the reader — an activity poll on
	// a live journal may not fail — but it must not claim to know its tail.
	fs.armHandleStatFailure()
	unsettled, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal with an unsamplable handle: %v", err)
	}
	if unsettled.Size > int64(len(mustReadFile(t, path))) {
		t.Fatalf("boundary reports %d bytes for a %d-byte journal", unsettled.Size, len(mustReadFile(t, path)))
	}

	// The handle's stat recovers. Nothing has repaired the fragment, so the
	// append has to inspect the tail before writing on it.
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_unexamined_tail", Type: JobShell}); err != nil {
		t.Fatalf("append after an unexamined torn tail: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after appending past a torn tail nobody examined: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	for _, id := range []string{"job_first", "job_after_unexamined_tail"} {
		if records[id] == nil {
			t.Fatalf("%q is missing from the reopened store's fold", id)
		}
	}
}

// replaceBeforeAppendFs installs a replacement journal under the name and then
// fails the held HANDLE's Stat once, so the append's pre-write question
// ("does the name still reach the file this store appends through?") is the one
// that cannot be answered.
type replaceBeforeAppendFs struct {
	afero.Fs
	failHandleStat atomic.Bool
}

func (f *replaceBeforeAppendFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &replaceBeforeAppendFile{File: file, fs: f}, nil
}

type replaceBeforeAppendFile struct {
	afero.File
	fs *replaceBeforeAppendFs
}

func (f *replaceBeforeAppendFile) Stat() (os.FileInfo, error) {
	if f.fs.failHandleStat.CompareAndSwap(true, false) {
		return nil, errors.New("transient handle stat failure")
	}
	return f.File.Stat()
}

// TestStoreAppendRefusesWhenTheJournalWasReplacedAndTheHandleCannotBeSampled
// pins High 2.
//
// The journal is renamed aside and a replacement — same bytes, same length, same
// timestamp — is installed under the name. The append's first question is whether
// the name still reaches the file it appends through, and that question is asked
// by statting the held handle. When that stat fails, the answer used to be
// "yes": the append proceeded through the handle, which still reaches the file
// that was moved aside, and the post-sync verification compared two samples of
// the REPLACEMENT and vouched for the write.
//
// A completion acknowledged that way is lost: it is not in the named journal, so
// activity and reopen never see it, and no caller retries.
//
// The assertion is the named journal's own contents after the append reports
// success — a reader of the name has to find the event.
func TestStoreAppendRefusesWhenTheJournalWasReplacedAndTheHandleCannotBeSampled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	saved := filepath.Join(dir, "jobs.jsonl.saved")

	fs := &replaceBeforeAppendFs{Fs: afero.NewOsFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Move the journal aside and install a replacement carrying the same bytes.
	// Same length and timestamp is what makes the content comparison read "no
	// change"; the identity is the only evidence left, and that is the question
	// the fixture is about to make unanswerable.
	original := mustReadFile(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := os.Rename(path, saved); err != nil {
		t.Fatalf("rename aside: %v", err)
	}
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("install replacement: %v", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("pin the replacement's mtime: %v", err)
	}

	fs.failHandleStat.Store(true)
	appendErr := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_replacement", Type: JobShell})
	named := mustReadFile(t, path)
	carries := bytes.Contains(named, []byte(`"job_after_replacement"`))

	if appendErr == nil && !carries {
		// The append reported success for an event the named journal never
		// received. That is the defect: success has to mean it landed.
		t.Fatal("the append reported success but the named journal does not hold the event")
	}
	if appendErr != nil && !errors.Is(appendErr, ErrJournalNameUnavailable) {
		// Refusing is right; refusing with a non-retryable answer strands the
		// event, because the caller has no way to tell that waiting will help.
		t.Fatalf("append refused with %v, want a retryable journal-name error", appendErr)
	}
	if appendErr != nil {
		// The caller retries once the handle can be sampled again, and the retry
		// must follow the name to the replacement.
		if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_replacement", Type: JobShell}); err != nil {
			t.Fatalf("retry after the replacement: %v", err)
		}
		if !bytes.Contains(mustReadFile(t, path), []byte(`"job_after_replacement"`)) {
			t.Fatal("the retry reported success but the named journal does not hold the event")
		}
	}
}

// truncateDuringSyncFs cuts the journal back while an append is in its fsync.
//
// The pathname keeps reaching the same inode, so every identity check passes:
// this is the in-place truncation a rotation or a foreign writer performs on a
// live file, not a rename.
type truncateDuringSyncFs struct {
	afero.Fs
	path string
	cut  atomic.Int64
	once sync.Once
	arm  atomic.Bool
}

func (f *truncateDuringSyncFs) armTruncation(cut int64) {
	f.cut.Store(cut)
	f.arm.Store(true)
}

func (f *truncateDuringSyncFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &truncateDuringSyncFile{File: file, fs: f}, nil
}

type truncateDuringSyncFile struct {
	afero.File
	fs *truncateDuringSyncFs
}

func (f *truncateDuringSyncFile) Sync() error {
	err := f.File.Sync()
	if f.fs.arm.CompareAndSwap(true, false) {
		f.fs.once.Do(func() {
			if truncErr := os.Truncate(f.fs.path, f.fs.cut.Load()); truncErr != nil {
				panic(truncErr)
			}
		})
	}
	return err
}

// TestStoreAppendReportsATruncationDuringItsSyncAsRetryable pins High 3.
//
// A foreign writer cuts the journal back while this append is in its fsync, far
// enough that the record it just wrote is gone. The pathname still reaches the
// same inode, so the identity check passes; the only evidence is that the file's
// length is now shorter than where this append's bytes ended.
//
// That evidence has two meanings and the store must not collapse them. A
// filesystem whose attributes lag also reports less than the append wrote — and
// there the bytes are present and the append succeeded. Here the bytes are gone.
// Reporting success is how a shell finalizer stops retrying a completion no
// reader can find.
//
// The assertion is the record: after the append reports its outcome, the named
// journal must either hold the event or the append must have said it did not
// land.
func TestStoreAppendReportsATruncationDuringItsSyncAsRetryable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	fs := &truncateDuringSyncFs{Fs: afero.NewOsFs(), path: path}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// The cut lands inside the first record, so nothing this store wrote after it
	// can still be in the file.
	kept := int64(len(mustReadFile(t, path))) - 10
	if kept <= 0 {
		t.Fatalf("fixture journal is too short to cut record bytes from")
	}
	fs.armTruncation(kept)

	appendErr := s.Append(Event{Kind: EventJobStarted, JobID: "job_cut_away", Type: JobShell})
	carries := bytes.Contains(mustReadFile(t, path), []byte(`"job_cut_away"`))

	if appendErr == nil && !carries {
		t.Fatalf("the append reported success for an event the journal no longer holds: it is %d bytes, cut back to %d", len(mustReadFile(t, path)), kept)
	}
	if appendErr != nil && !errors.Is(appendErr, ErrJournalReplaced) {
		t.Fatalf("append refused with %v, want a retryable truncation error", appendErr)
	}
}

// TestStoreAppendRefusesAMalformedTailItCannotRepair pins Medium 4.
//
// A foreign writer leaves an unterminated tail that is not a cut-off record but
// garbage: there is no newline to cut back to and no complete record to
// terminate. The bytes are evidence and must be preserved.
//
// What must not happen is the append treating that as a repaired tail. The
// fragment stays in the file, so the append joins its JSON onto it and fsyncs a
// line no reader can decode, reporting success — and the journal stops
// reopening. The same damage a torn tail causes, reached by a path that decided
// the tail was settled.
//
// The assertions are the preserved bytes and the reopen.
func TestStoreAppendRefusesAMalformedTailItCannotRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	garbage := []byte(`this is not one of our records`)
	if err := os.WriteFile(path, append(mustReadFile(t, path), garbage...), 0o644); err != nil {
		t.Fatalf("install a malformed tail: %v", err)
	}
	before := mustReadFile(t, path)

	appendErr := s.Append(Event{Kind: EventJobStarted, JobID: "job_after_garbage", Type: JobShell})
	if appendErr == nil {
		t.Fatal("the append reported success over a malformed tail it could not repair")
	}
	if !errors.Is(appendErr, ErrMalformedTrailingRecord) {
		t.Fatalf("append refused with %v, want the malformed-trailing-record error", appendErr)
	}
	// The garbage is preserved: a malformed tail is evidence, and rewriting it
	// away destroys the only copy of whatever the writer meant to record.
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("the refused append changed the journal from %d to %d bytes", len(before), len(after))
	}

	// A reader still answers with the bytes it can decode, and the store still
	// refuses writers until someone resolves the tail.
	if _, err := s.CommittedJournal(); err != nil {
		t.Fatalf("CommittedJournal over a malformed tail: %v", err)
	}
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_still_refused", Type: JobShell}); err == nil {
		t.Fatal("a second append also reported success over the malformed tail")
	}
}

// TestStoreAppendDoesNotReuseASurvivingBatchPrefix pins Medium 5.
//
// A batch is written and fsynced, and then the journal is rotated away before
// the append can verify where the bytes landed. The append is correctly refused
// and the batch's sequences are reserved.
//
// The rotation is then undone and the ORIGINAL journal comes back — carrying a
// complete PREFIX of that batch, which is the shape a crash or a partial
// rotation leaves. The lookup that asks "did this write arrive" compares the
// file's length against the whole batch, finds it short, and used to conclude
// the write was absent: it cleared the reservation, rewound the sequence, and
// allocated the retry a number the surviving record already holds. Two records
// on one sequence, and the fold keeps whichever it met first.
//
// The assertion is the journal's own sequences: no number may appear twice.
func TestStoreAppendDoesNotReuseASurvivingBatchPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	saved := filepath.Join(dir, "jobs.jsonl.rotated")

	fs := &renameOnSyncFs{Fs: afero.NewOsFs(), from: path, to: saved, leaveAbsent: true}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AppendBatch([]Event{
		{Kind: EventJobStarted, JobID: "job_a", Type: JobShell},
		{Kind: EventJobStarted, JobID: "job_b", Type: JobShell},
	}); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	batchStart := int64(len(mustReadFile(t, path)))

	// The rotation lands in this batch's fsync, so the name is gone by the time
	// the append asks whether its bytes reached it.
	fs.arm()
	if err := s.AppendBatch([]Event{
		{Kind: EventJobFinished, JobID: "job_a", Type: JobShell},
		{Kind: EventJobFinished, JobID: "job_b", Type: JobShell},
	}); err == nil {
		t.Fatal("the batch reported success although its journal was rotated away")
	}

	// The rotation is undone with only the first record of that batch surviving.
	rotated := mustReadFile(t, saved)
	cut := int64(-1)
	for i := batchStart; i < int64(len(rotated)); i++ {
		if rotated[i] == '\n' {
			cut = i + 1
			break
		}
	}
	if cut <= batchStart {
		t.Fatalf("the rotated journal holds no complete record past byte %d", batchStart)
	}
	// The ORIGINAL journal comes back — same inode, so the store's handle still
	// reaches it — carrying a complete prefix of the interrupted batch. That is
	// the shape a crash or a partial rotation leaves, and it is the only one
	// where the reservation question is about THIS file rather than a
	// replacement the store would adopt and take its sequences from.
	if err := os.Truncate(saved, cut); err != nil {
		t.Fatalf("cut the rotated journal back to its surviving prefix: %v", err)
	}
	if err := os.Rename(saved, path); err != nil {
		t.Fatalf("restore the rotated journal under its name: %v", err)
	}

	appendErr := s.Append(Event{Kind: EventJobFinished, JobID: "job_b", Type: JobShell})

	// Whether the append wrote or refused, the journal must not end up with one
	// sequence on two records. A reused number is the damage this test is about.
	seen := map[int64]bool{}
	for _, line := range bytes.Split(mustReadFile(t, path), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("journal line does not decode after the retry: %v", err)
		}
		if seen[e.Seq] {
			t.Fatalf("sequence %d appears twice in the journal: the retry reused a number the surviving batch prefix already holds", e.Seq)
		}
		seen[e.Seq] = true
	}
	if appendErr != nil && !errors.Is(appendErr, ErrJournalLookupInconclusive) {
		t.Fatalf("append refused with %v, want the inconclusive-lookup error", appendErr)
	}
}

// TestStoreBoundaryReportsAnOutOfBandEventPastAConfirmedEnd pins Medium 6.
//
// A confirmed append under lagging attributes records its own end and lets the
// boundary substitute it while the filesystem keeps reporting less. That
// substitution is what keeps the just-committed record visible.
//
// It is only honest while the file really is that long. A foreign writer that
// appends a complete event past the confirmed end makes the file longer; the
// attributes are still behind, and substituting the confirmed end there hides
// the foreign record — the boundary read has the file's real length in hand and
// reports a shorter one anyway, so live activity omits a job that is already
// durable until the attributes catch up.
//
// The assertion is the boundary's length and the scan it bounds.
func TestStoreBoundaryReportsAnOutOfBandEventPastAConfirmedEnd(t *testing.T) {
	base := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Freeze the reported attributes below the length the append confirmed, so
	// the boundary is on its substitution path.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)
	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_confirmed", Type: JobShell}); err != nil {
		t.Fatalf("append behind a lagging sample: %v", err)
	}

	// A foreign writer appends a complete event past the confirmed end while the
	// attributes stay frozen.
	outOfBand, err := json.Marshal(Event{Kind: EventJobStarted, JobID: "job_out_of_band", Type: JobShell})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := afero.WriteFile(base, path, append(append(mustReadFile(t, path), outOfBand...), '\n'), 0o644); err != nil {
		t.Fatalf("append out of band: %v", err)
	}

	committed, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	onDisk := int64(len(mustReadFile(t, path)))
	if committed.Size != onDisk {
		t.Fatalf("boundary reports %d bytes for a %d-byte journal: the completed out-of-band event is invisible", committed.Size, onDisk)
	}
	events, err := ScanEvents(t.Context(), path, ScanLimits{EndOffset: committed.Size, Bounded: true})
	if err != nil {
		t.Fatalf("bounded scan: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.JobID == "job_out_of_band" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the bounded scan did not return the out-of-band event: %+v", events)
	}
}

// TestStoreBoundaryRecordsAnUnmovedTimestampWithoutACursor pins Medium 7.
//
// An append that changes the file's length while its reported modification time
// does not move is direct evidence that timestamps here cannot resolve a write.
// The boundary has to record that, because on the same coarse clock a later
// same-length rewrite leaves the reconciled size and timestamp identical and the
// live fold would keep serving rows the rewrite removed.
//
// The detection used to sit beside the cursor's own checks, which return early
// for exactly this sample — so an invalid cursor meant the evidence was never
// looked for, and the boundary kept advertising trustworthy metadata.
//
// The assertion is the claim the boundary makes to its callers.
func TestStoreBoundaryRecordsAnUnmovedTimestampWithoutACursor(t *testing.T) {
	fs := frozenTimeFs{Fs: afero.NewMemMapFs()}
	const path = "/jobs.jsonl"
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_first", Type: JobShell}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Drop the cursor the way any failed bookkeeping does, so the append below
	// runs with no cursor to infer reliability from. The filesystem's timestamp
	// is frozen for the whole test, so the append changes the length and leaves
	// the reported modification time where it was.
	s.mu.Lock()
	s.cursor.valid = false
	s.cursor.disabled = false
	s.mu.Unlock()

	if err := s.Append(Event{Kind: EventJobStarted, JobID: "job_under_frozen_clock", Type: JobShell}); err != nil {
		t.Fatalf("append under a frozen timestamp: %v", err)
	}
	after, err := s.CommittedJournal()
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if after.MetadataResolvesWrites {
		t.Fatal("the boundary still advertises trustworthy metadata although this append changed the length and the timestamp did not move")
	}
}

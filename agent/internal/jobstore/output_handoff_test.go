package jobstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// compactionHandoffCensus records whether a directory barrier follows the
// removal of a compaction's pending journal. The journal's deletion is what
// retires the handoff, so it has to reach the disk before any later append's
// bytes can outlive it.
type compactionHandoffCensus struct {
	afero.Fs
	metaPath      string
	journalGone   bool
	dirAfterGone  int
	dirBeforeGone int
}

func (c *compactionHandoffCensus) Remove(name string) error {
	if err := c.Fs.Remove(name); err != nil {
		return err
	}
	if name == outputPendingMetaPath(c.metaPath) {
		c.journalGone = true
	}
	return nil
}

func (c *compactionHandoffCensus) Open(name string) (afero.File, error) {
	f, err := c.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return compactionHandoffFile{File: f, census: c, name: name}, nil
}

func (c *compactionHandoffCensus) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := c.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return compactionHandoffFile{File: f, census: c, name: name}, nil
}

type compactionHandoffFile struct {
	afero.File
	census *compactionHandoffCensus
	name   string
}

func (f compactionHandoffFile) Sync() error {
	if err := f.File.Sync(); err != nil {
		return err
	}
	if f.name != f.census.metaPath && f.name != f.census.metaPath+".tmp" {
		if f.census.journalGone {
			f.census.dirAfterGone++
		} else {
			f.census.dirBeforeGone++
		}
	}
	return nil
}

// A compaction replaces the log and writes a pending journal, then removes the
// journal. If that removal has not reached the disk when a later append's bytes
// do, recovery finds a stale journal describing a tail the expanded file no
// longer ends with, and refuses intact output. So the handoff must be committed
// by a directory barrier before the append returns.
func TestOutputCompactionHandoffIsCommittedBeforeLaterAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	census := &compactionHandoffCensus{Fs: afero.NewOsFs(), metaPath: outputMetaPath(path)}
	store, err := openOutputFs(census, path, 16)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // isolate the compaction's own commit from the tick
	// Twice the cap crosses the compaction threshold.
	if _, err := store.Append(bytes.Repeat([]byte("a"), 40)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if !census.journalGone {
		t.Fatal("no compaction ran: the pending journal was never removed")
	}
	if census.dirAfterGone == 0 {
		t.Fatalf("the pending journal's removal was never committed by a directory barrier (%d barriers before it, 0 after); a crash can restore the stale journal and refuse the surviving output",
			census.dirBeforeGone)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// closeFlushFailFS fails the metadata file's Sync once told to, so a store can
// reach the state where its durability barrier failed.
type closeFlushFailFS struct {
	afero.Fs
	metaPath string
	fail     bool
}

func (f *closeFlushFailFS) Open(name string) (afero.File, error) {
	file, err := f.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return closeFlushFailFile{File: file, fs: f, name: name}, nil
}

func (f *closeFlushFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return closeFlushFailFile{File: file, fs: f, name: name}, nil
}

type closeFlushFailFile struct {
	afero.File
	fs   *closeFlushFailFS
	name string
}

func (f closeFlushFailFile) Sync() error {
	if f.fs.fail && (f.name == f.fs.metaPath || f.name == f.fs.metaPath+".tmp") {
		return errors.New("injected metadata sync failure")
	}
	return f.File.Sync()
}

// If the close-time barrier fails, the store must not report the output as
// durable. Flush has to surface the same failure afterwards, so a caller that
// records a terminal byte count cannot succeed against bytes that never
// reached the disk.
func TestOutputFlushReportsAFailedCloseBarrier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	fs := &closeFlushFailFS{Fs: afero.NewOsFs(), metaPath: outputMetaPath(path)}
	store, err := openOutputFs(fs, path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // hold the tick off; the close owes the barrier
	if _, err := store.Append([]byte("output that must be durable\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Append's own metadata write succeeded, so the store holds bytes whose log
	// barrier is still owed. The fault starts now, at the close-time barrier.
	fs.fail = true
	if err := store.Close(); err == nil {
		t.Fatal("close reported success although its metadata barrier failed")
	}
	// The failed barrier must stay retryable and visible.
	if err := store.Flush(); err == nil {
		t.Fatal("Flush reported success after a failed close barrier: a terminal record would name bytes that never reached the disk")
	}
	fs.fail = false
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush after the fault cleared: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close after the fault cleared: %v", err)
	}
}

// After a compaction the store must hold a durable final sidecar bound to the
// compacted log, and it must have no pending journal left. A crash then has
// nothing stale to restore, so recovery cannot refuse the surviving tail.
func TestOutputCompactionLeavesNoStaleSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := openOutputFs(afero.NewOsFs(), path, 16)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.Append(bytes.Repeat([]byte("a"), 40)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := os.Stat(outputPendingMetaPath(outputMetaPath(path))); err == nil {
		t.Fatal("a pending journal survived the compaction")
	}
	// The surviving compacted log must read back rather than be refused.
	if _, _, err := OutputFileStats(path); err != nil {
		t.Fatalf("readers refused the compacted log: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// A crash between publishing a compaction's journal and replacing the log leaves
// the journal's tail ahead of a log that never grew. The journal must therefore
// only be published once the log holds the bytes it names: otherwise recovery
// sees a pending sidecar describing more than the file has, the journal carries
// no anchor to fall back to, and it refuses output that survived intact.
func TestOutputCompactionJournalIsPublishedOnlyAfterTheLogIsDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	census := &compactionJournalOrderCensus{
		Fs:          afero.NewOsFs(),
		journalPath: outputPendingMetaPath(outputMetaPath(path)),
		outputPath:  path,
	}
	store, err := openOutputFs(census, path, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour
	// The store's own open pays one barrier; what this test measures is the
	// ordering inside the append that publishes the journal.
	census.logSynced = false

	// Enough to cross the twice-the-cap compaction threshold and publish a journal.
	if _, err := store.Append(bytes.Repeat([]byte("a"), 24)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if !census.sawJournal {
		t.Fatal("the append did not publish a compaction journal; the test did not reach the handoff")
	}
	if census.journalBeforeLogSync {
		t.Fatal("the compaction journal was published before the log was synced: a crash there leaves a journal describing bytes the log does not hold, which recovery refuses")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// compactionJournalOrderCensus records whether the log was synced before the
// compaction journal replaced its name.
type compactionJournalOrderCensus struct {
	afero.Fs
	journalPath          string
	outputPath           string
	sawJournal           bool
	logSynced            bool
	journalBeforeLogSync bool
}

// Rename is the moment the journal becomes visible under its real name.
func (c *compactionJournalOrderCensus) Rename(oldname, newname string) error {
	if newname == c.journalPath {
		c.sawJournal = true
		if !c.logSynced {
			c.journalBeforeLogSync = true
		}
	}
	return c.Fs.Rename(oldname, newname)
}

func (c *compactionJournalOrderCensus) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := c.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	if name == c.outputPath {
		return &logSyncWatcher{File: f, c: c}, nil
	}
	return f, nil
}

type logSyncWatcher struct {
	afero.File
	c *compactionJournalOrderCensus
}

func (w *logSyncWatcher) Sync() error {
	if !w.c.sawJournal {
		w.c.logSynced = true
	}
	return w.File.Sync()
}

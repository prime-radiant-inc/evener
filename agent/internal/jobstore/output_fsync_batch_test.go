package jobstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// outputSyncCensus counts fsync calls by the file they reach, so a test can
// separate the log's barrier, the metadata's, and the parent directory's.
type outputSyncCensus struct {
	afero.Fs
	outputPath string
	metaPath   string
	output     int
	meta       int
	dir        int
}

func (c *outputSyncCensus) Open(name string) (afero.File, error) {
	f, err := c.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return outputSyncCensusFile{File: f, census: c, name: name}, nil
}

func (c *outputSyncCensus) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := c.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return outputSyncCensusFile{File: f, census: c, name: name}, nil
}

type outputSyncCensusFile struct {
	afero.File
	census *outputSyncCensus
	name   string
}

func (f outputSyncCensusFile) Sync() error {
	switch f.name {
	case f.census.outputPath:
		f.census.output++
	case f.census.metaPath, f.census.metaPath + ".tmp":
		f.census.meta++
	default:
		f.census.dir++
	}
	return f.File.Sync()
}

// A command that prints a megabyte reaches Append in 32 KiB chunks. Each chunk
// used to fsync the log, re-hash it, fsync a rewritten metadata file, and fsync
// the parent directory (#6401). The chunks now pay no barriers at all, and the
// job's finish pays one round.
func TestOutputAppendBatchesBarriersToTheFinish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	census := &outputSyncCensus{Fs: afero.NewOsFs(), outputPath: path, metaPath: outputMetaPath(path)}
	store, err := openOutputFs(census, path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // hold the tick off; only Close pays the finish barriers
	afterOpen := *census

	chunk := bytes.Repeat([]byte("x"), 32<<10)
	for range 8 {
		if _, err := store.Append(chunk); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// The metadata bytes are synced before every rename: a rename that outlives
	// a power loss while its contents do not would replace durable metadata with
	// an empty or malformed file. The log and the directory stay off the hot
	// path, which is what the three-barriers-per-chunk cost was.
	afterAppends := *census
	if got := afterAppends; got != (outputSyncCensus{Fs: census.Fs, outputPath: path, metaPath: outputMetaPath(path), output: afterOpen.output, meta: afterOpen.meta + 8, dir: afterOpen.dir}) {
		t.Fatalf("eight 32 KiB appends paid %d log, %d metadata, and %d directory fsyncs; want 0 log, 8 metadata (rename durability), 0 directory",
			got.output-afterOpen.output, got.meta-afterOpen.meta, got.dir-afterOpen.dir)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	afterClose := *census
	if got := afterClose.output - afterAppends.output; got != 1 {
		t.Fatalf("finish paid %d log fsyncs, want exactly 1", got)
	}
	if got := afterClose.meta - afterAppends.meta; got != 1 {
		t.Fatalf("finish paid %d metadata fsyncs, want exactly 1", got)
	}
	// The finish commits the rename that the appends left uncommitted, so it pays
	// the parent-directory barrier once rather than once per chunk (#6402).
	if got := afterClose.dir - afterAppends.dir; got != 1 {
		t.Fatalf("finish paid %d directory fsyncs, want exactly 1 for the commit", got)
	}
}

// The tick is what bounds how stale a running job's recovery anchor can be: an
// append pays the log barrier once the interval has elapsed, so a long-running
// job's anchor trails its output by at most one interval rather than growing
// unbounded. An append inside the interval pays none.
func TestOutputAppendPaysTheBarrierOnceTheSyncIntervalElapses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	census := &outputSyncCensus{Fs: afero.NewOsFs(), outputPath: path, metaPath: outputMetaPath(path)}
	store, err := openOutputFs(census, path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour
	atOpen := *census
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()

	if _, err := store.Append([]byte("first\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	afterFirst := *census
	if got := afterFirst.output - atOpen.output; got != 0 {
		t.Fatalf("an append inside the sync interval paid %d log fsyncs, want none", got)
	}

	// Age the anchor past the interval without sleeping: the tick reads
	// lastMetaSync, so moving it back is the interval elapsing.
	store.lastMetaSync = time.Now().Add(-2 * time.Hour)
	if _, err := store.Append([]byte("second\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := census.output - afterFirst.output; got != 1 {
		t.Fatalf("an append after the sync interval paid %d log fsyncs, want exactly 1", got)
	}
	// The barrier re-anchors the pace, so the next append owes nothing again.
	reAnchored := *census
	if _, err := store.Append([]byte("third\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := census.output - reAnchored.output; got != 0 {
		t.Fatalf("the append after a barrier paid %d log fsyncs, want none", got)
	}
}

// The parent-directory barrier commits a rename, so an append pays none and the
// barrier pays one — once, not once per 32 KiB chunk (#6402).
func TestOutputMetaDirectoryBarrierOnlyOnTheCreatingWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	census := &outputSyncCensus{Fs: afero.NewOsFs(), outputPath: path, metaPath: outputMetaPath(path)}
	store, err := openOutputFs(census, path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // hold the tick off; only Close commits the rename
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()
	// Open's metadata write is itself a barrier, so it commits its own rename.
	if census.dir != 1 {
		t.Fatalf("open paid %d directory fsyncs, want 1 for its barrier", census.dir)
	}

	for range 2 {
		if _, err := store.Append([]byte("output\n")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if census.dir != 1 {
		t.Fatalf("appends paid %d directory fsyncs, want none: only a barrier commits the rename", census.dir-1)
	}
}

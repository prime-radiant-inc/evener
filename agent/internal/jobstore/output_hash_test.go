package jobstore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
)

// outputReadCountingFS counts the bytes read back from the output file through
// handles opened by path: the store's own append handle is not one of them, so
// these are re-reads (hashing the file for its metadata checksum).
type outputReadCountingFS struct {
	afero.Fs
	output string
	read   atomic.Int64
}

func (fs *outputReadCountingFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return fs.Fs.(afero.Lstater).LstatIfPossible(name)
}

func (fs *outputReadCountingFS) Open(name string) (afero.File, error) {
	f, err := fs.Fs.Open(name)
	if err != nil || name != fs.output {
		return f, err
	}
	return &countingReadFile{File: f, read: &fs.read}, nil
}

type countingReadFile struct {
	afero.File
	read *atomic.Int64
}

func (f *countingReadFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.read.Add(int64(n))
	return n, err
}

// #3851: every append hashed the whole retained file for its metadata
// checksum, so a job's cost per append grew with its output (up to twice the
// cap). The checksum now follows the appended bytes, and an append re-reads
// nothing.
func TestOutputAppendHashesOnlyTheBytesItWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_H.log")
	fs := &outputReadCountingFS{Fs: afero.NewOsFs(), output: path}
	store, err := createOutputFsWithSync(fs, path, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunk := []byte(strings.Repeat("x", 99) + "\n")
	before := fs.read.Load()
	for range 50 {
		if _, err := store.Append(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if got := fs.read.Load() - before; got != 0 {
		t.Fatalf("50 appends re-read %d bytes of the output file, want 0", got)
	}
}

// The running checksum stays the checksum of the retained file across appends,
// compactions and a reopen, so durable readers still validate it.
func TestOutputRunningChecksumMatchesTheRetainedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_K.log")
	store, err := CreateOutput(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	for i := range 30 {
		line := strings.Repeat(string(rune('a'+i%26)), 9) + "\n"
		all.WriteString(line)
		if _, err := store.Append([]byte(line)); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		meta, ok, err := readOutputMeta(afero.NewOsFs(), outputMetaPath(path))
		if err != nil || !ok {
			t.Fatalf("read metadata: ok=%v err=%v", ok, err)
		}
		if meta.RetainedSHA256 != outputBytesSHA256(raw) {
			t.Fatalf("after %d bytes the metadata checksum does not match the %d-byte retained file", all.Len(), len(raw))
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOutput(path, 64)
	if err != nil {
		t.Fatalf("reopen validates the running checksum: %v", err)
	}
	if _, err := reopened.Append([]byte("after reopen\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := readOutputMeta(afero.NewOsFs(), outputMetaPath(path))
	if err != nil || meta.RetainedSHA256 != outputBytesSHA256(raw) {
		t.Fatalf("checksum after reopen and append does not match the file: %v", err)
	}
	_ = reopened.Close()
}

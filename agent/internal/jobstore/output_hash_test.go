package jobstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// An append re-reads nothing of the output file: the metadata checksum
// follows the bytes the append writes, so an append's cost stays flat as the
// job's output grows (#3851). countingFs wraps handles opened by path; the
// store's own append handle is not one of them.
func TestOutputAppendHashesOnlyTheBytesItWrites(t *testing.T) {
	const path = "/job_H.log"
	var bytesRead int64
	fs := countingFs{Fs: afero.NewMemMapFs(), bytesRead: &bytesRead}
	if err := afero.WriteFile(fs, path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := openOutputFsNoSync(fs, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunk := []byte(strings.Repeat("x", 99) + "\n")
	before := bytesRead
	for range 50 {
		if _, err := store.Append(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if got := bytesRead - before; got != 0 {
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

// shortWriteFile writes only the first half of the next Write and fails it,
// the way a full disk or an interrupted write leaves an append.
type shortWriteFile struct {
	afero.File
	short bool
}

func (f *shortWriteFile) Write(p []byte) (int, error) {
	if !f.short {
		return f.File.Write(p)
	}
	f.short = false
	n, _ := f.File.Write(p[:len(p)/2])
	return n, errors.New("short write")
}

// The running checksum follows the bytes that reached the file, not the bytes
// an append was asked to write.
func TestOutputRunningChecksumFollowsAShortWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_S.log")
	store, err := CreateOutputNoSync(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.f = &shortWriteFile{File: store.f, short: true}
	if n, err := store.Append([]byte("abcdef")); err == nil || n != 3 {
		t.Fatalf("short Append = %d, %v; want 3 and an error", n, err)
	}
	if _, err := store.Append([]byte("ghi\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := readOutputMeta(afero.NewOsFs(), outputMetaPath(path))
	if err != nil || string(raw) != "abcghi\n" || meta.RetainedSHA256 != outputBytesSHA256(raw) {
		t.Fatalf("after a short write the file is %q and its checksum matches=%v (%v)", raw, meta.RetainedSHA256 == outputBytesSHA256(raw), err)
	}
}

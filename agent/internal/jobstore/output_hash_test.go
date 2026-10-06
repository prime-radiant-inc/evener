package jobstore

import (
	"bytes"
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

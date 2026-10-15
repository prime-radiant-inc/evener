package execenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// readBounded is the single bounded read under every whole-file read; these
// tests pin its byte-cap contract directly, at small limits, against synthetic
// inputs.

// endlessReader never ends, the shape a device (/dev/zero) or an oversized
// procfs regular presents as.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// errTestRead is the sentinel errReader yields, so the propagation test can
// tell a real read failure from the cap's refusal.
var errTestRead = errors.New("test read failure")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errTestRead }

func TestReadBoundedReportsPastLimit(t *testing.T) {
	t.Parallel()
	const limit = 1 << 10
	got, over, err := readBounded(context.Background(), endlessReader{}, nil, limit)
	if err != nil {
		t.Fatalf("readBounded(endless): %v", err)
	}
	if !over {
		t.Fatalf("readBounded(endless) read %d bytes without reporting the cap", len(got))
	}
}

func TestReadBoundedReadsUnderLimit(t *testing.T) {
	t.Parallel()
	want := []byte("payload well under the cap\n")
	got, over, err := readBounded(context.Background(), bytes.NewReader(want), nil, 1<<10)
	if err != nil {
		t.Fatalf("readBounded(under cap): %v", err)
	}
	if over {
		t.Fatal("readBounded(under cap) reported the cap")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("readBounded(under cap) = %q, want %q", got, want)
	}
}

func TestReadBoundedAcceptsExactlyLimit(t *testing.T) {
	t.Parallel()
	const limit = 4
	want := bytes.Repeat([]byte("a"), limit)
	got, over, err := readBounded(context.Background(), bytes.NewReader(want), nil, limit)
	if err != nil {
		t.Fatalf("readBounded(exactly the cap): %v", err)
	}
	if over {
		t.Fatal("readBounded(exactly the cap) reported the cap")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("readBounded(exactly the cap) = %q, want %q", got, want)
	}
}

func TestReadBoundedPropagatesReadError(t *testing.T) {
	t.Parallel()
	_, over, err := readBounded(context.Background(), errReader{}, nil, 1<<10)
	if !errors.Is(err, errTestRead) {
		t.Fatalf("readBounded(failing reader) err = %v, want the reader's own error", err)
	}
	if over {
		t.Fatal("readBounded(failing reader) reported the cap for a genuine read failure")
	}
}

func TestReadFileCappedReadsARegularFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")
	want := []byte("ordinary source bytes\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadFileCapped(path)
	if err != nil {
		t.Fatalf("ReadFileCapped(regular): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFileCapped(regular) = %q, want %q", got, want)
	}
}

func TestReadFileCappedFailsFastPastCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "oversize.log")
	sparseOversize(t, path)
	got, err := ReadFileCapped(path)
	if err == nil {
		t.Fatalf("ReadFileCapped(oversize) = %d bytes, nil; want the read-limit refusal", len(got))
	}
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("ReadFileCapped(oversize) err = %v, want a *ReadLimitError", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
}

// sparseOversize leaves a fresh file at path one byte past the whole-file
// read cap; the fixture costs no I/O.
func sparseOversize(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create oversize: %v", err)
	}
	if err := os.Truncate(path, MaxWholeFileReadBytes+1); err != nil {
		t.Fatalf("truncate oversize: %v", err)
	}
}

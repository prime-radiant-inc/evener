package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
)

// bytesCountedFS counts the bytes a read_file call pulls through the
// environment's filesystem. It is the seam a windowed read is measured
// through: the bytes a read returns can look right while the read still walked
// the whole file, and only a count at the filesystem boundary tells the two
// apart.
type bytesCountedFS struct {
	afero.Fs
	read atomic.Int64
}

func (c *bytesCountedFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := c.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &bytesCountedFile{File: f, fs: c}, nil
}

// Bytes reports the bytes read through the filesystem so far.
func (c *bytesCountedFS) Bytes() int64 { return c.read.Load() }

type bytesCountedFile struct {
	afero.File
	fs *bytesCountedFS
}

func (f *bytesCountedFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.fs.read.Add(int64(n))
	return n, err
}

// readWindowByteBudget is what one bounded read of a large file is allowed to
// pull through the filesystem: four scan buffers' worth, a few hundred
// kilobytes. It is a literal, not derived from the implementation's buffer
// size, so this file compiles and fails against the pre-change code — which
// reads the whole file — and measures the shape of the read rather than a
// buffer size on the code that follows it.
const readWindowByteBudget = 4 * 64 << 10

// TestReadFileWindowReadsOnlyTheWindow is read_file's cost contract for an
// offset/limit read of a large file: a window near the top of a multi-megabyte
// file must cost the window's bytes, not the file's. Before the windowed read,
// read_file loaded every byte of the file to render 20 rows, so this fixture
// cost its own 4 MiB.
func TestReadFileWindowReadsOnlyTheWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	const row = "line of representative text content here\n"
	content := strings.Repeat(row, (4<<20)/len(row))
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write large.txt: %v", err)
	}

	counter := &bytesCountedFS{Fs: afero.NewOsFs()}
	env.SetFs(counter)

	offset, limit := 1, 20
	out, err := env.ReadFile("large.txt", &offset, &limit)
	if err != nil {
		t.Fatalf("ReadFile(offset=1, limit=20): %v", err)
	}
	// The window itself must be right, so a read that returns nothing cheaply
	// cannot pass this test.
	rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(rows) != limit {
		t.Fatalf("window = %d rows, want %d: %q", len(rows), limit, out)
	}
	if !strings.HasPrefix(rows[0], "   1\tline of representative") || !strings.HasPrefix(rows[limit-1], "  20\tline of representative") {
		t.Fatalf("window rows are not lines 1-%d of the file: first %q last %q", limit, rows[0], rows[limit-1])
	}

	if got := counter.Bytes(); got > readWindowByteBudget {
		t.Fatalf("a %d-line read of a %d-byte file read %d bytes, want at most %d: the read walked past the window",
			limit, len(content), got, readWindowByteBudget)
	}
}

// TestReadFileWindowReadsANearEOFFWindow is the same contract at the other end
// of the file: asking for 20 lines a few rows from the end must still cost the
// window, not the file. It is the case a read that must number lines cannot
// finish without scanning the file's prefix — see the remaining-cost note in
// read_window.go — so the budget here is the whole file's bytes plus a
// buffer: what it rules out is materializing the file, not scanning it.
func TestReadFileWindowReadsANearEOFFWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	const row = "line of representative text content here\n"
	const rows = 100000
	content := strings.Repeat(row, rows)
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write large.txt: %v", err)
	}
	counter := &bytesCountedFS{Fs: afero.NewOsFs()}
	env.SetFs(counter)

	offset, limit := rows-24, 20
	out, err := env.ReadFile("large.txt", &offset, &limit)
	if err != nil {
		t.Fatalf("ReadFile(near EOF): %v", err)
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(got) != limit || !strings.HasPrefix(got[0], "99976\tline of representative") {
		t.Fatalf("near-EOF window = %q, want 20 rows from line %d", out, offset)
	}
	// A line number is not a byte offset, so the scan of the prefix is
	// unavoidable; holding the file is not. The bound is the file plus one
	// buffer, which a whole-file read of the same file also meets — so the
	// claim this test carries is the allocation one, measured separately.
	if read := counter.Bytes(); read > int64(len(content))+readWindowByteBudget {
		t.Fatalf("near-EOF read pulled %d bytes for a %d-byte file", read, len(content))
	}
}

// sparseTextOversize writes a real text prefix longer than the windowed read's
// whole-file threshold and then extends the file past MaxWholeFileReadBytes with
// os.Truncate, so the tail is a hole rather than written bytes. The prefix is
// what the window reads: a hole would read as NULs and refuse the window as
// binary, so the sparse part must sit past everything the test asks for.
func sparseTextOversize(t *testing.T, path string) (prefixRows int) {
	t.Helper()
	const row = "line of representative text content here\n"
	// Two rows past the 128 KiB threshold, so the prefix alone is enough to put
	// the read on the scanning path whatever the hole contributes.
	prefixRows = readWindowNULFloor/len(row) + 2
	prefix := strings.Repeat(row, prefixRows)
	if len(prefix) <= readWindowNULFloor {
		t.Fatalf("prefix is %d bytes, want past the %d-byte whole-file threshold", len(prefix), readWindowNULFloor)
	}
	if err := os.WriteFile(path, []byte(prefix), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Truncate(path, MaxWholeFileReadBytes+1); err != nil {
		t.Fatalf("truncate %s past the cap: %v", path, err)
	}
	return prefixRows
}

// TestReadFileWindowAnswersAFilePastTheWholeFileCap is the 2 GiB sparse-log case
// from the issue at a fixture-sized cost: a file past MaxWholeFileReadBytes is
// refused by a whole-file read and answered by an offset/limit read. It is the
// contract the windowed read exists for — the cap bounded what a read holds, not
// what it may look at — so the two entries must differ here, and the whole-file
// refusal must still be the typed one.
func TestReadFileWindowAnswersAFilePastTheWholeFileCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	path := filepath.Join(dir, "oversize.log")
	rows := sparseTextOversize(t, path)
	if info, err := os.Stat(path); err != nil || info.Size() <= MaxWholeFileReadBytes {
		t.Fatalf("fixture = %v bytes (err %v), want past the %d-byte cap", info, err, MaxWholeFileReadBytes)
	}

	offset, limit := 1, 20
	out, err := env.ReadFileCtx(t.Context(), "oversize.log", &offset, &limit)
	if err != nil {
		t.Fatalf("windowed read of an oversize file: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(got) != limit {
		t.Fatalf("window = %d rows, want %d: %q", len(got), limit, out)
	}
	if !strings.HasPrefix(got[0], "   1\tline of representative") || !strings.HasPrefix(got[limit-1], "  20\tline of representative") {
		t.Fatalf("window rows are not lines 1-%d of the file: first %q last %q", limit, got[0], got[limit-1])
	}
	if rows < limit {
		t.Fatalf("fixture holds %d rows, fewer than the %d-row window", rows, limit)
	}

	_, wholeErr := env.ReadFileCtx(t.Context(), "oversize.log", nil, nil)
	rle, capped := errors.AsType[*ReadLimitError](wholeErr)
	if !capped {
		t.Fatalf("whole-file read of an oversize file err = %v, want a *ReadLimitError", wholeErr)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
	// The refusal must name read_file's own way past the cap. The shared text
	// only points at shell tools, so a model that follows it never finds
	// offset/limit.
	if !strings.Contains(wholeErr.Error(), "offset and limit") {
		t.Fatalf("whole-file refusal does not mention offset/limit: %q", wholeErr)
	}
}

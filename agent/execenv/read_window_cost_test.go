package execenv

import (
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

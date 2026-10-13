package execenv

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"
)

// MaxWholeFileReadBytes caps every whole-file read the file tools do —
// read_file, edit_file's read, apply_patch's ReadFileRaw, and the skill
// SKILL.md loads. Real source files rarely reach tens of megabytes; a
// multi-gigabyte log or a procfs regular such as /proc/kcore (140 TB by stat)
// is what the cap must fail fast on instead of allocating until a
// memory-limited container is SIGKilled (issue #4556). The value matches the
// read caps the repo already uses elsewhere (apilog's maxAPILogLineBytes,
// delegatestore's DefaultMaxLineBytes).
const MaxWholeFileReadBytes = 128 << 20

// errNotRegularFile is the refusal for an admitted path that is neither a
// regular file nor (where allowed) a directory.
var errNotRegularFile = errors.New("not a regular file")

// ReadLimitError reports a whole-file read that ran past the byte cap. The
// model can still page through the file with shell tools, so the message names
// the limit rather than truncating silently.
type ReadLimitError struct {
	Limit int64
}

func (e *ReadLimitError) Error() string {
	return fmt.Sprintf("file exceeds the %d MB whole-file read limit; read it in pieces with shell tools (head/tail)", e.Limit>>20)
}

// readAllCapped reads at most limit bytes from r and reports a *ReadLimitError
// once the input holds more. The bound is what keeps a whole-file read of a
// hostile input — a procfs regular such as /proc/kcore stats in the terabytes —
// from allocating until the container's memory cap kills the process
// (issue #4556).
func readAllCapped(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, &ReadLimitError{Limit: limit}
	}
	return b, nil
}

// ReadFileCapped reads the whole regular file at path on the OS filesystem,
// refusing devices and FIFOs and failing fast past MaxWholeFileReadBytes. It
// is the whole-file read seam for callers outside the environment's own file
// tools (skill loading).
func ReadFileCapped(path string) ([]byte, error) {
	return readFileCapped(afero.NewOsFs(), path)
}

// readFileCapped is the off arm's whole-file read: it opens without blocking
// (openNonblock, so a FIFO with no writer cannot hang the open), admits the
// handle only when it stats regular on that same handle — mirroring
// admitReadFD, so a device or FIFO the off arm once read is refused — and
// stops at MaxWholeFileReadBytes. O_NONBLOCK never needs clearing here: only
// a regular file is read, and a regular file's reads do not honor it. The
// admission runs through the afero.File interface, never a *os.File type
// assertion: SetFs accepts OS-backed wrappers (fault.FS, BasePathFs) whose
// wrapped handles must not hide the file's type, while an injected in-memory
// filesystem cannot hold a device or FIFO and its FileInfo modes carry no
// file-type bits, so the admission never refuses them.
func readFileCapped(fsys afero.Fs, name string) ([]byte, error) {
	f, err := fsys.OpenFile(name, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, serr := f.Stat()
	if serr != nil {
		return nil, serr
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read %q: %w", name, errNotRegularFile)
	}
	if info.Size() > MaxWholeFileReadBytes {
		return nil, &ReadLimitError{Limit: MaxWholeFileReadBytes}
	}
	return readAllCapped(f, MaxWholeFileReadBytes)
}

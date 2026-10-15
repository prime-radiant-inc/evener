package execenv

import (
	"context"
	"errors"
	"fmt"
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
// delegatestore's DefaultMaxLineBytes). A variable so a test can exercise the
// refusal with a fixture-sized cap.
var MaxWholeFileReadBytes int64 = 128 << 20

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

// ReadFileCapped reads the whole regular file at path on the OS filesystem,
// refusing devices and FIFOs and failing fast past MaxWholeFileReadBytes. It
// is the whole-file read seam for callers outside the environment's own file
// tools (skill loading).
func ReadFileCapped(path string) ([]byte, error) {
	return readFileCapped(afero.NewOsFs(), path)
}

// readFileCapped is readFileCappedCtx with context.Background(), for callers
// that carry no context.
func readFileCapped(fsys afero.Fs, name string) ([]byte, error) {
	return readFileCappedCtx(context.Background(), fsys, name)
}

// readFileCappedCtx is the off arm's whole-file read: it opens without blocking
// (openNonblock, so a FIFO with no writer cannot hang the open), admits the
// handle only when it stats regular on that same handle — mirroring
// admitReadFD, so a device or FIFO the off arm once read is refused — and
// stops at MaxWholeFileReadBytes. The read is bounded in time AND
// cancellation-observable through readBounded: a directly-named size-0
// poll-registered never-ready regular — the kmsg shape, root-only in practice
// — would otherwise block here indefinitely, because O_NONBLOCK only makes the
// descriptor pollable, not bounded (#4611's off-arm gap). O_NONBLOCK never
// needs clearing here: only a regular file is read, and a regular file's reads
// do not honor it. Admission runs through the afero.File interface, never a
// *os.File type assertion: SetFs accepts OS-backed wrappers (fault.FS,
// BasePathFs) whose wrapped handles must not hide the file's type, while an
// injected in-memory filesystem cannot hold a device or FIFO and its FileInfo
// modes carry no file-type bits, so the admission never refuses them.
func readFileCappedCtx(ctx context.Context, fsys afero.Fs, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	// The handle's deadline is armed only when it can take one (an OS-backed
	// regular on a pollable mount); an in-memory fs handle cannot, and its
	// reads never block past their data.
	var deadline readDeadlineSetter
	if d, ok := f.(readDeadlineSetter); ok {
		deadline = d
	}
	data, over, rerr := readBounded(ctx, f, deadline, MaxWholeFileReadBytes)
	if rerr != nil {
		if errors.Is(rerr, errGrepStreamStalled) {
			return nil, fmt.Errorf("read %q: %w", name, errGrepStreamStalled)
		}
		return nil, rerr
	}
	if over {
		return nil, &ReadLimitError{Limit: MaxWholeFileReadBytes}
	}
	return data, nil
}

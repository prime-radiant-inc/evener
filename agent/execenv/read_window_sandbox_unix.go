//go:build linux || darwin

package execenv

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// readNumberedWindowCtx is read_file's bounded read on the sandboxed (and
// granted-scratch) arm: the open and admission readFileCtx does — symlink-
// refusing, root-anchored, denylist-checked, refusing a FIFO or device — with
// the admitted handle handed to the window renderer instead of loaded whole.
func (s *sandboxFS) readNumberedWindowCtx(ctx context.Context, tool, abs, displayPath string, offsetLine, limitLines *int) (string, error) {
	// A cancelled call answers before touching the filesystem, mirroring the
	// off arm's pre-open fence.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd, err := s.openRead(tool, abs, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return "", err
	}
	f, err := admitReadFD(fd, abs, false, isPseudoFsPath(abs))
	if err != nil {
		return "", fmt.Errorf("read %q: %w", abs, err)
	}
	defer func() { _ = f.Close() }()
	info, serr := f.Stat()
	if serr != nil {
		return "", fmt.Errorf("read %q: %w", abs, serr)
	}
	return readNumberedWindowCtx(ctx, displayPath, abs, f, info.Size(), offsetLine, limitLines)
}

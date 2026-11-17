//go:build linux || darwin

package execenv

import "context"

// readNumberedWindowCtx is read_file's bounded read on the sandboxed (and
// granted-scratch) arm: the open and admission readFileCtx does — symlink-
// refusing, root-anchored, denylist-checked, refusing a FIFO or device — with
// the admitted handle handed to the window renderer instead of loaded whole.
// The open/admit/stat sequence is openAdmittedReadCtx, which readFileCtx shares,
// so a change to admission cannot reach one entry and miss the other.
func (s *sandboxFS) readNumberedWindowCtx(ctx context.Context, tool, abs, displayPath string, offsetLine, limitLines *int) (string, error) {
	f, size, err := s.openAdmittedReadCtx(ctx, tool, abs)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return readNumberedWindowCtx(ctx, displayPath, abs, f, size, offsetLine, limitLines)
}

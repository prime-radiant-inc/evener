//go:build !linux && !darwin

package execenv

import "context"

// readNumberedWindowCtx fails closed where no sandboxFS is ever built, exactly
// as readFileCtx does on this platform.
func (s *sandboxFS) readNumberedWindowCtx(ctx context.Context, tool, abs, displayPath string, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	return "", ReadFileMeta{}, errSandboxUnsupported()
}

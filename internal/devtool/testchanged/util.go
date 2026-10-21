package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// sayf and sayln write the tool's progress lines. Their return value is
// deliberately ignored: a tool whose stdout is a closed pipe has nothing
// useful to do about it, and every caller would otherwise carry the same
// unchecked-error noise.
func sayf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func sayln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

// inspectionTimeout bounds one git or `go list` call. Neither is a long-running
// job, and a wedged one must fail the run rather than hang a pre-push check
// nobody is watching.
const inspectionTimeout = 5 * time.Minute

// command is os/exec's constructor in one place, so every process this tool
// starts carries the same bound: a deadline, plus WaitDelay for a process that
// ignores cancellation. The caller cancels the returned context.
func command(parent context.Context, name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, inspectionTimeout)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 30 * time.Second
	return cmd, cancel
}

// relPath returns target relative to base, both cleaned.
func relPath(base, target string) (string, error) {
	return filepath.Rel(filepath.Clean(base), filepath.Clean(target))
}

// pathExists reports whether path exists at all.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

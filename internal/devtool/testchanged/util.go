package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// privateTestEnv returns the environment for one module's test run: the
// developer's own variables, with HOME, the XDG roots and TMPDIR pointed at
// privateRoot. That mirrors what `make test` gives each module, so a test that
// reads user configuration sees an empty directory rather than the developer's
// real state. GOCACHE, GOMODCACHE and GOPATH are left alone so the run reuses
// the existing build cache instead of rebuilding cold.
func privateTestEnv(privateRoot string) ([]string, error) {
	dirs := map[string]string{
		"HOME":            filepath.Join(privateRoot, "home"),
		"XDG_CONFIG_HOME": filepath.Join(privateRoot, "xdg-config"),
		"XDG_CACHE_HOME":  filepath.Join(privateRoot, "xdg-cache"),
		"XDG_STATE_HOME":  filepath.Join(privateRoot, "xdg-state"),
		"TMPDIR":          filepath.Join(privateRoot, "tmp"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	env := make([]string, 0, len(os.Environ())+len(dirs))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := dirs[name]; replace {
				continue
			}
		}
		env = append(env, entry)
	}
	for name, dir := range dirs {
		env = append(env, name+"="+dir)
	}
	sort.Strings(env)
	return env, nil
}

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

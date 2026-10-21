package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// goEnvValues reports the ambient values of the Go settings that would
// otherwise be re-derived from the private HOME. Without pinning them, swapping
// HOME gives the run an empty build cache, an empty module cache that must
// re-download every dependency, and no `go env -w` settings (GOPROXY,
// GOPRIVATE, GOFLAGS). scripts/lib/private-go-home.sh exists to resolve the
// same three defaults for `make test`; this is the Go-side equivalent.
func goEnvValues() map[string]string {
	out := map[string]string{}
	cmd, cancel := command(context.Background(), "go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV")
	defer cancel()
	body, err := cmd.Output()
	if err != nil {
		return out
	}
	var values map[string]string
	if err := json.Unmarshal(body, &values); err != nil {
		return out
	}
	for name, value := range values {
		if value != "" && value != "off" {
			out[name] = value
		}
	}
	return out
}

// privateTestEnv returns the environment for one module's test run: the
// developer's own variables, with HOME, the XDG roots and TMPDIR pointed at
// privateRoot, and the ambient Go cache and env paths pinned so the run reuses
// the existing build and module caches. That mirrors what `make test` gives
// each module, so a test that reads user configuration sees an empty directory
// rather than the developer's real state.
func privateTestEnv(privateRoot string) ([]string, error) {
	dirs := map[string]string{
		"HOME":            filepath.Join(privateRoot, "home"),
		"XDG_CONFIG_HOME": filepath.Join(privateRoot, "xdg-config"),
		"XDG_CACHE_HOME":  filepath.Join(privateRoot, "xdg-cache"),
		"XDG_STATE_HOME":  filepath.Join(privateRoot, "xdg-state"),
		"TMPDIR":          filepath.Join(privateRoot, "tmp"),
	}
	// GOENV names a file, not a directory; the rest are directories the run
	// needs to exist before go test starts.
	goEnv := goEnvValues()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	env := make([]string, 0, len(os.Environ())+len(dirs)+len(goEnv))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := dirs[name]; replace {
				continue
			}
			if _, replace := goEnv[name]; replace {
				continue
			}
		}
		env = append(env, entry)
	}
	for name, dir := range dirs {
		env = append(env, name+"="+dir)
	}
	for name, value := range goEnv {
		env = append(env, name+"="+value)
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

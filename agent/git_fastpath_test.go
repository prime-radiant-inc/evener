package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// fastGitDirForTest records the directory prependFastGitToPath injected in
// TestMain (empty when it changed nothing), so the self-tests can assert the
// reorder did not change which git the suite resolves.
var fastGitDirForTest string

// prependFastGitToPath puts the fastest-starting real `git` first on PATH for the
// duration of the test binary, and reports the directory it added (empty when it
// changed nothing).
//
// WHY: the worktree tests drive git through `sh -c "git …"`, so the shell resolves
// git from PATH on every call. On macOS, PATH normally finds /usr/bin/git, which
// is not git — it is Xcode's command-line-tools shim, and it re-execs the real
// binary inside the active developer directory. Measured on this repo's suite:
//
//	sh -c "git rev-parse"                      9.8 ms
//	/usr/bin/git rev-parse (shim)              6.9 ms
//	<Xcode>/usr/bin/git rev-parse (real)       3.3 ms
//
// So the shim costs ~3.6 ms of every git call, and the suite still makes ~2200 of
// them: roughly 8 s of pure re-exec overhead.
//
// This only ever REORDERS PATH to a git that is already installed and already
// what `git` resolves to underneath the shim. It does not change which git
// version is used, and it is a test-harness concern only — production resolves
// git through the user's own PATH, deliberately.
//
// Portability: the shim is macOS-specific, so this is a no-op everywhere else.
// It is also a no-op when `xcrun` is missing, when the resolved path is not
// executable, or when PATH already finds a non-shim git — every failure mode
// leaves PATH untouched and the suite simply pays the old cost.
func prependFastGitToPath() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	current, err := exec.LookPath("git")
	if err != nil {
		// No git on PATH at all. The tests that need git skip themselves; do
		// not synthesize a path they would not otherwise have found.
		return ""
	}
	if !isXcodeCLTShim(current) {
		return ""
	}
	fast := xcrunGitPath()
	if fast == "" || fast == current {
		return ""
	}
	dir := filepath.Dir(fast)
	if filepath.Base(fast) != "git" {
		// PATH resolution finds "git" by name, so only a directory whose git is
		// named git can substitute for the shim.
		return ""
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// isXcodeCLTShim reports whether path is one of the /usr/bin stubs that re-exec
// into the active developer directory rather than being the tool itself.
func isXcodeCLTShim(path string) bool {
	return filepath.Dir(path) == "/usr/bin"
}

// xcrunGitPath asks the active developer directory for its real git, returning ""
// when xcrun is absent, fails, or names something unusable.
func xcrunGitPath() string {
	out, err := exec.Command("xcrun", "--find", "git").Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return path
}

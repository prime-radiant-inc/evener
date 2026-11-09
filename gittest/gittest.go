// Package gittest builds real git repositories for tests under a hermetic
// environment: ambient git configuration and repository-selection variables
// (GIT_DIR, hooks, signing, init templates) cannot change what a fixture does.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

// configEnvironment names the git-config and init-template variables Env strips
// before setting its own, so an ambient value cannot win by position or install
// hooks into the fixture.
var configEnvironment = []string{
	"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS",
	"GIT_TEMPLATE_DIR",
}

// configEnvironmentPrefixes names the numbered GIT_CONFIG_KEY_n and
// GIT_CONFIG_VALUE_n pairs that GIT_CONFIG_COUNT injects.
var configEnvironmentPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// Env returns the environment a test's git subprocess runs with: the process
// environment minus repository-selection and config-injection variables, global
// and system config pointed at nothing, and a fixed author and committer.
func Env() []string {
	base := identifier.FilteredGitEnvironment(os.Environ())
	env := make([]string, 0, len(base)+8)
outer:
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		for _, banned := range configEnvironment {
			if key == banned {
				continue outer
			}
		}
		for _, prefix := range configEnvironmentPrefixes {
			if strings.HasPrefix(key, prefix) {
				continue outer
			}
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

// Run runs git in dir under Env and returns its trimmed combined output. It
// skips the test when git is not installed and fails it on a non-zero exit.
// The local file protocol is enabled so submodule fixtures work on git >= 2.38.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// NewLinkedWorktree builds an origin-less main repository with one commit and a
// linked worktree on branch feat, returning their absolute paths.
func NewLinkedWorktree(t testing.TB) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	Run(t, base, "init", "-q", "main")
	Run(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt = filepath.Join(base, "wt")
	Run(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	return main, wt
}

// Package hubtestenv builds the throwaway environment the evener-hub test
// binaries run in: HOME, the XDG bases and CODEX_HOME redirected under a
// temporary root, Evener's own EVENER_* configuration cleared, and the Go
// caches pinned back to their real locations.
//
// It lives in a non-test package so cmd/evener-hub and each of its
// sub-packages can share one implementation. Per-package TestMain functions
// keep only what is theirs: which defaults have to be contained, and any
// re-executed helper that must run before the redirect.
package hubtestenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/envvars"
)

// RootVar hands an inherited throwaway root down to re-executed test helpers.
// A child that inherits one uses it and leaves it for the parent to remove.
//
// Like the other EVENER_-prefixed names the harness owns, it describes the test
// rig rather than the product, so it is absent from envvars.All() and the scrub
// below leaves it alone.
const RootVar = "EVENER_HUB_TEST_ENV_ROOT"

// retiredEvenerEnvVars are names the product no longer declares but that a
// developer machine may still export from when it did. envvars cannot list them
// (nothing reads them any more), so they are carried here.
var retiredEvenerEnvVars = []string{"EVENER_API_TOKEN"}

// Env is a throwaway environment root and the knowledge of whether this process
// created it. Anything a test needs for the whole run rather than for one case —
// the live-stack binaries, for one — belongs under Root, so Discard is the only
// cleanup path that has to exist.
type Env struct {
	Root string
	// inherited records that a parent process built Root and will remove it.
	inherited bool
}

// NamedPath pairs a resolver's name with the path it resolved to, so a
// containment failure can say which default escaped.
type NamedPath struct {
	Name string
	Path string
}

// Redirect builds (or inherits) a throwaway root, redirects HOME, the XDG bases
// and CODEX_HOME into it, and clears every EVENER_* variable the product reads.
// prefix names the temporary directory so a leak can be traced to the package
// that leaked it. It writes to stderr and exits the process on failure, which is
// the only useful answer before m.Run.
func Redirect(prefix string) *Env {
	root, inherited := os.LookupEnv(RootVar)
	if inherited {
		if _, err := os.Stat(root); err != nil {
			inherited = false
		}
	}
	if !inherited {
		created, err := os.MkdirTemp("", prefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s test env: %v\n", prefix, err)
			os.Exit(1)
		}
		root = created
	}
	env := &Env{Root: root, inherited: inherited}
	for _, dir := range []string{"home", "config", "state", "cache", "codex"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "%s test env: %v\n", prefix, err)
			env.Discard()
			os.Exit(1)
		}
	}

	// Pin the Go build/module caches to their real locations before redirecting
	// HOME below, exactly as cmd/evener's TestMain does. All three default to
	// paths under $HOME, and a live-stack `go build` inherits this env, so
	// without the pin every run compiles from a cold cache into the throwaway
	// root — and leaves it there, because the module cache is written read-only
	// and the RemoveAll in Discard reports but cannot undo it.
	// TestGoSubprocessesCacheOutsideTheTestRoot is the guard.
	for _, key := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(out)); value != "" {
			_ = os.Setenv(key, value)
		}
	}

	_ = os.Setenv("HOME", filepath.Join(root, "home"))
	_ = os.Setenv("USERPROFILE", filepath.Join(root, "home")) // what os.UserHomeDir reads on Windows
	_ = os.Setenv(envvars.XDGConfigHome.Name, filepath.Join(root, "config"))
	_ = os.Setenv(envvars.XDGStateHome.Name, filepath.Join(root, "state"))
	_ = os.Setenv(envvars.XDGCacheHome.Name, filepath.Join(root, "cache"))
	_ = os.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	// Clear Evener's own configuration environment. HOME and the XDG roots above
	// redirect where these tests look; this decides what configures them, and
	// the two have to agree or the fixtures written into the throwaway root are
	// not what the code under test reads. EVENER_PROVIDERS_CONFIG is the sharp
	// edge — it names the providers.toml every evener process loads, so a value in
	// the developer's shell reached the live-stack hub and its `evener
	// launch-check` and enumerated that developer's real providers instead of
	// the scripted "fake" instance the harness had just written.
	// TestHostEvenerEnvNeverReachesTheTestEnvironment is the guard.
	for _, v := range ProductEvenerEnvVars() {
		_ = os.Unsetenv(v.Name)
	}
	return env
}

// Discard removes a root this process created, and says so when it cannot.
// Discarding that error is how a per-run module-cache leak grew to 15GB across
// hundreds of runs without anything reporting it: the cache is written
// read-only, RemoveAll failed on every run, and nobody heard. A root inherited
// from a parent process is left for that parent.
func (e *Env) Discard() {
	if e == nil || e.inherited {
		return
	}
	if err := os.RemoveAll(e.Root); err != nil {
		fmt.Fprintf(os.Stderr, "hub test env: leaked %s: %v\n", e.Root, err)
	}
}

// PathsOutside reports, as "name = path" lines, every path that does not
// resolve strictly inside e.Root. An entry that is a glob is judged on its own
// path and on each current match, so a symlinked project directory under the
// state root cannot point outside. Empty means the throwaway env contains them
// all.
func (e *Env) PathsOutside(paths []NamedPath) []string {
	var escaped []string
	for _, p := range paths {
		candidates := []string{p.Path}
		if strings.ContainsAny(p.Path, "*?[") {
			if matches, err := filepath.Glob(p.Path); err == nil {
				candidates = append(candidates, matches...)
			}
		}
		for _, path := range candidates {
			if !ContainedIn(e.Root, path) {
				escaped = append(escaped, fmt.Sprintf("%s = %q", p.Name, path))
			}
		}
	}
	return escaped
}

// BaseRoots names the four locations Redirect pins directly: the home directory
// and the three XDG bases. Every Evener default that is not spelled out in a
// config derives from one of them, so a package with no defaults of its own
// still has these worth guarding.
func BaseRoots() []NamedPath {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "(unresolved: " + err.Error() + ")"
	}
	return []NamedPath{
		{Name: "os.UserHomeDir", Path: home},
		{Name: envvars.XDGConfigHome.Name, Path: envvars.XDGConfigHome.Getenv()},
		{Name: envvars.XDGStateHome.Name, Path: envvars.XDGStateHome.Getenv()},
		{Name: envvars.XDGCacheHome.Name, Path: envvars.XDGCacheHome.Getenv()},
	}
}

// ProductEvenerEnvVars is every EVENER_* variable Evener itself reads. Redirect
// clears the lot; TestHostEvenerEnvNeverReachesTheTestEnvironment asserts it
// did. Deriving the set from envvars rather than writing it out is the point: a
// variable added to the product is isolated from these tests the day it exists,
// which a hand-kept list does not manage (EVENER_PROVIDERS_CONFIG was missing
// from one for as long as it took a developer to export it).
//
// The harness's own EVENER_-prefixed variables — RootVar, the env-scrub helper
// gate, EVENER_LIVE_TESTS, EVENER_TEST_PROVIDER and EVENER_TEST_MODEL — name the
// test rig, not the product, so they are absent from envvars and survive.
func ProductEvenerEnvVars() []envvars.Var {
	out := []envvars.Var{}
	for _, v := range envvars.All() {
		if strings.HasPrefix(v.Name, "EVENER_") {
			out = append(out, v)
		}
	}
	for _, name := range retiredEvenerEnvVars {
		out = append(out, envvars.Var{Name: name})
	}
	return out
}

// canonicalizeExisting resolves symlinks through the deepest ancestor of path
// that exists and rejoins the rest, so a default nothing has created yet still
// compares against the real location of the tree it would land in. It fails
// closed on a symlink it cannot resolve: a dangling link is where a write
// would create its target, and that target may be anywhere.
func canonicalizeExisting(path string) (string, bool) {
	path = filepath.Clean(path)
	var rest []string
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), true
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(append([]string{path}, rest...)...), true
		}
		rest = append([]string{filepath.Base(path)}, rest...)
		path = parent
	}
}

// ContainedIn reports whether path lies strictly inside root once both are
// resolved through canonicalizeExisting: a temp root reached through a symlink
// (macOS /var is /private/var) compares equal to itself, and a link planted
// under the root, dangling or not, cannot point a default outside it.
func ContainedIn(root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	realRoot, ok := canonicalizeExisting(root)
	if !ok {
		return false
	}
	realPath, ok := canonicalizeExisting(path)
	if !ok {
		return false
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

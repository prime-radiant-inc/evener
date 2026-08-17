package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

// testEnvRoot is the throwaway root TestMain builds and removes. Anything a
// test needs for the whole run rather than for one case — the live-stack
// binaries, for one — belongs under it, so the removal below is the only
// cleanup path that has to exist.
var testEnvRoot string

// testEnvRootVar hands an inherited throwaway root down to a re-executed copy
// of this test binary. Several tests run the binary again as a fake server
// (fakeCodexLaunchConfig points a launch config at os.Executable()), and the
// parent kills those children when its case ends -- so a child that minted its
// own root never reaches the removal below and leaks it. Owning the root is
// what carries the duty to remove it: a child that inherits one uses it and
// leaves it alone, and the parent's single RemoveAll collects everything.
const testEnvRootVar = "SERF_HUB_TEST_ENV_ROOT"

func TestMain(m *testing.M) {
	root, inherited := os.LookupEnv(testEnvRootVar)
	if inherited {
		if _, err := os.Stat(root); err != nil {
			inherited = false
		}
	}
	if !inherited {
		created, err := os.MkdirTemp("", "serf-hub-test-env-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-hub test env: %v\n", err)
			os.Exit(1)
		}
		root = created
	}
	testEnvRoot = root
	for _, dir := range []string{"home", "config", "state", "cache", "codex"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "serf-hub test env: %v\n", err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}

	// Pin the Go build/module caches to their real locations before redirecting
	// HOME below, exactly as cmd/serf's TestMain does. All three default to
	// paths under $HOME, and the live-stack `go build` inherits this env, so
	// without the pin every run compiles from a cold cache into the throwaway
	// root — and leaves it there, because the module cache is written read-only
	// and the RemoveAll below discards its error.
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
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	_ = os.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	for _, key := range []string{
		"SERF_MODEL",
		"SERF_REASONING_EFFORT",
		"SERF_API_TOKEN",
		"SERF_RUN_DIR",
		"SERF_STATE_DIR",
		"SERF_HUB_TOKEN",
		"SERF_HUB_SPAWNED",
		"SERF_HUB_SPAWNED_CODEX",
	} {
		_ = os.Unsetenv(key)
	}

	code := m.Run()
	// Say so when the root cannot be removed. Discarding this error is how a
	// per-run module-cache leak grew to 15GB across hundreds of runs without
	// anything reporting it: the cache is written read-only, RemoveAll failed on
	// every run, and nobody heard.
	if !inherited {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(os.Stderr, "serf-hub test env: leaked %s: %v\n", root, err)
		}
	}
	os.Exit(code)
}

// TestGoSubprocessesCacheOutsideTheTestRoot guards the live-stack builds
// against writing the Go module and build caches into TestMain's throwaway
// root. GOCACHE, GOPATH and GOMODCACHE all default to locations under $HOME,
// which TestMain redirects, and every `go build` these tests shell out to
// inherits that environment. Unpinned, each `go test ./cmd/serf-hub/` re-downloads
// the whole module graph (~118MB) plus a cold build cache into a directory
// TestMain's os.RemoveAll then cannot delete, because the module cache is
// written read-only and the removal error is discarded. Runs accumulate until
// the disk fills.
//
// This asserts against the real toolchain rather than the pinning loop's own
// bookkeeping, so it stays true whatever the mechanism: what matters is where a
// `go` subprocess spawned from this package actually resolves its caches.
func TestGoSubprocessesCacheOutsideTheTestRoot(t *testing.T) {
	for _, key := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", key, err)
		}
		got := strings.TrimSpace(string(out))
		if got == "" {
			t.Fatalf("go env %s resolved empty", key)
		}
		if strings.HasPrefix(got, testEnvRoot) {
			t.Fatalf("go env %s = %q, inside the throwaway test root %q; the cache it writes there outlives the run", key, got, testEnvRoot)
		}
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := fspaths.CanonicalizeDir(dir)
	if err != nil {
		t.Fatalf("canonicalize temp dir %s: %v", dir, err)
	}
	return resolved
}

func writeRendezvous(t *testing.T, dir string, e rendezvous.Entry) {
	t.Helper()
	if _, err := rendezvous.Write(dir, e); err != nil {
		t.Fatalf("write rendezvous: %v", err)
	}
}

// fakeProber is a hubcore.Prober that reports a fixed session_id/status (or
// fails), letting tests stand up a roster with a deterministic probe result.
type fakeProber struct {
	sessionID  string
	status     string
	pendingAsk bool
	shouldFail bool
}

func (p fakeProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	if p.shouldFail {
		return hubcore.ProbeResult{}
	}
	return hubcore.ProbeResult{SessionID: p.sessionID, Status: p.status, PendingAsk: p.pendingAsk, OK: true}
}

// TestReExecutedHelperLeavesNoThrowawayRoot pins the ownership rule that keeps
// this package's temp roots from accumulating.
//
// Several tests point a launch config at os.Executable() and run this binary
// again as a fake server (fakeCodexLaunchConfig). The parent kills those
// children when its case ends, so a child that called os.MkdirTemp for itself
// never reaches TestMain's removal and its root outlives the run -- one per
// launch, forever. Measured before the fix: `go test ./cmd/serf-hub/ -run Codex`
// left 21 behind, and `-short` left the same 21 while the live-stack e2e tests
// left none.
//
// A child that inherits SERF_HUB_TEST_ENV_ROOT must therefore create nothing of
// its own, and must not remove what it did not create.
func TestReExecutedHelperLeavesNoThrowawayRoot(t *testing.T) {
	pattern := filepath.Join(os.TempDir(), "serf-hub-test-env-*")
	before, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob throwaway roots: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// The child must be KILLED, not allowed to exit. A helper that exits
	// normally runs TestMain's removal and tidies up after itself even when it
	// minted its own root, so letting it finish measures nothing: the leak is
	// precisely the cleanup that a killed process never reaches. Production
	// kills these -- the fake server blocks in Serve until the parent is done.
	cmd := exec.Command(exe, "-test.run=^TestFakeCodexAppServerHelper$")
	cmd.Env = append(os.Environ(), testEnvRootVar+"="+testEnvRoot, "SERF_FAKE_CODEX_APP_SERVER=serve")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start re-executed helper: %v", err)
	}
	// Give the child time to reach TestMain's setup before killing it; that is
	// the window in which it would create a root.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if grown, _ := filepath.Glob(pattern); len(grown) != len(before) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	after, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob throwaway roots: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a re-executed helper minted %d throwaway root(s) of its own; every killed helper then leaks one", len(after)-len(before))
	}
	if _, err := os.Stat(testEnvRoot); err != nil {
		t.Fatalf("the helper removed the root it inherited: %v", err)
	}
}

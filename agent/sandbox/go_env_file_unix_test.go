//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// GOENV can name anything, and the probe reads it before any sandbox exists, so
// the host read takes only a bounded regular file: a FIFO would block session
// start and /dev/zero would never end.
func TestHostProbeReadsOnlyABoundedRegularGoEnvFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO support: %v", err)
	}
	huge := filepath.Join(dir, "huge")
	if err := os.WriteFile(huge, []byte(strings.Repeat("x", goEnvFileLimit+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(dir, "env")
	if err := os.WriteFile(small, []byte("GOPATH=/from/file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, "/dev/zero", dir, huge} {
		done := make(chan error, 1)
		go func() { _, err := (hostProbeSystem{}).readFile(path); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("readFile(%q) must refuse anything but a small regular file", path)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("readFile(%q) blocked", path)
		}
	}
	if data, err := (hostProbeSystem{}).readFile(small); err != nil || string(data) != "GOPATH=/from/file\n" {
		t.Errorf("readFile of a small regular file = %q, %v", data, err)
	}
}

// The floor judges the spawn's go env file as it is at each spawn: one that
// becomes a FIFO turns GOENV off for the spawn and, under the overlay, sends
// the Go caches to the scratch; repaired, it is read as usual again; and a
// healthy file the spawn names instead is never turned off.
func TestEnvFloorJudgesGoEnvFileReadabilityAtEachSpawn(t *testing.T) {
	home := servedHome(t)
	envFile := filepath.Join(overlayFixtureDir(t), "go.env")
	write := func() {
		t.Helper()
		_ = os.Remove(envFile)
		if err := os.WriteFile(envFile, []byte("GOPROXY=direct\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	tmp := "/tmp/evener-session-xyz"
	host := HostFacts{OS: "linux", Home: home, GoEnvFile: envFile}
	overlay := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{filepath.Join(home, ".cache"), filepath.Join(home, "go", "pkg")}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}
	private := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheSessionPrivate, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}
	env := []string{"HOME=" + home, "GOENV=" + envFile}

	healthy := func(when string) {
		t.Helper()
		out := ApplyEnvFloor(env, overlay, tmp)
		if v, _ := envValue(out, "GOENV"); v != envFile {
			t.Errorf("%s: a readable go env file must stay on, got GOENV=%q", when, v)
		}
		if _, ok := envValue(out, "GOCACHE"); ok {
			t.Errorf("%s: a served GOCACHE must be left alone: %v", when, out)
		}
	}
	healthy("at start")

	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(envFile, 0o600); err != nil {
		t.Skipf("no FIFO support: %v", err)
	}
	for _, policy := range []ResolvedPolicy{overlay, private} {
		out := ApplyEnvFloor(env, policy, tmp)
		if v, _ := envValue(out, "GOENV"); v != "off" {
			t.Errorf("%v: a go env file that became a FIFO must be turned off, got %q", policy.CacheStrategy, v)
		}
		if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
			t.Errorf("%v: with the go env file unreadable GOCACHE must go to the scratch, got %q", policy.CacheStrategy, v)
		}
	}
	other := filepath.Join(overlayFixtureDir(t), "other.env")
	if err := os.WriteFile(other, []byte("GOPROXY=direct\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(ApplyEnvFloor([]string{"HOME=" + home, "GOENV=" + other}, overlay, tmp), "GOENV"); v != other {
		t.Errorf("a healthy go env file the spawn names must stay on, got %q", v)
	}

	write()
	healthy("after repair")
}

// A go env file under a mask is not probed for readability: the spawn sees the
// mask, never the file, so an unreadable one there (a FIFO) must not turn the
// spawn's GOENV off.
func TestEnvFloorKeepsGoEnvOnForAnUnreadableHiddenFile(t *testing.T) {
	_, _, envFile, policyFor := goEnvLeakFixture(t)
	if err := syscall.Mkfifo(envFile, 0o600); err != nil {
		t.Fatal(err)
	}
	out := ApplyEnvFloor([]string{"HOME=/home/u", "GOENV=" + envFile}, policyFor(filepath.Dir(envFile)), "/tmp/evener-session-xyz")
	if v, _ := envValue(out, "GOENV"); v == "off" {
		t.Errorf("a hidden go env file must not turn GOENV off")
	}
}

// A FIFO go env file under /tmp is still probed: bwrap rebinds a /tmp worktree
// holding it, where the spawn's go would block on it, so GOENV goes off.
func TestEnvFloorTurnsGoEnvOffForAFIFOInATmpWorktree(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "evener-goenv-fifo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	envFile := filepath.Join(dir, "go.env")
	if err := syscall.Mkfifo(envFile, 0o600); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, Backend: BackendBwrap, CacheStrategy: CacheSessionPrivate, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: "/home/u", GoEnvFile: envFile}}
	out := ApplyEnvFloor([]string{"HOME=/home/u", "GOENV=" + envFile}, policy, "/tmp/evener-session-xyz")
	if v, _ := envValue(out, "GOENV"); v != "off" {
		t.Errorf("a FIFO go env file in a /tmp worktree must turn GOENV off, got %q", v)
	}
}

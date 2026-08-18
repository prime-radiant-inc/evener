package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// --- runProbe: bounded execution -------------------------------------------------

// TestRunProbe_ReturnsAvailableFromFunc proves the happy path: a probe that
// finishes well inside its timeout reports whatever it decided, unmodified.
func TestRunProbe_ReturnsAvailableFromFunc(t *testing.T) {
	got := runProbe(context.Background(), "widget", "rerun widget", time.Second, func(ctx context.Context) (bool, string) {
		return true, ""
	})
	if !got.Available || got.ID != "widget" || got.Reason != "" || got.Rerun != "rerun widget" {
		t.Fatalf("got %+v, want available widget with no reason", got)
	}
}

// TestRunProbe_ReturnsBlockedFromFunc proves a probe's own blocked verdict
// (not a timeout) passes through with its reason intact.
func TestRunProbe_ReturnsBlockedFromFunc(t *testing.T) {
	got := runProbe(context.Background(), "widget", "rerun widget", time.Second, func(ctx context.Context) (bool, string) {
		return false, "denied by policy"
	})
	if got.Available || got.Reason != "denied by policy" {
		t.Fatalf("got %+v, want blocked with reason %q", got, "denied by policy")
	}
}

// TestRunProbe_BoundedByTimeout proves the mutation this whole tool exists to
// prevent: a probe stuck on a stalled resource must classify as blocked
// promptly, not hang the gate. The injected func blocks on a channel that is
// never sent to; runProbe must still return within its timeout, not wait for
// the goroutine.
func TestRunProbe_BoundedByTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // let the leaked goroutine exit after the test observes the result

	start := time.Now()
	got := runProbe(context.Background(), "widget", "rerun widget", 20*time.Millisecond, func(ctx context.Context) (bool, string) {
		<-block
		return true, ""
	})
	elapsed := time.Since(start)

	if got.Available {
		t.Fatalf("got available=true from a probe that never returned; want blocked-by-timeout")
	}
	if !strings.Contains(got.Reason, "did not complete") {
		t.Fatalf("reason %q does not say the probe timed out", got.Reason)
	}
	// Generous margin over the 20ms timeout: this only needs to prove runProbe
	// did not wait for the full second the outer test timeout would allow.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("runProbe took %s, want well under 500ms (it must not wait for the blocked goroutine)", elapsed)
	}
}

// --- probeLoopbackBindWith ---------------------------------------------------------

func TestProbeLoopbackBindWith_Available(t *testing.T) {
	ok, reason := probeLoopbackBindWith(func() (io.Closer, error) {
		return io.NopCloser(nil), nil
	})
	if !ok || reason != "" {
		t.Fatalf("got (%v, %q), want (true, \"\")", ok, reason)
	}
}

func TestProbeLoopbackBindWith_Blocked(t *testing.T) {
	ok, reason := probeLoopbackBindWith(func() (io.Closer, error) {
		return nil, errors.New("bind: permission denied")
	})
	if ok {
		t.Fatalf("got available=true, want blocked")
	}
	if !strings.Contains(reason, "permission denied") {
		t.Fatalf("reason %q does not mention the underlying error", reason)
	}
}

// TestProbeLoopbackBind_RealSocket smoke-tests the real, uninjected wrapper
// against an actual loopback bind. This is the one deliberately real-boundary
// case: on any host that can run this repo's test suite at all, binding
// 127.0.0.1:0 is expected to work, so this pins the wrapper's plumbing
// (Listen + Close + error formatting) against the genuine syscall once.
func TestProbeLoopbackBind_RealSocket(t *testing.T) {
	ok, reason := probeLoopbackBind(context.Background())
	if !ok {
		t.Fatalf("real loopback bind reported blocked: %s", reason)
	}
}

// --- probeChromeCDPWith -------------------------------------------------------

func TestProbeChromeCDPWith_AvailableWhenAnExecutableCandidateExists(t *testing.T) {
	stat := func(path string) (fs.FileInfo, error) {
		if path == "/candidate/two" {
			return fakeFileInfo{mode: 0o755}, nil
		}
		return nil, os.ErrNotExist
	}
	ok, reason := probeChromeCDPWith([]string{"/candidate/one", "/candidate/two"}, stat)
	if !ok || reason != "" {
		t.Fatalf("got (%v, %q), want (true, \"\")", ok, reason)
	}
}

func TestProbeChromeCDPWith_BlockedWhenNoCandidateExists(t *testing.T) {
	stat := func(path string) (fs.FileInfo, error) { return nil, os.ErrNotExist }
	ok, reason := probeChromeCDPWith([]string{"/candidate/one", "/candidate/two"}, stat)
	if ok {
		t.Fatalf("got available=true, want blocked")
	}
	if !strings.Contains(reason, "/candidate/one") || !strings.Contains(reason, "/candidate/two") {
		t.Fatalf("reason %q does not name the checked candidates", reason)
	}
}

func TestProbeChromeCDPWith_SkipsNonExecutableCandidate(t *testing.T) {
	// A candidate that exists but is not executable (mode 0o644) must not
	// count as available - a stale, unexecutable file at that path is not a
	// working Chrome.
	stat := func(path string) (fs.FileInfo, error) {
		if path == "/candidate/one" {
			return fakeFileInfo{mode: 0o644}, nil
		}
		return nil, os.ErrNotExist
	}
	ok, _ := probeChromeCDPWith([]string{"/candidate/one"}, stat)
	if ok {
		t.Fatalf("got available=true for a non-executable candidate, want blocked")
	}
}

// --- probeProcessInspectWith ---------------------------------------------------

func TestProbeProcessInspectWith_Available(t *testing.T) {
	ok, reason := probeProcessInspectWith(context.Background(), func(ctx context.Context) error { return nil })
	if !ok || reason != "" {
		t.Fatalf("got (%v, %q), want (true, \"\")", ok, reason)
	}
}

func TestProbeProcessInspectWith_Blocked(t *testing.T) {
	ok, reason := probeProcessInspectWith(context.Background(), func(ctx context.Context) error {
		return errors.New("ps: operation not permitted")
	})
	if ok {
		t.Fatalf("got available=true, want blocked")
	}
	if !strings.Contains(reason, "operation not permitted") {
		t.Fatalf("reason %q does not mention the underlying error", reason)
	}
}

// TestProbeProcessInspect_RealPS smoke-tests the real wrapper's plumbing
// against the actual `ps` binary, the same real-boundary reasoning as
// TestProbeLoopbackBind_RealSocket.
func TestProbeProcessInspect_RealPS(t *testing.T) {
	ok, reason := probeProcessInspect(context.Background())
	if !ok {
		t.Fatalf("real process-inspect probe reported blocked: %s", reason)
	}
}

// --- probeGitCacheWith ----------------------------------------------------------

func TestProbeGitCacheWith_Available(t *testing.T) {
	var mkdirCalls, removeCalls []string
	mkdirAll := func(dir string, perm fs.FileMode) error {
		mkdirCalls = append(mkdirCalls, dir)
		return nil
	}
	createTemp := func(dir, pattern string) (*os.File, error) {
		return os.CreateTemp(t.TempDir(), pattern)
	}
	remove := func(name string) error {
		removeCalls = append(removeCalls, name)
		return nil
	}
	ok, reason := probeGitCacheWith("/tmp/git-cache", mkdirAll, createTemp, remove)
	if !ok || reason != "" {
		t.Fatalf("got (%v, %q), want (true, \"\")", ok, reason)
	}
	if len(mkdirCalls) != 1 || mkdirCalls[0] != "/tmp/git-cache" {
		t.Fatalf("mkdirCalls = %v, want one call for /tmp/git-cache", mkdirCalls)
	}
	if len(removeCalls) != 1 {
		t.Fatalf("removeCalls = %v, want exactly one cleanup", removeCalls)
	}
}

func TestProbeGitCacheWith_BlockedOnMkdirFailure(t *testing.T) {
	mkdirAll := func(dir string, perm fs.FileMode) error { return errors.New("mkdir: permission denied") }
	createTemp := func(dir, pattern string) (*os.File, error) {
		t.Fatalf("createTemp must not be called when mkdirAll fails")
		return nil, nil
	}
	remove := func(name string) error { return nil }
	ok, reason := probeGitCacheWith("/tmp/git-cache", mkdirAll, createTemp, remove)
	if ok {
		t.Fatalf("got available=true, want blocked")
	}
	if !strings.Contains(reason, "permission denied") || !strings.Contains(reason, "/tmp/git-cache") {
		t.Fatalf("reason %q does not name the path and the underlying error", reason)
	}
}

func TestProbeGitCacheWith_BlockedOnWriteFailure(t *testing.T) {
	mkdirAll := func(dir string, perm fs.FileMode) error { return nil }
	createTemp := func(dir, pattern string) (*os.File, error) { return nil, errors.New("no space left on device") }
	remove := func(name string) error {
		t.Fatalf("remove must not be called when createTemp failed")
		return nil
	}
	ok, reason := probeGitCacheWith("/tmp/git-cache", mkdirAll, createTemp, remove)
	if ok {
		t.Fatalf("got available=true, want blocked")
	}
	if !strings.Contains(reason, "no space left on device") {
		t.Fatalf("reason %q does not mention the underlying error", reason)
	}
}

// --- Classify: end-to-end shape -------------------------------------------------

// TestClassify_ReportsAllFourCapabilitiesInOrder pins the contract every
// downstream shell consumer relies on: Classify always reports exactly the
// four named capabilities, in a fixed order, never fewer (a probe that
// cannot decide still returns a blocked verdict, never omits itself).
func TestClassify_ReportsAllFourCapabilitiesInOrder(t *testing.T) {
	got := Classify(context.Background())
	want := []string{CapabilityLoopbackBind, CapabilityChromeCDP, CapabilityProcessInspect, CapabilityGitCache}
	if len(got) != len(want) {
		t.Fatalf("got %d capabilities, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d: got id %q, want %q", i, got[i].ID, id)
		}
		if got[i].Rerun == "" {
			t.Errorf("capability %q has no rerun command", id)
		}
	}
}

type fakeFileInfo struct {
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return "" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

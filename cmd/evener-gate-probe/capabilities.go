// Package main implements evener-gate-probe: a one-shot, bounded classifier for
// the sandbox-sensitive host capabilities the merge-approval gate's live/e2e
// test components depend on (loopback binds, a Chrome/Chromium binary for
// CDP-driven checks, process inspection via `ps`, and a writable external git
// cache directory). It never guesses: a probe that cannot decide in time
// classifies as blocked, never available. scripts/gate/gate-capability-preflight.sh
// is the only intended caller.
package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Capability ids. Renaming one means updating the matching skip-registry row
// in internal/devtool/gatesurface, which keys off these exact strings.
const (
	CapabilityLoopbackBind   = "loopback-bind"
	CapabilityChromeCDP      = "chrome-cdp"
	CapabilityProcessInspect = "process-inspect"
	CapabilityGitCache       = "git-cache"
)

// AllCapabilityIDs lists every capability Classify probes, in the fixed
// order it reports them.
var AllCapabilityIDs = []string{
	CapabilityLoopbackBind,
	CapabilityChromeCDP,
	CapabilityProcessInspect,
	CapabilityGitCache,
}

// Capability is one probe's honest verdict.
type Capability struct {
	ID        string
	Available bool
	Reason    string // empty when Available
	Rerun     string // exact command to re-probe just this one capability
}

// probeTimeout bounds every individual probe. A probe that cannot complete
// inside it classifies as blocked rather than hanging the gate - "cheap and
// bounded" from the kata's own acceptance criteria.
const probeTimeout = 5 * time.Second

// chromeCandidates mirrors scripts/ops/agent-chrome.sh's CHROME_CANDIDATES search
// order, so the two tools agree about what "a Chrome is available" means.
var chromeCandidates = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/usr/bin/google-chrome",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
}

// gitCacheDir is the path probeGitCache checks. EVENER_GATE_GIT_CACHE_DIR
// overrides the kata's literal /tmp/git-cache default for a host or fixture
// that uses a different location; it is gate-tooling-only, not a supported
// runtime variable for the evener/evener-hub product (docs/testing.md's env-var
// rule governs product-facing vars, not this gate's own internal plumbing -
// ROOT_FULL and WEB are the same kind of tooling-only variable already).
func gitCacheDir() string {
	if v := os.Getenv("EVENER_GATE_GIT_CACHE_DIR"); v != "" {
		return v
	}
	return "/tmp/git-cache"
}

// probeFunc is one capability check: decide available/blocked, honestly,
// without external side effects beyond what the probe itself needs to prove
// its answer.
type probeFunc func(ctx context.Context) (available bool, reason string)

// runProbe runs fn with a bound of timeout. A probe stuck on a stalled
// resource (kata 5gvk's own warning: "a probe that can hang on a stalled
// resource is worse than none") reports blocked-by-timeout instead of
// hanging the caller; the goroutine itself is abandoned rather than waited
// on; the leak is bounded to one stuck probe of the gate's own tooling, never
// the gate itself.
func runProbe(ctx context.Context, id, rerun string, timeout time.Duration, fn probeFunc) Capability {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := make(chan Capability, 1)
	go func() {
		ok, reason := fn(cctx)
		result <- Capability{ID: id, Available: ok, Reason: reason, Rerun: rerun}
	}()
	select {
	case c := <-result:
		return c
	case <-cctx.Done():
		return Capability{
			ID:        id,
			Available: false,
			Reason:    fmt.Sprintf("probe did not complete within %s", timeout),
			Rerun:     rerun,
		}
	}
}

// --- loopback bind ---------------------------------------------------------

// probeLoopbackBindWith decides from an injected bind attempt, so the
// blocked branch is provable without actually restricting a real socket.
func probeLoopbackBindWith(listen func() (io.Closer, error)) (bool, string) {
	l, err := listen()
	if err != nil {
		return false, fmt.Sprintf("cannot bind 127.0.0.1:0: %v", err)
	}
	_ = l.Close()
	return true, ""
}

func probeLoopbackBind(ctx context.Context) (bool, string) {
	return probeLoopbackBindWith(func() (io.Closer, error) {
		var lc net.ListenConfig
		return lc.Listen(ctx, "tcp", "127.0.0.1:0")
	})
}

// --- Chrome / CDP ------------------------------------------------------------

// probeChromeCDPWith reports available only for a candidate path that both
// exists and is executable - a stale, unexecutable file must not count.
//
// This checks binary presence only, not a live CDP handshake. Nothing in
// `make merge-approval-gate` currently launches Chrome (test-web-browser owns
// that, and it is a separate, non-gate target), so a full launch+CDP
// round trip would add real cost to classify a signal nothing yet consumes.
// The probe still runs and reports honestly; wiring a real consumer is
// future work if one is added to the gate.
func probeChromeCDPWith(candidates []string, stat func(string) (fs.FileInfo, error)) (bool, string) {
	for _, c := range candidates {
		info, err := stat(c)
		if err != nil {
			continue
		}
		if info.Mode()&0o111 != 0 {
			return true, ""
		}
	}
	return false, fmt.Sprintf("no Chrome/Chromium binary found (checked: %s)", strings.Join(candidates, ", "))
}

func probeChromeCDP(ctx context.Context) (bool, string) {
	return probeChromeCDPWith(chromeCandidates, os.Stat)
}

// --- process inspection -----------------------------------------------------

// probeProcessInspectWith decides from an injected `ps` invocation.
func probeProcessInspectWith(ctx context.Context, run func(context.Context) error) (bool, string) {
	if err := run(ctx); err != nil {
		return false, fmt.Sprintf("cannot inspect own process via ps: %v", err)
	}
	return true, ""
}

func probeProcessInspect(ctx context.Context) (bool, string) {
	return probeProcessInspectWith(ctx, func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(os.Getpid()))
		return cmd.Run()
	})
}

// --- external git cache -------------------------------------------------------

// probeGitCacheWith decides from injected filesystem operations: create the
// directory if needed, write a throwaway file inside it, then remove it.
// remove is never called when an earlier step already failed - there is
// nothing to clean up.
func probeGitCacheWith(dir string, mkdirAll func(string, fs.FileMode) error, createTemp func(dir, pattern string) (*os.File, error), remove func(string) error) (bool, string) {
	if err := mkdirAll(dir, 0o755); err != nil {
		return false, fmt.Sprintf("cannot create %s: %v", dir, err)
	}
	f, err := createTemp(dir, "gate-probe-*")
	if err != nil {
		return false, fmt.Sprintf("cannot write inside %s: %v", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := remove(name); err != nil {
		return false, fmt.Sprintf("cannot remove probe file in %s: %v", dir, err)
	}
	return true, ""
}

func probeGitCache(ctx context.Context, dir string) (bool, string) {
	return probeGitCacheWith(dir, os.MkdirAll, os.CreateTemp, os.Remove)
}

// --- Classify ----------------------------------------------------------------

// Classify runs every probe once, each bounded by probeTimeout, and returns
// one Capability per AllCapabilityIDs entry, in that fixed order.
func Classify(ctx context.Context) []Capability {
	return []Capability{
		runProbe(ctx, CapabilityLoopbackBind, "go run ./cmd/evener-gate-probe -only="+CapabilityLoopbackBind, probeTimeout, probeLoopbackBind),
		runProbe(ctx, CapabilityChromeCDP, "go run ./cmd/evener-gate-probe -only="+CapabilityChromeCDP, probeTimeout, probeChromeCDP),
		runProbe(ctx, CapabilityProcessInspect, "go run ./cmd/evener-gate-probe -only="+CapabilityProcessInspect, probeTimeout, probeProcessInspect),
		runProbe(ctx, CapabilityGitCache, "go run ./cmd/evener-gate-probe -only="+CapabilityGitCache, probeTimeout, func(ctx context.Context) (bool, string) {
			return probeGitCache(ctx, gitCacheDir())
		}),
	}
}

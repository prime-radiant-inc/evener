// Package e2ecap lets live/e2e tests detect the sandbox-sensitive host
// capabilities they need and skip themselves when the host lacks them.
//
// This is the per-test replacement for the deleted merge-gate capability
// preflight: instead of classifying the host once and skipping by name
// pattern, each e2e test probes exactly the capability it is about to use,
// at the moment it would use it. A probe that cannot decide in time treats
// the capability as missing.
package e2ecap

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// probeTimeout bounds one probe; a stuck resource reads as missing, never as
// a hang.
const probeTimeout = 5 * time.Second

// RequireLoopbackBind skips t when the host cannot bind a loopback port —
// the hub/daemon e2e tests' core requirement.
func RequireLoopbackBind(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("host cannot bind 127.0.0.1:0 (%v); skipping loopback e2e", err)
	}
	_ = l.Close()
}

// RequireProcessInspect skips t when `ps` cannot inspect this process — the
// e2e teardown/orphan checks' requirement on sandboxed hosts.
func RequireProcessInspect(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(os.Getpid())).Run(); err != nil {
		t.Skipf("cannot inspect own process via ps (%v); skipping e2e", err)
	}
}

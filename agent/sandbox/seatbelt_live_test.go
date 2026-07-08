//go:build darwin

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive the REAL /usr/bin/sandbox-exec on macOS and assert the
// kernel's actual verdict, not just the generated policy text. They are the
// paradise-park parity suite from the M6 plan (network denial, pseudo-fs/process
// exposure, worktree/secret confinement, git-config protection) plus M1's
// contract suite re-run against the live host's real facts.
//
// They are gated behind SERF_SEATBELT_LIVE=1 so a plain `make test` on macOS
// never invokes sandbox-exec (which needs a real policy environment and, for the
// network cases, internet). Run them with:
//
//	SERF_SEATBELT_LIVE=1 go test ./agent/sandbox/ -run TestSeatbeltLive -v

func requireLiveSeatbelt(t *testing.T) {
	t.Helper()
	if os.Getenv("SERF_SEATBELT_LIVE") != "1" {
		t.Skip("live seatbelt test: set SERF_SEATBELT_LIVE=1 to run on macOS")
	}
	if st, err := os.Stat(pathToSeatbelt); err != nil || st.IsDir() {
		t.Skipf("live seatbelt test: %s not available", pathToSeatbelt)
	}
}

// liveResolve resolves a policy for a real materialized git worktree against the
// live host's actual facts (RealProber), so the roots/masks match the Mac the
// test runs on.
func liveResolve(t *testing.T, mode Mode, netOn bool, extra ...func(*SandboxPolicy)) (ResolvedPolicy, string) {
	t.Helper()
	cwd := MaterializeWorkspace(t, MainCheckout)
	net := netOn
	policy := SandboxPolicy{Mode: mode, Network: &net}
	for _, f := range extra {
		f(&policy)
	}
	facts := RealProber{}.Probe()
	if !facts.SeatbeltAvailable() {
		t.Skip("live seatbelt test: RealProber reports sandbox-exec unavailable")
	}
	rp, err := Resolve(policy, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve(%v, net=%v): %v", mode, netOn, err)
	}
	return rp, cwd
}

// runUnderSeatbelt wraps command under the real sandbox-exec with rp's policy and
// returns the combined output and exit code. sessionTmp is a real writable temp
// dir the child may use.
func runUnderSeatbelt(t *testing.T, rp ResolvedPolicy, cwd string, command ...string) (string, int) {
	t.Helper()
	sessionTmp := t.TempDir()
	argv, err := seatbeltWrap(command, rp, sessionTmp, cwd)
	if err != nil {
		t.Fatalf("seatbeltWrap: %v", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv[0] is the hard-coded /usr/bin/sandbox-exec
	cmd.Dir = cwd
	cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
	out, err := cmd.CombinedOutput()
	exit := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		exit = -1
	}
	return string(out), exit
}

// TestSeatbeltLiveModesRun proves the base + platform-defaults let a real process
// exec and dyld-load in every mode — including restricted on a main checkout,
// the case Landlock could never serve.
func TestSeatbeltLiveModesRun(t *testing.T) {
	requireLiveSeatbelt(t)
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp, cwd := liveResolve(t, mode, true)
		out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/echo", "serf-live-ok")
		if exit != 0 || !strings.Contains(out, "serf-live-ok") {
			t.Errorf("%v: a confined /bin/echo must run (exit=%d out=%q)", mode, exit, out)
		}
	}
}

// TestSeatbeltLiveNetworkDenial: net=off blocks a real TCP connect; net=on allows
// it. Mirrors the Linux --unshare-net observable.
func TestSeatbeltLiveNetworkDenial(t *testing.T) {
	requireLiveSeatbelt(t)
	// Baseline: skip if the host itself has no outbound connectivity.
	probe := []string{"/bin/sh", "-c", "nc -G 3 -z 1.1.1.1 443"}
	if out, err := exec.Command(probe[0], probe[1:]...).CombinedOutput(); err != nil { //nolint:gosec,noctx // baseline reachability probe
		t.Skipf("no host connectivity for the network parity test: %v (%s)", err, out)
	}

	off, cwd := liveResolve(t, ModeWorkspaceWrite, false)
	if _, exit := runUnderSeatbelt(t, off, cwd, probe...); exit == 0 {
		t.Error("net=off: a confined TCP connect must fail, but it succeeded")
	}
	on, cwd2 := liveResolve(t, ModeWorkspaceWrite, true)
	if out, exit := runUnderSeatbelt(t, on, cwd2, probe...); exit != 0 {
		t.Errorf("net=on: a confined TCP connect must succeed (exit=%d out=%q)", exit, out)
	}
}

// TestSeatbeltLiveWorktreeWriteConfinement: workspace-write allows a worktree
// write and denies an out-of-worktree write; read-only denies even the worktree.
func TestSeatbeltLiveWorktreeWriteConfinement(t *testing.T) {
	requireLiveSeatbelt(t)
	outside := filepath.Join(t.TempDir(), "escape")

	ww, cwd := liveResolve(t, ModeWorkspaceWrite, true)
	if _, exit := runUnderSeatbelt(t, ww, cwd, "/bin/sh", "-c", "echo in >"+filepath.Join(cwd, "in-worktree")); exit != 0 {
		t.Error("workspace-write must allow a worktree write")
	}
	if _, exit := runUnderSeatbelt(t, ww, cwd, "/bin/sh", "-c", "echo out >"+outside); exit == 0 {
		t.Errorf("workspace-write must deny an out-of-worktree write to %q", outside)
	}

	ro, cwd2 := liveResolve(t, ModeReadOnly, true)
	if _, exit := runUnderSeatbelt(t, ro, cwd2, "/bin/sh", "-c", "echo x >"+filepath.Join(cwd2, "nope")); exit == 0 {
		t.Error("read-only must deny a worktree write")
	}
}

// TestSeatbeltLiveSecretDenied: a denylisted path (added via DenylistAdd, so the
// test is hermetic) is unreadable by a confined process even though it sits under
// the full-disk read grant.
func TestSeatbeltLiveSecretDenied(t *testing.T) {
	requireLiveSeatbelt(t)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "token")
	const sentinel = "SERF-LIVE-SECRET-42"
	if err := os.WriteFile(secret, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	rp, cwd := liveResolve(t, ModeReadOnly, true, func(p *SandboxPolicy) {
		p.DenylistAdd = append(p.DenylistAdd, secretDir)
	})
	out, _ := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "cat "+secret+" 2>&1 || true")
	if strings.Contains(out, sentinel) {
		t.Errorf("denylisted secret leaked through a confined read:\n%s", out)
	}
}

// TestSeatbeltLiveGitConfigProtected: git object writes work (commit succeeds)
// but a .git/config write is denied — the persistence-vector protection.
func TestSeatbeltLiveGitConfigProtected(t *testing.T) {
	requireLiveSeatbelt(t)
	rp, cwd := liveResolve(t, ModeWorkspaceWrite, true)
	// config write denied.
	if _, exit := runUnderSeatbelt(t, rp, cwd, "git", "config", "--local", "serf.escape", "1"); exit == 0 {
		t.Error("workspace-write must deny a .git/config write (git config --local)")
	}
	// object write allowed (staging + commit touches objects/refs/index/logs).
	if out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c",
		"git -c user.email=t@e -c user.name=t commit --allow-empty -m serf-live 2>&1"); exit != 0 {
		t.Errorf("workspace-write must allow git object writes (commit) (exit=%d):\n%s", exit, out)
	}
}

// TestSeatbeltLiveContractOnHost re-runs M1's exported contract suite against the
// live host's real facts, confirming the resolver agrees with the Mac it runs on.
func TestSeatbeltLiveContractOnHost(t *testing.T) {
	requireLiveSeatbelt(t)
	AssertResolve(t, Resolve)
}

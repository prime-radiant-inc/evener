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

// TestSeatbeltLiveFirmlinkAliasDenied proves the firmlink-alias defense against the
// real kernel: a denylisted path stays unreadable when reached via its
// /System/Volumes/Data firmlink spelling, not just its plain spelling. macOS
// firmlinks give a data-volume file two real paths EvalSymlinks does not collapse,
// so without the alias deny the data-volume spelling slips past the plain deny
// under the full-disk read grant. The alias is the /System/Volumes/Data-prefixed
// canonical path — exactly what denySection now denies.
func TestSeatbeltLiveFirmlinkAliasDenied(t *testing.T) {
	requireLiveSeatbelt(t)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "token")
	const sentinel = "SERF-LIVE-FIRMLINK-42"
	if err := os.WriteFile(secret, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	rp, cwd := liveResolve(t, ModeReadOnly, true, func(p *SandboxPolicy) {
		p.DenylistAdd = append(p.DenylistAdd, secretDir)
	})
	// Reach the same secret via its data-volume firmlink alias. Resolve the plain
	// path to its canonical form first (t.TempDir() is under a firmlinked root),
	// then prepend the data-volume prefix — the second real spelling of the file.
	canonical, err := filepath.EvalSymlinks(secret)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", secret, err)
	}
	aliasPath := dataVolumePrefix + canonical
	out, _ := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "cat "+aliasPath+" 2>&1 || true")
	if strings.Contains(out, sentinel) {
		t.Errorf("denylisted secret leaked through its firmlink alias %q:\n%s", aliasPath, out)
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

// goTelemetryDenialLine is the ONE tolerated stderr line from a sandboxed `go`
// invocation: Go's telemetry uploader stats/creates a token file under the
// user's Go config directory, which sits outside every sandbox root by
// design (see docs/sandboxing.md's "Known residual: Go telemetry noise is not
// suppressed"). GOTELEMETRY is a report-only `go env` value the toolchain
// never reads back from the process environment — `go env -w
// GOTELEMETRY=off` itself fails with "GOTELEMETRY cannot be modified" — so
// there is no env-floor lever that silences this; only a per-session HOME
// redirect or a new writable root would, and both were rejected as broader
// than the problem warrants. Ruled 2026-08-06: accept and document the
// noise, don't chase it. Go emits this via log.Printf with one of two
// prefixes depending on which filesystem call failed — "error acquiring
// upload taken: statting token file: ..." (sic, an upstream typo) or "error
// acquiring upload token: creating token file: ...".
const goTelemetryDenialLine = "error acquiring upload"

// TestSeatbeltLiveGoTestTelemetryDenialIsTheOnlyStderr proves the accepted
// residual is bounded: `go test` on a trivial module, run inside a restricted
// sandbox on this macOS/Seatbelt host (always CacheSessionPrivate — the
// tightest, most representative case), exits 0, and stderr contains AT MOST
// the one known, explained telemetry-denial line — never anything else. A
// change that widens the noise (a new denial, a different failure mode) must
// fail this test rather than slide through file-by-file.
func TestSeatbeltLiveGoTestTelemetryDenialIsTheOnlyStderr(t *testing.T) {
	requireLiveSeatbelt(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("live seatbelt test: go not found on PATH")
	}

	rp, cwd := liveResolve(t, ModeRestricted, true)
	if rp.CacheStrategy != CacheSessionPrivate {
		t.Fatalf("restricted mode must resolve CacheSessionPrivate, got %v", rp.CacheStrategy)
	}

	// A trivial module with no external dependencies: it exercises the build/test
	// path (and GOCACHE) without needing GOMODCACHE writes, keeping this test
	// focused on the telemetry denial.
	writeFile(t, filepath.Join(cwd, "go.mod"), "module serf-live-telemetry-probe\n\ngo 1.21\n")
	writeFile(t, filepath.Join(cwd, "probe_test.go"), "package probe\n\nimport \"testing\"\n\nfunc TestProbe(t *testing.T) {}\n")

	sessionTmp := t.TempDir()
	argv, err := seatbeltWrap([]string{goBin, "test", "./..."}, rp, sessionTmp, cwd)
	if err != nil {
		t.Fatalf("seatbeltWrap: %v", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv[0] is the hard-coded /usr/bin/sandbox-exec
	cmd.Dir = cwd
	cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if runErr != nil {
		t.Errorf("go test must exit 0, got %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout.String(), stderr.String())
	}
	for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, goTelemetryDenialLine) {
			t.Errorf("go test stderr must contain only the known telemetry-denial line, got unexpected line:\n%s\nfull stderr:\n%s", line, stderr.String())
		}
	}
}

// writeFile writes content to path, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%q): %v", path, err)
	}
}

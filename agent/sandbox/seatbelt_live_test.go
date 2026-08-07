//go:build darwin

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	return liveResolveKind(t, MainCheckout, mode, netOn, extra...)
}

// liveResolveKind is liveResolve over a chosen workspace layout. A linked
// worktree is the layout whose git metadata lives OUTSIDE the worktree, so its
// grants come from GitLayout.WritablePaths rather than from the worktree write
// root — the two cases must be exercised separately.
func liveResolveKind(t *testing.T, kind WorkspaceKind, mode Mode, netOn bool, extra ...func(*SandboxPolicy)) (ResolvedPolicy, string) {
	t.Helper()
	cwd := MaterializeWorkspace(t, kind)
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

// TestSeatbeltLivePackedRefsMaintenance proves the contract's promise that
// packed-refs is writable holds for the maintenance git ACTUALLY performs on it:
// git never writes packed-refs in place — it writes the sibling
// `packed-refs.lock` in the same directory and renames it over the target. A
// grant on the `packed-refs` FILE alone therefore leaves the lock on a
// write-denied parent and `git pack-refs` fails with
// "packed-refs.lock: Operation not permitted". Both layouts are exercised: a
// main checkout (whose .git sits under the worktree write root) and a linked
// worktree (whose common .git is outside it and reachable only through
// GitLayout.WritablePaths).
func TestSeatbeltLivePackedRefsMaintenance(t *testing.T) {
	requireLiveSeatbelt(t)
	for _, kind := range []WorkspaceKind{MainCheckout, LinkedWorktree} {
		t.Run(kind.String(), func(t *testing.T) {
			rp, cwd := liveResolveKind(t, kind, ModeWorkspaceWrite, true)
			// A commit gives the repo a ref to pack; pack-refs then drives the
			// lock+rename dance over <common>/packed-refs.
			//
			// rerere is forced OFF so the assertion depends only on the grant under
			// test. A developer with `rerere.enabled=true` in ~/.gitconfig (which the
			// sandbox reads) otherwise has `git commit` create <common>/rr-cache — a
			// SEPARATE ungranted common-dir surface, tracked apart from packed-refs.
			out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c",
				"git -c rerere.enabled=false -c user.email=t@e -c user.name=t commit -q --allow-empty -m serf-packed-refs && git pack-refs --all")
			if exit != 0 {
				t.Errorf("workspace-write must allow git packed-refs maintenance (exit=%d):\n%s", exit, out)
			}
			if strings.TrimSpace(out) != "" {
				t.Errorf("git packed-refs maintenance must produce no output, got:\n%s", out)
			}
		})
	}
}

// TestSeatbeltLiveGitConfigAndHooksStillDenied pins the persistence-vector
// protection against the packed-refs grant: widening the git-metadata write
// surface must NOT make any config or hook surface writable. It asserts the
// denial directly (a raw shell write to each surface), not just through the git
// porcelain, and in both layouts — for a linked worktree the surfaces exist on
// BOTH the per-worktree git dir and the common dir.
func TestSeatbeltLiveGitConfigAndHooksStillDenied(t *testing.T) {
	requireLiveSeatbelt(t)
	for _, kind := range []WorkspaceKind{MainCheckout, LinkedWorktree} {
		t.Run(kind.String(), func(t *testing.T) {
			rp, cwd := liveResolveKind(t, kind, ModeWorkspaceWrite, true)

			// An empty protected set would let every assertion below vacuously pass,
			// so a denial test must first prove it has something to deny.
			if len(rp.Git.ProtectedPaths) == 0 {
				t.Fatalf("%v: no protected git surfaces resolved — the denial assertions would pass vacuously", kind)
			}

			// Every protected surface the resolver claims to protect, proven denied
			// against the real kernel — config files by an append, the hooks dir by
			// planting a hook inside it.
			for _, p := range rp.Git.ProtectedPaths {
				target := p
				if filepath.Base(p) == "hooks" {
					target = filepath.Join(p, "post-commit")
				}
				out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c",
					"echo serf-escape >>"+target+" 2>&1")
				if exit == 0 {
					t.Errorf("write to protected git surface %q must be denied, but it succeeded:\n%s", target, out)
				}
			}

			// And the porcelain path stays denied too.
			if _, exit := runUnderSeatbelt(t, rp, cwd, "git", "config", "--local", "core.hooksPath", "/tmp/evil"); exit == 0 {
				t.Error("git config --local core.hooksPath must be denied")
			}
		})
	}
}

// liveInfraDir materializes a stand-in for the session's hook/MCP-server
// directory OUTSIDE the worktree — the shape of a plugin-cache hook script,
// which is what a real SessionStart hook execs. It returns the directory and the
// executable script inside it.
func liveInfraDir(t *testing.T) (dir, script string) {
	t.Helper()
	dir = t.TempDir()
	script = filepath.Join(dir, "session-start.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho serf-hook-ran\n"), 0o755); err != nil { //nolint:gosec // an executable test fixture
		t.Fatal(err)
	}
	return dir, script
}

// TestSeatbeltLiveInfraPathNotGrantedIsDenied is the RED reproduction, kept as a
// permanent control: a hook script living outside the worktree (the plugin
// cache) is NOT executable under restricted mode unless the session's hook/MCP
// paths are granted. This is the exit-126 failure the 2026-08-06 ruling
// addresses, and it pins that the grant below — not some unrelated widening — is
// what makes hooks work.
func TestSeatbeltLiveInfraPathNotGrantedIsDenied(t *testing.T) {
	requireLiveSeatbelt(t)
	_, script := liveInfraDir(t)
	rp, cwd := liveResolve(t, ModeRestricted, true)
	out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", script)
	if exit == 0 {
		t.Errorf("restricted mode must deny an UNGRANTED out-of-worktree script (exit=%d out=%q)", exit, out)
	}
}

// TestSeatbeltLiveInfraPathExecutableInEveryMode proves the 2026-08-06 ruling
// against the real kernel: hooks and MCP servers are session INFRASTRUCTURE, so
// once the session's configured hook/MCP paths are on the policy they execute in
// every sandbox mode — including restricted, whose spawned layer otherwise reads
// the worktree only.
func TestSeatbeltLiveInfraPathExecutableInEveryMode(t *testing.T) {
	requireLiveSeatbelt(t)
	infra, script := liveInfraDir(t)
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp, cwd := liveResolve(t, mode, true, func(p *SandboxPolicy) {
			p.InfraReadRoots = []string{infra}
		})
		out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", script)
		if exit != 0 || !strings.Contains(out, "serf-hook-ran") {
			t.Errorf("%v: a configured hook/MCP path must be executable (exit=%d out=%q)", mode, exit, out)
		}
	}
}

// TestSeatbeltLiveInfraPathIsReadOnly proves the infrastructure grant is READ/
// EXEC only: the same directory that just ran a hook script stays unwritable in
// every mode, so granting hooks never widens the write surface.
func TestSeatbeltLiveInfraPathIsReadOnly(t *testing.T) {
	requireLiveSeatbelt(t)
	infra, _ := liveInfraDir(t)
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp, cwd := liveResolve(t, mode, true, func(p *SandboxPolicy) {
			p.InfraReadRoots = []string{infra}
		})
		target := filepath.Join(infra, "planted")
		out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "echo x >"+target+" 2>&1")
		if exit == 0 {
			t.Errorf("%v: a hook/MCP path must stay write-denied, but %q was created:\n%s", mode, target, out)
		}
	}
}

// TestSeatbeltLiveInfraGrantNeverUnmasksDenylist proves the denylist still wins
// over the infrastructure grant, in both overlap shapes: a denylisted subtree
// INSIDE a granted hook path stays unreadable, and a hook path that IS itself
// denylisted is not granted at all. docs/sandboxing.md's floor — the denylist is
// authoritative over every allow — must not be dented by session infrastructure.
func TestSeatbeltLiveInfraGrantNeverUnmasksDenylist(t *testing.T) {
	requireLiveSeatbelt(t)
	const sentinel = "SERF-LIVE-INFRA-SECRET-42"

	t.Run("denylisted subtree inside a granted hook path", func(t *testing.T) {
		infra, script := liveInfraDir(t)
		secretDir := filepath.Join(infra, "credentials")
		if err := os.MkdirAll(secretDir, 0o700); err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(secretDir, "token")
		if err := os.WriteFile(secret, []byte(sentinel), 0o600); err != nil {
			t.Fatal(err)
		}
		rp, cwd := liveResolve(t, ModeRestricted, true, func(p *SandboxPolicy) {
			p.InfraReadRoots = []string{infra}
			p.DenylistAdd = append(p.DenylistAdd, secretDir)
		})
		// The hook itself still runs...
		if out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", script); exit != 0 {
			t.Fatalf("the granted hook must still run (exit=%d out=%q)", exit, out)
		}
		// ...but the denylisted subtree under it stays masked.
		out, _ := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "cat "+secret+" 2>&1 || true")
		if strings.Contains(out, sentinel) {
			t.Errorf("the hook/MCP grant un-masked a denylisted path:\n%s", out)
		}
	})

	t.Run("hook path that is itself denylisted", func(t *testing.T) {
		infra, script := liveInfraDir(t)
		rp, cwd := liveResolve(t, ModeRestricted, true, func(p *SandboxPolicy) {
			p.InfraReadRoots = []string{infra}
			p.DenylistAdd = append(p.DenylistAdd, infra)
		})
		if out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", script); exit == 0 {
			t.Errorf("a denylisted path must not become executable by also being a hook path:\n%s", out)
		}
	})
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

// realConfigGit is the git command prefix every live git assertion uses. It
// deliberately does NOT neutralize the developer's global git configuration:
// the 2026-08-07 ruling makes the global config files readable in restricted
// mode, and the acceptance criterion is that git works against the config the
// developer REALLY has. A test that set GIT_CONFIG_GLOBAL=/dev/null would be
// arranging that config out of existence and proving nothing about it.
//
// The only -c overrides are the committer identity, which is not a neutralization
// (git still reads the whole global config) but a floor: a scratch repo has no
// identity of its own, and a host whose developer never set user.email could not
// commit at all. Everything else — includes, aliases, rerere, excludes/attributes
// files, credential helpers — comes from the real global config.
const realConfigGit = "git -c user.email=t@e -c user.name=t "

// unexpectedStderr returns the non-empty stderr lines of a live git assertion.
// There is no tolerance list: a git invocation in a restricted sandbox must be
// PRISTINE on stderr (ruled 2026-08-07). The xcrun cache-write denial that used
// to be allowed here is gone at the source — the env floor puts the resolved
// developer toolchain's bin directory on PATH, so `git` is the real git rather
// than the /usr/bin shim that memoizes its lookup in the per-user temp
// directory. See ResolvedPolicy.ToolchainBinDir.
func unexpectedStderr(stderr string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// runUnderSeatbeltSplit is runUnderSeatbelt with the two streams kept apart, so
// a test can assert on stderr alone.
func runUnderSeatbeltSplit(t *testing.T, rp ResolvedPolicy, cwd string, command ...string) (stdout, stderr string, exit int) {
	t.Helper()
	sessionTmp := t.TempDir()
	argv, err := seatbeltWrap(command, rp, sessionTmp, cwd)
	if err != nil {
		t.Fatalf("seatbeltWrap: %v", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv[0] is the hard-coded /usr/bin/sandbox-exec
	cmd.Dir = cwd
	cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	exit = 0
	var ee *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &ee):
		exit = ee.ExitCode()
	default:
		exit = -1
	}
	return outBuf.String(), errBuf.String(), exit
}

// TestSeatbeltLiveGitRunsInRestrictedMode is the RED reproduction of the
// 2026-08-06 developer-toolchain ruling, kept as the permanent proof.
//
// On macOS /usr/bin/git is an xcrun shim: it locates the real git under the
// ACTIVE developer directory (`xcode-select -p`, typically
// /Applications/Xcode.app) or the standalone Command Line Tools
// (/Library/Developer/CommandLineTools). Neither lives under restricted mode's
// system read roots, so before the grant even `git --version` died with
//
//	xcrun: error: invalid active developer path
//	(/Applications/Xcode.app/Contents/Developer), missing xcrun at:
//	/Applications/Xcode.app/Contents/Developer/usr/bin/xcrun
//
// — the verified shape behind sessions that hand-rolled git operations rather
// than running git.
func TestSeatbeltLiveGitRunsInRestrictedMode(t *testing.T) {
	requireLiveSeatbelt(t)
	rp, cwd := liveResolve(t, ModeRestricted, true)
	if len(RealProber{}.Probe().DeveloperToolRoots) == 0 {
		t.Skip("live seatbelt test: no developer toolchain installed on this host")
	}
	stdout, stderr, exit := runUnderSeatbeltSplit(t, rp, cwd, "/bin/sh", "-c", realConfigGit+"--version")
	if exit != 0 {
		t.Fatalf("restricted mode must be able to run git (exit=%d)\nstdout:\n%s\nstderr:\n%s\n\nThis test runs git against YOUR REAL global config (that is the point of the 2026-08-07 ruling), so a failure here may be your gitconfig rather than a broken sandbox. Settings known to break it: commit.gpgsign=true (gpg is not reachable in the sandbox), a core.hooksPath under $HOME, and a core.excludesfile/core.attributesfile naming a file that EXISTS under $HOME (readable config, unreadable target -> a warning on stderr). Compare with GIT_CONFIG_GLOBAL=/dev/null to tell the two apart.", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "git version") {
		t.Errorf("git --version stdout = %q, want a version line", stdout)
	}
	if extra := unexpectedStderr(stderr); len(extra) > 0 {
		t.Errorf("git --version emitted unexpected stderr:\n%s", strings.Join(extra, "\n"))
	}
}

// TestSeatbeltLiveGitCommitInRestrictedMode is the acceptance bar for
// restricted-mode git: a FULL commit — stage a new file, write the object, move
// the ref — inside a restricted sandbox, exiting 0, against the developer's
// REAL global configuration (aliases, rerere, signoff, filters and all).
func TestSeatbeltLiveGitCommitInRestrictedMode(t *testing.T) {
	requireLiveSeatbelt(t)
	rp, cwd := liveResolve(t, ModeRestricted, true)
	if len(RealProber{}.Probe().DeveloperToolRoots) == 0 {
		t.Skip("live seatbelt test: no developer toolchain installed on this host")
	}
	writeFile(t, filepath.Join(cwd, "committed.txt"), "serf restricted commit\n")

	script := realConfigGit + "add committed.txt && " +
		realConfigGit + "commit -q -m serf-restricted-commit && " +
		realConfigGit + "log -1 --format=%s"
	stdout, stderr, exit := runUnderSeatbeltSplit(t, rp, cwd, "/bin/sh", "-c", script)
	if exit != 0 {
		t.Fatalf("restricted mode must allow a full git commit (exit=%d)\nstdout:\n%s\nstderr:\n%s\n\nThis test runs git against YOUR REAL global config (that is the point of the 2026-08-07 ruling), so a failure here may be your gitconfig rather than a broken sandbox. Settings known to break it: commit.gpgsign=true (gpg is not reachable in the sandbox), a core.hooksPath under $HOME, and a core.excludesfile/core.attributesfile naming a file that EXISTS under $HOME (readable config, unreadable target -> a warning on stderr). Compare with GIT_CONFIG_GLOBAL=/dev/null to tell the two apart.", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "serf-restricted-commit") {
		t.Errorf("the commit did not land; git log said %q", stdout)
	}
	if extra := unexpectedStderr(stderr); len(extra) > 0 {
		t.Errorf("git commit emitted unexpected stderr:\n%s", strings.Join(extra, "\n"))
	}
}

// requireLiveGlobalGitConfig skips unless this host really has a global git
// config. Without one the grant's assertions would pass vacuously — a host with
// nothing to read proves nothing about reading it.
func requireLiveGlobalGitConfig(t *testing.T) []string {
	t.Helper()
	paths := RealProber{}.Probe().GitGlobalConfigPaths
	if len(paths) == 0 {
		t.Skip("live seatbelt test: this host has no global git config to read")
	}
	return paths
}

// TestSeatbeltLiveGlobalGitConfigReadableInRestrictedMode is the direct
// observable of the 2026-08-07 ruling: inside a restricted sandbox git can READ
// the developer's real global config — `git config --global --list` exits 0 and
// reports settings sourced from the real file — while the home directory around
// it stays unreadable, because the grant is file-exact and not a home read.
func TestSeatbeltLiveGlobalGitConfigReadableInRestrictedMode(t *testing.T) {
	requireLiveSeatbelt(t)
	paths := requireLiveGlobalGitConfig(t)
	rp, cwd := liveResolve(t, ModeRestricted, true)
	if len(RealProber{}.Probe().DeveloperToolRoots) == 0 {
		t.Skip("live seatbelt test: no developer toolchain installed on this host")
	}

	stdout, stderr, exit := runUnderSeatbeltSplit(t, rp, cwd, "/bin/sh", "-c", realConfigGit+"config --global --list")
	if exit != 0 {
		t.Fatalf("restricted mode must be able to read the global git config (exit=%d)\nstderr:\n%s\n\nThis test runs git against YOUR REAL global config (that is the point of the 2026-08-07 ruling), so a failure here may be your gitconfig rather than a broken sandbox. Settings known to break it: commit.gpgsign=true (gpg is not reachable in the sandbox), a core.hooksPath under $HOME, and a core.excludesfile/core.attributesfile naming a file that EXISTS under $HOME (readable config, unreadable target -> a warning on stderr). Compare with GIT_CONFIG_GLOBAL=/dev/null to tell the two apart.", exit, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("git config --global --list produced nothing, so the grant proved nothing")
	}
	if extra := unexpectedStderr(stderr); len(extra) > 0 {
		t.Errorf("git config --global --list emitted unexpected stderr:\n%s", strings.Join(extra, "\n"))
	}

	// The grant is the FILE, not the tree around it. Listing the home directory
	// and reading a sibling of the config must both stay denied.
	home := RealProber{}.Probe().Home
	sibling := filepath.Join(filepath.Dir(paths[len(paths)-1]), "serf-should-not-be-readable")
	for _, denied := range []string{"ls " + home, "cat " + sibling} {
		if _, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", denied+" >/dev/null 2>&1"); exit == 0 {
			t.Errorf("the global-config grant must not widen into a home read, but %q succeeded", denied)
		}
	}
}

// runUnderSeatbeltCanon is runUnderSeatbelt with the policy's canonicalizer
// injected, so a test can exercise a grant SPELLING the real canonicalizer would
// collapse. It uses the production policy generator; only the path-resolution
// seam differs.
func runUnderSeatbeltCanon(t *testing.T, rp ResolvedPolicy, cwd string, canon Canonicalizer, command ...string) (string, int) {
	t.Helper()
	sessionTmp := t.TempDir()
	text, params := SeatbeltPolicy(rp, sessionTmp, canon)
	argv := seatbeltArgs(pathToSeatbelt, text, params, command)
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

// TestSeatbeltLiveSymlinkReadRootSpellings pins the safety property the
// symlink-alias read grant rests on, and is the evidence behind the macOS note in
// docs/sandboxing.md: the LITERAL link spelling alone grants no reach — an
// ungranted target stays unreadable through the link — so emitting the literal
// name beside the canonical one is a SPELLING alias and never a widening. The
// second case pins that a symlink-spelled read root does reach its target once
// the emitter grants both names.
//
// The converse — that the canonical target alone is not enough, because Seatbelt
// judges the path a process actually names — is pinned where it actually bites,
// by TestSeatbeltLiveGlobalGitConfigReadableInRestrictedMode: under `$HOME`
// nothing grants access to the link itself, so the read is refused at the link.
// It cannot be reproduced from a temp directory, because the platform defaults
// grant file-read-metadata over all of /private/var and the link there resolves
// freely.
func TestSeatbeltLiveSymlinkReadRootSpellings(t *testing.T) {
	requireLiveSeatbelt(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	writeFile(t, target, "SERF-LIVE-SYMLINK-42\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// A canonicalizer that leaves the link spelling alone, so case 2 can grant the
	// literal name and nothing else.
	linkStaysLiteral := func(p string) string {
		if p == link {
			return p
		}
		return realCanonicalizer(p)
	}
	readThroughLink := []string{"/bin/sh", "-c", "cat " + link + " 2>&1"}

	base, cwd := liveResolve(t, ModeRestricted, true)

	linkOnly := base
	linkOnly.Spawned.ReadRoots = append(slices.Clone(base.Spawned.ReadRoots), link)
	if out, exit := runUnderSeatbeltCanon(t, linkOnly, cwd, linkStaysLiteral, readThroughLink...); exit == 0 {
		t.Errorf("the link spelling alone must grant no reach to an ungranted target, but the read succeeded:\n%s", out)
	}

	bothSpellings := base
	bothSpellings.Spawned.ReadRoots = append(slices.Clone(base.Spawned.ReadRoots), link)
	out, exit := runUnderSeatbeltCanon(t, bothSpellings, cwd, realCanonicalizer, readThroughLink...)
	if exit != 0 || !strings.Contains(out, "SERF-LIVE-SYMLINK-42") {
		t.Errorf("a symlink-spelled read root must be granted under both spellings (exit=%d):\n%s", exit, out)
	}
}

// TestSeatbeltLiveGitCredentialsStayMasked is the single most important check on
// the 2026-08-07 grant. A readable global config makes a `credential.helper`
// line visible; the secret store that line points at must NOT become visible with
// it. The denylist is emitted after every allow and wins.
//
// It runs against a SYNTHETIC home so the assertion has a real secret to fail on
// — the developer's actual ~/.git-credentials may not exist, and "absent" is not
// "masked". The home is a real directory with real files, the policy is the real
// resolver's, and the verdict is the real kernel's.
func TestSeatbeltLiveGitCredentialsStayMasked(t *testing.T) {
	requireLiveSeatbelt(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".gitconfig")
	credentials := filepath.Join(home, ".git-credentials")
	const secret = "SERF-LIVE-GIT-CREDENTIAL-42"
	writeFile(t, config, "[credential]\n\thelper = store\n")
	writeFile(t, credentials, "https://user:"+secret+"@example.invalid\n")

	facts := RealProber{}.Probe()
	if !facts.SeatbeltAvailable() {
		t.Skip("live seatbelt test: RealProber reports sandbox-exec unavailable")
	}
	facts.Home = home
	facts.GitGlobalConfigPaths = []string{config}
	cwd := MaterializeWorkspace(t, MainCheckout)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Contains(rp.MaskedPaths, credentials) {
		t.Fatalf("the credential store must be on the denylist, got %v", rp.MaskedPaths)
	}

	out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "cat "+config+" 2>&1")
	if exit != 0 || !strings.Contains(out, "helper = store") {
		t.Fatalf("the granted global config must be readable (exit=%d):\n%s", exit, out)
	}
	out, exit = runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "cat "+credentials+" 2>&1")
	if exit == 0 {
		t.Errorf("~/.git-credentials must stay masked beside a readable global config, but the read succeeded:\n%s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the credential secret leaked through the global-config grant:\n%s", out)
	}
}

// TestSeatbeltLiveRestrictedGitWritesStillDenied pins what the READ-ONLY grant
// must leave untouched: in restricted mode, with the global config readable, every
// git config and hook WRITE surface is still denied — including the global config
// file itself. docs/sandboxing.md's anti-hook-planting argument rests on the write
// denial, never on unreadability.
func TestSeatbeltLiveRestrictedGitWritesStillDenied(t *testing.T) {
	requireLiveSeatbelt(t)
	paths := requireLiveGlobalGitConfig(t)
	rp, cwd := liveResolve(t, ModeRestricted, true)
	if len(rp.Git.ProtectedPaths) == 0 {
		t.Fatal("no protected git surfaces resolved — the denial assertions would pass vacuously")
	}

	for _, p := range rp.Git.ProtectedPaths {
		target := p
		if filepath.Base(p) == "hooks" {
			target = filepath.Join(p, "post-commit")
		}
		if out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "echo serf-escape >>"+target+" 2>&1"); exit == 0 {
			t.Errorf("write to protected git surface %q must be denied, but it succeeded:\n%s", target, out)
		}
	}
	if _, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", realConfigGit+"config --local core.hooksPath /tmp/evil"); exit == 0 {
		t.Error("git config --local core.hooksPath must be denied in restricted mode")
	}
	// The new grant is READ-only: the global config it makes readable must not
	// have become writable, by raw append or by porcelain.
	for _, p := range paths {
		if out, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", "echo serf-escape >>"+p+" 2>&1"); exit == 0 {
			t.Errorf("the global config %q must stay write-denied, but the append succeeded:\n%s", p, out)
		}
	}
	if _, exit := runUnderSeatbelt(t, rp, cwd, "/bin/sh", "-c", realConfigGit+"config --global core.hooksPath /tmp/evil"); exit == 0 {
		t.Error("git config --global core.hooksPath must be denied")
	}
}

// TestSeatbeltLiveDenylistBeatsDeveloperToolRoots pins the precedence the new
// grant must never invert: a denylisted directory INSIDE a granted
// developer-toolchain root stays unreadable against the real kernel. The grant
// is an allow; the denylist is emitted after every allow and wins.
func TestSeatbeltLiveDenylistBeatsDeveloperToolRoots(t *testing.T) {
	requireLiveSeatbelt(t)
	devRoots := RealProber{}.Probe().DeveloperToolRoots
	if len(devRoots) == 0 {
		t.Skip("live seatbelt test: no developer toolchain installed on this host")
	}
	// Pick a real readable file inside a granted developer root to read for.
	var probeFile string
	for _, root := range devRoots {
		candidate := filepath.Join(root, "usr", "share", "git-core")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			probeFile = candidate
			break
		}
	}
	if probeFile == "" {
		t.Skip("live seatbelt test: no readable probe path inside a developer root")
	}

	granted, cwd := liveResolve(t, ModeRestricted, true)
	if out, exit := runUnderSeatbelt(t, granted, cwd, "/bin/sh", "-c", "ls "+probeFile+" >/dev/null"); exit != 0 {
		t.Fatalf("baseline: %q must be readable under the developer-tools grant (exit=%d):\n%s", probeFile, exit, out)
	}

	denied, cwd2 := liveResolve(t, ModeRestricted, true, func(p *SandboxPolicy) {
		p.DenylistAdd = append(p.DenylistAdd, probeFile)
	})
	if slices.Contains(denied.Spawned.ReadRoots, probeFile) {
		t.Fatalf("a masked path must never survive as a read root: %v", denied.Spawned.ReadRoots)
	}
	if out, exit := runUnderSeatbelt(t, denied, cwd2, "/bin/sh", "-c", "ls "+probeFile+" >/dev/null 2>&1"); exit == 0 {
		t.Errorf("the denylist must beat the developer-tools grant, but %q was still readable:\n%s", probeFile, out)
	}
}

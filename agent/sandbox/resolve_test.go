package sandbox

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testHome = "/home/tester"

func bwrapHost() HostFacts {
	return HostFacts{OS: "linux", Home: testHome, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, LandlockABI: 4}
}
func bwrapHostNoOverlay() HostFacts {
	return HostFacts{OS: "linux", Home: testHome, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false}
}
func landlockHost() HostFacts {
	return HostFacts{OS: "linux", Home: testHome, LandlockABI: 3}
}
func bareLinuxHost() HostFacts { return HostFacts{OS: "linux", Home: testHome} }
func windowsHost() HostFacts   { return HostFacts{OS: "windows", Home: `C:\Users\tester`} }
func darwinSeatbeltHost() HostFacts {
	return HostFacts{OS: "darwin", Home: "/Users/tester", SandboxExecPath: "/usr/bin/sandbox-exec"}
}
func darwinBareHost() HostFacts { return HostFacts{OS: "darwin", Home: "/Users/tester"} }

func mainRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	root := clean(t.TempDir())
	runGit(t, root, "init", "-q")
	return root
}

func linkedWorktreeRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	main := clean(t.TempDir())
	runGit(t, main, "init", "-q")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(filepath.Dir(main), "wt")
	runGit(t, main, "worktree", "add", "-q", wt)
	return clean(wt)
}

func mustResolve(t *testing.T, pol SandboxPolicy, host HostFacts, cwd string) ResolvedPolicy {
	t.Helper()
	rp, err := Resolve(pol, host, cwd)
	if err != nil {
		t.Fatalf("Resolve(%v, net=%v) unexpectedly refused: %v", pol.Mode, pol.Network, err)
	}
	return rp
}

func mustRefuse(t *testing.T, pol SandboxPolicy, host HostFacts, cwd, wantBackend string) *RefusalError {
	t.Helper()
	_, err := Resolve(pol, host, cwd)
	if err == nil {
		t.Fatalf("Resolve(%v, net=%v) should have refused on host %s", pol.Mode, pol.Network, host.OS)
	}
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("Resolve refusal is not a *RefusalError: %T %v", err, err)
	}
	if wantBackend != "" && ref.RequiredBackend != wantBackend {
		t.Errorf("refusal RequiredBackend = %q, want %q (reason: %s)", ref.RequiredBackend, wantBackend, ref.Reason)
	}
	if ref.Error() == "" {
		t.Error("refusal error message is empty")
	}
	return ref
}

// TestResolveOffIsUnconstrained: off resolves on ANY host (even windows) with no
// backend and no containment — it is the "today's behavior" escape hatch.
func TestResolveOffIsUnconstrained(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	for _, host := range []HostFacts{bwrapHost(), landlockHost(), bareLinuxHost(), windowsHost(), darwinBareHost()} {
		rp := mustResolve(t, SandboxPolicy{Mode: ModeOff}, host, dir)
		if rp.Mode != ModeOff || rp.Backend != BackendNone {
			t.Errorf("off on %s: Mode=%v Backend=%v, want off/none", host.OS, rp.Mode, rp.Backend)
		}
		if !rp.Network {
			t.Errorf("off on %s: Network=false, want true (off never denies egress)", host.OS)
		}
		if len(rp.MaskedPaths) != 0 || rp.CacheStrategy != CacheNone {
			t.Errorf("off on %s carries containment it should not: masked=%v cache=%v", host.OS, rp.MaskedPaths, rp.CacheStrategy)
		}
	}
}

// TestResolveBwrapServesAllModes: the bwrap tier runs every mode with net on and
// off. Cache strategy follows the mode (+ overlay availability); restricted is
// always session-private.
func TestResolveBwrapServesAllModes(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)
	for _, net := range []bool{true, false} {
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: net}, bwrapHost(), main)
			if rp.Backend != BackendBwrap {
				t.Errorf("mode=%v net=%v: Backend=%v, want bwrap", mode, net, rp.Backend)
			}
			if rp.Network != net {
				t.Errorf("mode=%v: Network=%v, want %v", mode, rp.Network, net)
			}
			assertMaskedContainsDefaults(t, rp, bwrapHost().Home)
			assertNoRootIsMasked(t, rp)
		}
	}

	// Cache strategy: workspace-write overlays when the host supports it, else
	// session-private; restricted is always session-private; read-only none.
	if got := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: true}, bwrapHost(), main).CacheStrategy; got != CacheOverlay {
		t.Errorf("workspace-write cache = %v, want overlay when host supports it", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: true}, bwrapHostNoOverlay(), main).CacheStrategy; got != CacheSessionPrivate {
		t.Errorf("workspace-write cache (no overlay host) = %v, want session-private", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: true}, bwrapHost(), main).CacheStrategy; got != CacheSessionPrivate {
		t.Errorf("restricted cache = %v, want session-private always", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: true}, bwrapHost(), main).CacheStrategy; got != CacheNone {
		t.Errorf("read-only cache = %v, want none", got)
	}
}

// TestResolveReadWriteScopesPerMode pins the per-mode, per-layer grants that M2
// (file tools) and M3 (kernel) each satisfy.
func TestResolveReadWriteScopesPerMode(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)

	ro := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: true}, bwrapHost(), main)
	if ro.FileTool.Read != ReadAnywhere || len(ro.FileTool.WriteRoots) != 0 {
		t.Errorf("read-only file tool: read=%v writeRoots=%v, want anywhere/none", ro.FileTool.Read, ro.FileTool.WriteRoots)
	}
	if !ro.SessionTmp {
		t.Error("read-only must still provision a session tmp for scratch")
	}

	ww := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: true}, bwrapHost(), main)
	if ww.FileTool.Read != ReadAnywhere {
		t.Errorf("workspace-write file read = %v, want anywhere", ww.FileTool.Read)
	}
	if !slices.Contains(ww.FileTool.WriteRoots, main) {
		t.Errorf("workspace-write file writeRoots %v must include worktree %q", ww.FileTool.WriteRoots, main)
	}

	rs := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: true}, bwrapHost(), main)
	if rs.FileTool.Read != ReadWorktreeOnly {
		t.Errorf("restricted file read = %v, want worktree-only", rs.FileTool.Read)
	}
	if !slices.Contains(rs.FileTool.ReadRoots, main) {
		t.Errorf("restricted file readRoots %v must include worktree", rs.FileTool.ReadRoots)
	}
	// Spawned procs read system roots that file tools do not.
	if rs.Spawned.Read != ReadWorktreeOnly {
		t.Errorf("restricted spawned read = %v, want roots-only", rs.Spawned.Read)
	}
	if !slices.Contains(rs.Spawned.ReadRoots, "/usr") {
		t.Errorf("restricted spawned readRoots %v must include system read roots like /usr", rs.Spawned.ReadRoots)
	}
	if slices.Contains(rs.FileTool.ReadRoots, "/usr") {
		t.Error("restricted FILE-TOOL reads must NOT include system roots (only the kernel layer does)")
	}
}

// TestResolveRestrictedLinkedReadLayerSplit locks the finding-#3 fix: in a linked
// worktree, the common .git read grant is a SPAWNED need (git must read common
// config) and must live in the spawned layer ONLY — the file tools stay
// worktree-only, or the model could browse the whole main repo's .git via a file
// tool.
func TestResolveRestrictedLinkedReadLayerSplit(t *testing.T) {
	t.Parallel()
	linked := linkedWorktreeRepo(t)
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: true}, bwrapHost(), linked)

	layout := rp.Git
	if len(layout.ReadGrantPaths) == 0 {
		t.Fatal("expected a linked worktree to carry a common-.git read grant")
	}
	for _, grant := range layout.ReadGrantPaths {
		if slices.Contains(rp.FileTool.ReadRoots, grant) {
			t.Errorf("common-.git read grant %q leaked into FileTool.ReadRoots %v (file tools must stay worktree-only)", grant, rp.FileTool.ReadRoots)
		}
		if !slices.Contains(rp.Spawned.ReadRoots, grant) {
			t.Errorf("common-.git read grant %q missing from Spawned.ReadRoots %v", grant, rp.Spawned.ReadRoots)
		}
	}
}

// TestResolveLandlockFloor is the finding-#2 disposition: Landlock is allowlist-
// only and cannot subtract the in-worktree .git pointer inside an allowlisted
// root, so it can no longer serve ANY sandboxed mode — not even the restricted +
// net=on + linked-worktree cell it used to. Every sandboxed request on a
// Landlock-only host refuses naming bwrap; such hosts get only --sandbox off.
func TestResolveLandlockFloor(t *testing.T) {
	t.Parallel()
	linked := linkedWorktreeRepo(t)
	main := mainRepo(t)

	// The cell Landlock used to serve now refuses: the reason must cite the
	// in-worktree .git pointer that allowlist-only Landlock cannot protect.
	ref := mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: true}, landlockHost(), linked, "bwrap")
	if !strings.Contains(ref.Reason, ".git pointer") {
		t.Errorf("restricted+linked refusal reason should cite the in-worktree .git pointer, got: %s", ref.Reason)
	}

	// restricted on a MAIN checkout needs subtraction → refuse naming bwrap.
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: true}, landlockHost(), main, "bwrap")
	// restricted + net=off (Landlock can't isolate UDP/DNS) → refuse.
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: false}, landlockHost(), linked, "bwrap")
	// subtractive modes → refuse.
	mustRefuse(t, SandboxPolicy{Mode: ModeReadOnly, Network: true}, landlockHost(), linked, "bwrap")
	mustRefuse(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: true}, landlockHost(), linked, "bwrap")
}

// TestResolveNeitherAndWindowsRefuseAllSandboxed: with no backend, every
// sandboxed mode refuses (off already covered).
func TestResolveNeitherAndWindowsRefuseAllSandboxed(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	for _, host := range []HostFacts{bareLinuxHost(), windowsHost()} {
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: true}, host, dir, "")
		}
	}
}

// TestResolveDarwinSeatbeltServesAllModes: darwin+sandbox-exec is full-contract
// (Seatbelt is deny-capable), cache always session-private (no overlay on macOS).
func TestResolveDarwinSeatbeltServesAllModes(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)
	for _, net := range []bool{true, false} {
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: net}, darwinSeatbeltHost(), main)
			if rp.Backend != BackendSeatbelt {
				t.Errorf("darwin mode=%v net=%v: Backend=%v, want seatbelt", mode, net, rp.Backend)
			}
			if mode == ModeWorkspaceWrite && rp.CacheStrategy != CacheSessionPrivate {
				t.Errorf("darwin workspace-write cache = %v, want session-private (no overlay on macOS)", rp.CacheStrategy)
			}
		}
	}
	// darwin without sandbox-exec refuses sandboxed modes naming sandbox-exec.
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: true}, darwinBareHost(), main, "sandbox-exec")
}

func assertMaskedContainsDefaults(t *testing.T, rp ResolvedPolicy, home string) {
	t.Helper()
	for _, want := range []string{"/proc", "/sys", filepath.Join(home, ".ssh"), filepath.Join(home, ".aws")} {
		if !slices.Contains(rp.MaskedPaths, want) {
			t.Errorf("MaskedPaths missing %q: %v", want, rp.MaskedPaths)
		}
	}
}

func assertNoRootIsMasked(t *testing.T, rp ResolvedPolicy) {
	t.Helper()
	roots := slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots)
	for _, r := range roots {
		for _, m := range rp.MaskedPaths {
			if r == m || pathUnder(r, m) {
				t.Errorf("granted root %q is at/under masked path %q", r, m)
			}
		}
	}
}

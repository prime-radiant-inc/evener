package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const testHome = "/home/tester"

// netPtr returns a *bool for the tri-state SandboxPolicy.Network field (nil =
// unset default; a non-nil value = explicit choice).
func netPtr(b bool) *bool { return &b }

// netStr renders a tri-state network value for test diagnostics.
func netStr(n *bool) string {
	if n == nil {
		return "default(on)"
	}
	return strconv.FormatBool(*n)
}

func bwrapHost() HostFacts {
	return HostFacts{OS: "linux", Home: testHome, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
}
func bwrapHostNoOverlay() HostFacts {
	return HostFacts{OS: "linux", Home: testHome, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false}
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
		t.Fatalf("Resolve(%v, net=%v) unexpectedly refused: %v", pol.Mode, netStr(pol.Network), err)
	}
	return rp
}

func mustRefuse(t *testing.T, pol SandboxPolicy, host HostFacts, cwd, wantBackend string) *RefusalError {
	t.Helper()
	_, err := Resolve(pol, host, cwd)
	if err == nil {
		t.Fatalf("Resolve(%v, net=%v) should have refused on host %s", pol.Mode, netStr(pol.Network), host.OS)
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
	for _, host := range []HostFacts{bwrapHost(), bareLinuxHost(), windowsHost(), darwinBareHost()} {
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
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: netPtr(net)}, bwrapHost(), main)
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
	if got := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: netPtr(true)}, bwrapHost(), main).CacheStrategy; got != CacheOverlay {
		t.Errorf("workspace-write cache = %v, want overlay when host supports it", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: netPtr(true)}, bwrapHostNoOverlay(), main).CacheStrategy; got != CacheSessionPrivate {
		t.Errorf("workspace-write cache (no overlay host) = %v, want session-private", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true)}, bwrapHost(), main).CacheStrategy; got != CacheSessionPrivate {
		t.Errorf("restricted cache = %v, want session-private always", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: netPtr(true)}, bwrapHost(), main).CacheStrategy; got != CacheNone {
		t.Errorf("read-only cache = %v, want none", got)
	}
}

// TestResolveReadWriteScopesPerMode pins the per-mode, per-layer grants that M2
// (file tools) and M3 (kernel) each satisfy.
func TestResolveReadWriteScopesPerMode(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)

	ro := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: netPtr(true)}, bwrapHost(), main)
	if ro.FileTool.Read != ReadAnywhere || len(ro.FileTool.WriteRoots) != 0 {
		t.Errorf("read-only file tool: read=%v writeRoots=%v, want anywhere/none", ro.FileTool.Read, ro.FileTool.WriteRoots)
	}
	if !ro.SessionTmp {
		t.Error("read-only must still provision a session tmp for scratch")
	}

	ww := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: netPtr(true)}, bwrapHost(), main)
	if ww.FileTool.Read != ReadAnywhere {
		t.Errorf("workspace-write file read = %v, want anywhere", ww.FileTool.Read)
	}
	if !slices.Contains(ww.FileTool.WriteRoots, main) {
		t.Errorf("workspace-write file writeRoots %v must include worktree %q", ww.FileTool.WriteRoots, main)
	}

	rs := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true)}, bwrapHost(), main)
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
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true)}, bwrapHost(), linked)

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

// TestResolveNonBwrapLinuxRefusesAllModes: bwrap is the only Linux backend, so a
// Linux host that is not bwrap-capable can enforce no sandboxed mode — every mode,
// with net on or off and in any git layout, refuses with no backend that would
// satisfy it (RequiredBackend ""). Such hosts get only --sandbox off.
func TestResolveNonBwrapLinuxRefusesAllModes(t *testing.T) {
	t.Parallel()
	linked := linkedWorktreeRepo(t)
	main := mainRepo(t)

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		for _, net := range []bool{true, false} {
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: netPtr(net)}, bareLinuxHost(), linked, "")
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: netPtr(net)}, bareLinuxHost(), main, "")
		}
	}
}

// TestResolveNeitherAndWindowsRefuseAllSandboxed: with no backend, every
// sandboxed mode refuses (off already covered).
func TestResolveNeitherAndWindowsRefuseAllSandboxed(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	for _, host := range []HostFacts{bareLinuxHost(), windowsHost()} {
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: netPtr(true)}, host, dir, "")
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
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: netPtr(net)}, darwinSeatbeltHost(), main)
			if rp.Backend != BackendSeatbelt {
				t.Errorf("darwin mode=%v net=%v: Backend=%v, want seatbelt", mode, net, rp.Backend)
			}
			if mode == ModeWorkspaceWrite && rp.CacheStrategy != CacheSessionPrivate {
				t.Errorf("darwin workspace-write cache = %v, want session-private (no overlay on macOS)", rp.CacheStrategy)
			}
		}
	}
	// darwin without sandbox-exec refuses sandboxed modes naming sandbox-exec.
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true)}, darwinBareHost(), main, "sandbox-exec")
}

// TestResolveAbsolutizesRelativeCwd is part of the finding-#4 fix: a relative cwd
// must be absolutized so the resolved grant roots are absolute. ResolvedPolicy
// documents absolute roots, and the enforcement layers compare absolute paths — a
// relative root would silently never match.
func TestResolveAbsolutizesRelativeCwd(t *testing.T) {
	t.Parallel()
	abs := clean(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil {
		t.Skipf("cannot compute a relative path to the temp dir: %v", err)
	}
	if filepath.IsAbs(rel) {
		t.Skipf("relative path %q is unexpectedly absolute", rel)
	}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true)}, bwrapHost(), rel)
	if !filepath.IsAbs(rp.Git.WorktreeRoot) {
		t.Errorf("relative cwd produced a relative worktree root %q", rp.Git.WorktreeRoot)
	}
	for _, r := range slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots) {
		if !filepath.IsAbs(r) {
			t.Errorf("relative cwd produced a relative grant root %q", r)
		}
	}
}

// TestResolveRefusesRelativeExtraRoots is part of the finding-#4 fix: extra roots
// are folded verbatim into the grants, so a relative entry would emit a relative
// grant root. Resolve refuses fail-closed rather than resolve a leaky policy.
func TestResolveRefusesRelativeExtraRoots(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true), ExtraReadRoots: []string{"rel/read/root"}}, bwrapHost(), main, "")
	mustRefuse(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: netPtr(true), ExtraWritableRoots: []string{"rel/write/root"}}, bwrapHost(), main, "")
	// An absolute extra root still resolves — the refusal is specific to relative entries.
	mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true), ExtraReadRoots: []string{"/opt/extra"}}, bwrapHost(), main)
}

// TestResolveSkipsWhitespaceOnlyExtraRoots pins the whitespace-only extra-root
// fix: a "   " entry passes the absolute-path check (it trims to empty and is
// skipped there) but must NOT flow into the resolved grants as a relative root —
// filepath.Clean does not trim whitespace, so an un-skipped "   " would become a
// relative grant root the enforcement layers never match. It is treated as
// empty/skipped, not emitted, and never triggers a refusal.
func TestResolveSkipsWhitespaceOnlyExtraRoots(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)
	rp := mustResolve(t, SandboxPolicy{
		Mode: ModeRestricted, Network: netPtr(true),
		ExtraReadRoots:     []string{"   "},
		ExtraWritableRoots: []string{"\t"},
	}, bwrapHost(), main)
	for _, r := range slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots) {
		if strings.TrimSpace(r) == "" {
			t.Errorf("whitespace-only extra root leaked into grants as %q", r)
		}
		if !filepath.IsAbs(r) {
			t.Errorf("whitespace-only extra root produced a non-absolute grant root %q", r)
		}
	}
}

// TestResolveNetworkDefaultsOnWhenUnset is the finding-#5 regression: the network
// decision is documented as defaulting ON when sandboxed, but a bool zero value
// silently meant OFF, so SandboxPolicy{Mode: ModeRestricted} disabled network.
// An unset (nil) value must resolve to on; an explicit off/on is honored.
func TestResolveNetworkDefaultsOnWhenUnset(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)

	// Unset → on (the documented default when sandboxed).
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted}, bwrapHost(), main); !rp.Network {
		t.Errorf("unset network must resolve to on when sandboxed, got %v", rp.Network)
	}
	// Explicit off is still expressible.
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(false)}, bwrapHost(), main); rp.Network {
		t.Errorf("explicit network=off must resolve to off, got %v", rp.Network)
	}
	// Explicit on.
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: netPtr(true)}, bwrapHost(), main); !rp.Network {
		t.Errorf("explicit network=on must resolve to on, got %v", rp.Network)
	}
}

// TestResolveRefusesUnresolvableConfigInclude is the git-config include fix's
// fail-closed arm: an include path that cannot be resolved to a concrete file
// (a glob) yet could land inside a writable root must refuse the whole session,
// not silently leave a potential writable-root include unprotected.
func TestResolveRefusesUnresolvableConfigInclude(t *testing.T) {
	t.Parallel()
	root := mainRepo(t)
	// A glob include path relative to .git → resolves under the worktree, cannot
	// be enumerated. Resolve must fail closed with a *RefusalError.
	appendFile(t, filepath.Join(root, ".git", "config"), "\n[include]\n\tpath = ../*.cfg\n")
	mustRefuse(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: netPtr(true)}, bwrapHost(), root, "")
}

// TestResolveConfigIncludeOutsideWorktreeResolves: an include of a file OUTSIDE
// any writable root (an absolute path under /etc) is a legitimate read git
// performs and must not refuse or over-protect — only writable-root includes are
// the persistence vector.
func TestResolveConfigIncludeOutsideWorktreeResolves(t *testing.T) {
	t.Parallel()
	root := mainRepo(t)
	appendFile(t, filepath.Join(root, ".git", "config"), "\n[include]\n\tpath = /etc/nonexistent-gitconfig\n")
	rp := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: netPtr(true)}, bwrapHost(), root)
	if slices.Contains(rp.Git.ProtectedPaths, "/etc/nonexistent-gitconfig") {
		t.Errorf("an include outside every writable root should not be added to ProtectedPaths: %v", rp.Git.ProtectedPaths)
	}
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

// TestCacheRootsCoverDefaultGoModCache verifies the overlay strategy's coverage
// claim for GOMODCACHE: cacheRootsFor grants a FIXED $HOME/go/pkg (it does not
// read the actual GOPATH), so overlay coverage of GOMODCACHE holds only when
// GOPATH is at its default ($HOME/go, so GOMODCACHE defaults to
// $HOME/go/pkg/mod). This pins that the default case is covered; a custom
// GOPATH under the overlay strategy is a known, unaddressed residual (Linux-only
// — this host's Seatbelt backend always uses CacheSessionPrivate, which
// isRedirectedCacheVar now covers unconditionally, so the residual has no
// exposure on macOS or in restricted mode on any host).
func TestCacheRootsCoverDefaultGoModCache(t *testing.T) {
	home := "/home/tester"
	defaultGoModCache := filepath.Join(home, "go", "pkg", "mod")
	roots := cacheRootsFor(ModeWorkspaceWrite, home)
	if !isUnderAnyRoot(defaultGoModCache, roots) {
		t.Errorf("default-GOPATH GOMODCACHE %q must fall under a cache root, got roots %v", defaultGoModCache, roots)
	}

	customGoModCache := "/custom/gopath/pkg/mod"
	if isUnderAnyRoot(customGoModCache, roots) {
		t.Errorf("custom-GOPATH GOMODCACHE %q unexpectedly fell under a cache root %v — the known residual has been closed elsewhere; update this test's comment", customGoModCache, roots)
	}
}

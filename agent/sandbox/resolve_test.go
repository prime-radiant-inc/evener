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

// netStr renders a tri-state network value for test diagnostics.
//
//go:fix inline
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
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(net)}, bwrapHost(), main)
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
	if got := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, bwrapHost(), main).CacheStrategy; got != CacheOverlay {
		t.Errorf("workspace-write cache = %v, want overlay when host supports it", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, bwrapHostNoOverlay(), main).CacheStrategy; got != CacheSessionPrivate {
		t.Errorf("workspace-write cache (no overlay host) = %v, want session-private", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, bwrapHost(), main).CacheStrategy; got != CacheSessionPrivate {
		t.Errorf("restricted cache = %v, want session-private always", got)
	}
	if got := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, bwrapHost(), main).CacheStrategy; got != CacheNone {
		t.Errorf("read-only cache = %v, want none", got)
	}
}

// TestResolveReadWriteScopesPerMode pins the per-mode, per-layer grants that M2
// (file tools) and M3 (kernel) each satisfy.
func TestResolveReadWriteScopesPerMode(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)

	ro := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, bwrapHost(), main)
	if ro.FileTool.Read != ReadAnywhere || len(ro.FileTool.WriteRoots) != 0 {
		t.Errorf("read-only file tool: read=%v writeRoots=%v, want anywhere/none", ro.FileTool.Read, ro.FileTool.WriteRoots)
	}
	if !ro.SessionTmp {
		t.Error("read-only must still provision a session tmp for scratch")
	}

	ww := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, bwrapHost(), main)
	if ww.FileTool.Read != ReadAnywhere {
		t.Errorf("workspace-write file read = %v, want anywhere", ww.FileTool.Read)
	}
	if !slices.Contains(ww.FileTool.WriteRoots, main) {
		t.Errorf("workspace-write file writeRoots %v must include worktree %q", ww.FileTool.WriteRoots, main)
	}

	rs := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, bwrapHost(), main)
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
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, bwrapHost(), linked)

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
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: new(net)}, bareLinuxHost(), linked, "")
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: new(net)}, bareLinuxHost(), main, "")
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
			mustRefuse(t, SandboxPolicy{Mode: mode, Network: new(true)}, host, dir, "")
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
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(net)}, darwinSeatbeltHost(), main)
			if rp.Backend != BackendSeatbelt {
				t.Errorf("darwin mode=%v net=%v: Backend=%v, want seatbelt", mode, net, rp.Backend)
			}
			if mode == ModeWorkspaceWrite && rp.CacheStrategy != CacheSessionPrivate {
				t.Errorf("darwin workspace-write cache = %v, want session-private (no overlay on macOS)", rp.CacheStrategy)
			}
		}
	}
	// darwin without sandbox-exec refuses sandboxed modes naming sandbox-exec.
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, darwinBareHost(), main, "sandbox-exec")
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
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, bwrapHost(), rel)
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
	mustRefuse(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true), ExtraReadRoots: []string{"rel/read/root"}}, bwrapHost(), main, "")
	mustRefuse(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{"rel/write/root"}}, bwrapHost(), main, "")
	// An absolute extra root still resolves — the refusal is specific to relative entries.
	mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true), ExtraReadRoots: []string{"/opt/extra"}}, bwrapHost(), main)
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
		Mode: ModeRestricted, Network: new(true),
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
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(false)}, bwrapHost(), main); rp.Network {
		t.Errorf("explicit network=off must resolve to off, got %v", rp.Network)
	}
	// Explicit on.
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, bwrapHost(), main); !rp.Network {
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
	mustRefuse(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, bwrapHost(), root, "")
}

// TestResolveConfigIncludeOutsideWorktreeResolves: an include of a file OUTSIDE
// any writable root (an absolute path under /etc) is a legitimate read git
// performs and must not refuse or over-protect — only writable-root includes are
// the persistence vector.
func TestResolveConfigIncludeOutsideWorktreeResolves(t *testing.T) {
	t.Parallel()
	root := mainRepo(t)
	appendFile(t, filepath.Join(root, ".git", "config"), "\n[include]\n\tpath = /etc/nonexistent-gitconfig\n")
	rp := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, bwrapHost(), root)
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

// --- session infrastructure (hook + MCP-server) roots ----------------------

// TestInfraReadRootsReachTheSpawnedLayerOnly pins the shape of the 2026-08-06
// ruling's grant: the session's hook/MCP paths join the SPAWNED read surface
// (which is what execs a hook script), and nothing else. They never reach the
// file-tool layer — the model must not gain a browse grant over the plugin cache
// just because a hook lives there — and never a write root in any mode.
func TestInfraReadRootsReachTheSpawnedLayerOnly(t *testing.T) {
	root := mainRepo(t)
	infra := clean(t.TempDir())
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		t.Run(mode.String(), func(t *testing.T) {
			pol := SandboxPolicy{Mode: mode, Network: new(true), InfraReadRoots: []string{infra}}
			rp := mustResolve(t, pol, bwrapHost(), root)

			if slices.Contains(rp.FileTool.ReadRoots, infra) {
				t.Errorf("a hook/MCP path must not become a FILE-TOOL read root: %v", rp.FileTool.ReadRoots)
			}
			for _, w := range slices.Concat(rp.FileTool.WriteRoots, rp.Spawned.WriteRoots) {
				if w == infra {
					t.Errorf("a hook/MCP path must never become a write root: %v / %v", rp.FileTool.WriteRoots, rp.Spawned.WriteRoots)
				}
			}
			// The spawned layer must be able to reach it: either it reads
			// anywhere already, or the root is granted explicitly.
			if rp.Spawned.Read != ReadAnywhere && !slices.Contains(rp.Spawned.ReadRoots, infra) {
				t.Errorf("a hook/MCP path must be readable by spawned processes, got roots %v", rp.Spawned.ReadRoots)
			}
		})
	}
}

// TestInfraReadRootsLeaveTheWriteSurfaceByteIdentical is the write-surface proof
// for the ruling: adding the session's hook/MCP paths changes NOTHING about what
// a session may write, in any mode or layout.
func TestInfraReadRootsLeaveTheWriteSurfaceByteIdentical(t *testing.T) {
	infra := clean(t.TempDir())
	for _, layout := range []func(*testing.T) string{mainRepo, linkedWorktreeRepo} {
		root := layout(t)
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			base := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(true)}, bwrapHost(), root)
			with := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(true), InfraReadRoots: []string{infra}}, bwrapHost(), root)
			if !slices.Equal(base.FileTool.WriteRoots, with.FileTool.WriteRoots) {
				t.Errorf("%v: file-tool write roots changed: %v -> %v", mode, base.FileTool.WriteRoots, with.FileTool.WriteRoots)
			}
			if !slices.Equal(base.Spawned.WriteRoots, with.Spawned.WriteRoots) {
				t.Errorf("%v: spawned write roots changed: %v -> %v", mode, base.Spawned.WriteRoots, with.Spawned.WriteRoots)
			}
			if !slices.Equal(base.Git.ProtectedPaths, with.Git.ProtectedPaths) {
				t.Errorf("%v: git protected paths changed: %v -> %v", mode, base.Git.ProtectedPaths, with.Git.ProtectedPaths)
			}
		}
	}
}

// TestInfraReadRootsNeverUnmaskADenylistedPath pins the floor: a hook or MCP
// server configured to live at (or under) a denylisted path does not get that
// path back. The credential denylist is authoritative over the infrastructure
// grant, not the other way round.
func TestInfraReadRootsNeverUnmaskADenylistedPath(t *testing.T) {
	root := mainRepo(t)
	host := bwrapHost()
	for _, masked := range []string{
		filepath.Join(host.Home, ".ssh"),           // a default credential dir
		filepath.Join(host.Home, ".ssh", "nested"), // beneath one
		"/proc", // the non-removable pseudo-fs floor
		"/proc/self",
	} {
		pol := SandboxPolicy{Mode: ModeRestricted, Network: new(true), InfraReadRoots: []string{masked}}
		rp := mustResolve(t, pol, host, root)
		if slices.Contains(rp.Spawned.ReadRoots, masked) {
			t.Errorf("denylisted path %q was granted as a hook/MCP root: %v", masked, rp.Spawned.ReadRoots)
		}
	}
}

// TestInfraReadRootsMustBeAbsolute: a relative infrastructure root would emit a
// relative grant the absolute-path enforcement layers never match — a silent
// grant loss that would make hooks fail exactly as before. Refuse instead.
func TestInfraReadRootsMustBeAbsolute(t *testing.T) {
	root := mainRepo(t)
	pol := SandboxPolicy{Mode: ModeRestricted, Network: new(true), InfraReadRoots: []string{"relative/hooks"}}
	ref := mustRefuse(t, pol, bwrapHost(), root, "")
	if !strings.Contains(ref.Reason, "relative/hooks") {
		t.Errorf("refusal must name the offending root, got: %s", ref.Reason)
	}
}

// TestResolveWriteBlockedOffConfinesFileTools pins the degraded read-only
// delegate scope: on a host with no sandbox backend the derived read-only box
// resolves as write-blocked OFF instead of refusing, so the delegate keeps its
// capability while the in-process file tools carry the whole write boundary.
// Enforced() stays false — there is no OS sandbox — while FileToolConfined() is
// true, the separate question the file-tool layer asks.
//
// The grants must be the ones the file tools will actually enforce, not a
// hand-built shell of them: the same credential/pseudo-fs denylist every
// sandboxed mode masks, the worktree as a read anchor, and no write root at all.
func TestResolveWriteBlockedOffConfinesFileTools(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)
	for _, host := range []HostFacts{bareLinuxHost(), darwinBareHost(), bwrapHost()} {
		rp := mustResolve(t, SandboxPolicy{Mode: ModeOff, WriteBlocked: true}, host, main)
		if rp.Mode != ModeOff || rp.Backend != BackendNone {
			t.Errorf("write-blocked off on %s: Mode=%v Backend=%v, want off/none", host.OS, rp.Mode, rp.Backend)
		}
		if rp.Enforced() {
			t.Errorf("write-blocked off on %s reports an OS sandbox in force", host.OS)
		}
		if !rp.FileToolConfined() || !rp.WriteBlocked {
			t.Errorf("write-blocked off on %s lost its file-tool confinement: confined=%v blocked=%v", host.OS, rp.FileToolConfined(), rp.WriteBlocked)
		}
		if len(rp.FileTool.WriteRoots) != 0 || len(rp.Spawned.WriteRoots) != 0 {
			t.Errorf("write-blocked off on %s granted a write root: file=%v spawned=%v", host.OS, rp.FileTool.WriteRoots, rp.Spawned.WriteRoots)
		}
		if !rp.Network {
			t.Errorf("write-blocked off on %s denied egress; off applies no network confinement", host.OS)
		}
		// The denylist is the whole point of "reads anywhere MINUS the masked set".
		// Without it the file tools would read ~/.ssh and /proc/<evener-pid>/environ,
		// which every other confining mode denies.
		assertMaskedContainsDefaults(t, rp, host.Home)
		assertNoRootIsMasked(t, rp)
		// The worktree is the read ANCHOR (see openRead): without it a workspace
		// reached through a symlinked ancestor is unreadable.
		if !slices.Contains(rp.FileTool.ReadRoots, main) {
			t.Errorf("write-blocked off on %s: FileTool.ReadRoots=%v, want the worktree %q as a read anchor", host.OS, rp.FileTool.ReadRoots, main)
		}
		if rp.Git.WorktreeRoot != main {
			t.Errorf("write-blocked off on %s did not resolve its git layout: WorktreeRoot=%q, want %q", host.OS, rp.Git.WorktreeRoot, main)
		}
	}
}

// TestResolveWriteBlockedOffNeedsAnAbsoluteHome: the write-blocked box leans
// entirely on the file-tool denylist, which is home-relative. A non-absolute home
// would join to relative entries the enforcement layer never matches — a silent
// unmask of every credential directory — so it refuses, exactly as every
// sandboxed mode does. Plain off is unaffected: it masks nothing and confines
// nothing.
func TestResolveWriteBlockedOffNeedsAnAbsoluteHome(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	homeless := HostFacts{OS: "linux", Home: ""}
	ref := mustRefuse(t, SandboxPolicy{Mode: ModeOff, WriteBlocked: true}, homeless, dir, "")
	if !strings.Contains(ref.Reason, "credential denylist") {
		t.Errorf("refusal must name the unmask it prevents, got: %s", ref.Reason)
	}
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeOff}, homeless, dir); rp.FileToolConfined() {
		t.Error("plain off must still resolve on a host with no home; it confines nothing")
	}
}

// TestResolveWriteBlockedOffRefusesWhereFileToolsCannotEnforce: the degraded box
// has no backend behind it, so the in-process file tools ARE the enforcement. On a
// platform whose file-tool primitives do not exist, every operation would fail
// closed instead — a delegate with broken file tools rather than a confined one.
// Refuse at the resolver so no caller can produce that env, whatever it asks for.
func TestResolveWriteBlockedOffRefusesWhereFileToolsCannotEnforce(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	ref := mustRefuse(t, SandboxPolicy{Mode: ModeOff, WriteBlocked: true}, windowsHost(), dir, "")
	if !strings.Contains(ref.Reason, "file tools") || !strings.Contains(ref.Reason, windowsHost().OS) {
		t.Errorf("refusal must say the file tools cannot enforce on this host, got: %s", ref.Reason)
	}
	// Plain off still resolves everywhere, including Windows: it is today's
	// behavior with no containment and no enforcement to be missing.
	if rp := mustResolve(t, SandboxPolicy{Mode: ModeOff}, windowsHost(), dir); rp.FileToolConfined() {
		t.Error("plain off on windows must stay unconfined, not refuse")
	}
}

// TestFileToolConfinedSeparatesFromEnforced: the two predicates answer different
// questions. Every enforced mode confines the file tools, a plain off policy
// confines nothing (today's os path, byte-identical), and only the write-blocked
// off policy separates them.
func TestFileToolConfinedSeparatesFromEnforced(t *testing.T) {
	t.Parallel()
	main := mainRepo(t)
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(true)}, bwrapHost(), main)
		if !rp.Enforced() || !rp.FileToolConfined() {
			t.Errorf("mode %v: Enforced=%v FileToolConfined=%v, want both true", mode, rp.Enforced(), rp.FileToolConfined())
		}
	}
	plain := mustResolve(t, SandboxPolicy{Mode: ModeOff}, bareLinuxHost(), main)
	if plain.Enforced() || plain.FileToolConfined() {
		t.Errorf("plain off: Enforced=%v FileToolConfined=%v, want both false", plain.Enforced(), plain.FileToolConfined())
	}

	// A third question: whether the file-tool layer can enforce ANYTHING on this
	// host. Its race-safe primitives exist on linux and darwin only, and the
	// stand-ins elsewhere fail closed on every operation — so a policy that leans
	// on file-tool enforcement alone must never be derived off those two.
	for _, host := range []HostFacts{bwrapHost(), bareLinuxHost(), darwinSeatbeltHost(), darwinBareHost()} {
		if !FileToolEnforceable(host) {
			t.Errorf("FileToolEnforceable(%s) = false, want true", host.OS)
		}
	}
	if FileToolEnforceable(windowsHost()) {
		t.Error("FileToolEnforceable(windows) = true, but every file-tool operation there fails closed")
	}
}

// TestWriteBlockedOffGrantsSessionScratch: WriteBlocked promises the separately
// provisioned session scratch stays writable (SandboxPolicy.WriteBlocked's
// contract), so the late-bound scratch grant must reach a degraded policy — the
// question WithSessionScratch asks is file-tool confinement, not OS enforcement.
func TestWriteBlockedOffGrantsSessionScratch(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	scratch := clean(t.TempDir())
	rp := mustResolve(t, SandboxPolicy{Mode: ModeOff, WriteBlocked: true}, bareLinuxHost(), dir)
	granted := rp.WithSessionScratch(scratch)
	if !slices.Contains(granted.FileTool.WriteRoots, scratch) {
		t.Fatalf("degraded policy must keep its session scratch writable: %v", granted.FileTool.WriteRoots)
	}
	if slices.Contains(granted.FileTool.WriteRoots, dir) {
		t.Fatalf("the scratch grant must not widen to the workspace: %v", granted.FileTool.WriteRoots)
	}
	// A plain off policy is untouched: its file tools never build an enforcement
	// layer, so a scratch grant would be a meaningless (and misleading) root.
	if plain := mustResolve(t, SandboxPolicy{Mode: ModeOff}, bareLinuxHost(), dir).WithSessionScratch(scratch); len(plain.FileTool.WriteRoots) != 0 {
		t.Fatalf("plain off gained a file-tool write root: %v", plain.FileTool.WriteRoots)
	}
}

package sandboxtest

import (
	"errors"
	"path/filepath"
	"slices"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/identifier"
)

// TestingT is the subset of *testing.T the contract harness needs, allowing
// callers like recordingT to intercept failures when verifying oracle rejections.
type TestingT interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	TempDir() string
}

// ResolveFunc is the resolver signature the contract runs against — sandbox.Resolve
// and wrapped resolver checks in backend parity tests.
type ResolveFunc func(sandbox.SandboxPolicy, sandbox.HostFacts, string) (sandbox.ResolvedPolicy, error)

// ContractCase is one cell of the fail-closed floor matrix: a (mode, network,
// host, workspace-kind) request paired with its EXPECTED resolution — either the
// scalar enforcement decisions or a typed refusal. The expected values are
// copied from the spec's floor matrix, not from the resolver, so a wrong resolver
// is caught. Backend tests import ContractCases() to hold their backends to the
// same grants, denials, and refusals.
type ContractCase struct {
	Name      string
	Mode      sandbox.Mode
	Net       bool
	Host      sandbox.HostFacts
	Workspace sandbox.WorkspaceKind // Main / Linked / NonGit; the harness materializes a real repo of this kind

	// WantRefusal is the fail-closed floor outcome.
	WantRefusal         bool
	WantRequiredBackend string // when WantRefusal: the backend that WOULD satisfy ("bwrap"/"sandbox-exec"/"")

	// The remaining fields are the expected resolution when !WantRefusal.
	WantBackend   sandbox.Backend
	WantNetwork   bool
	WantCache     sandbox.CacheStrategy
	WantFileRead  sandbox.ReadScope
	WantSpawnRead sandbox.ReadScope
	// WantWorktreeWrite is whether the worktree is a writable root in BOTH layers
	// (the read-only vs workspace-write/restricted distinction — "read-only cannot
	// commit"). Independent of the resolver: it encodes the spec's mode semantics.
	WantWorktreeWrite bool
}

// ContractCases returns the golden floor-matrix table: every (mode × host tier ×
// net) cell of the spec's "full contract or refuse" floor, plus the darwin/Seatbelt
// tier. It is data-only (no resolver call) so downstream backends can import the
// cases directly.
func ContractCases() []ContractCase {
	const home = "/fixture-home/contract"
	bwrap := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	bwrapNoOverlay := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false}
	bare := sandbox.HostFacts{OS: "linux", Home: home}
	windows := sandbox.HostFacts{OS: "windows", Home: `C:\Users\contract`}
	seatbelt := sandbox.HostFacts{OS: "darwin", Home: "/Users/contract", SandboxExecPath: "/usr/bin/sandbox-exec"}
	darwinBare := sandbox.HostFacts{OS: "darwin", Home: "/Users/contract"}

	var cases []ContractCase

	// --- off: resolves on every host, no backend, no containment. ---
	for _, h := range []sandbox.HostFacts{bwrap, bare, windows, seatbelt, darwinBare} {
		cases = append(cases, ContractCase{
			Name: "off/" + h.OS + backendTag(h), Mode: sandbox.ModeOff, Net: true, Host: h, Workspace: sandbox.MainCheckout,
			WantBackend: sandbox.BackendNone, WantNetwork: true, WantCache: sandbox.CacheNone,
			WantFileRead: sandbox.ReadAnywhere, WantSpawnRead: sandbox.ReadAnywhere,
		})
	}

	// --- bwrap tier: every mode, net on/off, all run. ---
	for _, net := range []bool{true, false} {
		cases = append(cases,
			modeCase("read-only", sandbox.ModeReadOnly, net, bwrap, sandbox.MainCheckout, sandbox.BackendBwrap, sandbox.CacheNone, sandbox.ReadAnywhere, sandbox.ReadAnywhere, false),
			modeCase("workspace-write", sandbox.ModeWorkspaceWrite, net, bwrap, sandbox.MainCheckout, sandbox.BackendBwrap, sandbox.CacheOverlay, sandbox.ReadAnywhere, sandbox.ReadAnywhere, true),
			modeCase("restricted", sandbox.ModeRestricted, net, bwrap, sandbox.MainCheckout, sandbox.BackendBwrap, sandbox.CacheSessionPrivate, sandbox.ReadWorktreeOnly, sandbox.ReadWorktreeOnly, true),
		)
	}
	cases = append(cases,
		// workspace-write on a bwrap host WITHOUT overlay support → session-private cache.
		modeCase("workspace-write-no-overlay", sandbox.ModeWorkspaceWrite, true, bwrapNoOverlay, sandbox.MainCheckout, sandbox.BackendBwrap, sandbox.CacheSessionPrivate, sandbox.ReadAnywhere, sandbox.ReadAnywhere, true),
	)

	// --- refusal tiers: every sandboxed mode × net cell refuses, so no refusal
	// cell (esp. net=off) can be silently dropped and let a backend resolve it. ---

	// non-bwrap linux + windows: no usable backend at all → every sandboxed cell
	// refuses with no backend that would satisfy it. The Linux tier exercises both
	// git shapes: the full mode × net product on a main checkout, plus a linked
	// worktree cell so both layouts are covered.
	cases = append(cases, refuseTier("no-backend/linux", bare, sandbox.MainCheckout, "")...)
	cases = append(cases, refuseCase("no-backend/linux/restricted-linked", sandbox.ModeRestricted, true, bare, sandbox.LinkedWorktree, ""))
	cases = append(cases, refuseTier("no-backend/windows", windows, sandbox.MainCheckout, "")...)

	// --- darwin + Seatbelt: deny-capable, full matrix; cache always session-private. ---
	for _, net := range []bool{true, false} {
		cases = append(cases,
			modeCase("seatbelt/read-only", sandbox.ModeReadOnly, net, seatbelt, sandbox.MainCheckout, sandbox.BackendSeatbelt, sandbox.CacheNone, sandbox.ReadAnywhere, sandbox.ReadAnywhere, false),
			modeCase("seatbelt/workspace-write", sandbox.ModeWorkspaceWrite, net, seatbelt, sandbox.MainCheckout, sandbox.BackendSeatbelt, sandbox.CacheSessionPrivate, sandbox.ReadAnywhere, sandbox.ReadAnywhere, true),
			modeCase("seatbelt/restricted", sandbox.ModeRestricted, net, seatbelt, sandbox.MainCheckout, sandbox.BackendSeatbelt, sandbox.CacheSessionPrivate, sandbox.ReadWorktreeOnly, sandbox.ReadWorktreeOnly, true),
		)
	}
	// darwin without sandbox-exec: every cell refuses naming sandbox-exec.
	cases = append(cases, refuseTier("darwin-bare", darwinBare, sandbox.MainCheckout, "sandbox-exec")...)

	return cases
}

// refuseTier generates the full sandboxed mode × net refusal product for a host
// tier that cannot enforce any sandboxed mode, so every refusal cell is asserted.
func refuseTier(namePrefix string, host sandbox.HostFacts, ws sandbox.WorkspaceKind, requiredBackend string) []ContractCase {
	var out []ContractCase
	for _, m := range []sandbox.Mode{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite, sandbox.ModeRestricted} {
		for _, net := range []bool{true, false} {
			out = append(out, refuseCase(namePrefix+"/"+m.String(), m, net, host, ws, requiredBackend))
		}
	}
	return out
}

func backendTag(h sandbox.HostFacts) string {
	switch {
	case h.BwrapCapable:
		return "-bwrap"
	case h.SeatbeltAvailable():
		return "-seatbelt"
	default:
		return "-none"
	}
}

func modeCase(name string, mode sandbox.Mode, net bool, host sandbox.HostFacts, ws sandbox.WorkspaceKind, backend sandbox.Backend, cache sandbox.CacheStrategy, fileRead, spawnRead sandbox.ReadScope, worktreeWrite bool) ContractCase {
	return ContractCase{
		Name: name + netTag(net), Mode: mode, Net: net, Host: host, Workspace: ws,
		WantBackend: backend, WantNetwork: net, WantCache: cache, WantFileRead: fileRead, WantSpawnRead: spawnRead,
		WantWorktreeWrite: worktreeWrite,
	}
}

func refuseCase(name string, mode sandbox.Mode, net bool, host sandbox.HostFacts, ws sandbox.WorkspaceKind, requiredBackend string) ContractCase {
	return ContractCase{
		Name: name + netTag(net), Mode: mode, Net: net, Host: host, Workspace: ws,
		WantRefusal: true, WantRequiredBackend: requiredBackend,
	}
}

func netTag(net bool) string {
	if net {
		return "/net-on"
	}
	return "/net-off"
}

// AssertResolve runs the golden ContractCases() table against resolve, materializing
// a real git workspace of each case's kind. It asserts the exact refusal-or-resolution
// per the spec floor matrix AND the universal containment invariants (an enforced
// policy masks the pseudo-fs floor; no granted root is at/under a masked path).
func AssertResolve(t TestingT, resolve ResolveFunc) {
	assertResolveWith(t, resolve, MaterializeWorkspace)
}

// contractWorkspaceMaterializer is the external filesystem/Git boundary the
// resolver contract needs. Production tests use MaterializeWorkspace; an offline
// caller can provide the same structural .git layouts directly under a temp root.
// Keeping this boundary explicit lets the contract assertions exercise the real
// resolver without requiring an ambient Git binary.
type contractWorkspaceMaterializer func(TestingT, sandbox.WorkspaceKind) string

func assertResolveWith(t TestingT, resolve ResolveFunc, materialize contractWorkspaceMaterializer) {
	t.Helper()
	for _, tc := range ContractCases() {
		cwd := materialize(t, tc.Workspace)
		host := tc.Host
		net := tc.Net
		rp, err := resolve(sandbox.SandboxPolicy{Mode: tc.Mode, Network: &net}, host, cwd)

		if tc.WantRefusal {
			assertRefusal(t, tc, err)
			continue
		}
		if err != nil {
			t.Errorf("case %s: unexpected refusal: %v", tc.Name, err)
			continue
		}
		assertResolution(t, tc, rp, cwd, host.Home)
	}
}

func assertRefusal(t TestingT, tc ContractCase, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("case %s: expected a refusal, got a resolved policy", tc.Name)
		return
	}
	var ref *sandbox.RefusalError
	if !errors.As(err, &ref) {
		t.Errorf("case %s: refusal is not *RefusalError: %T", tc.Name, err)
		return
	}
	if ref.RequiredBackend != tc.WantRequiredBackend {
		t.Errorf("case %s: RequiredBackend = %q, want %q (reason: %s)", tc.Name, ref.RequiredBackend, tc.WantRequiredBackend, ref.Reason)
	}
}

func assertResolution(t TestingT, tc ContractCase, rp sandbox.ResolvedPolicy, cwd, home string) {
	t.Helper()
	if rp.Backend != tc.WantBackend {
		t.Errorf("case %s: Backend = %v, want %v", tc.Name, rp.Backend, tc.WantBackend)
	}
	if rp.Network != tc.WantNetwork {
		t.Errorf("case %s: Network = %v, want %v", tc.Name, rp.Network, tc.WantNetwork)
	}
	if rp.CacheStrategy != tc.WantCache {
		t.Errorf("case %s: CacheStrategy = %v, want %v", tc.Name, rp.CacheStrategy, tc.WantCache)
	}
	if rp.FileTool.Read != tc.WantFileRead {
		t.Errorf("case %s: FileTool.Read = %v, want %v", tc.Name, rp.FileTool.Read, tc.WantFileRead)
	}
	if rp.Spawned.Read != tc.WantSpawnRead {
		t.Errorf("case %s: Spawned.Read = %v, want %v", tc.Name, rp.Spawned.Read, tc.WantSpawnRead)
	}

	// Universal containment invariants (hold for every enforced cell).
	if !rp.Enforced() {
		return
	}

	// Write scope — the read-only vs writable distinction ("read-only cannot
	// commit"): the worktree is a writable root in BOTH layers iff WantWorktreeWrite.
	// The grant test is ancestor-aware (a root grants a target when it equals OR is
	// an ancestor of it, matching how the enforcement layers apply a root); an
	// exact-match check would let an over-broad ancestor grant — e.g. a read-only
	// resolver returning WriteRoots ["/"] — pass unnoticed.
	if got := rootGrants(rp.FileTool.WriteRoots, cwd); got != tc.WantWorktreeWrite {
		t.Errorf("case %s: FileTool worktree-writable = %v, want %v (roots: %v)", tc.Name, got, tc.WantWorktreeWrite, rp.FileTool.WriteRoots)
	}
	if got := rootGrants(rp.Spawned.WriteRoots, cwd); got != tc.WantWorktreeWrite {
		t.Errorf("case %s: Spawned worktree-writable = %v, want %v (roots: %v)", tc.Name, got, tc.WantWorktreeWrite, rp.Spawned.WriteRoots)
	}
	if !rp.SessionTmp {
		t.Errorf("case %s: enforced policy must provision a session tmp", tc.Name)
	}

	// Denylist floor: an enforced policy masks the pseudo-fs floor AND every
	// credential directory. Without the credential check a backend that masks only
	// /proc but leaves ~/.ssh/~/.aws readable would be certified green.
	wantMasked := []string{"/proc", "/sys", "/dev/fd", "/dev/mem", "/run/user"}
	for _, rel := range [][]string{{".ssh"}, {".aws"}, {".config", "gcloud"}, {".gnupg"}, {".kube"}, {".git-credentials"}} {
		wantMasked = append(wantMasked, filepath.Join(append([]string{home}, rel...)...))
	}
	for _, m := range wantMasked {
		if !slices.Contains(rp.MaskedPaths, m) {
			t.Errorf("case %s: MaskedPaths missing %q: %v", tc.Name, m, rp.MaskedPaths)
		}
	}

	// The two-layer read split for restricted: spawned procs read system roots
	// (to execute); file tools do NOT (the model may only browse the worktree).
	if tc.Mode == sandbox.ModeRestricted {
		if !slices.Contains(rp.Spawned.ReadRoots, "/usr") {
			t.Errorf("case %s: restricted Spawned.ReadRoots must include system roots like /usr: %v", tc.Name, rp.Spawned.ReadRoots)
		}
		if slices.Contains(rp.FileTool.ReadRoots, "/usr") {
			t.Errorf("case %s: restricted FileTool.ReadRoots must NOT include system roots: %v", tc.Name, rp.FileTool.ReadRoots)
		}
		// A restricted file-tool read must not be granted an over-broad ancestor of
		// the worktree (the model browses only the worktree, never "/" or a parent).
		// An exact-root check would miss a resolver returning ReadRoots ["/"].
		for _, r := range rp.FileTool.ReadRoots {
			if r != cwd && identifier.PathUnder(cwd, r) {
				t.Errorf("case %s: restricted FileTool.ReadRoots contains over-broad parent %q of worktree %q: %v", tc.Name, r, cwd, rp.FileTool.ReadRoots)
			}
		}
	}

	roots := slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots)
	for _, r := range roots {
		if rp.Masks(r) {
			t.Errorf("case %s: granted root %q is masked: %v", tc.Name, r, rp.MaskedPaths)
		}
	}
}

// rootGrants reports whether any root equals or is an ancestor of target — the
// access model the enforcement layers apply (a grant on a root covers everything
// beneath it). Used instead of an exact-match check so an over-broad ancestor
// grant (e.g. WriteRoots ["/"]) is detected rather than passing unnoticed.
func rootGrants(roots []string, target string) bool {
	for _, r := range roots {
		if r == target || identifier.PathUnder(target, r) {
			return true
		}
	}
	return false
}

// ReRootCase is one cell of the re-root contract: a (mode, network, host)
// request whose resolved policy is re-rooted from one lane to a sibling lane of
// the same main repo. AssertReRoot materializes the lanes and asserts the
// re-rooted policy re-anchors to the TARGET lane (never the source's), so every
// backend is held to the same "same policy, different worktree" semantics.
// Data-only, like ContractCase.
type ReRootCase struct {
	Name string
	Mode sandbox.Mode
	Net  bool
	Host sandbox.HostFacts
	// WantWorktreeWrite mirrors ContractCase: whether the worktree is a writable
	// root (false only for read-only). Encodes the spec's mode semantics, not the
	// resolver's output.
	WantWorktreeWrite bool
}

// ReRootCases returns the golden re-root table: each enforceable (mode × backend
// tier) cell, so a backend's re-root path is pinned to the same re-anchoring
// semantics. Data-only (no ReRoot call).
func ReRootCases() []ReRootCase {
	const home = "/fixture-home/contract"
	bwrap := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	seatbelt := sandbox.HostFacts{OS: "darwin", Home: "/Users/contract", SandboxExecPath: "/usr/bin/sandbox-exec"}
	var cases []ReRootCase
	for _, h := range []sandbox.HostFacts{bwrap, seatbelt} {
		cases = append(cases,
			ReRootCase{Name: "read-only" + backendTag(h), Mode: sandbox.ModeReadOnly, Net: true, Host: h, WantWorktreeWrite: false},
			ReRootCase{Name: "workspace-write" + backendTag(h), Mode: sandbox.ModeWorkspaceWrite, Net: true, Host: h, WantWorktreeWrite: true},
			ReRootCase{Name: "restricted" + backendTag(h), Mode: sandbox.ModeRestricted, Net: true, Host: h, WantWorktreeWrite: true},
		)
	}
	return cases
}

// AssertReRoot holds ReRoot to its contract: for each case it resolves a policy
// at lane A of a shared main repo and re-roots it to a sibling lane B, asserting
// the re-rooted policy anchors its grants at B and NOT at A. It materializes a
// real main checkout and two linked worktrees (no mocks; skips when git is
// unavailable).
func AssertReRoot(t TestingT, cases []ReRootCase) {
	assertReRootWith(t, cases, materializeReRootLanes)
}

// contractReRootMaterializer is the external Git-layout boundary for the
// re-root contract. The production implementation below shells out to Git;
// offline callers can materialize the equivalent linked-worktree pointers
// directly beneath their own temp root.
type contractReRootMaterializer func(TestingT) (main, laneA, laneB string)

func assertReRootWith(t TestingT, cases []ReRootCase, materialize contractReRootMaterializer) {
	t.Helper()
	main, laneA, laneB := materialize(t)

	for _, tc := range cases {
		host := tc.Host
		if host.Home == "" {
			host.Home = "/fixture-home/contract"
		}
		net := tc.Net
		rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: tc.Mode, Network: &net}, host, laneA)
		if err != nil {
			t.Errorf("case %s: resolve at lane A: %v", tc.Name, err)
			continue
		}
		rerooted, err := rp.ReRoot(laneB)
		if err != nil {
			t.Errorf("case %s: re-root to lane B: %v", tc.Name, err)
			continue
		}
		assertRerooted(t, tc, rerooted, main, laneA, laneB)
	}
}

func assertRerooted(t TestingT, tc ReRootCase, rerooted *sandbox.ResolvedPolicy, main, laneA, laneB string) {
	if rerooted == nil {
		t.Errorf("case %s: re-root returned a nil policy", tc.Name)
		return
	}
	if !rerooted.Enforced() {
		t.Errorf("case %s: re-root must stay enforced, got off", tc.Name)
		return
	}
	if !slices.Contains(rerooted.MaskedPaths, "/proc") {
		t.Errorf("case %s: re-rooted policy must keep the pseudo-fs floor masked: %v", tc.Name, rerooted.MaskedPaths)
	}
	// The shared main repo (parent of neither sibling lane) must never leak as a
	// write root — a check the lane-vs-lane assertions below cannot see, since
	// `main` is not an ancestor of either sibling lane.
	if rootGrants(rerooted.FileTool.WriteRoots, main) || rootGrants(rerooted.Spawned.WriteRoots, main) {
		t.Errorf("case %s: re-root leaked the shared main repo %q as a write root: file=%v spawned=%v", tc.Name, main, rerooted.FileTool.WriteRoots, rerooted.Spawned.WriteRoots)
	}
	if !tc.WantWorktreeWrite {
		// read-only: no worktree write in either lane.
		if rootGrants(rerooted.FileTool.WriteRoots, laneB) || rootGrants(rerooted.Spawned.WriteRoots, laneB) {
			t.Errorf("case %s: read-only re-root granted a write to lane B: file=%v spawned=%v", tc.Name, rerooted.FileTool.WriteRoots, rerooted.Spawned.WriteRoots)
		}
		return
	}
	// The write grant must re-anchor to lane B and must NOT still cover lane A.
	if !rootGrants(rerooted.FileTool.WriteRoots, laneB) {
		t.Errorf("case %s: re-rooted FileTool must grant writes to target lane %q: %v", tc.Name, laneB, rerooted.FileTool.WriteRoots)
	}
	if rootGrants(rerooted.FileTool.WriteRoots, laneA) {
		t.Errorf("case %s: re-rooted FileTool must NOT still grant the source lane %q (leaked roots): %v", tc.Name, laneA, rerooted.FileTool.WriteRoots)
	}
	if !rootGrants(rerooted.Spawned.WriteRoots, laneB) {
		t.Errorf("case %s: re-rooted Spawned must grant writes to target lane %q: %v", tc.Name, laneB, rerooted.Spawned.WriteRoots)
	}
	if rootGrants(rerooted.Spawned.WriteRoots, laneA) {
		t.Errorf("case %s: re-rooted Spawned must NOT still grant the source lane %q (leaked roots): %v", tc.Name, laneA, rerooted.Spawned.WriteRoots)
	}
}

// contractGitRunner is the external git boundary used by the contract
// materializers.
type contractGitRunner func(TestingT, string, ...string)

func defaultGitRunner(t TestingT) contractGitRunner {
	requireGitHarness(t)
	return gitHarness
}

func materializeReRootLanes(t TestingT) (main, laneA, laneB string) {
	t.Helper()
	return materializeReRootLanesWith(t, defaultGitRunner(t))
}

func materializeReRootLanesWith(t TestingT, run contractGitRunner) (main, laneA, laneB string) {
	t.Helper()
	main = identifier.ResolveClean(t.TempDir())
	run(t, main, "init", "-q")
	run(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	laneA = main + "-a"
	laneB = main + "-b"
	run(t, main, "worktree", "add", "-q", laneA)
	run(t, main, "worktree", "add", "-q", laneB)
	laneA = identifier.ResolveClean(laneA)
	laneB = identifier.ResolveClean(laneB)
	return main, laneA, laneB
}

// MaterializeWorkspace creates a real git workspace of the requested kind under a
// temp dir and returns its cwd. It uses the real git binary (no mocks); when git
// is unavailable it skips. NonGit returns a bare temp dir.
func MaterializeWorkspace(t TestingT, kind sandbox.WorkspaceKind) string {
	t.Helper()
	switch kind {
	case sandbox.NonGit:
		return materializeWorkspaceWith(t, kind, nil)
	case sandbox.MainCheckout, sandbox.LinkedWorktree:
		return materializeWorkspaceWith(t, kind, defaultGitRunner(t))
	default:
		t.Fatalf("MaterializeWorkspace: unsupported kind %v", kind)
		return ""
	}
}

func materializeWorkspaceWith(t TestingT, kind sandbox.WorkspaceKind, run contractGitRunner) string {
	t.Helper()
	switch kind {
	case sandbox.NonGit:
		return identifier.ResolveClean(t.TempDir())
	case sandbox.MainCheckout:
		root := identifier.ResolveClean(t.TempDir())
		run(t, root, "init", "-q")
		return root
	case sandbox.LinkedWorktree:
		main := identifier.ResolveClean(t.TempDir())
		run(t, main, "init", "-q")
		run(t, main, "commit", "-q", "--allow-empty", "-m", "init")
		// A sibling path derived from the unique temp `main`, so repeated
		// LinkedWorktree materializations in one run never collide.
		wt := main + "-wt"
		run(t, main, "worktree", "add", "-q", wt)
		return identifier.ResolveClean(wt)
	default:
		t.Fatalf("MaterializeWorkspace: unsupported kind %v", kind)
		return ""
	}
}

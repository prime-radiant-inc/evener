package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
)

// TestingT is the subset of *testing.T the contract harness needs. Depending on
// this interface instead of importing "testing" keeps package sandbox free of a
// testing dependency in the production binaries (cmd/serf) that import it for
// Resolve/ParseMode, while *testing.T satisfies it structurally in test code.
type TestingT interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	TempDir() string
}

// ResolveFunc is the resolver signature the contract runs against — sandbox.Resolve
// in M1, and the same function threaded through each backend's tests in M2/M3/M6.
type ResolveFunc func(SandboxPolicy, HostFacts, string) (ResolvedPolicy, error)

// ContractCase is one cell of the fail-closed floor matrix: a (mode, network,
// host, workspace-kind) request paired with its EXPECTED resolution — either the
// scalar enforcement decisions or a typed refusal. The expected values are
// copied from the spec's floor matrix, not from the resolver, so a wrong resolver
// is caught. M2/M3/M6 import ContractCases() to hold their backends to the same
// grants/denials/refusals this table pins for M1.
type ContractCase struct {
	Name      string
	Mode      Mode
	Net       bool
	Host      HostFacts
	Workspace WorkspaceKind // Main / Linked / NonGit; the harness materializes a real repo of this kind

	// WantRefusal is the fail-closed floor outcome.
	WantRefusal         bool
	WantRequiredBackend string // when WantRefusal: the backend that WOULD satisfy ("bwrap"/"sandbox-exec"/"")

	// The remaining fields are the expected resolution when !WantRefusal.
	WantBackend   Backend
	WantNetwork   bool
	WantCache     CacheStrategy
	WantFileRead  ReadScope
	WantSpawnRead ReadScope
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
	const home = "/home/contract"
	bwrap := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	bwrapNoOverlay := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false}
	bare := HostFacts{OS: "linux", Home: home}
	windows := HostFacts{OS: "windows", Home: `C:\Users\contract`}
	seatbelt := HostFacts{OS: "darwin", Home: "/Users/contract", SandboxExecPath: "/usr/bin/sandbox-exec"}
	darwinBare := HostFacts{OS: "darwin", Home: "/Users/contract"}

	var cases []ContractCase

	// --- off: resolves on every host, no backend, no containment. ---
	for _, h := range []HostFacts{bwrap, bare, windows, seatbelt, darwinBare} {
		cases = append(cases, ContractCase{
			Name: "off/" + h.OS + backendTag(h), Mode: ModeOff, Net: true, Host: h, Workspace: MainCheckout,
			WantBackend: BackendNone, WantNetwork: true, WantCache: CacheNone,
			WantFileRead: ReadAnywhere, WantSpawnRead: ReadAnywhere,
		})
	}

	// --- bwrap tier: every mode, net on/off, all run. ---
	for _, net := range []bool{true, false} {
		cases = append(cases,
			modeCase("read-only", ModeReadOnly, net, bwrap, MainCheckout, BackendBwrap, CacheNone, ReadAnywhere, ReadAnywhere, false),
			modeCase("workspace-write", ModeWorkspaceWrite, net, bwrap, MainCheckout, BackendBwrap, CacheOverlay, ReadAnywhere, ReadAnywhere, true),
			modeCase("restricted", ModeRestricted, net, bwrap, MainCheckout, BackendBwrap, CacheSessionPrivate, ReadWorktreeOnly, ReadWorktreeOnly, true),
		)
	}
	cases = append(cases,
		// workspace-write on a bwrap host WITHOUT overlay support → session-private cache.
		modeCase("workspace-write-no-overlay", ModeWorkspaceWrite, true, bwrapNoOverlay, MainCheckout, BackendBwrap, CacheSessionPrivate, ReadAnywhere, ReadAnywhere, true),
	)

	// --- refusal tiers: every sandboxed mode × net cell refuses, so no refusal
	// cell (esp. net=off) can be silently dropped and let a backend resolve it. ---

	// non-bwrap linux + windows: no usable backend at all → every sandboxed cell
	// refuses with no backend that would satisfy it. The Linux tier exercises both
	// git shapes: the full mode × net product on a main checkout, plus a linked
	// worktree cell so both layouts are covered.
	cases = append(cases, refuseTier("no-backend/linux", bare, MainCheckout, "")...)
	cases = append(cases, refuseCase("no-backend/linux/restricted-linked", ModeRestricted, true, bare, LinkedWorktree, ""))
	cases = append(cases, refuseTier("no-backend/windows", windows, MainCheckout, "")...)

	// --- darwin + Seatbelt: deny-capable, full matrix; cache always session-private. ---
	for _, net := range []bool{true, false} {
		cases = append(cases,
			modeCase("seatbelt/read-only", ModeReadOnly, net, seatbelt, MainCheckout, BackendSeatbelt, CacheNone, ReadAnywhere, ReadAnywhere, false),
			modeCase("seatbelt/workspace-write", ModeWorkspaceWrite, net, seatbelt, MainCheckout, BackendSeatbelt, CacheSessionPrivate, ReadAnywhere, ReadAnywhere, true),
			modeCase("seatbelt/restricted", ModeRestricted, net, seatbelt, MainCheckout, BackendSeatbelt, CacheSessionPrivate, ReadWorktreeOnly, ReadWorktreeOnly, true),
		)
	}
	// darwin without sandbox-exec: every cell refuses naming sandbox-exec.
	cases = append(cases, refuseTier("darwin-bare", darwinBare, MainCheckout, "sandbox-exec")...)

	return cases
}

// refuseTier generates the full sandboxed mode × net refusal product for a host
// tier that cannot enforce any sandboxed mode, so every refusal cell is asserted.
func refuseTier(namePrefix string, host HostFacts, ws WorkspaceKind, requiredBackend string) []ContractCase {
	var out []ContractCase
	for _, m := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		for _, net := range []bool{true, false} {
			out = append(out, refuseCase(namePrefix+"/"+m.String(), m, net, host, ws, requiredBackend))
		}
	}
	return out
}

func backendTag(h HostFacts) string {
	switch {
	case h.BwrapCapable:
		return "-bwrap"
	case h.SeatbeltAvailable():
		return "-seatbelt"
	default:
		return "-none"
	}
}

func modeCase(name string, mode Mode, net bool, host HostFacts, ws WorkspaceKind, backend Backend, cache CacheStrategy, fileRead, spawnRead ReadScope, worktreeWrite bool) ContractCase {
	return ContractCase{
		Name: name + netTag(net), Mode: mode, Net: net, Host: host, Workspace: ws,
		WantBackend: backend, WantNetwork: net, WantCache: cache, WantFileRead: fileRead, WantSpawnRead: spawnRead,
		WantWorktreeWrite: worktreeWrite,
	}
}

func refuseCase(name string, mode Mode, net bool, host HostFacts, ws WorkspaceKind, requiredBackend string) ContractCase {
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
// policy masks the pseudo-fs floor; no granted root is at/under a masked path). M1
// runs it with sandbox.Resolve; M2/M3/M6 reuse ContractCases() to hold their
// backends to the same contract.
func AssertResolve(t TestingT, resolve ResolveFunc) {
	t.Helper()
	for _, tc := range ContractCases() {
		cwd := MaterializeWorkspace(t, tc.Workspace)
		host := tc.Host
		if host.Home == "" {
			host.Home = "/home/contract"
		}
		net := tc.Net
		rp, err := resolve(SandboxPolicy{Mode: tc.Mode, Network: &net}, host, cwd)

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
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Errorf("case %s: refusal is not *RefusalError: %T", tc.Name, err)
		return
	}
	if ref.RequiredBackend != tc.WantRequiredBackend {
		t.Errorf("case %s: RequiredBackend = %q, want %q (reason: %s)", tc.Name, ref.RequiredBackend, tc.WantRequiredBackend, ref.Reason)
	}
}

func assertResolution(t TestingT, tc ContractCase, rp ResolvedPolicy, cwd, home string) {
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
	if tc.Mode == ModeRestricted {
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
			if r != cwd && pathUnder(cwd, r) {
				t.Errorf("case %s: restricted FileTool.ReadRoots contains over-broad parent %q of worktree %q: %v", tc.Name, r, cwd, rp.FileTool.ReadRoots)
			}
		}
	}

	roots := slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots)
	for _, r := range roots {
		for _, m := range rp.MaskedPaths {
			if r == m || pathUnder(r, m) {
				t.Errorf("case %s: granted root %q is at/under masked path %q", tc.Name, r, m)
			}
		}
	}
}

// rootGrants reports whether any root equals or is an ancestor of target — the
// access model the enforcement layers apply (a grant on a root covers everything
// beneath it). Used instead of an exact-match check so an over-broad ancestor
// grant (e.g. WriteRoots ["/"]) is detected rather than passing unnoticed.
func rootGrants(roots []string, target string) bool {
	for _, r := range roots {
		if r == target || pathUnder(target, r) {
			return true
		}
	}
	return false
}

// ReRootCase is one cell of the re-root contract: a (mode, network, host)
// request whose resolved policy is re-rooted from one lane to a sibling lane of
// the same main repo. AssertReRoot materializes the lanes and asserts the
// re-rooted policy re-anchors to the TARGET lane (never the source's), so every
// backend (bwrap now, Seatbelt in M6) is held to the same "same policy, different
// worktree" semantics. Data-only, like ContractCase.
type ReRootCase struct {
	Name string
	Mode Mode
	Net  bool
	Host HostFacts
	// WantWorktreeWrite mirrors ContractCase: whether the worktree is a writable
	// root (false only for read-only). Encodes the spec's mode semantics, not the
	// resolver's output.
	WantWorktreeWrite bool
}

// ReRootCases returns the golden re-root table: each enforceable (mode × backend
// tier) cell, so a backend's re-root path is pinned to the same re-anchoring
// semantics M4 established. Data-only (no ReRoot call).
func ReRootCases() []ReRootCase {
	const home = "/home/contract"
	bwrap := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	seatbelt := HostFacts{OS: "darwin", Home: "/Users/contract", SandboxExecPath: "/usr/bin/sandbox-exec"}
	var cases []ReRootCase
	for _, h := range []HostFacts{bwrap, seatbelt} {
		cases = append(cases,
			ReRootCase{Name: "read-only" + backendTag(h), Mode: ModeReadOnly, Net: true, Host: h, WantWorktreeWrite: false},
			ReRootCase{Name: "workspace-write" + backendTag(h), Mode: ModeWorkspaceWrite, Net: true, Host: h, WantWorktreeWrite: true},
			ReRootCase{Name: "restricted" + backendTag(h), Mode: ModeRestricted, Net: true, Host: h, WantWorktreeWrite: true},
		)
	}
	return cases
}

// AssertReRoot holds ReRoot to its contract: for each case it resolves a policy
// at lane A of a shared main repo and re-roots it to a sibling lane B, asserting
// the re-rooted policy anchors its grants at B and NOT at A (the containment the
// whole milestone rests on). It materializes a real main checkout and two linked
// worktrees (no mocks; skips when git is unavailable). M6 reuses it so Seatbelt
// satisfies the same re-root semantics.
func AssertReRoot(t TestingT, cases []ReRootCase) {
	t.Helper()
	requireGitHarness(t)
	main := resolveCleanPath(t.TempDir())
	gitHarness(t, main, "init", "-q")
	gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	laneA := main + "-a"
	laneB := main + "-b"
	gitHarness(t, main, "worktree", "add", "-q", laneA)
	gitHarness(t, main, "worktree", "add", "-q", laneB)
	laneA = resolveCleanPath(laneA)
	laneB = resolveCleanPath(laneB)

	for _, tc := range cases {
		host := tc.Host
		if host.Home == "" {
			host.Home = "/home/contract"
		}
		net := tc.Net
		rp, err := Resolve(SandboxPolicy{Mode: tc.Mode, Network: &net}, host, laneA)
		if err != nil {
			t.Errorf("case %s: resolve at lane A: %v", tc.Name, err)
			continue
		}
		rerooted, err := rp.ReRoot(laneB)
		if err != nil {
			t.Errorf("case %s: re-root to lane B: %v", tc.Name, err)
			continue
		}
		if rerooted == nil {
			t.Errorf("case %s: re-root returned a nil policy", tc.Name)
			continue
		}
		if !rerooted.Enforced() {
			t.Errorf("case %s: re-root must stay enforced, got off", tc.Name)
			continue
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
			continue
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
}

// MaterializeWorkspace creates a real git workspace of the requested kind under a
// temp dir and returns its cwd. It uses the real git binary (no mocks); when git
// is unavailable it skips. NonGit returns a bare temp dir.
func MaterializeWorkspace(t TestingT, kind WorkspaceKind) string {
	t.Helper()
	switch kind {
	case NonGit:
		return resolveCleanPath(t.TempDir())
	case MainCheckout:
		requireGitHarness(t)
		root := resolveCleanPath(t.TempDir())
		gitHarness(t, root, "init", "-q")
		return root
	case LinkedWorktree:
		requireGitHarness(t)
		main := resolveCleanPath(t.TempDir())
		gitHarness(t, main, "init", "-q")
		gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")
		// A sibling path derived from the unique temp `main`, so repeated
		// LinkedWorktree materializations in one run never collide.
		wt := main + "-wt"
		gitHarness(t, main, "worktree", "add", "-q", wt)
		return resolveCleanPath(wt)
	default:
		t.Fatalf("MaterializeWorkspace: unsupported kind %v", kind)
		return ""
	}
}

func requireGitHarness(t TestingT) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available")
	}
}

func gitHarness(t TestingT, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

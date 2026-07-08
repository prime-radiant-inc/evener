package sandbox

import (
	"slices"
	"testing"
)

// TestContractMatrix runs the golden floor-matrix table against the real
// resolver. This is the exact suite M2/M3/M6 re-run against their backends, so a
// green here is the cross-backend contract for M1.
func TestContractMatrix(t *testing.T) {
	t.Parallel()
	AssertResolve(t, Resolve)
}

// TestContractCoverage guards the golden table's breadth: every backend outcome,
// both net values, all four modes, the darwin/Seatbelt tier, and — for the
// primary bwrap tier — the FULL (mode × net) product, so a future edit can't
// silently drop a bwrap cell. (Category checks alone would miss a single dropped
// cell while another cell keeps that backend/mode/OS represented; the explicit
// bwrap-matrix check below closes that gap for the tier M2/M3 lean on hardest.)
func TestContractCoverage(t *testing.T) {
	t.Parallel()
	cases := ContractCases()

	// The bwrap tier must carry every sandboxed mode × net cell (a per-cell drop,
	// not just a whole-tier drop, must fail).
	type cell struct {
		mode Mode
		net  bool
	}
	bwrapCells := map[cell]bool{}
	for _, c := range cases {
		if c.Host.BwrapCapable && !c.WantRefusal && c.Mode != ModeOff {
			bwrapCells[cell{c.Mode, c.Net}] = true
		}
	}
	for _, m := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		for _, net := range []bool{true, false} {
			if !bwrapCells[cell{m, net}] {
				t.Errorf("bwrap tier missing cell (mode=%v net=%v)", m, net)
			}
		}
	}

	// Every backend outcome is represented among the resolving cells.
	backends := map[Backend]bool{}
	oses := map[string]bool{}
	modes := map[Mode]bool{}
	var refusals, resolutions int
	sawNetOff := false
	sawNoBackendRefusal, sawRequiredSeatbelt := false, false

	for _, c := range cases {
		oses[c.Host.OS] = true
		modes[c.Mode] = true
		if !c.Net {
			sawNetOff = true
		}
		if c.WantRefusal {
			refusals++
			switch c.WantRequiredBackend {
			case "":
				sawNoBackendRefusal = true
			case "sandbox-exec":
				sawRequiredSeatbelt = true
			}
			continue
		}
		resolutions++
		backends[c.WantBackend] = true
	}

	// The Linux tier has exactly one backend (bwrap); a non-bwrap Linux host never
	// resolves, it refuses with no backend that would satisfy it.
	for _, b := range []Backend{BackendNone, BackendBwrap, BackendSeatbelt} {
		if !backends[b] {
			t.Errorf("golden table has no resolving case with backend %v", b)
		}
	}
	for _, m := range AllModes() {
		if !modes[m] {
			t.Errorf("golden table missing mode %v", m)
		}
	}
	for _, os := range []string{"linux", "darwin", "windows"} {
		if !oses[os] {
			t.Errorf("golden table missing OS tier %q", os)
		}
	}
	if !sawNetOff {
		t.Error("golden table never exercises net=off")
	}
	if refusals == 0 || resolutions == 0 {
		t.Errorf("golden table lacks refusals (%d) or resolutions (%d)", refusals, resolutions)
	}
	if !sawNoBackendRefusal {
		t.Error("golden table has no refusal with no backend that would satisfy it (RequiredBackend \"\")")
	}
	if !sawRequiredSeatbelt {
		t.Error("golden table has no refusal that names sandbox-exec as the required backend")
	}
}

// TestContractRefusalTiersAreComplete guards that every refusal tier asserts the
// FULL sandboxed mode × net product, not just net=on for one mode. A dropped
// refusal cell (e.g. no net=off, or only restricted for darwin-bare) would let a
// backend that silently resolves that (mode, net) escape the contract.
func TestContractRefusalTiersAreComplete(t *testing.T) {
	t.Parallel()
	type cell struct {
		mode Mode
		net  bool
	}
	tiers := map[string]map[cell]bool{}
	for _, c := range ContractCases() {
		if !c.WantRefusal {
			continue
		}
		tier := refusalTierLabel(c.Host)
		if tiers[tier] == nil {
			tiers[tier] = map[cell]bool{}
		}
		tiers[tier][cell{c.Mode, c.Net}] = true
	}

	for _, tier := range []string{"bare-linux", "windows", "darwin-bare"} {
		cells := tiers[tier]
		if cells == nil {
			t.Errorf("no refusal cases for tier %q", tier)
			continue
		}
		for _, m := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			for _, net := range []bool{true, false} {
				if !cells[cell{m, net}] {
					t.Errorf("refusal tier %q missing cell (mode=%v net=%v)", tier, m, net)
				}
			}
		}
	}
}

// refusalTierLabel classifies a HostFacts into the refusal tier it belongs to
// (the tiers that cannot enforce any sandboxed mode).
func refusalTierLabel(h HostFacts) string {
	switch {
	case h.OS == "windows":
		return "windows"
	case h.OS == "darwin" && !h.SeatbeltAvailable():
		return "darwin-bare"
	case h.OS == "linux" && !h.BwrapCapable:
		return "bare-linux"
	default:
		return "enforcing/" + h.OS
	}
}

// recordingT is a sandbox.TestingT that records Errorf/Fatalf calls instead of
// failing, so a test can assert that AssertResolve REJECTS a bad resolver. It
// borrows a real *testing.T for TempDir/Helper and delegates Skipf (git-missing
// skips the whole test).
type recordingT struct {
	*testing.T
	errors int
}

func (r *recordingT) Errorf(string, ...any) { r.errors++ }
func (r *recordingT) Fatalf(string, ...any) { r.errors++ }

// TestAssertResolveRejectsOverBroadGrants proves the strengthened oracle: a
// resolver that grants an over-broad ancestor ("/" as a write root, or as a
// restricted file-tool read root) must FAIL AssertResolve. Exact-root checks let
// such grants slip through; ancestor-aware checks catch them.
func TestAssertResolveRejectsOverBroadGrants(t *testing.T) {
	t.Parallel()
	overBroad := func(p SandboxPolicy, h HostFacts, cwd string) (ResolvedPolicy, error) {
		rp, err := Resolve(p, h, cwd)
		if err != nil {
			return rp, err
		}
		if rp.Enforced() {
			// "/" is an ancestor of every worktree yet equals no worktree, so an
			// exact-match oracle would never flag it.
			rp.FileTool.WriteRoots = append(rp.FileTool.WriteRoots, "/")
			rp.FileTool.ReadRoots = append(rp.FileTool.ReadRoots, "/")
			rp.Spawned.WriteRoots = append(rp.Spawned.WriteRoots, "/")
		}
		return rp, nil
	}
	rec := &recordingT{T: t}
	AssertResolve(rec, overBroad)
	if rec.errors == 0 {
		t.Fatal("AssertResolve accepted an over-broad-granting resolver; the oracle must reject over-broad ancestor grants")
	}

	// Sanity: the real resolver still passes the strengthened oracle.
	if rec2 := (&recordingT{T: t}); func() int { AssertResolve(rec2, Resolve); return rec2.errors }() != 0 {
		t.Fatalf("real Resolve failed the strengthened oracle with %d errors", rec2.errors)
	}
}

// TestContractCasesAreDataOnly proves ContractCases() is a pure data function
// (safe for backends to import and iterate): it returns an equal table on every
// call and never shares mutable backing state between calls.
func TestContractCasesAreDataOnly(t *testing.T) {
	t.Parallel()
	a := ContractCases()
	b := ContractCases()
	if len(a) != len(b) {
		t.Fatalf("ContractCases() length varies: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("case %d differs between calls:\n a: %+v\n b: %+v", i, a[i], b[i])
		}
	}
	// Names are unique — a duplicate name would mask a dropped/duplicated cell.
	names := make([]string, len(a))
	for i, c := range a {
		names[i] = c.Name
	}
	slices.Sort(names)
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Errorf("duplicate contract case name %q", names[i])
		}
	}
}

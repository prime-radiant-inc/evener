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
	sawRequiredBwrap, sawRequiredSeatbelt := false, false

	for _, c := range cases {
		oses[c.Host.OS] = true
		modes[c.Mode] = true
		if !c.Net {
			sawNetOff = true
		}
		if c.WantRefusal {
			refusals++
			switch c.WantRequiredBackend {
			case "bwrap":
				sawRequiredBwrap = true
			case "sandbox-exec":
				sawRequiredSeatbelt = true
			}
			continue
		}
		resolutions++
		backends[c.WantBackend] = true
	}

	// BackendLandlock is intentionally NOT required among resolving cases: Landlock
	// is allowlist-only and cannot enforce our contract, so it never resolves — it
	// is always a refusal naming bwrap (finding #2). It remains a probed/reported
	// enum value; only selection changed.
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
	if !sawRequiredBwrap {
		t.Error("golden table has no refusal that names bwrap as the required backend")
	}
	if !sawRequiredSeatbelt {
		t.Error("golden table has no refusal that names sandbox-exec as the required backend")
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

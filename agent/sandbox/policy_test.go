package sandbox

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestModeRoundTrip proves every Mode round-trips through its wire name and
// that an unknown name is a typed error (not a silent default). The mode name
// is what the --sandbox flag carries and what a session persists, so a drift
// between String() and ParseMode would corrupt a resumed session's policy.
func TestModeRoundTrip(t *testing.T) {
	t.Parallel()
	for _, m := range AllModes() {
		name := m.String()
		if name == "" {
			t.Fatalf("Mode(%d).String() is empty", int(m))
		}
		got, err := ParseMode(name)
		if err != nil {
			t.Fatalf("ParseMode(%q): unexpected error %v", name, err)
		}
		if got != m {
			t.Fatalf("ParseMode(%q) = %v, want %v", name, got, m)
		}
	}

	// Off must be the zero value so a nil/absent policy is exactly today's
	// behavior (the whole "off is a no-op" guarantee rides on this).
	if ModeOff != 0 {
		t.Fatalf("ModeOff must be the zero value, got %d", int(ModeOff))
	}

	// The four spec mode names, spelled exactly as the --sandbox flag accepts.
	wantNames := map[Mode]string{
		ModeOff:            "off",
		ModeReadOnly:       "read-only",
		ModeWorkspaceWrite: "workspace-write",
		ModeRestricted:     "restricted",
	}
	for m, want := range wantNames {
		if got := m.String(); got != want {
			t.Errorf("Mode %d name = %q, want %q", int(m), got, want)
		}
	}

	if _, err := ParseMode("bogus"); err == nil {
		t.Error("ParseMode(\"bogus\") should error on an unknown name")
	}
	if _, err := ParseMode(""); err == nil {
		t.Error("ParseMode(\"\") should error on an empty name")
	}
	// Case/whitespace are normalized so "--sandbox Read-Only" and stray spaces
	// still resolve rather than silently falling through to a refusal path.
	if got, err := ParseMode("  Read-Only "); err != nil || got != ModeReadOnly {
		t.Errorf("ParseMode(\"  Read-Only \") = %v, %v; want ModeReadOnly, nil", got, err)
	}
}

// TestAtLeastAsConfiningMatrix pins the full mode-confinement lattice used by the
// per-delegate sandbox no-escalation floor: child.AtLeastAsConfining(parent) is
// true iff the child is no looser than the parent on BOTH the read and write axes.
// Modes are NOT a total order — read-only and restricted are incomparable and must
// be refused BOTH directions — so the whole 4x4 matrix is pinned cell-by-cell. A
// single wrong cell is a containment hole (a delegate less confined than its
// parent) or a false refusal.
func TestAtLeastAsConfiningMatrix(t *testing.T) {
	t.Parallel()
	// rows = child, cols = parent; want[child][parent] per the spec's matrix.
	want := map[Mode]map[Mode]bool{
		ModeOff: {
			ModeOff: true, ModeReadOnly: false, ModeWorkspaceWrite: false, ModeRestricted: false,
		},
		ModeReadOnly: {
			ModeOff: true, ModeReadOnly: true, ModeWorkspaceWrite: true, ModeRestricted: false,
		},
		ModeWorkspaceWrite: {
			ModeOff: true, ModeReadOnly: false, ModeWorkspaceWrite: true, ModeRestricted: false,
		},
		ModeRestricted: {
			ModeOff: true, ModeReadOnly: false, ModeWorkspaceWrite: true, ModeRestricted: true,
		},
	}
	for _, child := range AllModes() {
		for _, parent := range AllModes() {
			if got := child.AtLeastAsConfining(parent); got != want[child][parent] {
				t.Errorf("%s.AtLeastAsConfining(%s) = %v, want %v", child, parent, got, want[child][parent])
			}
		}
	}

	// The incomparable pair is refused BOTH directions — the property the plan
	// calls out explicitly, restated so a regression names it.
	if ModeReadOnly.AtLeastAsConfining(ModeRestricted) {
		t.Error("read-only must NOT be at least as confining as restricted (incomparable: read-only reads outside the worktree)")
	}
	if ModeRestricted.AtLeastAsConfining(ModeReadOnly) {
		t.Error("restricted must NOT be at least as confining as read-only (incomparable: restricted writes the worktree)")
	}

	// Every mode is at least as confining as itself (reflexive), and every mode is
	// allowed under an off parent (off is loosest on both axes).
	for _, m := range AllModes() {
		if !m.AtLeastAsConfining(m) {
			t.Errorf("%s must be at least as confining as itself", m)
		}
		if !m.AtLeastAsConfining(ModeOff) {
			t.Errorf("%s must be allowed under an off parent", m)
		}
	}
}

// TestDefaultDenylistIncludesPseudoFS pins the spec's secrets+pseudo-fs denylist
// exactly: the pseudo-filesystem masks (so read_file("/proc/<serf-pid>/environ")
// can't read serf's own API key) and every credential directory. A dropped entry
// here is a silent hole in the containment floor.
func TestDefaultDenylistIncludesPseudoFS(t *testing.T) {
	t.Parallel()
	home := "/home/tester"
	got := DefaultDenylist(home)

	wantPseudoFS := []string{
		"/proc", "/sys", "/dev/fd", "/dev/mem", "/run/user",
		"/run/docker.sock", "/var/run/docker.sock", "/run/podman/podman.sock",
		"/run/containerd/containerd.sock", "/run/dbus/system_bus_socket",
	}
	for _, p := range wantPseudoFS {
		if !slices.Contains(got, p) {
			t.Errorf("default denylist missing pseudo-fs path %q; got %v", p, got)
		}
	}

	wantSecrets := []string{
		".ssh", ".aws", ".config/gcloud", ".netrc", ".config/serf",
		".gnupg", ".docker/config.json", ".kube", ".git-credentials",
	}
	for _, rel := range wantSecrets {
		abs := filepath.Join(home, rel)
		if !slices.Contains(got, abs) {
			t.Errorf("default denylist missing credential path %q; got %v", abs, got)
		}
	}

	// Every entry is absolute after resolution — the enforcement layers compare
	// absolute paths, so a relative leak would never match and silently allow.
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Errorf("default denylist entry %q is not absolute", p)
		}
	}

	// Exact-set assertion: the default denylist is precisely the pseudo-fs set
	// plus the home-resolved credential set — no more, no less. Inclusion checks
	// alone would miss an accidental extra/duplicated entry; the spec says the
	// set matches "exactly", so pin cardinality and membership.
	want := append([]string{}, wantPseudoFS...)
	for _, rel := range wantSecrets {
		want = append(want, filepath.Join(home, rel))
	}
	slices.Sort(want)
	gotSorted := append([]string{}, got...)
	slices.Sort(gotSorted)
	if !slices.Equal(gotSorted, want) {
		t.Errorf("default denylist is not the exact spec set:\n got: %v\nwant: %v", gotSorted, want)
	}
}

// TestDefaultDenylistIsCopy guards value-immutability: DefaultDenylist must hand
// back a fresh slice each call so a caller mutating the result can never poison
// the process-wide default set (a mid-session denylist relaxation would be an
// escape).
func TestDefaultDenylistIsCopy(t *testing.T) {
	t.Parallel()
	home := "/home/tester"
	a := DefaultDenylist(home)
	if len(a) == 0 {
		t.Fatal("default denylist is empty")
	}
	a[0] = "/tmp/poisoned"
	b := DefaultDenylist(home)
	if b[0] == "/tmp/poisoned" {
		t.Fatal("DefaultDenylist returned an aliased slice; mutation leaked into the default set")
	}
}

// TestPolicyDenylistAddRemove proves the user extension knobs: an add extends
// the masked set, a remove punches a hole in the default set, and the resolution
// is a pure function of the (immutable) SandboxPolicy value — there is no mutator
// the model could reach mid-session.
func TestPolicyDenylistAddRemove(t *testing.T) {
	t.Parallel()
	home := "/home/tester"

	pol := SandboxPolicy{
		Mode:        ModeRestricted,
		DenylistAdd: []string{"/opt/secret-vault", ".myapp/creds"},
		// Punch a hole in a removable credential entry, and ATTEMPT to remove a
		// pseudo-fs floor entry (which must be ignored — see below).
		DenylistRemove: []string{".config/serf", "/proc"},
	}

	eff := pol.EffectiveDenylist(home)

	// Added absolute + added home-relative both present.
	if !slices.Contains(eff, "/opt/secret-vault") {
		t.Errorf("user-added absolute path missing from effective denylist: %v", eff)
	}
	if !slices.Contains(eff, filepath.Join(home, ".myapp", "creds")) {
		t.Errorf("user-added home-relative path missing from effective denylist: %v", eff)
	}
	// Removed entry is gone even though it is in the default set.
	removed := filepath.Join(home, ".config", "serf")
	if slices.Contains(eff, removed) {
		t.Errorf("user-removed path %q still present in effective denylist: %v", removed, eff)
	}
	// The pseudo-fs floor is NON-REMOVABLE: even an explicit DenylistRemove of
	// /proc must be ignored (masking /proc guards serf's own API key in
	// /proc/<pid>/environ — a user must not be able to punch that hole open).
	if !slices.Contains(eff, "/proc") {
		t.Errorf("DenylistRemove punched the non-removable /proc floor: %v", eff)
	}
	for _, floor := range []string{"/sys", "/dev/fd", "/dev/mem", "/run/user"} {
		if !slices.Contains(eff, floor) {
			t.Errorf("effective denylist dropped pseudo-fs floor entry %q: %v", floor, eff)
		}
	}

	// Purity: resolving twice yields equal results and does not mutate the policy.
	eff2 := pol.EffectiveDenylist(home)
	if !slices.Equal(eff, eff2) {
		t.Errorf("EffectiveDenylist is not deterministic:\n first: %v\nsecond: %v", eff, eff2)
	}
	if len(pol.DenylistAdd) != 2 || len(pol.DenylistRemove) != 2 {
		t.Errorf("EffectiveDenylist mutated the policy value: %+v", pol)
	}
}

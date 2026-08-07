package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPermitRefusesTopLevelSystemDirs covers the reported multi-tenant escape
// shapes directly: a root at "/Users", "/home", "/private", "/var", "/Volumes",
// or "/tmp" is refused even with NO shared anchors configured, because each is
// fewer than two path components deep — the depth floor alone is enough to stop
// them.
func TestPermitRefusesTopLevelSystemDirs(t *testing.T) {
	guard := NewRootGuard()
	for _, p := range []string{"/", "/Users", "/home", "/private", "/var", "/Volumes", "/tmp"} {
		if got := guard.Permit(p); got != "" {
			t.Errorf("Permit(%q) = %q, want refused (fewer than two path components)", p, got)
		}
	}
}

// TestPermitDepthFloorOffByOne pins the exact boundary: one component is
// refused, two is permitted. An off-by-one here (< vs <=) would either let
// "/Users" through or wrongly refuse "/Users/x".
func TestPermitDepthFloorOffByOne(t *testing.T) {
	// Anchor on something unrelated so these results are due to depth alone.
	guard := NewRootGuard(filepath.Join(t.TempDir(), "unrelated-anchor"))

	cases := []struct {
		path string
		want string
	}{
		{"/", ""},
		{"/Users", ""},
		{"/Users/x", "/Users/x"},
		{"/opt/x", "/opt/x"},
	}
	for _, c := range cases {
		if got := guard.Permit(c.path); got != c.want {
			t.Errorf("Permit(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestPermitRefusesAnchorsAndAncestors covers the core multi-tenant rule: a
// root at or above the home, worktree, or temp anchor is refused, whatever its
// exact depth.
func TestPermitRefusesAnchorsAndAncestors(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "userhome")
	worktree := filepath.Join(base, "work", "lane1")
	tmp := filepath.Join(base, "tmproot")
	for _, d := range []string{home, worktree, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	guard := NewRootGuard(home, worktree, tmp)

	refused := []string{
		home,                   // the anchor itself
		filepath.Dir(home),     // ancestor of home
		base,                   // further ancestor, shared by every lane
		worktree,               // the worktree anchor itself
		filepath.Dir(worktree), // the parent every other lane shares
		tmp,                    // the temp anchor itself
	}
	for _, r := range refused {
		if got := guard.Permit(r); got != "" {
			t.Errorf("Permit(%q) = %q, want refused (at or above a shared anchor)", r, got)
		}
	}
}

// TestPermitAllowsPathUnderHome is the positive case: a legitimate plugin-dir-
// shaped path UNDER home must still be permitted. Without this test, a future
// over-tightening of the guard (e.g. refusing anything "near" an anchor rather
// than only at-or-above it) would silently kill the feature the guard exists to
// serve.
func TestPermitAllowsPathUnderHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "userhome")
	plugin := filepath.Join(home, ".claude", "plugins", "x")
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", plugin, err)
	}
	guard := NewRootGuard(home)

	if got := guard.Permit(plugin); got != plugin {
		t.Errorf("Permit(%q) = %q, want permitted (legitimate path under home)", plugin, got)
	}
}

// TestPermitAllowsPathOutsideAnchors: a deep path sharing none of the guard's
// anchors is permitted untouched.
func TestPermitAllowsPathOutsideAnchors(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "userhome")
	guard := NewRootGuard(home)

	sibling := filepath.Join(base, "unrelated", "dir")
	if got := guard.Permit(sibling); got != sibling {
		t.Errorf("Permit(%q) = %q, want permitted (outside every anchor)", sibling, got)
	}
}

// isCaseInsensitiveFS probes dir (which must exist) by creating a directory and
// stat'ing it under an upper-cased spelling of its own name, and reports
// whether the filesystem treats the two spellings as the same file. It probes
// rather than trusting runtime.GOOS because macOS volumes can be formatted
// case-sensitive and Linux can mount case-insensitive filesystems.
func isCaseInsensitiveFS(t *testing.T, dir string) bool {
	t.Helper()
	const name = "case-probe"
	lower := filepath.Join(dir, name)
	if err := os.Mkdir(lower, 0o755); err != nil {
		t.Fatalf("creating case probe dir: %v", err)
	}
	fiLower, err := os.Stat(lower)
	if err != nil {
		t.Fatalf("stat probe dir: %v", err)
	}
	upper := filepath.Join(dir, strings.ToUpper(name))
	fiUpper, err := os.Stat(upper)
	if err != nil {
		return false // the upper-cased spelling does not exist: case-sensitive fs
	}
	return os.SameFile(fiLower, fiUpper)
}

// TestPermitSpellingIndependenceViaSameFile is the subtle one: on a
// case-insensitive filesystem, a case-variant spelling of an anchor names the
// SAME directory the kernel would honour, but filepath.EvalSymlinks returns
// whichever spelling it was handed — so string comparison alone cannot catch
// it. Permit must catch it via os.SameFile instead. Skips cleanly on a
// case-sensitive filesystem, detected by probing rather than by runtime.GOOS.
func TestPermitSpellingIndependenceViaSameFile(t *testing.T) {
	base := t.TempDir()
	if !isCaseInsensitiveFS(t, base) {
		t.Skip("filesystem is case-sensitive; SameFile spelling-independence path is not exercised")
	}

	home := filepath.Join(base, "userhome")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatalf("Mkdir(%q): %v", home, err)
	}
	guard := NewRootGuard(home)

	variant := filepath.Join(filepath.Dir(home), strings.ToUpper(filepath.Base(home)))
	if got := guard.Permit(variant); got != "" {
		t.Fatalf("Permit(%q) = %q, want refused: a case-variant spelling of an anchor must be caught by os.SameFile", variant, got)
	}
}

// TestPermitSymlinkResolvedBeforeGuard proves symlink resolution happens
// BEFORE the guard runs: a symlink pointing at (or through a chain to) home
// must resolve to home's canonical form and be refused, exactly as if the
// caller had named home directly.
func TestPermitSymlinkResolvedBeforeGuard(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "userhome")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatalf("Mkdir(%q): %v", home, err)
	}
	guard := NewRootGuard(home)

	// The canonical form of home itself, computed independently, is what a
	// resolved symlink to home must match — a string check that catches a
	// CanonicalPath that stopped following links, even though Permit's own
	// SameFile fallback (which stats and follows the symlink argument itself)
	// would otherwise mask that regression.
	homeCanonical := CanonicalPath(home)
	if homeCanonical == "" {
		t.Fatalf("CanonicalPath(%q) = \"\", want home's own canonical form", home)
	}

	link := filepath.Join(base, "link-to-home")
	if err := os.Symlink(home, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	resolved := CanonicalPath(link)
	if resolved != homeCanonical {
		t.Fatalf("CanonicalPath(%q) = %q, want %q (home's canonical form): a symlink to home must resolve before comparison", link, resolved, homeCanonical)
	}
	if got := guard.Permit(resolved); got != "" {
		t.Errorf("Permit(CanonicalPath(%q)) = %q, want refused: a symlink to home must resolve and be refused", link, got)
	}

	// A chain of symlinks must resolve fully, not just one hop.
	mid := filepath.Join(base, "mid-link")
	if err := os.Symlink(link, mid); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	resolvedChain := CanonicalPath(mid)
	if resolvedChain != homeCanonical {
		t.Fatalf("CanonicalPath(%q) = %q, want %q (home's canonical form): a symlink CHAIN must resolve fully, not just one hop", mid, resolvedChain, homeCanonical)
	}
	if got := guard.Permit(resolvedChain); got != "" {
		t.Errorf("Permit(CanonicalPath(%q)) = %q, want refused: a symlink CHAIN to home must resolve fully and be refused", mid, got)
	}
}

// TestCanonicalDirBrokenSymlinkYieldsNothing: a broken symlink (the target does
// not exist) must not silently fall back to the link's own textual path — that
// would hand out an unresolved spelling the guard's anchor comparisons could
// miss. CanonicalDir requires an existing directory, so it must yield "".
func TestCanonicalDirBrokenSymlinkYieldsNothing(t *testing.T) {
	base := t.TempDir()
	broken := filepath.Join(base, "broken-link")
	if err := os.Symlink(filepath.Join(base, "does-not-exist"), broken); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if got := CanonicalDir(broken); got != "" {
		t.Errorf("CanonicalDir(%q) = %q, want empty for a broken symlink", broken, got)
	}
}

// TestStripDataVolumeAliasStripsPrefix is a pure, hermetic unit test of the
// firmlink-alias string transform: it needs no real /System/Volumes/Data, so it
// runs on every host and OS.
func TestStripDataVolumeAliasStripsPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/System/Volumes/Data", "/"},
		{"/System/Volumes/Data/Users/jesse", "/Users/jesse"},
		{"/Users/jesse", "/Users/jesse"},
		// Must not strip a path that merely shares the alias prefix textually.
		{"/System/Volumes/DataExtra/x", "/System/Volumes/DataExtra/x"},
	}
	for _, c := range cases {
		if got := StripDataVolumeAlias(c.in); got != c.want {
			t.Errorf("StripDataVolumeAlias(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPermitStripsFirmlinkAliasBeforeGuard proves the alias strip is wired into
// Permit itself: a candidate named through its /System/Volumes/Data firmlink
// alias is refused exactly as its plain spelling would be, because
// EvalSymlinks does not collapse firmlinks (they are not symlinks) and would
// otherwise walk straight past the guard. Skips cleanly on a host with no
// /System/Volumes/Data (i.e. not macOS, or an unusual mount layout).
func TestPermitStripsFirmlinkAliasBeforeGuard(t *testing.T) {
	fi, err := os.Stat(dataVolumePrefix)
	if err != nil || !fi.IsDir() {
		t.Skipf("no %s on this host; firmlink alias stripping is not exercised", dataVolumePrefix)
	}

	// A fabricated anchor that need not exist on disk: NewRootGuard cleans (does
	// not drop) an unresolvable anchor, so it still participates in the textual
	// at-or-above check.
	anchor := "/Users/serf-rootguard-test-anchor-does-not-exist"
	guard := NewRootGuard(anchor)

	alias := dataVolumePrefix + anchor
	if got := guard.Permit(alias); got != "" {
		t.Errorf("Permit(%q) = %q, want refused: the firmlink alias of an anchor must strip to the plain spelling before the guard runs", alias, got)
	}
}

// TestPermitPathHygiene covers the input-normalization edge cases: empty,
// whitespace, relative, trailing slash, repeated separators, and ".." traversal.
func TestPermitPathHygiene(t *testing.T) {
	guard := NewRootGuard(filepath.Join(t.TempDir(), "unrelated-anchor"))

	cases := []struct {
		name string
		path string
		want string
	}{
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"relative path", "relative/path", ""},
		{"relative dot-slash", "./relative/path", ""},
		{"trailing slash", "/opt/toolchain/", "/opt/toolchain"},
		{"repeated separators", "//Users//x//y", "/Users/x/y"},
		{"dot-dot traversal staying deep enough", "/opt/x/../y", "/opt/y"},
		{"dot-dot traversal escaping to depth 1", "/opt/x/../../y", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guard.Permit(c.path); got != c.want {
				t.Errorf("Permit(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestPermitDegradesWithoutHomeAnchor: if the home anchor cannot be determined
// (e.g. os.UserHomeDir() failed) and is simply omitted from the anchor list,
// the depth floor and the remaining anchors must still hold — the guard must
// not silently disable itself for lack of one anchor.
func TestPermitDegradesWithoutHomeAnchor(t *testing.T) {
	base := t.TempDir()
	worktree := filepath.Join(base, "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", worktree, err)
	}
	guard := NewRootGuard(worktree) // home intentionally omitted

	if got := guard.Permit("/"); got != "" {
		t.Errorf("Permit(\"/\") = %q, want refused even without a home anchor", got)
	}
	if got := guard.Permit("/Users"); got != "" {
		t.Errorf("Permit(\"/Users\") = %q, want refused even without a home anchor", got)
	}
	if got := guard.Permit(worktree); got != "" {
		t.Errorf("Permit(%q) = %q, want the worktree anchor itself still refused", worktree, got)
	}

	other := filepath.Join(base, "elsewhere", "dir")
	if got := guard.Permit(other); got != other {
		t.Errorf("Permit(%q) = %q, want permitted (outside every remaining anchor)", other, got)
	}
}

// TestNewRootGuardCleansUnresolvableAnchorsRatherThanDropping: an anchor that
// cannot be resolved (does not exist) is cleaned and KEPT, not silently
// dropped — a dropped anchor would leave that path ungoverned.
func TestNewRootGuardCleansUnresolvableAnchorsRatherThanDropping(t *testing.T) {
	const anchor = "/nonexistent-serf-rootguard-test-anchor/child"
	guard := NewRootGuard("", anchor) // the empty string must be dropped without effect

	if got := guard.Permit(anchor); got != "" {
		t.Errorf("Permit(%q) = %q, want refused: an unresolvable anchor must still be enforced, not dropped", anchor, got)
	}
}

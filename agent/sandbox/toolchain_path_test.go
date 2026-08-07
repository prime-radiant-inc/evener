package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The developer-toolchain bin directory on PATH is what keeps a sandboxed `git`
// off the /usr/bin xcrun shim. The shim memoizes its tool lookup in a cache file
// under the per-user temp directory that confstr(_CS_DARWIN_USER_TEMP_DIR)
// reports — a shared, multi-tenant location no mode makes writable — and when
// that write fails it re-runs `xcodebuild -find git` on EVERY invocation,
// printing two denial lines and costing seconds. Naming the real binary's
// directory skips the shim entirely; it grants nothing, because the directory is
// already inside the read grant the toolchain ruling established.

// developerBinHost builds darwin facts whose active toolchain is a real
// directory tree with a usr/bin inside it, plus the granted root that contains
// it — the shape RealProber produces on a Mac with Xcode installed.
func developerBinHost(t *testing.T) (host HostFacts, root, binDir string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "Xcode.app")
	binDir = filepath.Join(root, "Contents", "Developer", "usr", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	host = darwinHostWithDeveloperTools(root)
	host.DeveloperToolBinDir = binDir
	return host, root, binDir
}

func TestResolveNamesTheToolchainBinDir(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	host, _, binDir := developerBinHost(t)

	for _, mode := range []Mode{ModeRestricted, ModeWorkspaceWrite, ModeReadOnly} {
		rp, err := Resolve(SandboxPolicy{Mode: mode}, host, cwd)
		if err != nil {
			t.Fatalf("Resolve(%v): %v", mode, err)
		}
		if rp.ToolchainBinDir != binDir {
			t.Errorf("%v: ToolchainBinDir = %q, want %q", mode, rp.ToolchainBinDir, binDir)
		}
	}

	rp, err := Resolve(SandboxPolicy{Mode: ModeOff}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve(off): %v", err)
	}
	if rp.ToolchainBinDir != "" {
		t.Errorf("off must name no toolchain bin dir, got %q", rp.ToolchainBinDir)
	}
}

// TestResolveRefusesAnUnreadableToolchainBinDir is the fail-closed half: putting
// a directory on PATH that the spawned layer cannot READ would make `git` fail
// to exec — strictly worse than the noise this removes. So the bin directory is
// named only when it really sits inside a granted spawned read root.
func TestResolveRefusesAnUnreadableToolchainBinDir(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	host, _, binDir := developerBinHost(t)
	// A toolchain root the guard refuses (here: none granted at all) leaves the
	// bin dir outside every spawned read root under restricted mode.
	host.DeveloperToolRoots = nil

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if isUnderAnyRoot(binDir, rp.Spawned.ReadRoots) {
		t.Fatalf("test setup: bin dir must not be readable for this case, roots %v", rp.Spawned.ReadRoots)
	}
	if rp.ToolchainBinDir != "" {
		t.Errorf("an unreadable toolchain bin dir must not go on PATH, got %q", rp.ToolchainBinDir)
	}
}

// TestResolveRefusesASharedToolchainBinDir keeps the untrusted-input property:
// `xcode-select -p` honours $DEVELOPER_DIR, so a bin dir derived from it passes
// the SAME RootGuard as the read grant. A value naming the home directory or a
// shared temp tree is refused outright rather than prepended to PATH.
func TestResolveRefusesASharedToolchainBinDir(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	home := filepath.Dir(cwd)
	for _, shared := range []string{home, filepath.Dir(home), "/", os.TempDir(), "relative/not/abs"} {
		host := darwinHostWithDeveloperTools("/Applications/Xcode.app")
		host.Home = home
		host.DeveloperToolBinDir = shared
		rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite}, host, cwd)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if rp.ToolchainBinDir != "" {
			t.Errorf("shared toolchain bin dir %q must be refused, got %q", shared, rp.ToolchainBinDir)
		}
	}
}

// TestResolveRefusesAMaskedToolchainBinDir: a masked toolchain is unreadable in
// every mode, so naming it on PATH would break exec rather than speed it up.
func TestResolveRefusesAMaskedToolchainBinDir(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	host, root, _ := developerBinHost(t)

	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, DenylistAdd: []string{root}}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rp.ToolchainBinDir != "" {
		t.Errorf("a masked toolchain bin dir must not go on PATH, got %q", rp.ToolchainBinDir)
	}
}

func TestEnvFloorPutsTheToolchainAheadOfTheSystemDirs(t *testing.T) {
	t.Parallel()
	const bin = "/Applications/Xcode.app/Contents/Developer/usr/bin"
	policy := ResolvedPolicy{Mode: ModeRestricted, ToolchainBinDir: bin}

	out := ApplyEnvFloor([]string{"PATH=/opt/homebrew/bin:/usr/bin:/bin"}, policy, "")
	got, _ := envValue(out, "PATH")
	want := "/opt/homebrew/bin:" + bin + ":/usr/bin:/bin"
	if got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
}

// The insertion point is deliberate: the toolchain shadows the /usr/bin shim and
// NOTHING else. A developer who put their own git ahead of /usr/bin keeps it.
func TestEnvFloorKeepsUserToolPrecedence(t *testing.T) {
	t.Parallel()
	const bin = "/Applications/Xcode.app/Contents/Developer/usr/bin"
	policy := ResolvedPolicy{Mode: ModeRestricted, ToolchainBinDir: bin}

	out := ApplyEnvFloor([]string{"PATH=/home/u/bin:/opt/homebrew/bin"}, policy, "")
	got, _ := envValue(out, "PATH")
	if want := "/home/u/bin:/opt/homebrew/bin:" + bin; got != want {
		t.Errorf("with no system dir on PATH the toolchain goes last: got %q, want %q", got, want)
	}
}

func TestEnvFloorLeavesPathAloneWithoutAToolchain(t *testing.T) {
	t.Parallel()
	in := []string{"PATH=/usr/bin:/bin"}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeRestricted}, "")
	if got, _ := envValue(out, "PATH"); got != "/usr/bin:/bin" {
		t.Errorf("PATH must be untouched when no toolchain is named, got %q", got)
	}

	// No PATH in, no PATH out: the floor never invents a search path.
	out = ApplyEnvFloor([]string{"HOME=/home/u"}, ResolvedPolicy{Mode: ModeRestricted, ToolchainBinDir: "/x/usr/bin"}, "")
	if _, ok := envValue(out, "PATH"); ok {
		t.Errorf("the floor must not invent a PATH: %v", out)
	}
}

func TestEnvFloorDoesNotDuplicateTheToolchainEntry(t *testing.T) {
	t.Parallel()
	const bin = "/Applications/Xcode.app/Contents/Developer/usr/bin"
	policy := ResolvedPolicy{Mode: ModeRestricted, ToolchainBinDir: bin}

	out := ApplyEnvFloor([]string{"PATH=" + bin + ":/usr/bin"}, policy, "")
	got, _ := envValue(out, "PATH")
	if strings.Count(got, bin) != 1 {
		t.Errorf("the toolchain must appear once, got %q", got)
	}
	if got != bin+":/usr/bin" {
		t.Errorf("an already-present toolchain must leave PATH untouched, got %q", got)
	}
}

// The floor is a pure function of its inputs, PATH rewrite included.
func TestEnvFloorPathRewriteDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []string{"PATH=/usr/bin:/bin", "HOME=/home/u"}
	before := slices.Clone(in)
	ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeRestricted, ToolchainBinDir: "/x/usr/bin"}, "")
	if !slices.Equal(in, before) {
		t.Errorf("ApplyEnvFloor mutated its input: %v", in)
	}
}

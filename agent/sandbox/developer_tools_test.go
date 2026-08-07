package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// darwinHostWithDeveloperTools is a Seatbelt-capable Mac whose probe found the
// given developer-toolchain roots.
func darwinHostWithDeveloperTools(roots ...string) HostFacts {
	h := darwinSeatbeltHost()
	h.DeveloperToolRoots = roots
	return h
}

// developerToolDirs materializes two real directories standing in for
// /Applications/Xcode.app and /Library/Developer/CommandLineTools. They must
// exist on disk because RootGuard stats a candidate to compare file identities.
func developerToolDirs(t *testing.T) (xcode, clt string) {
	t.Helper()
	base := t.TempDir()
	xcode = filepath.Join(base, "Xcode.app")
	clt = filepath.Join(base, "CommandLineTools")
	for _, d := range []string{xcode, clt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return xcode, clt
}

// TestRestrictedGrantsDeveloperToolRootsToSpawnedOnly is the resolver half of the
// 2026-08-06 ruling: a restricted session's SPAWNED processes may read the
// developer toolchain (macOS ships git as an xcrun shim that execs the real
// binary out of it), while the model's file tools stay confined to the worktree.
func TestRestrictedGrantsDeveloperToolRootsToSpawnedOnly(t *testing.T) {
	t.Parallel()
	xcode, clt := developerToolDirs(t)
	cwd := mainRepo(t)

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinHostWithDeveloperTools(xcode, clt), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, root := range []string{xcode, clt} {
		if !slices.Contains(rp.Spawned.ReadRoots, root) {
			t.Errorf("restricted spawned read roots must include developer root %q, got %v", root, rp.Spawned.ReadRoots)
		}
		if slices.Contains(rp.FileTool.ReadRoots, root) {
			t.Errorf("developer root %q must NOT reach the file-tool layer: %v", root, rp.FileTool.ReadRoots)
		}
	}
}

// TestDeveloperToolRootsNeverWidenTheWriteSurface pins the READ-ONLY half of the
// ruling: adding developer roots must leave both layers' write roots byte-identical.
func TestDeveloperToolRootsNeverWidenTheWriteSurface(t *testing.T) {
	t.Parallel()
	xcode, clt := developerToolDirs(t)
	cwd := mainRepo(t)

	without, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinSeatbeltHost(), cwd)
	if err != nil {
		t.Fatalf("Resolve (no developer roots): %v", err)
	}
	with, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinHostWithDeveloperTools(xcode, clt), cwd)
	if err != nil {
		t.Fatalf("Resolve (developer roots): %v", err)
	}
	if !slices.Equal(without.Spawned.WriteRoots, with.Spawned.WriteRoots) {
		t.Errorf("developer roots changed the spawned write surface:\n without: %v\n with:    %v", without.Spawned.WriteRoots, with.Spawned.WriteRoots)
	}
	if !slices.Equal(without.FileTool.WriteRoots, with.FileTool.WriteRoots) {
		t.Errorf("developer roots changed the file-tool write surface:\n without: %v\n with:    %v", without.FileTool.WriteRoots, with.FileTool.WriteRoots)
	}
	if !slices.Equal(without.FileTool.ReadRoots, with.FileTool.ReadRoots) {
		t.Errorf("developer roots changed the file-tool read surface:\n without: %v\n with:    %v", without.FileTool.ReadRoots, with.FileTool.ReadRoots)
	}
}

// TestDeveloperToolRootsAreRestrictedModeOnly: the other modes' spawned layer
// already reads anywhere-minus-the-denylist, so the grant must add nothing there
// (a stray root list would turn ReadAnywhere into a roots-only surface for a
// backend that consults ReadRoots).
func TestDeveloperToolRootsAreRestrictedModeOnly(t *testing.T) {
	t.Parallel()
	xcode, clt := developerToolDirs(t)
	cwd := mainRepo(t)

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite} {
		rp, err := Resolve(SandboxPolicy{Mode: mode}, darwinHostWithDeveloperTools(xcode, clt), cwd)
		if err != nil {
			t.Fatalf("Resolve(%v): %v", mode, err)
		}
		if len(rp.Spawned.ReadRoots) != 0 {
			t.Errorf("%v spawned read roots must stay empty (ReadAnywhere), got %v", mode, rp.Spawned.ReadRoots)
		}
	}
}

// TestDeveloperToolRootsRefuseSharedTrees proves the grant reuses the RootGuard:
// a developer directory that names the home directory, an ancestor of it, the
// worktree, a temp root, or a one-component path is refused outright. Such a
// value would hand a spawned process a whole multi-tenant tree, and the denylist
// could not catch it — filterMasked drops roots at or BENEATH a masked path, and
// these sit ABOVE them.
//
// The value is host-derived but not host-fixed: `xcode-select -p` honours
// $DEVELOPER_DIR, so it is treated as untrusted input like any other configured
// root.
func TestDeveloperToolRootsRefuseSharedTrees(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	home := filepath.Dir(cwd) // make the worktree sit under the resolved home
	host := darwinHostWithDeveloperTools(
		"/",
		"/Applications",
		home,
		filepath.Dir(home),
		cwd,
		os.TempDir(),
		"relative/not/absolute",
	)
	host.Home = home

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, bad := range []string{"/", "/Applications", home, filepath.Dir(home), os.TempDir(), "relative/not/absolute"} {
		if slices.Contains(rp.Spawned.ReadRoots, bad) {
			t.Errorf("unsafe developer root %q must be refused, got roots %v", bad, rp.Spawned.ReadRoots)
		}
	}
	// The worktree is granted on its own merits (it is the session's lane), never
	// as a developer root — so its presence here is not a guard failure. What must
	// not happen is any root ABOVE it appearing.
	for _, root := range rp.Spawned.ReadRoots {
		if root != cwd && pathUnder(cwd, root) {
			t.Errorf("a granted root %q is an ancestor of the worktree %q", root, cwd)
		}
	}
}

// TestDeveloperToolRootsLoseToTheDenylist pins denylist precedence over the new
// grant, in both halves: the non-removable pseudo-filesystem floor and a
// user-added secret directory. Resolve's filterMasked must drop a developer root
// that is at or beneath either.
func TestDeveloperToolRootsLoseToTheDenylist(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	secretDir := t.TempDir()
	inSecret := filepath.Join(secretDir, "Xcode.app")
	if err := os.MkdirAll(inSecret, 0o755); err != nil {
		t.Fatal(err)
	}

	// "/proc" is on the non-removable floor; DenylistRemove must not free it.
	host := darwinHostWithDeveloperTools("/proc", "/proc/self", inSecret)
	policy := SandboxPolicy{
		Mode:           ModeRestricted,
		DenylistAdd:    []string{secretDir},
		DenylistRemove: []string{"/proc"},
	}
	rp, err := Resolve(policy, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Contains(rp.MaskedPaths, "/proc") {
		t.Fatalf("the pseudo-filesystem floor must be non-removable; masked = %v", rp.MaskedPaths)
	}
	for _, denied := range []string{"/proc", "/proc/self", inSecret} {
		if slices.Contains(rp.Spawned.ReadRoots, denied) {
			t.Errorf("masked developer root %q must be dropped, got roots %v", denied, rp.Spawned.ReadRoots)
		}
	}
}

// TestResolveWithoutDeveloperToolsStillStarts: a Mac with no toolchain installed
// (and every non-darwin host) contributes no roots, and that must resolve
// normally — a missing path is never a session-start failure.
func TestResolveWithoutDeveloperToolsStillStarts(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinSeatbeltHost(), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, want := range defaultSystemReadRoots {
		if !slices.Contains(rp.Spawned.ReadRoots, want) {
			t.Fatalf("system read root %q missing: %v", want, rp.Spawned.ReadRoots)
		}
	}
}

// stubProbeSystem answers only the calls the developer-tools probe makes.
type stubProbeSystem struct {
	xcodeSelectOut string
	xcodeSelectErr error
}

func (stubProbeSystem) goos() string                                 { return "darwin" }
func (stubProbeSystem) userHomeDir() (string, error)                 { return "/Users/tester", nil }
func (stubProbeSystem) lookPath(string) (string, error)              { return "", errors.New("not found") }
func (stubProbeSystem) nonDirectoryFile(string) bool                 { return false }
func (stubProbeSystem) run(context.Context, string, ...string) error { return errors.New("no") }
func (stubProbeSystem) combinedOutput(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("no")
}
func (s stubProbeSystem) output(_ context.Context, name string, _ ...string) ([]byte, error) {
	if name == xcodeSelectPath {
		return []byte(s.xcodeSelectOut), s.xcodeSelectErr
	}
	return nil, errors.New("no")
}

// TestProbeDeveloperToolRootsHandlesAMissingToolchain: xcode-select failing, or
// naming a directory that does not exist, must contribute nothing rather than
// emit a bogus root.
func TestProbeDeveloperToolRootsHandlesAMissingToolchain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		system stubProbeSystem
	}{
		{"xcode-select fails", stubProbeSystem{xcodeSelectErr: errors.New("no developer dir")}},
		{"xcode-select is silent", stubProbeSystem{xcodeSelectOut: "\n"}},
		{"the named directory is absent", stubProbeSystem{xcodeSelectOut: "/nonexistent/Developer\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, root := range probeDeveloperToolRoots(tc.system) {
				if root != commandLineToolsRoot {
					t.Errorf("unexpected developer root %q", root)
				}
			}
		})
	}
}

// TestEnclosingAppBundleWidensToTheBundle: an Xcode install's active developer
// directory is inside the .app, but its tools dyld-load frameworks and stat an
// Info.plist from SIBLING bundle directories, so the bundle is the grant unit. A
// developer directory outside any bundle (the Command Line Tools) is unchanged.
func TestEnclosingAppBundleWidensToTheBundle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"/Applications/Xcode.app/Contents/Developer", "/Applications/Xcode.app"},
		{"/Applications/Xcode-beta.app/Contents/Developer", "/Applications/Xcode-beta.app"},
		{"/Library/Developer/CommandLineTools", "/Library/Developer/CommandLineTools"},
		{"", ""},
	} {
		if got := enclosingAppBundle(tc.in); got != tc.want {
			t.Errorf("enclosingAppBundle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

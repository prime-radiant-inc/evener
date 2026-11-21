package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

// darwinHostWithGoToolchain is a Seatbelt-capable Mac whose probe resolved the
// given Go toolchain roots.
func darwinHostWithGoToolchain(roots ...string) HostFacts {
	h := darwinSeatbeltHost()
	h.GoToolchainRoots = roots
	return h
}

// goToolchainDir materializes a real directory standing in for a version-manager
// or SDK GOROOT. It must exist on disk because RootGuard stats a candidate to
// compare file identities.
func goToolchainDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "go1.24.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRestrictedGrantsGoToolchainRootToSpawnedOnly pins the resolver half of the
// ruling: a restricted session's SPAWNED processes may read the host's Go
// toolchain root, because the go command can run from a granted path while its
// tool directory (GOROOT/pkg/tool) sits outside every system read root, while
// the model's file tools stay confined to the worktree.
func TestRestrictedGrantsGoToolchainRootToSpawnedOnly(t *testing.T) {
	t.Parallel()
	root := goToolchainDir(t)
	cwd := mainRepo(t)

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinHostWithGoToolchain(root), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Contains(rp.Spawned.ReadRoots, root) {
		t.Errorf("restricted spawned read roots must include the Go toolchain root %q, got %v", root, rp.Spawned.ReadRoots)
	}
	if slices.Contains(rp.FileTool.ReadRoots, root) {
		t.Errorf("Go toolchain root %q must NOT reach the file-tool layer: %v", root, rp.FileTool.ReadRoots)
	}
}

// TestGoToolchainRootNeverWidensTheWriteSurface pins the READ-ONLY half of the
// ruling: adding the root must leave both layers' write roots and the file-tool
// read roots byte-identical.
func TestGoToolchainRootNeverWidensTheWriteSurface(t *testing.T) {
	t.Parallel()
	root := goToolchainDir(t)
	cwd := mainRepo(t)

	without, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinSeatbeltHost(), cwd)
	if err != nil {
		t.Fatalf("Resolve (no Go toolchain): %v", err)
	}
	with, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinHostWithGoToolchain(root), cwd)
	if err != nil {
		t.Fatalf("Resolve (Go toolchain): %v", err)
	}
	if !slices.Equal(without.Spawned.WriteRoots, with.Spawned.WriteRoots) {
		t.Errorf("Go toolchain root changed the spawned write surface:\n without: %v\n with:    %v", without.Spawned.WriteRoots, with.Spawned.WriteRoots)
	}
	if !slices.Equal(without.FileTool.WriteRoots, with.FileTool.WriteRoots) {
		t.Errorf("Go toolchain root changed the file-tool write surface:\n without: %v\n with:    %v", without.FileTool.WriteRoots, with.FileTool.WriteRoots)
	}
	if !slices.Equal(without.FileTool.ReadRoots, with.FileTool.ReadRoots) {
		t.Errorf("Go toolchain root changed the file-tool read surface:\n without: %v\n with:    %v", without.FileTool.ReadRoots, with.FileTool.ReadRoots)
	}
}

// TestGoToolchainRootIsRestrictedModeOnly: the other modes' spawned layer
// already reads anywhere-minus-the-denylist, so this grant must add nothing
// there. This policy configures no InfraReadRoots, so the list must resolve
// empty.
func TestGoToolchainRootIsRestrictedModeOnly(t *testing.T) {
	t.Parallel()
	root := goToolchainDir(t)
	cwd := mainRepo(t)

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite} {
		rp, err := Resolve(SandboxPolicy{Mode: mode}, darwinHostWithGoToolchain(root), cwd)
		if err != nil {
			t.Fatalf("Resolve(%v): %v", mode, err)
		}
		if len(rp.Spawned.ReadRoots) != 0 {
			t.Errorf("%v spawned read roots must stay empty (ReadAnywhere), got %v", mode, rp.Spawned.ReadRoots)
		}
	}
}

// TestGoToolchainRootRefusesSharedTrees proves the grant reuses the RootGuard: a
// GOROOT naming the home directory, an ancestor of it, the worktree, a temp
// root, the filesystem root, or a relative path is refused outright. Such a
// value would hand a spawned process a whole multi-tenant tree, and the denylist
// could not catch it — filterMasked drops roots at or BENEATH a masked path, and
// these sit ABOVE them.
//
// A GOROOT under the home directory IS admitted: version-manager installs and
// golang.org/dl SDKs live under $HOME, below the shared-tree anchors.
func TestGoToolchainRootRefusesSharedTrees(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	home := filepath.Dir(cwd) // make the worktree sit under the resolved home
	goRoot := filepath.Join(home, "sdk", "go1.24.1")
	if err := os.MkdirAll(goRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	host := darwinHostWithGoToolchain(
		"/",
		home,
		filepath.Dir(home),
		cwd,
		os.TempDir(),
		"relative/not/absolute",
		goRoot,
	)
	host.Home = home

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, bad := range []string{"/", home, filepath.Dir(home), os.TempDir(), "relative/not/absolute"} {
		if slices.Contains(rp.Spawned.ReadRoots, bad) {
			t.Errorf("unsafe Go toolchain root %q must be refused, got roots %v", bad, rp.Spawned.ReadRoots)
		}
	}
	if !slices.Contains(rp.Spawned.ReadRoots, goRoot) {
		t.Errorf("a GOROOT under the home directory must be admitted, got roots %v", rp.Spawned.ReadRoots)
	}
	// The worktree is granted on its own merits (it is the session's lane), so
	// its presence in the resolved roots cannot show whether the guard refused it
	// as a GOROOT candidate. Check the guard directly, and check no granted root
	// sits ABOVE the worktree.
	if got := guardedHostRoots([]string{cwd}, home, cwd); len(got) != 0 {
		t.Errorf("the worktree must be refused as a Go toolchain root, got %v", got)
	}
	for _, root := range rp.Spawned.ReadRoots {
		if root != cwd && identifier.PathUnder(cwd, root) {
			t.Errorf("a granted root %q is above the worktree %q", root, cwd)
		}
	}
}

// TestGoToolchainRootLosesToTheDenylist pins denylist precedence over the new
// grant, in both halves: the non-removable pseudo-filesystem floor and a
// user-added secret directory. Resolve's filterMasked must drop a GOROOT that is
// at or beneath either.
func TestGoToolchainRootLosesToTheDenylist(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	secretDir := t.TempDir()
	inSecret := filepath.Join(secretDir, "go1.24.1")
	if err := os.MkdirAll(inSecret, 0o755); err != nil {
		t.Fatal(err)
	}

	// "/proc" is on the non-removable floor; DenylistRemove must not free it.
	host := darwinHostWithGoToolchain("/proc", inSecret)
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
	for _, denied := range []string{"/proc", inSecret} {
		if slices.Contains(rp.Spawned.ReadRoots, denied) {
			t.Errorf("masked Go toolchain root %q must be dropped, got roots %v", denied, rp.Spawned.ReadRoots)
		}
	}
}

// goToolchainProbeSystem answers only the calls the GOROOT probe makes and
// records the argv handed to output; the rest of probeSystem comes from the
// shared stub.
type goToolchainProbeSystem struct {
	stubProbeSystem
	goBin       string
	goBinErr    error
	prefixFiles map[string]bool
	gorootOut   string
	gorootErr   error
	outputName  string
	outputArgs  []string
}

func (s *goToolchainProbeSystem) nonDirectoryFile(path string) bool { return s.prefixFiles[path] }

func (s *goToolchainProbeSystem) lookPath(name string) (string, error) {
	if name != "go" {
		return "", errors.New("unexpected lookPath")
	}
	return s.goBin, s.goBinErr
}

func (s *goToolchainProbeSystem) output(_ context.Context, name string, args ...string) ([]byte, error) {
	s.outputName = name
	s.outputArgs = slices.Clone(args)
	return []byte(s.gorootOut), s.gorootErr
}

// TestProbeGoToolchainRoots pins the probe contract: one bounded
// `go env -C / GOROOT` query outside any module, admitted only when the answer
// is an absolute path naming an existing directory. Every failure contributes
// no root rather than a bogus grant.
func TestProbeGoToolchainRoots(t *testing.T) {
	t.Parallel()
	validDir := goToolchainDir(t)
	absent := filepath.Join(t.TempDir(), "absent")

	for _, tc := range []struct {
		name    string
		system  *goToolchainProbeSystem
		want    []string
		wantArg []string
	}{
		{
			name:    "existing directory",
			system:  &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: validDir + "\n"},
			want:    []string{validDir},
			wantArg: []string{"env", "-C", "/", "GOROOT"},
		},
		{
			name:   "trailing newline tolerated",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: validDir + "\n\n"},
			want:   []string{validDir},
		},
		{
			name:   "command error",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootErr: errors.New("boom")},
		},
		{
			name:   "empty output",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: "\n"},
		},
		{
			name:   "relative output",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: "relative/not/absolute\n"},
		},
		{
			// Clean(".") stays relative; canonicalizing it would have named
			// the test's own working directory and granted that instead.
			name:   "relative output naming an existing directory",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: ".\n"},
		},
		{
			name:   "nonexistent directory",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: absent + "\n"},
		},
		{
			name:   "go not on PATH",
			system: &goToolchainProbeSystem{goBinErr: errors.New("not found")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := probeGoToolchainRoots(tc.system)
			if !slices.Equal(got, tc.want) {
				t.Errorf("probeGoToolchainRoots = %v, want %v", got, tc.want)
			}
			if tc.wantArg != nil {
				if tc.system.outputName != tc.system.goBin {
					t.Errorf("probe ran %q, want the resolved go binary %q", tc.system.outputName, tc.system.goBin)
				}
				if !slices.Equal(tc.system.outputArgs, tc.wantArg) {
					t.Errorf("probe argv = %v, want %v", tc.system.outputArgs, tc.wantArg)
				}
			}
		})
	}
}

// TestProbeGoToolchainRootsKeepsTheLiteralSymlinkSpelling: go reports the
// spelling GOROOT names, so the probe must return it as-is; the Seatbelt read
// emitter derives the canonical alias from that spelling, and a child opening
// the link path needs the literal grant.
func TestProbeGoToolchainRootsKeepsTheLiteralSymlinkSpelling(t *testing.T) {
	t.Parallel()
	target := goToolchainDir(t)
	link := filepath.Join(filepath.Dir(target), "go-current")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	system := &goToolchainProbeSystem{goBin: "/usr/local/bin/go", gorootOut: link + "\n"}
	if got := probeGoToolchainRoots(system); !slices.Equal(got, []string{link}) {
		t.Errorf("probeGoToolchainRoots = %v, want the literal spelling %q", got, link)
	}
}

// TestProbeGoToolchainRootsFallsBackToAnInstallPrefix: a daemon launched by
// launchd inherits a PATH without the shell rc chain, so Homebrew's go is
// invisible to lookPath. The probe must still find a standard install prefix.
func TestProbeGoToolchainRootsFallsBackToAnInstallPrefix(t *testing.T) {
	t.Parallel()
	dir := goToolchainDir(t)
	system := &goToolchainProbeSystem{
		goBinErr:    errors.New("not found"),
		prefixFiles: map[string]bool{"/opt/homebrew/bin/go": true},
		gorootOut:   dir + "\n",
	}
	if got := probeGoToolchainRoots(system); !slices.Equal(got, []string{dir}) {
		t.Errorf("probeGoToolchainRoots = %v, want %v", got, []string{dir})
	}
	if system.outputName != "/opt/homebrew/bin/go" {
		t.Errorf("probe ran %q, want the Homebrew install prefix", system.outputName)
	}
}

// TestSeatbeltProfileGrantsGoToolchainRoot: the Seatbelt profile must carry the
// Go toolchain root as a readable-root param and reference that key in the
// file-read allow, or the grant never reaches the kernel.
func TestSeatbeltProfileGrantsGoToolchainRoot(t *testing.T) {
	t.Parallel()
	root := goToolchainDir(t)
	cwd := mainRepo(t)

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, darwinHostWithGoToolchain(root), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	text, params := SeatbeltPolicy(rp, "/evener-session-tmp", identityCanon)

	key := paramKeyForPath(params, root)
	if key == "" {
		t.Fatalf("no Seatbelt param names the Go toolchain root %q; params = %v", root, params)
	}
	if !strings.HasPrefix(key, "READABLE_ROOT_") {
		t.Errorf("Go toolchain root param %q must be a read root", key)
	}
	start := strings.Index(text, "(allow file-read*")
	if start < 0 {
		t.Fatalf("policy has no file-read allow:\n%s", text)
	}
	block := text[start:]
	// The rule's terminator is a line holding only ")"; every inner line ends
	// with its own ")" for (subpath (param "...")), so search the line start.
	if end := strings.Index(block, "\n)"); end >= 0 {
		block = block[:end+2]
	}
	if !strings.Contains(block, subpathParam(key)) {
		t.Errorf("the file-read allow must reference %s:\n%s", subpathParam(key), text)
	}
	// The restricted unix-socket allow is derived from read roots, but a
	// toolchain read grant is not a socket surface: it must not appear there.
	if socket := "(allow network-outbound (remote unix-socket " + subpathParam(key) + "))"; strings.Contains(text, socket) {
		t.Errorf("the Go toolchain root must not earn a unix-socket allow:\n%s", text)
	}
	if !strings.Contains(text, "(remote unix-socket") {
		t.Errorf("the restricted policy lost its ordinary unix-socket grants:\n%s", text)
	}
}

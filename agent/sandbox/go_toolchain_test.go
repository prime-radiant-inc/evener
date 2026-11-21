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

// goRootPair is the probe's answer for a directory: the spelling go reported,
// plus its canonical form when the two differ.
func goRootPair(dir string) []string {
	if canonical := CanonicalDir(dir); canonical != "" && canonical != dir {
		return []string{dir, canonical}
	}
	return []string{dir}
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

// TestGoToolchainGrantRefusesTheWorktree: a grant the worktree controls is not
// a host grant. A GOROOT inside the worktree adds nothing (the worktree is
// already readable) and must not be granted by a spelling the session can
// retarget; a launcher inside the worktree refuses the whole grant.
func TestGoToolchainGrantRefusesTheWorktree(t *testing.T) {
	t.Parallel()
	cwd := mainRepo(t)
	root := goToolchainDir(t)
	inWorktree := filepath.Join(cwd, "goroot")

	host := darwinHostWithGoToolchain(inWorktree, root)
	host.GoToolchainGoBin = filepath.Join(cwd, "bin", "go")
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, refused := range []string{inWorktree, root} {
		if slices.Contains(rp.Spawned.ReadRoots, refused) {
			t.Errorf("%q must not be granted when the worktree controls the launcher: %v", refused, rp.Spawned.ReadRoots)
		}
	}

	// A trusted launcher keeps the host root; the worktree-internal spelling
	// still contributes nothing.
	host = darwinHostWithGoToolchain(inWorktree, root)
	host.GoToolchainGoBin = "/usr/local/go/bin/go"
	rp, err = Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, inWorktree) {
		t.Errorf("a GOROOT inside the worktree must not be granted separately: %v", rp.Spawned.ReadRoots)
	}
	if !slices.Contains(rp.Spawned.ReadRoots, root) {
		t.Errorf("the trusted Go toolchain root %q must stay granted, got %v", root, rp.Spawned.ReadRoots)
	}
}

// TestGoToolchainSocketExemptionSparesIndependentlyGrantedRoots: when the
// GOROOT is also an extra read root the user asked for, the toolchain grant
// must not strip the unix-socket reach that root's own grant earned.
func TestGoToolchainSocketExemptionSparesIndependentlyGrantedRoots(t *testing.T) {
	t.Parallel()
	root := goToolchainDir(t)
	cwd := mainRepo(t)

	policy := SandboxPolicy{Mode: ModeRestricted, ExtraReadRoots: []string{root}}
	rp, err := Resolve(policy, darwinHostWithGoToolchain(root), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	text, params := SeatbeltPolicy(rp, "/evener-session-tmp", identityCanon)
	key := paramKeyForPath(params, root)
	if key == "" {
		t.Fatalf("no Seatbelt param names the extra read root %q; params = %v", root, params)
	}
	if !strings.Contains(text, "(allow network-outbound (remote unix-socket "+subpathParam(key)+"))") {
		t.Errorf("an independently granted read root must keep its unix-socket allow:\n%s", text)
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
	answers     map[string]string
	gorootOut   string
	gorootErr   error
	outputName  string
	outputArgs  []string
	outputEnv   []string
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
	if answer, ok := s.answers[name]; ok {
		return []byte(answer), nil
	}
	return []byte(s.gorootOut), s.gorootErr
}

func (s *goToolchainProbeSystem) outputWithEnv(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
	s.outputEnv = slices.Clone(extraEnv)
	return s.output(ctx, name, args...)
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
			want:    goRootPair(validDir),
			wantArg: []string{"env", "-C", "/", "GOROOT"},
		},
		{
			name:   "trailing newline tolerated",
			system: &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: validDir + "\n\n"},
			want:   goRootPair(validDir),
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
			got, _ := probeGoToolchainRoots(tc.system, "")
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
	want := []string{link, target}
	if got, _ := probeGoToolchainRoots(system, ""); !slices.Equal(got, want) {
		t.Errorf("probeGoToolchainRoots = %v, want the literal and canonical spellings %v", got, want)
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
	want := goRootPair(dir)
	if got, launcher := probeGoToolchainRoots(system, ""); !slices.Equal(got, want) || launcher != "/opt/homebrew/bin/go" {
		t.Errorf("probeGoToolchainRoots = %v, launcher %q, want %v from the Homebrew prefix", got, launcher, want)
	}
	if system.outputName != "/opt/homebrew/bin/go" {
		t.Errorf("probe ran %q, want the Homebrew install prefix", system.outputName)
	}
}

// TestProbeGoToolchainRootsPrefersTheSessionPATHLauncher: spawned commands run
// with the login-shell PATH, which can name a Go the daemon's own PATH does
// not. The probe must ask that launcher first, or the grant covers a toolchain
// the session never runs and the SDK it does run stays unreadable.
func TestProbeGoToolchainRootsPrefersTheSessionPATHLauncher(t *testing.T) {
	t.Parallel()
	sdkGo := "/Users/someone/sdk/go1.24/bin/go"
	daemonGo := "/usr/local/go/bin/go"
	sdkRoot := goToolchainDir(t)
	daemonRoot := goToolchainDir(t)
	system := &goToolchainProbeSystem{
		goBin:       daemonGo,
		prefixFiles: map[string]bool{sdkGo: true},
		answers:     map[string]string{sdkGo: sdkRoot, daemonGo: daemonRoot},
	}
	roots, launcher := probeGoToolchainRoots(system, "/Users/someone/sdk/go1.24/bin:/usr/local/bin")
	if launcher != sdkGo {
		t.Fatalf("launcher = %q, want the go the session PATH resolves first (%q)", launcher, sdkGo)
	}
	if want := goRootPair(sdkRoot); !slices.Equal(roots, want) {
		t.Errorf("roots = %v, want the session launcher's GOROOT %v", roots, want)
	}
}

// TestProbeGoToolchainRootsPinsToolchainSelection: the discovery query must not
// load a workspace or switch to (and download) a toolchain before the sandbox
// exists.
func TestProbeGoToolchainRootsPinsToolchainSelection(t *testing.T) {
	t.Parallel()
	root := goToolchainDir(t)
	system := &goToolchainProbeSystem{goBin: "/usr/local/go/bin/go", gorootOut: root + "\n"}
	if _, launcher := probeGoToolchainRoots(system, ""); launcher == "" {
		t.Fatalf("probe contributed no launcher")
	}
	for _, want := range probeGoToolchainEnv {
		if !slices.Contains(system.outputEnv, want) {
			t.Errorf("probe env %v lacks %q", system.outputEnv, want)
		}
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

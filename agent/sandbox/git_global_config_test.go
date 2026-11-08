package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// darwinHostWithGitGlobalConfig is a Seatbelt-capable Mac whose probe found the
// given global git config files.
func darwinHostWithGitGlobalConfig(paths ...string) HostFacts {
	h := darwinSeatbeltHost()
	h.GitGlobalConfigPaths = paths
	return h
}

// gitGlobalConfigFiles materializes a fake home holding both files git consults
// for global configuration. They must exist on disk because RootGuard stats a
// candidate to compare file identities.
func gitGlobalConfigFiles(t *testing.T) (home, dotfile, xdgFile string) {
	t.Helper()
	home = t.TempDir()
	dotfile = filepath.Join(home, ".gitconfig")
	xdgFile = filepath.Join(home, ".config", "git", "config")
	if err := os.MkdirAll(filepath.Dir(xdgFile), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{dotfile, xdgFile} {
		if err := os.WriteFile(f, []byte("[user]\n\tname = t\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, dotfile, xdgFile
}

// TestRestrictedGrantsGlobalGitConfigToSpawnedOnly is the resolver half of the
// 2026-08-07 ruling: a restricted session's SPAWNED processes may read the
// user's global git config (git fatals on a present-but-unreadable one), while
// the model's file tools stay confined to the worktree — the model gains no
// browse grant over anything in the home directory.
func TestRestrictedGrantsGlobalGitConfigToSpawnedOnly(t *testing.T) {
	t.Parallel()
	home, dotfile, xdgFile := gitGlobalConfigFiles(t)
	cwd := mainRepo(t)
	host := darwinHostWithGitGlobalConfig(xdgFile, dotfile)
	host.Home = home

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, f := range []string{xdgFile, dotfile} {
		if !slices.Contains(rp.Spawned.ReadRoots, f) {
			t.Errorf("restricted spawned read roots must include global git config %q, got %v", f, rp.Spawned.ReadRoots)
		}
		if slices.Contains(rp.FileTool.ReadRoots, f) {
			t.Errorf("global git config %q must NOT reach the file-tool layer: %v", f, rp.FileTool.ReadRoots)
		}
	}
	// File-exact: the grant is the config FILES, never the directories holding
	// them — a home read (or a whole ~/.config read) is exactly what the ruling
	// did not authorize.
	for _, dir := range []string{home, filepath.Join(home, ".config"), filepath.Dir(xdgFile)} {
		if slices.Contains(rp.Spawned.ReadRoots, dir) {
			t.Errorf("the grant must stay file-exact, but directory %q became a read root: %v", dir, rp.Spawned.ReadRoots)
		}
	}
}

// TestGlobalGitConfigNeverWidensTheWriteSurface pins the READ-ONLY half of the
// ruling: the grant must leave both layers' write roots and the protected git
// surfaces byte-identical. The anti-hook-planting argument in docs/sandboxing.md
// rests on config being unWRITABLE, not on it being unreadable.
func TestGlobalGitConfigNeverWidensTheWriteSurface(t *testing.T) {
	t.Parallel()
	home, dotfile, xdgFile := gitGlobalConfigFiles(t)
	cwd := mainRepo(t)
	bare := darwinSeatbeltHost()
	bare.Home = home
	granted := darwinHostWithGitGlobalConfig(xdgFile, dotfile)
	granted.Home = home

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		without, err := Resolve(SandboxPolicy{Mode: mode}, bare, cwd)
		if err != nil {
			t.Fatalf("Resolve(%v, no global config): %v", mode, err)
		}
		with, err := Resolve(SandboxPolicy{Mode: mode}, granted, cwd)
		if err != nil {
			t.Fatalf("Resolve(%v, global config): %v", mode, err)
		}
		if !slices.Equal(without.Spawned.WriteRoots, with.Spawned.WriteRoots) {
			t.Errorf("%v: the grant changed the spawned write surface:\n without: %v\n with:    %v", mode, without.Spawned.WriteRoots, with.Spawned.WriteRoots)
		}
		if !slices.Equal(without.FileTool.WriteRoots, with.FileTool.WriteRoots) {
			t.Errorf("%v: the grant changed the file-tool write surface:\n without: %v\n with:    %v", mode, without.FileTool.WriteRoots, with.FileTool.WriteRoots)
		}
		if !slices.Equal(without.FileTool.ReadRoots, with.FileTool.ReadRoots) {
			t.Errorf("%v: the grant changed the file-tool read surface:\n without: %v\n with:    %v", mode, without.FileTool.ReadRoots, with.FileTool.ReadRoots)
		}
		if !slices.Equal(without.Git.ProtectedPaths, with.Git.ProtectedPaths) {
			t.Errorf("%v: the grant changed the protected git surfaces:\n without: %v\n with:    %v", mode, without.Git.ProtectedPaths, with.Git.ProtectedPaths)
		}
		if !slices.Equal(without.MaskedPaths, with.MaskedPaths) {
			t.Errorf("%v: the grant changed the denylist:\n without: %v\n with:    %v", mode, without.MaskedPaths, with.MaskedPaths)
		}
	}
}

// TestGlobalGitConfigIsRestrictedModeOnly: the other modes' spawned layer already
// reads anywhere-minus-the-denylist, so the grant must add nothing there (a stray
// root list would turn ReadAnywhere into a roots-only surface for a backend that
// consults ReadRoots).
func TestGlobalGitConfigIsRestrictedModeOnly(t *testing.T) {
	t.Parallel()
	home, dotfile, xdgFile := gitGlobalConfigFiles(t)
	cwd := mainRepo(t)
	host := darwinHostWithGitGlobalConfig(xdgFile, dotfile)
	host.Home = home

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite} {
		rp, err := Resolve(SandboxPolicy{Mode: mode}, host, cwd)
		if err != nil {
			t.Fatalf("Resolve(%v): %v", mode, err)
		}
		if len(rp.Spawned.ReadRoots) != 0 {
			t.Errorf("%v spawned read roots must stay empty (ReadAnywhere), got %v", mode, rp.Spawned.ReadRoots)
		}
	}
}

// TestGlobalGitConfigLosesToTheDenylist pins the precedence that keeps the grant
// safe: a global config file the denylist covers never survives as a read root.
// The live counterpart (TestSeatbeltLiveGitCredentialsStayMasked) proves the same
// thing against the real kernel for ~/.git-credentials, the file a readable
// `credential.helper` line points at.
func TestGlobalGitConfigLosesToTheDenylist(t *testing.T) {
	t.Parallel()
	home, dotfile, _ := gitGlobalConfigFiles(t)
	cwd := mainRepo(t)
	host := darwinHostWithGitGlobalConfig(dotfile)
	host.Home = home

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, DenylistAdd: []string{dotfile}}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, dotfile) {
		t.Errorf("a denylisted global git config must never survive as a read root: %v", rp.Spawned.ReadRoots)
	}
}

// TestGlobalGitConfigRefusesSharedTrees proves the grant reuses the RootGuard: a
// configured config path that names the home directory, an ancestor of it, or a
// one-component path is refused outright rather than granted. Such a value would
// hand a spawned process a whole multi-tenant tree, and the denylist could not
// catch it — filterMasked drops roots at or BENEATH a masked path, and these sit
// ABOVE them.
func TestGlobalGitConfigRefusesSharedTrees(t *testing.T) {
	t.Parallel()
	home, dotfile, _ := gitGlobalConfigFiles(t)
	cwd := mainRepo(t)
	host := darwinHostWithGitGlobalConfig(home, filepath.Dir(home), "/etc", dotfile)
	host.Home = home

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, refused := range []string{home, filepath.Dir(home)} {
		if slices.Contains(rp.Spawned.ReadRoots, refused) {
			t.Errorf("the guard must refuse shared tree %q, got read roots %v", refused, rp.Spawned.ReadRoots)
		}
	}
	if !slices.Contains(rp.Spawned.ReadRoots, dotfile) {
		t.Errorf("the legitimate config file must still be granted: %v", rp.Spawned.ReadRoots)
	}
}

// TestResolveWithoutGlobalGitConfigStillStarts: a host where neither global
// config file exists contributes nothing, and that must resolve normally — a
// missing path is never a session-start failure.
func TestResolveWithoutGlobalGitConfigStillStarts(t *testing.T) {
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

// gitConfigProbeSystem answers the calls probeGitGlobalConfigPaths makes: a home
// directory, an environment, and a real on-disk existence test.
type gitConfigProbeSystem struct {
	stubProbeSystem
	home    string
	homeErr error
}

func (s gitConfigProbeSystem) userHomeDir() (string, error) { return s.home, s.homeErr }
func (gitConfigProbeSystem) nonDirectoryFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// TestProbeGitGlobalConfigPaths pins which paths git actually consults
// (git-config(1) FILES: $XDG_CONFIG_HOME/git/config then ~/.gitconfig, BOTH read
// when both exist) and that anything else contributes nothing.
func TestProbeGitGlobalConfigPaths(t *testing.T) {
	t.Parallel()
	home, dotfile, xdgFile := gitGlobalConfigFiles(t)

	xdgHome := t.TempDir()
	xdgConfig := filepath.Join(xdgHome, "git", "config")
	if err := os.MkdirAll(filepath.Dir(xdgConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgConfig, []byte("[user]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	emptyHome := t.TempDir()
	// A DIRECTORY named like the config file must never be admitted: granting it
	// would widen a file-exact grant into a tree.
	dirHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirHome, ".gitconfig"), 0o755); err != nil {
		t.Fatal(err)
	}
	envGlobal := filepath.Join(t.TempDir(), "env-global")
	if err := os.WriteFile(envGlobal, []byte("[user]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envDir := t.TempDir()

	for _, tc := range []struct {
		name   string
		system gitConfigProbeSystem
		want   []string
	}{
		{
			name:   "both files, XDG unset",
			system: gitConfigProbeSystem{home: home},
			want:   []string{xdgFile, dotfile},
		},
		{
			name:   "XDG_CONFIG_HOME redirects the first file",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"XDG_CONFIG_HOME": xdgHome}}, home: home},
			want:   []string{xdgConfig, dotfile},
		},
		{
			name:   "a relative XDG_CONFIG_HOME is ignored",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"XDG_CONFIG_HOME": "relative/config"}}, home: home},
			want:   []string{dotfile},
		},
		{
			name:   "no global config at all",
			system: gitConfigProbeSystem{home: emptyHome},
			want:   nil,
		},
		{
			name:   "a directory is never admitted",
			system: gitConfigProbeSystem{home: dirHome},
			want:   nil,
		},
		{
			name:   "no resolvable home",
			system: gitConfigProbeSystem{homeErr: errors.New("no home")},
			want:   nil,
		},
		{
			name:   "GIT_CONFIG_GLOBAL names the global config in place of the FILES",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_GLOBAL": envGlobal}}, home: home},
			want:   []string{envGlobal},
		},
		{
			name:   "an explicit empty GIT_CONFIG_GLOBAL names no config",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_GLOBAL": ""}}, home: home},
			want:   nil,
		},
		{
			name:   "a relative GIT_CONFIG_GLOBAL contributes nothing",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_GLOBAL": "config"}}, home: home},
			want:   nil,
		},
		{
			name:   "a missing GIT_CONFIG_GLOBAL contributes nothing",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_GLOBAL": filepath.Join(home, "absent")}}, home: home},
			want:   nil,
		},
		{
			name:   "a directory GIT_CONFIG_GLOBAL contributes nothing",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_GLOBAL": envDir}}, home: home},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeGitGlobalConfigPaths(tc.system); !slices.Equal(got, tc.want) {
				t.Errorf("probeGitGlobalConfigPaths = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProbeGitSystemConfigPaths pins that only an actual override that git will
// read is granted: a non-empty absolute existing FILE, with GIT_CONFIG_NOSYSTEM
// not enabled. The built-in /etc/gitconfig already sits under a restricted
// system read root.
func TestProbeGitSystemConfigPaths(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	systemConfig := filepath.Join(home, "gitconfig-system")
	if err := os.WriteFile(systemConfig, []byte("[user]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		system gitConfigProbeSystem
		want   []string
	}{
		{
			name:   "an absolute existing override is granted",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig}}, home: home},
			want:   []string{systemConfig},
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM=1 disables the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "1"}}, home: home},
			want:   nil,
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM=true disables the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "true"}}, home: home},
			want:   nil,
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM=0 leaves the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "0"}}, home: home},
			want:   []string{systemConfig},
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM=0x0 leaves the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "0x0"}}, home: home},
			want:   []string{systemConfig},
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM=0x1 disables the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "0x1"}}, home: home},
			want:   nil,
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM=010 disables the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "010"}}, home: home},
			want:   nil,
		},
		{
			name:   "GIT_CONFIG_NOSYSTEM with a leading space and 0 leaves the override",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": " 0"}}, home: home},
			want:   []string{systemConfig},
		},
		{
			name:   "a value git rejects as a boolean grants nothing because git will not run",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": systemConfig, "GIT_CONFIG_NOSYSTEM": "maybe"}}, home: home},
			want:   nil,
		},
		{
			name:   "a relative override contributes nothing",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": "gitconfig-system"}}, home: home},
			want:   nil,
		},
		{
			name:   "a missing override contributes nothing",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": filepath.Join(home, "absent")}}, home: home},
			want:   nil,
		},
		{
			name:   "an empty override contributes nothing",
			system: gitConfigProbeSystem{stubProbeSystem: stubProbeSystem{env: map[string]string{"GIT_CONFIG_SYSTEM": ""}}, home: home},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeGitSystemConfigPaths(tc.system); !slices.Equal(got, tc.want) {
				t.Errorf("probeGitSystemConfigPaths = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRestrictedGrantsGitConfigOverridesToSpawnedOnly pins the resolver half of
// the GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM fix: both overrides reach the spawned
// layer (git must read the file the environment names) and neither reaches the
// file tools, which stay confined to the worktree.
func TestRestrictedGrantsGitConfigOverridesToSpawnedOnly(t *testing.T) {
	t.Parallel()
	home, dotfile, _ := gitGlobalConfigFiles(t)
	systemConfig := filepath.Join(home, "gitconfig-system")
	if err := os.WriteFile(systemConfig, []byte("[user]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := mainRepo(t)
	host := darwinHostWithGitGlobalConfig(dotfile)
	host.Home = home
	host.GitSystemConfigPaths = []string{systemConfig}

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, f := range []string{dotfile, systemConfig} {
		if !slices.Contains(rp.Spawned.ReadRoots, f) {
			t.Errorf("restricted spawned read roots must include git config %q, got %v", f, rp.Spawned.ReadRoots)
		}
		if slices.Contains(rp.FileTool.ReadRoots, f) {
			t.Errorf("git config %q must NOT reach the file-tool layer: %v", f, rp.FileTool.ReadRoots)
		}
	}
}

// TestGitSystemConfigOverrideLosesToTheDenylist pins that the env-named system
// file is subject to the same denylist precedence as the global files: a
// denylisted entry never survives as a spawned read root.
func TestGitSystemConfigOverrideLosesToTheDenylist(t *testing.T) {
	t.Parallel()
	home, dotfile, _ := gitGlobalConfigFiles(t)
	systemConfig := filepath.Join(home, "gitconfig-system")
	if err := os.WriteFile(systemConfig, []byte("[user]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := darwinHostWithGitGlobalConfig(dotfile)
	host.Home = home
	host.GitSystemConfigPaths = []string{systemConfig}

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, DenylistAdd: []string{systemConfig}}, host, mainRepo(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, systemConfig) {
		t.Errorf("a denylisted system git config must never survive as a read root: %v", rp.Spawned.ReadRoots)
	}
}

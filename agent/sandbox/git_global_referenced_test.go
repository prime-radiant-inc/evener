package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fixtureHomeProbeSystem is hostProbeSystem with the home and the per-user config
// dir pointed at a fixture, so probeHost derives the global git config facts — the
// config files AND the files they name — from that fixture, while the rest of the
// probe (bwrap capability, uname) stays real. It names no environment at all, so no
// XDG_CONFIG_HOME and no Go setting is read from the runner's real environment.
type fixtureHomeProbeSystem struct {
	hostProbeSystem
	home string
}

func (s fixtureHomeProbeSystem) userHomeDir() (string, error) { return s.home, nil }

func (fixtureHomeProbeSystem) getenv(string) string { return "" }

func (fixtureHomeProbeSystem) userConfigDir() (string, error) {
	return "", errors.New("fixture home has no per-user config dir")
}

// writeFixtureFile writes one fixture file, failing the test on error.
func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// probeReferencedPaths runs the referenced-file probe the way probeHost does: the
// config files are probed first and handed to it, so the test exercises the same
// call shape as production.
func probeReferencedPaths(system probeSystem) []string {
	return probeGitGlobalReferencedPaths(system, probeGitGlobalConfigPaths(system))
}

// TestProbeGitGlobalReferencedPaths pins which files the probe admits from a global
// git config's core.excludesFile and core.attributesFile: existing non-directory
// files, `~/x` expanded against the home exactly as git expands it, the LAST
// assignment of each key winning, and nothing else — no relative value git would
// resolve against a working directory, no other user's home, no directory, no
// missing file, and no value read out of a section git does not read that key from.
func TestProbeGitGlobalReferencedPaths(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home, ignore, attrs string) []string
	}{
		{
			name: "tilde values for both keys",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = ~/ignore\n\tattributesFile = ~/attrs\n")
				return []string{ignore, attrs}
			},
		},
		{
			name: "absolute values",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = "+ignore+"\n\tattributesFile = "+attrs+"\n")
				return []string{ignore, attrs}
			},
		},
		{
			name: "quoted value with a trailing comment",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = \"~/ignore\" # a comment\n")
				return []string{ignore}
			},
		},
		{
			name: "the last assignment of a key wins",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, "first"), "first\n")
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = ~/first\n\texcludesFile = ~/ignore\n")
				return []string{ignore}
			},
		},
		{
			name: "the later config file wins",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				xdgFile := filepath.Join(home, ".config", "git", "config")
				if err := os.MkdirAll(filepath.Dir(xdgFile), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, filepath.Join(home, "xdg-ignore"), "xdg\n")
				writeFixtureFile(t, xdgFile, "[core]\n\texcludesFile = ~/xdg-ignore\n")
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/ignore\n")
				return []string{ignore}
			},
		},
		{
			name: "a relative value is refused",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = relative-ignore\n")
				return nil
			},
		},
		{
			name: "another user's home is refused",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~other/ignore\n")
				return nil
			},
		},
		{
			name: "a bare tilde is refused",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~\n")
				return nil
			},
		},
		{
			name: "a directory value is refused",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				if err := os.MkdirAll(filepath.Join(home, "attrs-dir"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\tattributesFile = ~/attrs-dir\n")
				return nil
			},
		},
		{
			name: "a missing file contributes nothing",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/not-there\n")
				return nil
			},
		},
		{
			name: "a subsection is not the bare core section",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core \"x\"]\n\texcludesFile = ~/ignore\n[core.excludesfile]\n\tpath = ~/attrs\n")
				return nil
			},
		},
		{
			name: "whitespace inside the brackets is not the bare section",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[ core ]\n\texcludesFile = ~/ignore\n")
				return nil
			},
		},
		{
			name: "the bare section is case-insensitive",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[CoRe]\n\texcludesFile = ~/ignore\n")
				return []string{ignore}
			},
		},
		{
			name: "a symlink to an existing file is admitted",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				link := filepath.Join(home, "ignore-link")
				if err := os.Symlink(ignore, link); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/ignore-link\n")
				return []string{link}
			},
		},
		{
			name: "a .. after a symlink keeps the spelling git hands the kernel",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				// ~/link -> <home>/subdir, so `~/link/../ignore` is the file git opens
				// (<home>/ignore) — but only after the kernel resolves the link. Cleaning
				// the value first would name <home>/ignore by a different route and could
				// grant a file git never opened; the probe must report the value as
				// written and let the resolver's confinement check judge it.
				subdir := filepath.Join(home, "subdir")
				if err := os.MkdirAll(subdir, 0o755); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(home, "link")
				if err := os.Symlink(subdir, link); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/link/../ignore\n")
				return []string{home + string(filepath.Separator) + "link" + string(filepath.Separator) + ".." + string(filepath.Separator) + "ignore"}
			},
		},
		{
			name: "both keys naming one file yield one entry",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = ~/ignore\n\tattributesFile = ~/ignore\n")
				return []string{ignore}
			},
		},
		{
			name: "many assignments yield one grant per key",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				var config strings.Builder
				config.WriteString("[core]\n")
				for i := range 200 {
					writeFixtureFile(t, filepath.Join(home, "many-"+strconv.Itoa(i)), "x\n")
					config.WriteString("\texcludesFile = ~/many-" + strconv.Itoa(i) + "\n")
				}
				config.WriteString("\tattributesFile = ~/attrs\n")
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), config.String())
				return []string{filepath.Join(home, "many-199"), attrs}
			},
		},
		{
			name: "an empty last assignment clears an earlier value",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = ~/ignore\n\texcludesFile =\n")
				return nil
			},
		},
		{
			name: "a quoted empty last assignment clears an earlier value",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"),
					"[core]\n\texcludesFile = ~/ignore\n\texcludesFile = \"\"\n")
				return nil
			},
		},
		{
			name: "a config larger than the bounded read names nothing",
			setup: func(t *testing.T, home, ignore, attrs string) []string {
				var config strings.Builder
				config.WriteString("[core]\n\texcludesFile = ~/ignore\n")
				for config.Len() <= goEnvFileLimit {
					config.WriteString("# padding past the probe's bounded read\n")
				}
				writeFixtureFile(t, filepath.Join(home, ".gitconfig"), config.String())
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, ignore, attrs := referencedFilesFixture(t)
			want := tc.setup(t, home, ignore, attrs)
			got := probeReferencedPaths(fixtureHomeProbeSystem{home: home})
			if !slices.Equal(got, want) {
				t.Errorf("probeGitGlobalReferencedPaths = %v, want %v", got, want)
			}
		})
	}
}

// TestProbeGitGlobalReferencedPathsNeedsAHome: no resolvable home contributes
// nothing, because a `~` value could not be expanded against one, and a directory
// named like a config file is not a config the probe reads.
func TestProbeGitGlobalReferencedPathsNeedsAHome(t *testing.T) {
	t.Parallel()

	if got := probeReferencedPaths(gitConfigProbeSystem{homeErr: errors.New("no home")}); got != nil {
		t.Errorf("no home must contribute nothing, got %v", got)
	}
	if got := probeReferencedPaths(fixtureHomeProbeSystem{home: ""}); got != nil {
		t.Errorf("an empty home must contribute nothing, got %v", got)
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gitconfig", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := probeReferencedPaths(fixtureHomeProbeSystem{home: home}); got != nil {
		t.Errorf("a directory named like a config file must contribute nothing, got %v", got)
	}
}

// TestBareSectionNameAndConfigValueLine pin the two config-reading helpers the core
// parse shares with the git-config include protection: a section is bare only when
// its header carries no subsection and no whitespace (git reads `core.excludesFile`
// from `[core]` and `[CoRe]` alone), and a key matches case-insensitively with a
// quoted or comment-trailing value handled the way git handles it.
func TestBareSectionNameAndConfigValueLine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		header string
		want   string
	}{
		{"[core]", "core"},
		{"[CoRe]", "CoRe"},
		{"[core \"x\"]", ""},
		{"[core.excludesfile]", "core.excludesfile"},
		{"[ core ]", ""},
		{"[core ]", ""},
		{"[includeIf \"gitdir:x\"]", ""},
	} {
		if got := bareSectionName(tc.header); got != tc.want {
			t.Errorf("bareSectionName(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}

	for _, tc := range []struct {
		line, key, want string
		assigned        bool
	}{
		{"excludesFile = ~/ignore", "excludesFile", "~/ignore", true},
		{"EXCLUDESFILE = ~/ignore", "excludesFile", "~/ignore", true},
		{"attributesFile = ~/attrs # comment", "attributesFile", "~/attrs", true},
		{"attributesFile = \"~/attrs\"", "attributesFile", "~/attrs", true},
		{"excludesFile =", "excludesFile", "", true},
		{"excludesFile = \"\"", "excludesFile", "", true},
		{"path = ../*.cfg", "excludesFile", "", false},
		{"not-a-key = x", "excludesFile", "", false},
	} {
		got, assigned := configValueLine(tc.line, tc.key)
		if got != tc.want || assigned != tc.assigned {
			t.Errorf("configValueLine(%q, %q) = (%q, %v), want (%q, %v)", tc.line, tc.key, got, assigned, tc.want, tc.assigned)
		}
	}
}

// TestGitConfigReferencedPath pins the value resolution on its own: only a `~/x`
// or absolute value names a file a grant can cover, because a relative value is one
// git resolves against the working directory of the git command (the admission
// check in the probe cannot pin this on its own — a relative path simply fails to
// exist relative to the probe's own working directory).
func TestGitConfigReferencedPath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value, want string
	}{
		{"~/ignore", "/home/u/ignore"},
		{"/etc/ignore", "/etc/ignore"},
		{"ignore", ""},
		{"sub/dir/ignore", ""},
		{"~other/ignore", ""},
		{"~", ""},
		{"", ""},
	} {
		if got := gitConfigReferencedPath(tc.value, "/home/u"); got != tc.want {
			t.Errorf("gitConfigReferencedPath(%q, %q) = %q, want %q", tc.value, "/home/u", got, tc.want)
		}
	}
}

// TestRestrictedGrantsGitGlobalReferencedFilesToSpawnedOnly is the resolver half of
// #4234: a restricted session's SPAWNED processes may read the files its global git
// config names, while the model's file tools stay confined to the worktree — the
// model gains no browse grant over anything in the home directory.
func TestRestrictedGrantsGitGlobalReferencedFilesToSpawnedOnly(t *testing.T) {
	t.Parallel()
	home, ignore, attrs := referencedFilesFixture(t)
	cwd := mainRepo(t)
	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{ignore, attrs}

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, f := range []string{ignore, attrs} {
		if !slices.Contains(rp.Spawned.ReadRoots, f) {
			t.Errorf("restricted spawned read roots must include the file the global config names, %q; got %v", f, rp.Spawned.ReadRoots)
		}
		if slices.Contains(rp.FileTool.ReadRoots, f) {
			t.Errorf("%q must NOT reach the file-tool layer: %v", f, rp.FileTool.ReadRoots)
		}
	}
	// File-exact: the grant is the FILES, never the directories holding them — a home
	// read is exactly what the ruling did not authorize.
	for _, dir := range []string{home, filepath.Dir(ignore)} {
		if slices.Contains(rp.Spawned.ReadRoots, dir) {
			t.Errorf("the grant must stay file-exact, but directory %q became a read root: %v", dir, rp.Spawned.ReadRoots)
		}
	}
}

// TestGlobalGitReferencedFilesNeverWidenTheWriteSurface pins the READ-ONLY half: the
// grant must leave both layers' write roots, the file-tool read roots, the protected
// git surfaces and the denylist byte-identical.
func TestGlobalGitReferencedFilesNeverWidenTheWriteSurface(t *testing.T) {
	t.Parallel()
	home, ignore, attrs := referencedFilesFixture(t)
	cwd := mainRepo(t)
	bare := darwinSeatbeltHost()
	bare.Home = home
	granted := bare
	granted.GitGlobalReferencedPaths = []string{ignore, attrs}

	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		without, err := Resolve(SandboxPolicy{Mode: mode}, bare, cwd)
		if err != nil {
			t.Fatalf("Resolve(%v, no referenced files): %v", mode, err)
		}
		with, err := Resolve(SandboxPolicy{Mode: mode}, granted, cwd)
		if err != nil {
			t.Fatalf("Resolve(%v, referenced files): %v", mode, err)
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

// TestGlobalGitReferencedFilesAreRestrictedModeOnly: the other modes' spawned layer
// already reads anywhere-minus-the-denylist, so the grant must add nothing there (a
// stray root list would turn ReadAnywhere into a roots-only surface for a backend
// that consults ReadRoots).
func TestGlobalGitReferencedFilesAreRestrictedModeOnly(t *testing.T) {
	t.Parallel()
	home, ignore, attrs := referencedFilesFixture(t)
	cwd := mainRepo(t)
	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{ignore, attrs}

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

// TestGlobalGitReferencedFileLosesToTheDenylist pins the precedence that keeps the
// grant safe: a referenced file the denylist covers never survives as a read root.
func TestGlobalGitReferencedFileLosesToTheDenylist(t *testing.T) {
	t.Parallel()
	home, ignore, _ := referencedFilesFixture(t)
	cwd := mainRepo(t)
	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{ignore}

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, DenylistAdd: []string{ignore}}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, ignore) {
		t.Errorf("a denylisted referenced file must never survive as a read root: %v", rp.Spawned.ReadRoots)
	}
}

// TestGlobalGitReferencedFileRefusesSharedTrees proves the grant reuses the
// RootGuard: a referenced path that names the home directory, an ancestor of it, or
// a one-component path is refused outright rather than granted, while the
// legitimate file beside it is still granted.
func TestGlobalGitReferencedFileRefusesSharedTrees(t *testing.T) {
	t.Parallel()
	home, ignore, _ := referencedFilesFixture(t)
	cwd := mainRepo(t)
	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{home, filepath.Dir(home), ignore}

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, refused := range []string{home, filepath.Dir(home)} {
		if slices.Contains(rp.Spawned.ReadRoots, refused) {
			t.Errorf("the guard must refuse shared tree %q, got read roots %v", refused, rp.Spawned.ReadRoots)
		}
	}
	if !slices.Contains(rp.Spawned.ReadRoots, ignore) {
		t.Errorf("the legitimate referenced file must still be granted: %v", rp.Spawned.ReadRoots)
	}
}

// TestGlobalGitReferencedSymlinkIsNeverGranted pins the hole a config-named symlink
// would otherwise open: bwrap's read-only bind of a symlink follows it and
// materializes the TARGET's content at the literal name (verified on the live
// kernel), and the session can rewrite a target inside its own worktree between
// spawns, so a referenced path that resolves through a symlink at all is refused —
// whether the target is masked or ordinary. The live integration test drives the
// retarget itself.
func TestGlobalGitReferencedSymlinkIsNeverGranted(t *testing.T) {
	t.Parallel()
	home, _, _ := referencedFilesFixture(t)
	cwd := mainRepo(t)

	secretDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secretDir, "id_rsa")
	writeFixtureFile(t, secret, "PRIVATE KEY\n")
	maskedLink := filepath.Join(home, "masked-link")
	if err := os.Symlink(secret, maskedLink); err != nil {
		t.Fatal(err)
	}
	dotfiles := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o755); err != nil {
		t.Fatal(err)
	}
	ordinary := filepath.Join(dotfiles, "ignore")
	writeFixtureFile(t, ordinary, "ignored.txt\n")
	ordinaryLink := filepath.Join(home, "ordinary-link")
	if err := os.Symlink(ordinary, ordinaryLink); err != nil {
		t.Fatal(err)
	}

	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{maskedLink, ordinaryLink}

	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, maskedLink) {
		t.Errorf("a referenced symlink whose target is masked must not become a read root: %v", rp.Spawned.ReadRoots)
	}
	if slices.Contains(rp.Spawned.ReadRoots, ordinaryLink) {
		t.Errorf("a referenced symlink must not become a read root even with an ordinary target — the session can re-point a name inside its own worktree: %v", rp.Spawned.ReadRoots)
	}
}

// TestConfinedReferencedFiles pins the admission the config-file grant does not need:
// a file a global config NAMES is granted only when nothing about its name can
// change, because bwrap binds that name afresh at every spawn. A name that resolves
// through a symlink (or through a `..` the kernel resolves after one), or that sits
// inside a root the session may write, is refused — the grant would otherwise be a
// way to read whatever the session later points it at.
func TestConfinedReferencedFiles(t *testing.T) {
	t.Parallel()
	home := clean(t.TempDir())
	writable := filepath.Join(home, "work")
	subdir := filepath.Join(home, "subdir")
	for _, dir := range []string{writable, subdir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plain := filepath.Join(home, "plain")
	writeFixtureFile(t, plain, "plain\n")
	link := filepath.Join(home, "link")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	inWritable := filepath.Join(writable, "swappable")
	writeFixtureFile(t, inWritable, "writable\n")
	linkIntoWritable := filepath.Join(home, "link-into-writable")
	if err := os.Symlink(inWritable, linkIntoWritable); err != nil {
		t.Fatal(err)
	}
	// A `..` after a symlink to a directory OUTSIDE the home: git opens the file where
	// the kernel's resolution lands, not where lexical cleaning would put it.
	elsewhere := clean(t.TempDir())
	if err := os.MkdirAll(filepath.Join(elsewhere, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(elsewhere, "ignore"), "elsewhere\n")
	elseLink := filepath.Join(home, "else-link")
	if err := os.Symlink(filepath.Join(elsewhere, "subdir"), elseLink); err != nil {
		t.Fatal(err)
	}
	dotdot := elseLink + string(filepath.Separator) + ".." + string(filepath.Separator) + "ignore"

	got := confinedReferencedFiles([]string{plain, link, inWritable, linkIntoWritable, dotdot}, []string{writable})
	if want := []string{plain}; !slices.Equal(got, want) {
		t.Errorf("confinedReferencedFiles = %v, want %v", got, want)
	}
}

// TestExtraReadRootSymlinkIntoAMaskedTargetIsDropped pins the resolved-path rule
// for a root that has nothing to do with git config, since filterMasked applies it
// to every read root: a policy-supplied read root reached through a symlink is
// judged at the path it really exposes, because bwrap's read-only bind of a symlink
// follows it and materializes the target's content at the literal name.
func TestExtraReadRootSymlinkIntoAMaskedTargetIsDropped(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	secretDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secretDir, "id_rsa")
	writeFixtureFile(t, secret, "PRIVATE KEY\n")
	maskedLink := filepath.Join(home, "link-into-mask")
	if err := os.Symlink(secret, maskedLink); err != nil {
		t.Fatal(err)
	}
	ordinary := filepath.Join(home, "ordinary")
	writeFixtureFile(t, ordinary, "ordinary\n")
	ordinaryLink := filepath.Join(home, "ordinary-link")
	if err := os.Symlink(ordinary, ordinaryLink); err != nil {
		t.Fatal(err)
	}

	host := darwinSeatbeltHost()
	host.Home = home
	rp, err := Resolve(SandboxPolicy{
		Mode:           ModeRestricted,
		ExtraReadRoots: []string{maskedLink, ordinaryLink},
	}, host, mainRepo(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, layer := range []struct {
		name  string
		roots []string
	}{
		{"spawned", rp.Spawned.ReadRoots},
		{"file tool", rp.FileTool.ReadRoots},
	} {
		if slices.Contains(layer.roots, maskedLink) {
			t.Errorf("%s read roots must drop a root whose resolved target is masked: %v", layer.name, layer.roots)
		}
		if !slices.Contains(layer.roots, ordinaryLink) {
			t.Errorf("%s read roots must keep a root whose target is ordinary: %v", layer.name, layer.roots)
		}
	}
}

// TestGlobalGitReferencedFileIsConfinedBeforeTheSharedTreeGuard pins the ORDER of the
// two admissions. The shared-tree guard cleans each candidate, so a value whose
// cleaned spelling is a real path of its own — a `..` the kernel resolves after a
// symlink — must be judged before that cleaning: otherwise the grant lands on
// whatever the cleaned spelling names, which here is a whole DIRECTORY rather than the
// file git opens, breaking the file-exact boundary.
func TestGlobalGitReferencedFileIsConfinedBeforeTheSharedTreeGuard(t *testing.T) {
	t.Parallel()
	home := clean(t.TempDir())
	elsewhere := clean(t.TempDir())
	if err := os.MkdirAll(filepath.Join(elsewhere, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(elsewhere, "notes"), "notes\n")
	link := filepath.Join(home, "link")
	if err := os.Symlink(filepath.Join(elsewhere, "subdir"), link); err != nil {
		t.Fatal(err)
	}
	// `~/link/../notes` is <elsewhere>/notes to the kernel, while its cleaned spelling
	// is <home>/notes — made a directory here so a grant of it would expose a tree.
	dir := filepath.Join(home, "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{link + string(filepath.Separator) + ".." + string(filepath.Separator) + "notes"}
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, host, mainRepo(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, dir) {
		t.Errorf("the cleaned spelling of a `..`-after-a-symlink value must never be granted, least of all as a directory: %v", rp.Spawned.ReadRoots)
	}
}

// TestGlobalGitReferencedFileUnderAWritableAliasIsRefused pins the writable-root
// containment check at the RESOLVED spelling too: an extra writable root the user
// configured as a symlink makes the files beneath its target writable through the
// alias, so a config-named file there can be swapped for a link to anything at any
// time. unmaskedContentRoots already judges writable roots both ways.
func TestGlobalGitReferencedFileUnderAWritableAliasIsRefused(t *testing.T) {
	t.Parallel()
	home := clean(t.TempDir())
	storage := clean(t.TempDir())
	referenced := filepath.Join(storage, "ignore")
	writeFixtureFile(t, referenced, "ignored.txt\n")
	alias := filepath.Join(home, "writable-alias")
	if err := os.Symlink(storage, alias); err != nil {
		t.Fatal(err)
	}

	host := darwinSeatbeltHost()
	host.Home = home
	host.GitGlobalReferencedPaths = []string{referenced}
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, ExtraWritableRoots: []string{alias}}, host, mainRepo(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if slices.Contains(rp.Spawned.ReadRoots, referenced) {
		t.Errorf("a file inside a root the session may write THROUGH A SYMLINK must not be granted: %v", rp.Spawned.ReadRoots)
	}
}

// referencedFilesFixture materializes a home holding two referenced files (one per
// core key) and returns their paths.
func referencedFilesFixture(t *testing.T) (home, ignore, attrs string) {
	t.Helper()
	// Canonicalized, because the confinement rule compares a path with what it
	// resolves to: on macOS an uncleaned t.TempDir() spelling (/var/folders/...) has
	// already been resolved through /var by the filesystem.
	home = clean(t.TempDir())
	ignore = filepath.Join(home, "ignore")
	attrs = filepath.Join(home, "attrs")
	writeFixtureFile(t, ignore, "ignored.txt\n")
	writeFixtureFile(t, attrs, "*.txt text\n")
	return home, ignore, attrs
}

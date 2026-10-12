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
			got := probeGitGlobalReferencedPaths(fixtureHomeProbeSystem{home: home})
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

	if got := probeGitGlobalReferencedPaths(gitConfigProbeSystem{homeErr: errors.New("no home")}); got != nil {
		t.Errorf("no home must contribute nothing, got %v", got)
	}
	if got := probeGitGlobalReferencedPaths(fixtureHomeProbeSystem{home: ""}); got != nil {
		t.Errorf("an empty home must contribute nothing, got %v", got)
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gitconfig", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := probeGitGlobalReferencedPaths(fixtureHomeProbeSystem{home: home}); got != nil {
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
	}{
		{"excludesFile = ~/ignore", "excludesFile", "~/ignore"},
		{"EXCLUDESFILE = ~/ignore", "excludesFile", "~/ignore"},
		{"attributesFile = ~/attrs # comment", "attributesFile", "~/attrs"},
		{"attributesFile = \"~/attrs\"", "attributesFile", "~/attrs"},
		{"path = ../*.cfg", "excludesFile", ""},
		{"not-a-key = x", "excludesFile", ""},
		{"excludesFile =", "excludesFile", ""},
	} {
		if got := configValueLine(tc.line, tc.key); got != tc.want {
			t.Errorf("configValueLine(%q, %q) = %q, want %q", tc.line, tc.key, got, tc.want)
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

// TestGlobalGitReferencedSymlinkIsJudgedAtItsTarget pins the fix for the hole a
// config-named symlink would otherwise open: bwrap's read-only bind of a symlink
// follows it and materializes the TARGET's content at the literal name (verified on
// the live kernel), so a referenced path whose resolved target is masked must never
// become a read root — while a symlink whose target is ordinary is still granted.
func TestGlobalGitReferencedSymlinkIsJudgedAtItsTarget(t *testing.T) {
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
	if !slices.Contains(rp.Spawned.ReadRoots, ordinaryLink) {
		t.Errorf("a referenced symlink whose target is ordinary must still be granted: %v", rp.Spawned.ReadRoots)
	}
}

// referencedFilesFixture materializes a home holding two referenced files (one per
// core key) and returns their paths.
func referencedFilesFixture(t *testing.T) (home, ignore, attrs string) {
	t.Helper()
	home = t.TempDir()
	ignore = filepath.Join(home, "ignore")
	attrs = filepath.Join(home, "attrs")
	writeFixtureFile(t, ignore, "ignored.txt\n")
	writeFixtureFile(t, attrs, "*.txt text\n")
	return home, ignore, attrs
}

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The probe lists the plugin store's installed plugin files and the user skills
// directory under Evener's config root, wherever XDG_CONFIG_HOME puts it.
func TestProbeFindsEvenerContentRoots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		root string
	}{
		{"default", nil, "/Users/tester/.config/evener"},
		{"XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg/evener"},
		// Evener resolves a relative XDG_CONFIG_HOME against its working
		// directory, and the probe records that absolute root.
		{"relative XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "rel"}, mustAbs(filepath.Join("rel", "evener"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := probeHost(stubProbeSystem{env: tc.env})
			want := []string{tc.root + "/plugins/cache", tc.root + "/plugins/bundled", tc.root + "/skills"}
			if !slices.Equal(facts.EvenerContentRoots, want) {
				t.Errorf("probe = %v, want %v", facts.EvenerContentRoots, want)
			}
		})
	}
}

// A session run with EVENER_NO_USER_SKILLS=1 loads no user skills, so the probe
// leaves that directory out; installed plugins stay, since their hooks run.
func TestProbeLeavesUserSkillsOutWhenTheyAreNotLoaded(t *testing.T) {
	t.Parallel()
	facts := probeHost(stubProbeSystem{env: map[string]string{"EVENER_NO_USER_SKILLS": "1"}})
	if slices.Contains(facts.EvenerContentRoots, "/Users/tester/.config/evener/skills") {
		t.Errorf("user skills must be left out when EVENER_NO_USER_SKILLS=1: %v", facts.EvenerContentRoots)
	}
	if !slices.Contains(facts.EvenerContentRoots, "/Users/tester/.config/evener/plugins/cache") {
		t.Errorf("installed plugins must stay: %v", facts.EvenerContentRoots)
	}
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return abs
}

// contentHost is a bwrap host whose installed plugins and user skills sit where
// they do by default: inside the masked ~/.config/evener.
func contentHost() (HostFacts, string, string) {
	host := bwrapHost()
	store := filepath.Join(testHome, ".config", "evener", "plugins")
	skills := filepath.Join(testHome, ".config", "evener", "skills")
	host.EvenerContentRoots = []string{filepath.Join(store, "cache"), filepath.Join(store, "bundled"), skills}
	return host, store, skills
}

// The plugin store and the user skills directory hold content the session
// loads (skills it hands the model by BaseDirectory, hook scripts it runs), so
// every mode reads them, read-only, through the ~/.config/evener credential
// mask (#4171). The rest of ~/.config/evener stays masked.
func TestEvenerContentRootsAreReadableThroughTheCredentialMask(t *testing.T) {
	root := mainRepo(t)
	host, store, skills := contentHost()
	skillFile := filepath.Join(store, "cache", "mkt", "plugin", "abc", "skills", "review", "template.md")
	userSkillFile := filepath.Join(skills, "mine", "SKILL.md")
	credential := filepath.Join(testHome, ".config", "evener", "hub.toml")
	// The store's own metadata records marketplace URLs, which may carry tokens.
	metadata := []string{filepath.Join(store, "known_marketplaces.json"), filepath.Join(store, "marketplaces", "mkt", ".git", "config")}
	// An installed copy cloned from git keeps its remote URL, which may carry a
	// token, in its .git; skills and hooks never need it, so it stays masked.
	metadata = append(metadata,
		filepath.Join(store, "cache", "mkt", "plugin", "abc", ".git", "config"),
		filepath.Join(store, "cache", "mkt", "plugin", "abc", ".git"),
		filepath.Join(skills, "mine", ".git", "config"),
		// On a case-insensitive filesystem .GIT is the same directory.
		filepath.Join(store, "cache", "mkt", "plugin", "abc", ".GIT", "config"))
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		t.Run(mode.String(), func(t *testing.T) {
			rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(true)}, host, root)
			for _, path := range []string{skillFile, userSkillFile} {
				if !rp.FileToolCanRead(path) {
					t.Errorf("the file tools must read %q", path)
				}
				if rp.Masks(path) {
					t.Errorf("%q must not be masked", path)
				}
			}
			for _, path := range append([]string{credential}, metadata...) {
				if rp.FileToolCanRead(path) || !rp.Masks(path) {
					t.Errorf("the rest of ~/.config/evener, store metadata included, must stay masked: %q", path)
				}
			}
			for _, w := range slices.Concat(rp.FileTool.WriteRoots, rp.Spawned.WriteRoots) {
				if pathUnder(w, store) || pathUnder(store, w) {
					t.Errorf("the plugin store must never be writable: write root %q", w)
				}
			}
		})
	}
}

// A content root may not unmask what it contains, sit under the pseudo-fs
// floor, or reach above the home directory, worktree or temp roots.
func TestEvenerContentRootsRefuseRootsThatWouldUnmaskSecrets(t *testing.T) {
	root := mainRepo(t)
	for _, bad := range []string{
		filepath.Join(testHome, ".config", "evener"),
		filepath.Join(testHome, ".config"),
		testHome,
		"/proc/self",
		"relative/plugins",
	} {
		t.Run(bad, func(t *testing.T) {
			host := bwrapHost()
			host.EvenerContentRoots = []string{bad}
			rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
			if len(rp.UnmaskedRoots) != 0 {
				t.Errorf("content root %q must be refused, got %v", bad, rp.UnmaskedRoots)
			}
			if !rp.Masks(filepath.Join(testHome, ".config", "evener", "hub.toml")) {
				t.Errorf("content root %q must not unmask the credential directory", bad)
			}
		})
	}
}

// When XDG_CONFIG_HOME moves Evener's config root, the mask follows it (the
// credentials live there), and the carve-out is cut from that mask instead.
func TestEvenerContentRootsCarveOutInsideARelocatedConfigRoot(t *testing.T) {
	root := mainRepo(t)
	host := bwrapHost()
	host.EvenerConfigRoot = "/xdg/evener"
	host.EvenerContentRoots = []string{"/xdg/evener/plugins/cache", "/xdg/evener/plugins/bundled", "/xdg/evener/skills"}
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(true)}, host, root)
		if !rp.Masks("/xdg/evener/hub.toml") || !rp.Masks("/xdg/evener/plugins/known_marketplaces.json") {
			t.Errorf("%v: the relocated config root must be masked: %v", mode, rp.MaskedPaths)
		}
		if rp.Masks("/xdg/evener/plugins/cache/mkt/p/abc/SKILL.md") || !rp.FileToolCanRead("/xdg/evener/skills/mine/SKILL.md") {
			t.Errorf("%v: the carve-out must apply inside the relocated config root: %v", mode, rp.UnmaskedRoots)
		}
	}
}

// A content root the session could write through (inside the worktree, say)
// would not stay read-only, so it is refused and stays masked.
func TestEvenerContentRootsUnderAWriteRootAreRefused(t *testing.T) {
	root := mainRepo(t)
	host := bwrapHost()
	config := filepath.Join(root, ".xdg", "evener")
	host.EvenerConfigRoot = config
	host.EvenerContentRoots = []string{filepath.Join(config, "plugins", "cache")}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("a content root under a write root must be refused, got %v", rp.UnmaskedRoots)
	}
	if !rp.Masks(filepath.Join(config, "plugins", "cache", "x")) {
		t.Errorf("the refused content root must stay masked")
	}
}

// The carve-out cuts only Evener's own config mask. A denylist entry the user
// added above it (masking all of ~/.config) keeps the content masked.
func TestEvenerContentRootsHonourAUserMaskAboveThem(t *testing.T) {
	root := mainRepo(t)
	host, store, _ := contentHost()
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{"~/.config"}}, host, root)
	if len(rp.UnmaskedRoots) != 0 || !rp.Masks(filepath.Join(store, "cache", "x")) {
		t.Errorf("a user mask above the content roots must keep them masked, got %v", rp.UnmaskedRoots)
	}
}

// A content root that resolves, through a symlink, into another masked
// directory is refused: re-granting it would expose that directory.
func TestEvenerContentRootsRefuseASymlinkIntoAnotherMask(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(ssh, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".config", "evener")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ssh, filepath.Join(config, "plugins")); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{filepath.Join(config, "plugins", "cache")}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("a content root resolving into ~/.ssh must be refused, got %v", rp.UnmaskedRoots)
	}
}

// A content root that is itself a symlink pointing elsewhere inside Evener's
// config mask (to the config root, or to the plugin store with its metadata)
// is refused: the backends re-grant the resolved path, which would expose the
// credentials and marketplace URLs the carve-out keeps masked.
func TestEvenerContentRootsRefuseASymlinkWithinTheConfigMask(t *testing.T) {
	root := mainRepo(t)
	for _, target := range []string{".", "plugins"} {
		t.Run(target, func(t *testing.T) {
			home := clean(t.TempDir())
			config := filepath.Join(home, ".config", "evener")
			store := filepath.Join(config, "plugins")
			if err := os.MkdirAll(store, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "hub.toml"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(store, "cache")
			if err := os.Symlink(filepath.Join(config, target), cache); err != nil {
				t.Fatal(err)
			}
			host := bwrapHost()
			host.Home = home
			host.EvenerContentRoots = []string{cache}
			rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
			if len(rp.UnmaskedRoots) != 0 {
				t.Errorf("a content root symlinked to %q inside the config mask must be refused, got %v", target, rp.UnmaskedRoots)
			}
			// The backends re-check each spawn (a symlink can appear after
			// resolution) with the same predicate.
			resolved, err := filepath.EvalSymlinks(cache)
			if err != nil {
				t.Fatal(err)
			}
			if !carveOutEscapes(resolved, cache, rp.MaskedPaths) {
				t.Errorf("the backends' re-check must refuse %q resolving to %q", cache, resolved)
			}
		})
	}
}

// A carve-out that resolves to an ancestor of a masked path (cache -> ~/.config,
// holding ~/.config/gcloud) is refused even when Evener's config mask itself was
// removed from the denylist, since re-granting it would expose that mask.
func TestEvenerContentRootsRefuseAResolutionAboveAMask(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	dotConfig := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(dotConfig, "gcloud"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dotConfig, "evener", "plugins")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(store, "cache")
	if err := os.Symlink(dotConfig, cache); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistRemove: []string{".config/evener"}}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("a content root resolving above ~/.config/gcloud must be refused, got %v", rp.UnmaskedRoots)
	}
}

// A denylist entry the user added for a relocated config root is the user's
// boundary, not Evener's automatic config mask, so it is not carved.
func TestEvenerContentRootsHonourAUserMaskOnTheRelocatedConfigRoot(t *testing.T) {
	root := mainRepo(t)
	host := bwrapHost()
	host.EvenerConfigRoot = "/xdg/evener"
	host.EvenerContentRoots = []string{"/xdg/evener/plugins/cache"}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{"/xdg/evener"}}, host, root)
	if len(rp.UnmaskedRoots) != 0 || !rp.Masks("/xdg/evener/plugins/cache/x") {
		t.Errorf("a user mask on the relocated config root must keep its content masked, got %v", rp.UnmaskedRoots)
	}
}

// A masked path that is itself a symlink into a content root (~/.ssh pointing at
// a directory under the skills dir) would make its secrets readable through the
// carve-out, so that content root is refused.
func TestEvenerContentRootsRefuseOneHoldingAMasksTarget(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	skills := filepath.Join(home, ".config", "evener", "skills")
	keys := filepath.Join(skills, "mine", "ssh")
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keys, filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{skills}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("a content root holding ~/.ssh's target must be refused, got %v", rp.UnmaskedRoots)
	}
}

// Until the backends re-check carve-outs at each spawn, only the file tools get
// them: spawned processes keep the whole mask, so their read roots (which a
// backend grants at their canonical path) never include a carve-out.
func TestEvenerContentRootsStayOutOfTheSpawnedLayer(t *testing.T) {
	root := mainRepo(t)
	host, store, _ := contentHost()
	plugin := filepath.Join(store, "cache", "mkt", "plugin", "abc")
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true), InfraReadRoots: []string{plugin}}, host, root)
	for _, r := range rp.Spawned.ReadRoots {
		if isUnderAnyRoot(r, rp.UnmaskedRoots) {
			t.Errorf("spawned read root %q lies in a carve-out: %v", r, rp.Spawned.ReadRoots)
		}
	}
}

// With the home reached through a symlink (macOS /var -> /private/var, a
// symlinked /home), the config mask is listed under both spellings, and the
// carve-out still applies under both: the file tools see an opened directory by
// its real path.
func TestEvenerContentRootsSurviveASymlinkedHome(t *testing.T) {
	root := mainRepo(t)
	realHome := clean(t.TempDir())
	linkHome := filepath.Join(clean(t.TempDir()), "home-link")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(linkHome, ".config", "evener", "plugins", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = linkHome
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	realCache := filepath.Join(realHome, ".config", "evener", "plugins", "cache")
	for _, p := range []string{filepath.Join(cache, "x"), filepath.Join(realCache, "x")} {
		if rp.Masks(p) {
			t.Errorf("%q must not be masked: unmasked %v, masked %v", p, rp.UnmaskedRoots, rp.MaskedPaths)
		}
	}
	if !rp.Masks(filepath.Join(realHome, ".config", "evener", "hub.toml")) {
		t.Errorf("the config root must stay masked under its real spelling: %v", rp.MaskedPaths)
	}
}

// A writable root that is a symlink to a content root would let writes reach
// the carve-out through its alias, so overlap is judged by resolved location.
func TestEvenerContentRootsRefuseOneAWritableRootAliases(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(clean(t.TempDir()), "writable-alias")
	if err := os.Symlink(cache, alias); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{alias}}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("a content root a writable root aliases must be refused, got %v", rp.UnmaskedRoots)
	}
}

// .git stays masked under a content root's resolved spelling too (a symlinked
// config directory), not just its literal one.
func TestEvenerContentRootsKeepGitMaskedUnderTheResolvedSpelling(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	realConfig := filepath.Join(clean(t.TempDir()), "srv-evener-config")
	if err := os.MkdirAll(filepath.Join(realConfig, "plugins", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(home, ".config", "evener")); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{filepath.Join(home, ".config", "evener", "plugins", "cache")}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, host, root)
	realCache := filepath.Join(realConfig, "plugins", "cache")
	if rp.Masks(filepath.Join(realCache, "p", "SKILL.md")) {
		t.Errorf("ordinary content must stay readable under the resolved spelling: %v / %v", rp.UnmaskedRoots, rp.MaskedPaths)
	}
	if !rp.Masks(filepath.Join(realCache, "p", ".git", "config")) {
		t.Errorf(".git must stay masked under the resolved spelling")
	}
}

// Adding ~/.config/evener to the denylist yourself is your own boundary, so
// nothing is carved out of it.
func TestEvenerContentRootsHonourAnExplicitConfigMask(t *testing.T) {
	root := mainRepo(t)
	host, _, _ := contentHost()
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{".config/evener"}}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("an explicit ~/.config/evener mask must keep its content masked, got %v", rp.UnmaskedRoots)
	}
}

// An explicit denylist entry naming the real location of a symlinked config
// root is the user's boundary under both spellings, so nothing is carved.
func TestEvenerContentRootsHonourAnExplicitMaskOfTheRealConfigRoot(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	realConfig := filepath.Join(clean(t.TempDir()), "real-evener")
	if err := os.MkdirAll(filepath.Join(realConfig, "plugins", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(home, ".config", "evener")); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{filepath.Join(home, ".config", "evener", "plugins", "cache")}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{realConfig}}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("an explicit mask of the real config root must keep its content masked, got %v", rp.UnmaskedRoots)
	}
}

// A content root that does not exist yet still gets its resolved spelling
// registered (from its existing ancestors), so content installed mid-session
// under a symlinked home reads by its canonical path too.
func TestEvenerContentRootsRegisterAMissingRootsResolvedSpelling(t *testing.T) {
	root := mainRepo(t)
	realHome := clean(t.TempDir())
	linkHome := filepath.Join(clean(t.TempDir()), "home-link")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realHome, ".config", "evener", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = linkHome
	host.EvenerContentRoots = []string{filepath.Join(linkHome, ".config", "evener", "plugins", "cache")}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if rp.Masks(filepath.Join(realHome, ".config", "evener", "plugins", "cache", "new", "SKILL.md")) {
		t.Errorf("a missing root's resolved spelling must be carved out too: %v", rp.UnmaskedRoots)
	}
}

// A content root missing at resolution is pinned to where creating it would
// land: installing it there later reads normally, while redirecting an
// enclosing directory before its first read cannot move the grant.
func TestCarveOutPinsAMissingRootToWhereItWouldLand(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	config := filepath.Join(home, ".config", "evener")
	cache := filepath.Join(config, "plugins", "cache")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if !slices.Contains(rp.UnmaskedRoots, cache) {
		t.Fatalf("a missing content root must still be carved out: %v", rp.UnmaskedRoots)
	}
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if !rp.CarveOutIntact(cache) {
		t.Errorf("a root installed in place mid-session must be granted")
	}
	elsewhere := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "plugins", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(config, filepath.Join(home, "evener-moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, config); err != nil {
		t.Fatal(err)
	}
	if rp.CarveOutIntact(cache) {
		t.Errorf("a missing root whose enclosing directory was redirected must not be granted")
	}
}

// Under a symlinked home whose ~/.config/evener does not exist yet, the
// content roots are still carved out (the missing mask is resolved through
// its existing ancestors like the roots are), content installed later reads
// under both spellings, and the rest of the config stays masked under both.
func TestEvenerContentRootsBeforeTheConfigDirExistsUnderASymlinkedHome(t *testing.T) {
	root := mainRepo(t)
	realHome := clean(t.TempDir())
	linkHome := filepath.Join(clean(t.TempDir()), "home-link")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(linkHome, ".config", "evener", "plugins", "cache")
	host := bwrapHost()
	host.Home = linkHome
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if !slices.Contains(rp.UnmaskedRoots, cache) {
		t.Fatalf("a content root under a missing config dir must be carved out: %v", rp.UnmaskedRoots)
	}
	realCache := filepath.Join(realHome, ".config", "evener", "plugins", "cache")
	if err := os.MkdirAll(realCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if !rp.CarveOutIntact(cache) {
		t.Errorf("content installed in place must be granted")
	}
	for _, p := range []string{filepath.Join(cache, "p", "SKILL.md"), filepath.Join(realCache, "p", "SKILL.md")} {
		if rp.Masks(p) {
			t.Errorf("%s must be readable", p)
		}
	}
	for _, p := range []string{filepath.Join(linkHome, ".config", "evener", "hub.toml"), filepath.Join(realHome, ".config", "evener", "hub.toml")} {
		if !rp.Masks(p) {
			t.Errorf("%s must stay masked", p)
		}
	}
}

// On a case-insensitive volume (macOS's default), denylisting the config root
// under another case is the same directory, so nothing is carved from it.
func TestEvenerContentRootsHonourACaseVariantConfigMask(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".CONFIG", "EVENER")); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{".CONFIG/EVENER"}}, host, root)
	if len(rp.UnmaskedRoots) != 0 {
		t.Errorf("a case-variant denylist entry for the config root must keep its content masked, got %v", rp.UnmaskedRoots)
	}
}

// On a case-insensitive volume, a denylist entry naming a content root (or a
// directory inside one) under another case is the same directory, so that
// root is not carved out and stays masked.
func TestEvenerContentRootsHonourACaseVariantMaskOfAContentRoot(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	skills := filepath.Join(home, ".config", "evener", "skills")
	if err := os.MkdirAll(filepath.Join(skills, "mine", "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "evener", "SKILLS")); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{skills}
	for _, entry := range []string{".config/evener/SKILLS", ".config/evener/skills/MINE/PRIVATE"} {
		rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{entry}}, host, root)
		if slices.Contains(rp.UnmaskedRoots, skills) {
			t.Errorf("denylisting %s must keep the skills dir masked, got %v", entry, rp.UnmaskedRoots)
		}
		if !rp.Masks(filepath.Join(skills, "mine", "private", "notes.md")) {
			t.Errorf("denylisting %s: the denied directory must stay masked", entry)
		}
	}
}

// Letter case is ignored only where resolution saw a case-insensitive volume.
// On a case-sensitive one, a carve-out swapped after resolution for a link to a
// separate directory spelled in another case is a different place, refused.
func TestCarveOutRefusesACaseVariantRedirectOnACaseSensitiveVolume(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".CONFIG")); err == nil {
		t.Skip("this volume is case-insensitive")
	}
	other := filepath.Join(home, ".config", "evener", "plugins", "CACHE")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{cache}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if err := os.Rename(cache, cache+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, cache); err != nil {
		t.Fatal(err)
	}
	if rp.CarveOutIntact(cache) || rp.CarveOutResolvesTo(cache, other) {
		t.Errorf("a case-variant directory on a case-sensitive volume must not pass for the carve-out")
	}
}

// An explicit denylist entry for the config root spelled in another case, made
// while the directory did not exist, is still the user's boundary once the
// directory is created: it is never mistaken for Evener's own config mask.
func TestCarveOutHonoursACaseVariantConfigMaskCreatedLater(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".CONFIG")); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	skills := filepath.Join(home, ".config", "evener", "skills")
	host := bwrapHost()
	host.Home = home
	host.EvenerContentRoots = []string{skills}
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistAdd: []string{".CONFIG/EVENER"}}, host, root)
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(rp.UnmaskedRoots, skills) && rp.CarveOutIntact(skills) {
		t.Errorf("an explicit case-variant config mask must keep the skills dir masked once it exists")
	}
}

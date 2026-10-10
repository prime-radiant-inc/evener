package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
		filepath.Join(skills, "mine", ".git", "config"))
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
			if rp.Spawned.Read != ReadAnywhere && !slices.Contains(rp.Spawned.ReadRoots, filepath.Join(store, "cache")) {
				t.Errorf("spawned processes must read the plugin store, got roots %v", rp.Spawned.ReadRoots)
			}
			for _, w := range slices.Concat(rp.FileTool.WriteRoots, rp.Spawned.WriteRoots) {
				if pathUnder(w, store) || pathUnder(store, w) {
					t.Errorf("the plugin store must never be writable: write root %q", w)
				}
			}
		})
	}
}

// A hook script installed in the plugin store is a hook/MCP read root under the
// mask; with the store carved out it survives resolution in restricted mode,
// whose spawned layer reads only its roots.
func TestPluginHookRootsSurviveResolutionInsideThePluginStore(t *testing.T) {
	root := mainRepo(t)
	host, store, _ := contentHost()
	plugin := filepath.Join(store, "cache", "mkt", "plugin", "abc")
	rp := mustResolve(t, SandboxPolicy{Mode: ModeRestricted, Network: new(true), InfraReadRoots: []string{plugin}}, host, root)
	if !slices.Contains(rp.Spawned.ReadRoots, plugin) {
		t.Errorf("an installed plugin's hook root must stay a spawned read root: %v", rp.Spawned.ReadRoots)
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

// The probe finds Evener's config root (under $XDG_CONFIG_HOME, else
// ~/.config) and, inside it, the installed plugins (the store's cache and
// bundled directories, not its marketplace metadata) and the user skills.
func TestProbeFindsEvenerConfigAndContentRoots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		root string
	}{
		{"default", nil, "/Users/tester/.config/evener"},
		{"XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg/evener"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := probeHost(stubProbeSystem{env: tc.env})
			want := []string{tc.root + "/plugins/cache", tc.root + "/plugins/bundled", tc.root + "/skills"}
			if facts.EvenerConfigRoot != tc.root || !slices.Equal(facts.EvenerContentRoots, want) {
				t.Errorf("probe = %q %v, want %q %v", facts.EvenerConfigRoot, facts.EvenerContentRoots, tc.root, want)
			}
		})
	}
}

// evenerContentFixture is a home whose ~/.config/evener holds a plugin skill,
// that plugin's hook script, a user skill and a stand-in credential file, for
// the backend tests that check the carve-out with a real sandbox.
type evenerContentFixture struct {
	config, store, skills, plugin     string
	template, hook, userSkill, secret string
	metadata, gitConfig               string
}

// contentRoots are the roots the probe would report for this home.
func (f evenerContentFixture) contentRoots() []string {
	return []string{filepath.Join(f.store, "cache"), filepath.Join(f.store, "bundled"), f.skills}
}

func writeEvenerContentFixture(t *testing.T, home string) evenerContentFixture {
	t.Helper()
	f := evenerContentFixture{config: filepath.Join(home, ".config", "evener")}
	f.store = filepath.Join(f.config, "plugins")
	f.skills = filepath.Join(f.config, "skills")
	f.plugin = filepath.Join(f.store, "cache", "mkt", "plugin", "abc")
	f.template = filepath.Join(f.plugin, "skills", "review", "template.md")
	f.hook = filepath.Join(f.plugin, "hooks", "start.sh")
	f.userSkill = filepath.Join(f.skills, "mine", "SKILL.md")
	f.secret = filepath.Join(f.config, "hub.toml")
	f.metadata = filepath.Join(f.store, "known_marketplaces.json")
	f.gitConfig = filepath.Join(f.plugin, ".git", "config")
	for path, body := range map[string]string{f.gitConfig: "[remote \"origin\"]\n\turl = https://user:token@example.com/p.git\n", f.template: "template\n", f.hook: "#!/bin/sh\necho HOOK-RAN\n", f.userSkill: "user skill\n", f.secret: "token = 'x'\n", f.metadata: `{"mkt":{"source":{"url":"https://user:token@example.com/m.git"}}}`} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// evenerContentScript reads both skills, runs the hook, tries to write into the
// store and to read the credential file, the store's marketplace metadata and
// the installed copy's .git/config (a bwrap mask over a file reads as empty,
// hence the -s), printing a marker for each outcome.
const evenerContentScript = `set -u
test "$(cat "$1")" = template && echo SKILL-READ
test "$(cat "$2")" = "user skill" && echo USER-SKILL-READ
"$3"
if (printf x > "$(dirname "$1")/planted") 2>/dev/null; then echo STORE-WRITABLE; fi
if cat "$4" >/dev/null 2>&1 && test -s "$4"; then echo SECRET-VISIBLE; fi
if cat "$5" >/dev/null 2>&1 && test -s "$5"; then echo METADATA-VISIBLE; fi
if cat "$6" >/dev/null 2>&1 && test -s "$6"; then echo PLUGIN-GIT-VISIBLE; fi`

// scriptCommand is the confined command running evenerContentScript.
func (f evenerContentFixture) scriptCommand(shell string) []string {
	return []string{shell, "-c", evenerContentScript, "content-test", f.template, f.userSkill, f.hook, f.secret, f.metadata, f.gitConfig}
}

func assertEvenerContentOutput(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"SKILL-READ", "USER-SKILL-READ", "HOOK-RAN"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s: the sandbox must read and run Evener content:\n%s", want, out)
		}
	}
	for _, bad := range []string{"STORE-WRITABLE", "SECRET-VISIBLE", "METADATA-VISIBLE", "PLUGIN-GIT-VISIBLE"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: the store must stay read-only and the rest of ~/.config/evener masked:\n%s", bad, out)
		}
	}
}

// When XDG_CONFIG_HOME moves Evener's config root, the mask follows it (the
// credentials live there), and the carve-out is cut from that mask instead.
func TestEvenerConfigRootIsMaskedWhereverItLives(t *testing.T) {
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

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

// The probe names the installed plugins (the store's cache and bundled
// directories, not its marketplace metadata) and the user skills directory
// where Evener puts them: under $XDG_CONFIG_HOME/evener, else ~/.config/evener.
func TestProbeEvenerContentRootsFollowTheConfigRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"default", nil, []string{"/Users/tester/.config/evener/plugins/cache", "/Users/tester/.config/evener/plugins/bundled", "/Users/tester/.config/evener/skills"}},
		{"XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, []string{"/xdg/evener/plugins/cache", "/xdg/evener/plugins/bundled", "/xdg/evener/skills"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeEvenerContentRoots(stubProbeSystem{env: tc.env}); !slices.Equal(got, tc.want) {
				t.Errorf("probeEvenerContentRoots = %v, want %v", got, tc.want)
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
	metadata                          string
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
	for path, body := range map[string]string{f.template: "template\n", f.hook: "#!/bin/sh\necho HOOK-RAN\n", f.userSkill: "user skill\n", f.secret: "token = 'x'\n", f.metadata: `{"mkt":{"source":{"url":"https://user:token@example.com/m.git"}}}`} {
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
// store and to read the credential file and the store's marketplace metadata (a
// bwrap mask over a file reads as empty, hence the -s), printing a marker for
// each outcome.
const evenerContentScript = `set -u
test "$(cat "$1")" = template && echo SKILL-READ
test "$(cat "$2")" = "user skill" && echo USER-SKILL-READ
"$3"
if (printf x > "$(dirname "$1")/planted") 2>/dev/null; then echo STORE-WRITABLE; fi
if cat "$4" >/dev/null 2>&1 && test -s "$4"; then echo SECRET-VISIBLE; fi
if cat "$5" >/dev/null 2>&1 && test -s "$5"; then echo METADATA-VISIBLE; fi`

// scriptCommand is the confined command running evenerContentScript.
func (f evenerContentFixture) scriptCommand(shell string) []string {
	return []string{shell, "-c", evenerContentScript, "content-test", f.template, f.userSkill, f.hook, f.secret, f.metadata}
}

func assertEvenerContentOutput(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"SKILL-READ", "USER-SKILL-READ", "HOOK-RAN"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s: the sandbox must read and run Evener content:\n%s", want, out)
		}
	}
	for _, bad := range []string{"STORE-WRITABLE", "SECRET-VISIBLE", "METADATA-VISIBLE"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: the store must stay read-only and the rest of ~/.config/evener masked:\n%s", bad, out)
		}
	}
}

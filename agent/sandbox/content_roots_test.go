package sandbox

import (
	"path/filepath"
	"slices"
	"testing"
)

// contentHost is a bwrap host whose Evener plugin store and user skills
// directory sit where they do by default: inside the masked ~/.config/evener.
func contentHost() (HostFacts, string, string) {
	host := bwrapHost()
	store := filepath.Join(testHome, ".config", "evener", "plugins")
	skills := filepath.Join(testHome, ".config", "evener", "skills")
	host.EvenerContentRoots = []string{store, skills}
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
			if rp.FileToolCanRead(credential) || !rp.Masks(credential) {
				t.Errorf("the rest of ~/.config/evener must stay masked: %q", credential)
			}
			if rp.Spawned.Read != ReadAnywhere && !slices.Contains(rp.Spawned.ReadRoots, store) {
				t.Errorf("spawned processes must read the plugin store, got roots %v", rp.Spawned.ReadRoots)
			}
			for _, w := range slices.Concat(rp.FileTool.WriteRoots, rp.Spawned.WriteRoots) {
				if w == store || pathUnder(w, store) || pathUnder(store, w) {
					t.Errorf("the plugin store must never be writable: write root %q", w)
				}
			}
		})
	}
}

// A hook script installed in the plugin store is a hook/MCP read root under the
// mask; with the store carved out it now survives resolution in restricted mode,
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

// The probe finds the plugin store and user skills directory where Evener puts
// them: under $XDG_CONFIG_HOME/evener, else ~/.config/evener.
func TestProbeEvenerContentRootsFollowTheConfigRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"default", nil, []string{"/Users/tester/.config/evener/plugins", "/Users/tester/.config/evener/skills"}},
		{"XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, []string{"/xdg/evener/plugins", "/xdg/evener/skills"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeEvenerContentRoots(stubProbeSystem{env: tc.env}); !slices.Equal(got, tc.want) {
				t.Errorf("probeEvenerContentRoots = %v, want %v", got, tc.want)
			}
		})
	}
}

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// A plugin skill's BaseDirectory and the user's own skills sit inside the masked
// ~/.config/evener by default. The file tools must read them in every mode,
// read-only, while the rest of ~/.config/evener stays masked (#4171).
func TestFileToolsReadEvenerContentThroughTheCredentialMask(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			home := realTempDir(t)
			worktree := filepath.Join(home, "project")
			config := filepath.Join(home, ".config", "evener")
			store := filepath.Join(config, "plugins")
			skills := filepath.Join(config, "skills")
			template := filepath.Join(store, "cache", "mkt", "plugin", "abc", "skills", "review", "template.md")
			userSkill := filepath.Join(skills, "mine", "SKILL.md")
			secret := filepath.Join(config, "hub.toml")
			metadata := filepath.Join(store, "known_marketplaces.json")
			pluginGit := filepath.Join(store, "cache", "mkt", "plugin", "abc", ".git", "config")
			for path, body := range map[string]string{pluginGit: "url = https://user:token@example.com/p.git\n", template: "template\n", userSkill: "user skill\n", secret: "token = 'x'\n", metadata: "{}\n"} {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(worktree, 0o755); err != nil {
				t.Fatal(err)
			}
			host := sbTestHost(home)
			host.EvenerContentRoots = []string{filepath.Join(store, "cache"), filepath.Join(store, "bundled"), skills}
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, worktree)
			if err != nil {
				t.Fatalf("Resolve(%v): %v", mode, err)
			}
			env := NewLocalExecutionEnvironment(worktree)
			env.Sandbox = &rp
			t.Cleanup(env.Cleanup)

			for path, want := range map[string]string{template: "template", userSkill: "user skill"} {
				if got, err := env.ReadFile(path, nil, nil); err != nil || !strings.Contains(got, want) {
					t.Errorf("read_file %s: got %q err %v", path, got, err)
				}
			}
			if entries, err := env.ListDirectory(filepath.Dir(template), 1); err != nil || len(entries) == 0 {
				t.Errorf("list_dir of the skill's BaseDirectory: %d entries, err %v", len(entries), err)
			}
			_, err = env.ReadFile(secret, nil, nil)
			mustDenied(t, err, "read_file of the rest of ~/.config/evener")
			_, err = env.ReadFile(metadata, nil, nil)
			mustDenied(t, err, "read_file of the plugin store's marketplace metadata")
			_, err = env.ReadFile(pluginGit, nil, nil)
			mustDenied(t, err, "read_file of an installed plugin's .git/config")
			_, err = env.WriteFile(filepath.Join(filepath.Dir(template), "planted.md"), "x")
			mustDenied(t, err, "write_file into the plugin store")
		})
	}
}

// A content root swapped for a symlink after resolution (here, to an unrelated
// directory outside every mask) must not become a door for restricted file tools:
// the root is re-validated when the tools first open it.
func TestFileToolsRefuseAContentRootRedirectedAfterResolution(t *testing.T) {
	t.Parallel()
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	elsewhere := filepath.Join(home, "elsewhere")
	for _, dir := range []string{worktree, filepath.Dir(cache), elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "private.txt"), []byte("not plugin content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{cache}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.Symlink(elsewhere, cache); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	_, err = env.ReadFile(filepath.Join(cache, "private.txt"), nil, nil)
	mustDenied(t, err, "read_file through a content root redirected after resolution")
}

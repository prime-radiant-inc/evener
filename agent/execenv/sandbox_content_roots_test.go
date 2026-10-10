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

// The re-validation also judges the directory actually opened, not only the
// path checked just before: rootFd compares the opened fd's canonical path with
// the carve-out's place, so a swap between the check and the open is refused.
func TestFileToolsJudgeTheOpenedCarveOutRoot(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	elsewhere := filepath.Join(home, "elsewhere")
	for _, dir := range []string{worktree, cache, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{cache}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !rp.CarveOutResolvesTo(cache, cache) {
		t.Fatalf("the carve-out's own path must be accepted")
	}
	if rp.CarveOutResolvesTo(cache, elsewhere) {
		t.Errorf("a carve-out opened at %q must be refused", elsewhere)
	}
}

// Redirecting the enclosing config directory after resolution (swapping
// ~/.config/evener for a link elsewhere) must not move what the carve-out
// grants: the root is held to the location it resolved to at session start.
func TestFileToolsHoldACarveOutToItsResolvedLocation(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	config := filepath.Join(home, ".config", "evener")
	cache := filepath.Join(config, "plugins", "cache")
	elsewhere := filepath.Join(home, "elsewhere")
	for _, dir := range []string{worktree, cache, filepath.Join(elsewhere, "plugins", "cache")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "plugins", "cache", "private.txt"), []byte("not granted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{cache}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.Rename(config, filepath.Join(home, "evener-moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, config); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	_, err = env.ReadFile(filepath.Join(cache, "private.txt"), nil, nil)
	mustDenied(t, err, "read_file through a carve-out whose config directory was redirected")
}

// With the home reached through a symlink, the file tools read a skill under
// both spellings of its path.
func TestFileToolsReadACarveOutUnderBothSpellings(t *testing.T) {
	realHome := realTempDir(t)
	linkHome := filepath.Join(realTempDir(t), "home-link")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(realHome, "project")
	skill := filepath.Join(realHome, ".config", "evener", "skills", "mine", "SKILL.md")
	for _, dir := range []string{worktree, filepath.Dir(skill)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skill, []byte("user skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(linkHome)
	host.EvenerContentRoots = []string{filepath.Join(linkHome, ".config", "evener", "skills")}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	for _, p := range []string{filepath.Join(linkHome, ".config", "evener", "skills", "mine", "SKILL.md"), skill} {
		if got, err := env.ReadFile(p, nil, nil); err != nil || !strings.Contains(got, "user skill") {
			t.Errorf("read_file %s: got %q err %v", p, got, err)
		}
	}
}

// The carve-out is read-only however a write reaches it: a writable root that
// becomes a symlink to the plugin cache after resolution must not let the file
// tools write into the cache.
func TestFileToolsNeverWriteACarveOutThroughAWritableAlias(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	for _, dir := range []string{worktree, cache} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(home, "alias")
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{cache}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, ExtraWritableRoots: []string{alias}}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.Symlink(cache, alias); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	for _, p := range []string{filepath.Join(cache, "planted.md"), filepath.Join(alias, "planted.md")} {
		_, err := env.WriteFile(p, "x")
		mustDenied(t, err, "write_file %s into the carve-out", p)
	}
	if _, err := os.Stat(filepath.Join(cache, "planted.md")); !os.IsNotExist(err) {
		t.Errorf("nothing may be written into the carve-out: %v", err)
	}
}

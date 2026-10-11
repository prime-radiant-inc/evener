package execenv

import (
	"fmt"
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

// On a case-insensitive volume, a denylist entry spelled in another case for a
// skills dir that does not exist yet still wins once the dir is created: the
// file tools judge the carve-out against the user's masks when they open it.
func TestFileToolsHonourACaseVariantMaskOfASkillsDirCreatedLater(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	config := filepath.Join(home, ".config", "evener")
	for _, dir := range []string{worktree, config} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".CONFIG", "EVENER")); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	skills := filepath.Join(config, "skills")
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, DenylistAdd: []string{".config/evener/SKILLS"}}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	private := filepath.Join(skills, "mine", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(private), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	_, err = env.ReadFile(private, nil, nil)
	mustDenied(t, err, "read_file under a case-variant denylisted skills dir created after resolution")
}

// On a case-insensitive volume, a home spelled in another case than on disk
// still reads its skills: the opened directory's real casing is the same
// directory as the pinned location.
func TestFileToolsReadACarveOutUnderAHomeSpelledInAnotherCase(t *testing.T) {
	parent := realTempDir(t)
	home := filepath.Join(parent, "casehome")
	skill := filepath.Join(home, ".config", "evener", "skills", "mine", "SKILL.md")
	worktree := filepath.Join(home, "project")
	for _, dir := range []string{worktree, filepath.Dir(skill)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skill, []byte("user skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spelled := filepath.Join(parent, "CASEHOME")
	if _, err := os.Stat(spelled); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	host := sbTestHost(spelled)
	host.EvenerContentRoots = []string{filepath.Join(spelled, ".config", "evener", "skills")}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, filepath.Join(spelled, "project"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(filepath.Join(spelled, "project"))
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	path := filepath.Join(spelled, ".config", "evener", "skills", "mine", "SKILL.md")
	if got, err := env.ReadFile(path, nil, nil); err != nil || !strings.Contains(got, "user skill") {
		t.Errorf("read_file %s: got %q err %v", path, got, err)
	}
}

// A read grant inside a carve-out never becomes an anchor of its own: reads
// there anchor at the validated carve-out root, so redirecting the carve-out
// after resolution cannot steer them elsewhere.
func TestFileToolsAnchorAReadGrantInsideACarveOutAtTheCarveOut(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	config := filepath.Join(home, ".config", "evener")
	cache := filepath.Join(config, "plugins", "cache")
	elsewhere := filepath.Join(home, "elsewhere")
	for _, dir := range []string{worktree, filepath.Join(cache, "mkt"), filepath.Join(elsewhere, "mkt")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "mkt", "private.txt"), []byte("not granted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{cache}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, ExtraReadRoots: []string{filepath.Join(cache, "mkt")}}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.Rename(cache, cache+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, cache); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	_, err = env.ReadFile(filepath.Join(cache, "mkt", "private.txt"), nil, nil)
	mustDenied(t, err, "read_file through a read grant inside a carve-out redirected after resolution")
}

// In the read-anywhere modes a path spelled in another case reaches the opened
// file without a carve-out anchor; a case-variant denylist entry for a skills
// dir created after resolution must still deny it.
func TestFileToolsHonourACaseVariantMaskThroughAnotherSpellingInReadAnywhereModes(t *testing.T) {
	for _, mode := range []sandbox.Mode{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite} {
		t.Run(mode.String(), func(t *testing.T) {
			home := realTempDir(t)
			worktree := filepath.Join(home, "project")
			config := filepath.Join(home, ".config", "evener")
			for _, dir := range []string{worktree, config} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(home, ".CONFIG", "EVENER")); err != nil {
				t.Skip("this volume is case-sensitive")
			}
			skills := filepath.Join(config, "skills")
			host := sbTestHost(home)
			host.EvenerContentRoots = []string{skills}
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, DenylistAdd: []string{".config/evener/SKILLS"}}, host, worktree)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			private := filepath.Join(skills, "mine", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(private), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(private, []byte("private\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			env := NewLocalExecutionEnvironment(worktree)
			env.Sandbox = &rp
			t.Cleanup(env.Cleanup)
			for _, p := range []string{private, filepath.Join(home, ".CONFIG", "EVENER", "skills", "mine", "SKILL.md")} {
				_, err = env.ReadFile(p, nil, nil)
				mustDenied(t, err, "read_file %s under a case-variant denylisted skills dir", p)
			}
		})
	}
}

// A credential mask redirected into a carve-out after the file tools opened
// it (~/.ssh made a link into the skills dir) is refused on the next read, as
// a fresh spawn refuses it, and reads come back once the link is gone.
func TestFileToolsRefuseACachedCarveOutAMaskNowPointsInto(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	skills := filepath.Join(home, ".config", "evener", "skills")
	keys := filepath.Join(skills, "mine", "ssh")
	for _, dir := range []string{worktree, keys} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skill := filepath.Join(skills, "mine", "SKILL.md")
	key := filepath.Join(keys, "id_ed25519")
	for path, body := range map[string]string{skill: "user skill\n", key: "PRIVATE KEY\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if _, err := env.ReadFile(skill, nil, nil); err != nil {
		t.Fatalf("priming read of the skill: %v", err)
	}
	link := filepath.Join(home, ".ssh")
	if err := os.Symlink(keys, link); err != nil {
		t.Fatal(err)
	}
	_, err = env.ReadFile(key, nil, nil)
	mustDenied(t, err, "read_file of a key a credential mask now points to inside the carve-out")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReadFile(skill, nil, nil); err != nil || !strings.Contains(got, "user skill") {
		t.Errorf("reads must resume once the mask no longer points into the carve-out: %q %v", got, err)
	}
}

// A credential mask pointed into a carve-out after resolution is refused
// whichever root the read is anchored at: here a writable root that became a
// link to the carve-out, which the carve-out's own root checks never see.
func TestFileToolsRefuseAMaskPointedIntoACarveOutThroughAWritableAlias(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	skills := filepath.Join(home, ".config", "evener", "skills")
	keys := filepath.Join(skills, "mine", "ssh")
	for _, dir := range []string{worktree, keys} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(keys, "id_ed25519"), []byte("PRIVATE KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, ExtraWritableRoots: []string{alias}}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.Symlink(skills, alias); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".ssh")
	if err := os.Symlink(keys, link); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	_, err = env.ReadFile(filepath.Join(alias, "mine", "ssh", "id_ed25519"), nil, nil)
	mustDenied(t, err, "read_file of a mask's new target through a writable alias of the carve-out")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReadFile(filepath.Join(skills, "mine", "ssh", "id_ed25519"), nil, nil); err != nil || !strings.Contains(got, "PRIVATE KEY") {
		t.Errorf("reads must resume once the mask no longer points into the carve-out: %q %v", got, err)
	}
}

// A carve-out root replaced in place after the file tools opened it (moved
// aside and recreated at its location) is read again from the new directory:
// the stale cached descriptor is dropped rather than refusing forever.
func TestFileToolsReopenACarveOutRecreatedInPlace(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	skills := filepath.Join(home, ".config", "evener", "skills")
	for _, dir := range []string{worktree, filepath.Join(skills, "mine")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skill := filepath.Join(skills, "mine", "SKILL.md")
	if err := os.WriteFile(skill, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if _, err := env.ReadFile(skill, nil, nil); err != nil {
		t.Fatalf("priming read: %v", err)
	}
	if err := os.Rename(skills, skills+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skills, "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReadFile(skill, nil, nil); err != nil || !strings.Contains(got, "new") {
		t.Errorf("a carve-out recreated in place must read from the new directory: %q %v", got, err)
	}
}

// On macOS the file tools read a skill named through the data-volume firmlink
// spelling of a configured config root, in every mode.
func TestFileToolsReadACarveOutThroughTheFirmlinkSpelling(t *testing.T) {
	home := realTempDir(t)
	alias := "/System/Volumes/Data" + home
	if _, err := os.Stat(alias); err != nil {
		t.Skip("no data-volume firmlink spelling on this host")
	}
	worktree := filepath.Join(home, "project")
	skill := filepath.Join(home, ".config", "evener", "skills", "mine", "SKILL.md")
	for _, dir := range []string{worktree, filepath.Dir(skill)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skill, []byte("user skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			host := sandbox.HostFacts{OS: "darwin", Home: home, SandboxExecPath: "/fixture/sandbox-exec"}
			host.EvenerConfigRoot = filepath.Join(alias, ".config", "evener")
			host.EvenerContentRoots = []string{filepath.Join(alias, ".config", "evener", "skills")}
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, worktree)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			env := NewLocalExecutionEnvironment(worktree)
			env.Sandbox = &rp
			t.Cleanup(env.Cleanup)
			path := filepath.Join(alias, ".config", "evener", "skills", "mine", "SKILL.md")
			if got, err := env.ReadFile(path, nil, nil); err != nil || !strings.Contains(got, "user skill") {
				t.Errorf("read_file %s: got %q err %v", path, got, err)
			}
		})
	}
}

// The descriptors of carve-out roots replaced in place are closed once no
// file-tool operation holds the layer, so repeated replacement in a long
// session does not pile them up.
func TestFileToolsCloseStaleCarveOutFdsOnceDrained(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	skills := filepath.Join(home, ".config", "evener", "skills")
	skill := filepath.Join(skills, "mine", "SKILL.md")
	for _, dir := range []string{worktree, filepath.Dir(skill)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skill, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	for i := range 3 {
		if _, err := env.ReadFile(skill, nil, nil); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if err := os.Rename(skills, fmt.Sprintf("%s.old%d", skills, i)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skill, []byte("v\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.ReadFile(skill, nil, nil); err != nil {
		t.Fatalf("final read: %v", err)
	}
	env.sbMu.Lock()
	sfs := env.sbfs
	env.sbMu.Unlock()
	sfs.mu.Lock()
	stale := len(sfs.staleFds)
	sfs.mu.Unlock()
	if stale != 0 {
		t.Errorf("stale carve-out fds must be closed once drained, %d still held", stale)
	}
}

// On a normalization-insensitive volume (APFS) a home configured in one Unicode
// normalization names a directory stored in the other; the file tools read its
// skills, since locations compare in the filesystem's own spelling.
func TestFileToolsReadACarveOutUnderAHomeSpelledInAnotherNormalization(t *testing.T) {
	parent := realTempDir(t)
	stored := filepath.Join(parent, "café") // NFD on disk
	skill := filepath.Join(stored, ".config", "evener", "skills", "mine", "SKILL.md")
	if err := os.MkdirAll(filepath.Join(stored, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("user skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(parent, "café") // NFC in the environment
	if _, err := os.Stat(configured); err != nil {
		t.Skip("this volume is normalization-sensitive")
	}
	host := sbTestHost(configured)
	host.EvenerContentRoots = []string{filepath.Join(configured, ".config", "evener", "skills")}
	worktree := filepath.Join(configured, "project")
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	path := filepath.Join(configured, ".config", "evener", "skills", "mine", "SKILL.md")
	if got, err := env.ReadFile(path, nil, nil); err != nil || !strings.Contains(got, "user skill") {
		t.Errorf("read_file %s: got %q err %v", path, got, err)
	}
}

// With home configured in another letter case than it is stored (CASEHOME vs
// casehome), a plugin's .git stays masked when read through the stored
// spelling, while its skills stay readable under both.
func TestFileToolsMaskGitUnderTheStoredSpellingOfAHome(t *testing.T) {
	parent := realTempDir(t)
	home := filepath.Join(parent, "casehome")
	plugin := filepath.Join(home, ".config", "evener", "plugins", "cache", "mkt", "p")
	for _, d := range []string{filepath.Join(home, "project"), filepath.Join(plugin, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{filepath.Join(plugin, ".git", "config"): "url = https://u:token@x/p.git\n", filepath.Join(plugin, "SKILL.md"): "skill\n"} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spelled := filepath.Join(parent, "CASEHOME")
	if _, err := os.Stat(spelled); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			host := sbTestHost(spelled)
			host.EvenerContentRoots = []string{filepath.Join(spelled, ".config", "evener", "plugins", "cache")}
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, filepath.Join(spelled, "project"))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			env := NewLocalExecutionEnvironment(filepath.Join(spelled, "project"))
			env.Sandbox = &rp
			t.Cleanup(env.Cleanup)
			for _, h := range []string{home, spelled} {
				p := strings.Replace(filepath.Join(plugin, ".git", "config"), home, h, 1)
				_, err := env.ReadFile(p, nil, nil)
				mustDenied(t, err, "read_file %s", p)
				s := strings.Replace(filepath.Join(plugin, "SKILL.md"), home, h, 1)
				if got, err := env.ReadFile(s, nil, nil); err != nil || !strings.Contains(got, "skill") {
					t.Errorf("read_file %s: %q %v", s, got, err)
				}
			}
		})
	}
}

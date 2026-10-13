package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evenerContentFixture is a home whose ~/.config/evener holds a plugin skill,
// that plugin's hook script, a user skill and a stand-in credential file, for
// the backend tests that check the carve-out with a real sandbox.
type evenerContentFixture struct {
	config, store, skills, plugin     string
	template, hook, userSkill, secret string
	metadata, gitConfig               string
	// linkedGitConfig, when set, is the config of a directory inside the
	// store that another plugin's .git links to, read by its own path.
	linkedGitConfig string
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

// writeSymlinkedEvenerContentFixture is writeEvenerContentFixture with
// ~/.config/evener a symlink to a directory elsewhere (a dotfile-managed
// config). The fixture's paths are the literal spellings under home, the ones
// skill discovery advertises.
func writeSymlinkedEvenerContentFixture(t *testing.T, home string) evenerContentFixture {
	t.Helper()
	return writeEvenerContentFixtureLinkedTo(t, home, filepath.Join(home, "dotfiles", "evener"))
}

// writeGitAncestryEvenerContentFixture is writeSymlinkedEvenerContentFixture
// with the real config dir inside a dotfiles repository's .git directory, so
// a .git component sits above the carve-outs, not inside them.
func writeGitAncestryEvenerContentFixture(t *testing.T, home string) evenerContentFixture {
	t.Helper()
	return writeEvenerContentFixtureLinkedTo(t, home, filepath.Join(home, "dotfiles", ".git", "evener"))
}

// writeEvenerContentFixtureLinkedTo writes the fixture with ~/.config/evener
// a symlink to realConfig.
func writeEvenerContentFixtureLinkedTo(t *testing.T, home, realConfig string) evenerContentFixture {
	t.Helper()
	if err := os.MkdirAll(realConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(home, ".config", "evener")); err != nil {
		t.Fatal(err)
	}
	return writeEvenerContentFixture(t, home)
}

// writeLinkedGitEvenerContentFixture is writeEvenerContentFixture plus a
// second plugin whose .git is a symlink to real.git elsewhere in the store,
// whose config holds a credential too.
func writeLinkedGitEvenerContentFixture(t *testing.T, home string) evenerContentFixture {
	t.Helper()
	f := writeEvenerContentFixture(t, home)
	realGit := filepath.Join(f.store, "cache", "mkt", "linked", "real.git")
	f.linkedGitConfig = filepath.Join(realGit, "config")
	linked := filepath.Join(f.store, "cache", "mkt", "linked", "plugin")
	for _, d := range []string{realGit, linked} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.linkedGitConfig, []byte("[remote \"origin\"]\n\turl = https://user:token@example.com/l.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realGit, filepath.Join(linked, ".git")); err != nil {
		t.Fatal(err)
	}
	return f
}

// evenerContentLayouts are the config layouts the backend content tests run.
var evenerContentLayouts = map[string]func(*testing.T, string) evenerContentFixture{
	"plain":            writeEvenerContentFixture,
	"symlinked-config": writeSymlinkedEvenerContentFixture,
	"linked-git":       writeLinkedGitEvenerContentFixture,
	"git-ancestry":     writeGitAncestryEvenerContentFixture,
}

// evenerContentScript reads both skills, runs the hook, tries to write into the
// store and to read the credential file, the store's marketplace metadata and
// the installed copy's .git/config (a bwrap mask over a file reads as empty,
// hence the -s), and, when the layout has one, the config a symlinked .git
// leads to read by its own path, printing a marker for each outcome.
const evenerContentScript = `set -u
test "$(cat "$1")" = template && echo SKILL-READ
test "$(cat "$2")" = "user skill" && echo USER-SKILL-READ
"$3"
if (printf x > "$(dirname "$1")/planted") 2>/dev/null; then echo STORE-WRITABLE; fi
if cat "$4" >/dev/null 2>&1 && test -s "$4"; then echo SECRET-VISIBLE; fi
if cat "$5" >/dev/null 2>&1 && test -s "$5"; then echo METADATA-VISIBLE; fi
if cat "$6" >/dev/null 2>&1 && test -s "$6"; then echo PLUGIN-GIT-VISIBLE; fi
if test -n "$7" && cat "$7" >/dev/null 2>&1 && test -s "$7"; then echo LINKED-GIT-VISIBLE; fi`

// scriptCommand is the confined command running evenerContentScript.
func (f evenerContentFixture) scriptCommand(shell string) []string {
	return []string{shell, "-c", evenerContentScript, "content-test", f.template, f.userSkill, f.hook, f.secret, f.metadata, f.gitConfig, f.linkedGitConfig}
}

func assertEvenerContentOutput(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"SKILL-READ", "USER-SKILL-READ", "HOOK-RAN"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s: the sandbox must read and run Evener content:\n%s", want, out)
		}
	}
	for _, bad := range []string{"STORE-WRITABLE", "SECRET-VISIBLE", "METADATA-VISIBLE", "PLUGIN-GIT-VISIBLE", "LINKED-GIT-VISIBLE"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: the store must stay read-only and the rest of ~/.config/evener masked:\n%s", bad, out)
		}
	}
}

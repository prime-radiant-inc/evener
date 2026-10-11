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
	realConfig := filepath.Join(home, "dotfiles", "evener")
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

// evenerContentLayouts are the config layouts the backend content tests run.
var evenerContentLayouts = map[string]func(*testing.T, string) evenerContentFixture{
	"plain":            writeEvenerContentFixture,
	"symlinked-config": writeSymlinkedEvenerContentFixture,
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

package agent

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

func TestSetMemoryFrontmatterField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw, line, want string }{
		{"no frontmatter gains a block", "# Title\nbody\n", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\n---\n# Title\nbody\n"},
		{"appends a missing key, keeps every other byte", "---\ndescription: d\nodd:   spacing  \n---\nbody", "by: s1\n",
			"---\ndescription: d\nodd:   spacing  \nby: s1\n---\nbody"},
		{"replaces an existing key and its continuation lines", "---\nupdated: |\n  old\n  older\ntags:\n- a\n---\nb\n", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\ntags:\n- a\n---\nb\n"},
		{"does not match a longer key", "---\nbyline: x\n---\n", "by: s1\n",
			"---\nbyline: x\nby: s1\n---\n"},
		{"empty block", "---\n---\nbody\n", "by: s1\n", "---\nby: s1\n---\nbody\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(setMemoryFrontmatterField([]byte(tc.raw), tc.line)); got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// yaml.v3 quotes what needs quoting and never wraps a long value.
func TestMemoryYAMLFieldRoundTrips(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"Money is: cents", "- dash", "#hash", "2026-10-08", strings.Repeat("word ", 40) + "end"} {
		raw := setMemoryFrontmatterField([]byte("body\n"), memoryYAMLField("description", value))
		if got := parseMemoryPage("a.md", raw, time.Time{}); !got.HasDescription || got.Description != value {
			t.Fatalf("value %q read back as %+v from %q", value, got, raw)
		}
	}
}

// A page is found by a link or a bare path; the first line naming it wins and
// anything that is not a local Markdown page is skipped.
func TestParseLegacyMemoryIndex(t *testing.T) {
	t.Parallel()
	index := "# Project memory\n\n" +
		"- [Running the real test suite](testing.md): plain `go test` silently skips everything\n" +
		"- [Integer cents](./money/cents.md#rule) — Money is integer cents\n" +
		"- [Logging rollout status](logging-rollout.md)\n" +
		"* see vitest.md for the loader quirk\n" +
		"- `ticks.md`: bare path in backticks\n" +
		"- **bold.md** — bare path in bold\n" +
		"- [cpp](cpp.md) — learned C++\n" +
		"- [again](testing.md): a second line for the same page loses\n" +
		"- [external](https://example.com/x.md): skipped\n" +
		"- [escape](../other/x.md): skipped\n" +
		"- [not markdown](notes.txt): skipped\n" +
		"- [hidden](.draft/x.md): skipped\n" +
		"- **no-link** — [feedback] a line with no page\n"
	want := map[string]string{
		"testing.md":         "plain `go test` silently skips everything",
		"money/cents.md":     "Money is integer cents",
		"logging-rollout.md": "Logging rollout status",
		"vitest.md":          "see for the loader quirk",
		"ticks.md":           "bare path in backticks",
		"bold.md":            "bare path in bold",
		"cpp.md":             "learned C++",
	}
	if got := parseLegacyMemoryIndex(index); !maps.Equal(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// A crash after some page writes but before the rename leaves MEMORY.md and
// some described pages (kept.md here); the next run finishes without rewriting
// them. A linked page that can't be read (dir.md is a directory) is skipped and
// the index is still renamed.
func TestMigrateMemoryScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory", "personal")
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(scope, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("MEMORY.md", "- [plain](plain.md) — plain page gets this\n- [kept](kept.md) — loses to the page's own\n- [bad](bad.md) — skipped, frontmatter unreadable\n- [gone](gone.md) — page missing\n- [dir](dir.md) — not a file\n")
	write("plain.md", "# Plain\nbody\n")
	write("kept.md", "---\ndescription: the page's own\n---\nbody\n")
	write("bad.md", "---\ndescription: a: b\n---\nbody\n")
	if err := os.Mkdir(filepath.Join(scope, "dir.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(memoryLegacyIndexBackup, "an earlier backup is replaced\n")

	want := map[string]string{
		"plain.md": "---\ndescription: plain page gets this\n---\n# Plain\nbody\n",
		"kept.md":  "---\ndescription: the page's own\n---\nbody\n",
		"bad.md":   "---\ndescription: a: b\n---\nbody\n",
	}
	check := func(run string) {
		t.Helper()
		for rel, body := range want {
			raw, err := os.ReadFile(filepath.Join(scope, rel))
			if err != nil || string(raw) != body {
				t.Fatalf("%s: %s=%q, %v", run, rel, raw, err)
			}
		}
		for _, rel := range []string{"gone.md", "MEMORY.md"} {
			if _, err := os.Stat(filepath.Join(scope, rel)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("%s: %s should not exist: %v", run, rel, err)
			}
		}
		backup, err := os.ReadFile(filepath.Join(scope, memoryLegacyIndexBackup))
		if err != nil || !strings.HasPrefix(string(backup), "- [plain]") {
			t.Fatalf("%s: backup=%q, %v", run, backup, err)
		}
	}
	for _, run := range []string{"first run", "second run"} {
		if err := migrateMemoryScope(env); err != nil {
			t.Fatalf("%s: %v", run, err)
		}
		check(run)
	}
}

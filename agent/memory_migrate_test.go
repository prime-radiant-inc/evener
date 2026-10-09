package agent

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

// newMemoryMigrateScope is a confined environment rooted at a fresh personal
// memory scope, and that scope's directory.
func newMemoryMigrateScope(t *testing.T) (*execenv.LocalExecutionEnvironment, string) {
	t.Helper()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.Cleanup)
	return env, filepath.Join(root, "memory", "personal")
}

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
		{"replaces a block scalar with blank lines inside it", "---\nupdated: |\n  old\n\n  older\ntags: [a]\n---\nb\n", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\ntags: [a]\n---\nb\n"},
		{"keeps a blank line that ends the replaced value", "---\nnote: |\n  x\n\nkeep: 1\n---\n", "note: y\n",
			"---\nnote: y\n\nkeep: 1\n---\n"},
		{"a value ending in --- does not close the block", "---\ndescription: a---\nb: 1\n---\nbody\n", "by: s1\n",
			"---\ndescription: a---\nb: 1\nby: s1\n---\nbody\n"},
		{"replaces a double-quoted key", "---\n\"description\": \"\"\nb: 1\n---\nx\n", "description: d\n",
			"---\ndescription: d\nb: 1\n---\nx\n"},
		{"replaces a single-quoted key", "---\n'description': ''\n---\nx\n", "description: d\n",
			"---\ndescription: d\n---\nx\n"},
		{"replaces a key with a space before its colon", "---\ndescription : \"\"\n---\nx\n", "description: d\n",
			"---\ndescription: d\n---\nx\n"},
		{"replaces a flow value spread over lines", "---\ntags: [a,\nb]\nk: 1\n---\n", "tags: [c]\n",
			"---\ntags: [c]\nk: 1\n---\n"},
		{"keeps a comment that ends the replaced value", "---\nnote: x\n# about keep\nkeep: 1\n---\n", "note: y\n",
			"---\nnote: y\n# about keep\nkeep: 1\n---\n"},
		{"leaves a flow mapping as it is", "---\n{description: d}\n---\nx\n", "by: s1\n",
			"---\n{description: d}\n---\nx\n"},
		{"leaves a block ended by ... as it is", "---\ndescription: d\n...\n---\nx\n", "by: s1\n",
			"---\ndescription: d\n...\n---\nx\n"},
		{"leaves a block ended by ... and a comment as it is", "---\ndescription: d\n... # end\n---\nx\n", "by: s1\n",
			"---\ndescription: d\n... # end\n---\nx\n"},
		{"a block that does not parse gains the line", "---\ndescription: a: b\n---\nx\n", "by: s1\n",
			"---\ndescription: a: b\nby: s1\n---\nx\n"},
		{"a key-like line inside a block scalar is not a key", "---\nnote: |\n  description: no\ndescription: old\n---\n", "description: new\n",
			"---\nnote: |\n  description: no\ndescription: new\n---\n"},
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
		"- **no-link** — [feedback] a line with no page\n" +
		"- see old.md.txt for the old notes\n" +
		"- a.md/foo is a directory path\n" +
		"- [emph](emph.md) — **important** note\n"
	want := map[string]string{
		"testing.md":         "plain `go test` silently skips everything",
		"money/cents.md":     "Money is integer cents",
		"logging-rollout.md": "Logging rollout status",
		"vitest.md":          "see for the loader quirk",
		"ticks.md":           "bare path in backticks",
		"bold.md":            "bare path in bold",
		"cpp.md":             "learned C++",
		"emph.md":            "**important** note",
	}
	got := make(map[string]string)
	for _, entry := range parseLegacyMemoryIndex(index) {
		got[entry.Link] = entry.Description
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// A crash after some page writes but before the rename leaves MEMORY.md and
// some described pages (kept.md here); the next run finishes without rewriting
// them. A linked page that can't be read (dir.md is a directory) is skipped and
// the index is still renamed.
func TestMigrateMemoryScope(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
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

// A hand-written MEMORY.md that reappears after migration (an older Evener
// build writing it again) is migrated again into the next free backup name;
// no backup is ever overwritten or removed. An index identical to an
// existing backup adds none.
func TestMigrateMemoryScopeKeepsEarlierBackups(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	indexes := []string{"- [a](a.md) — first index\n", "- [b](b.md) — recreated index\n", "- [c](c.md) — third index\n"}
	backups := []string{memoryLegacyIndexBackup, memoryLegacyIndexBackup + ".2", memoryLegacyIndexBackup + ".3"}
	// The fourth and fifth runs repeat earlier indexes and add no backup.
	for i, index := range append(slices.Clone(indexes), indexes[0], indexes[1]) {
		if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte(index), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := migrateMemoryScope(env); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		kept := min(i+1, len(backups))
		for j, backup := range backups[:kept] {
			raw, err := os.ReadFile(filepath.Join(scope, backup))
			if err != nil || string(raw) != indexes[j] {
				t.Fatalf("run %d: %s=%q, %v", i+1, backup, raw, err)
			}
		}
		entries, err := os.ReadDir(scope)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, entry := range entries {
			if entry.Name() == "MEMORY.md" {
				t.Fatalf("run %d: MEMORY.md left in place", i+1)
			}
			if strings.HasPrefix(entry.Name(), memoryLegacyIndexBackup) {
				n++
			}
		}
		if n != kept {
			t.Fatalf("run %d: %d backups, want %d", i+1, n, kept)
		}
	}
}

// Migration writes a description linked by a differently cased name into the
// page on disk.
func TestMigrateMemoryScopeResolvesLinkCase(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte("- [x](Notes.md) — the notes page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "notes.md"), []byte("body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(scope, "notes.md")); err != nil || string(raw) != "---\ndescription: the notes page\n---\nbody\n" {
		t.Fatalf("notes.md=%q, %v", raw, err)
	}
}

// The root index is excluded from the pages whatever its case (see
// isMemoryPagePath), so migration finds it whatever its case too. On a
// case-sensitive filesystem a scope can hold several; each is migrated, the
// one named exactly MEMORY.md first, so its descriptions win.
func TestMigrateMemoryScopeFindsTheRootIndexInAnyCase(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	names := func(t *testing.T, dir string) []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}

	t.Run("a lower-case index", func(t *testing.T) {
		t.Parallel()
		env, scope := newMemoryMigrateScope(t)
		write(t, filepath.Join(scope, "memory.md"), "- [a](a.md) — from the index\n")
		write(t, filepath.Join(scope, "a.md"), "body\n")
		if err := migrateMemoryScope(env); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(scope, "a.md")); got != "---\ndescription: from the index\n---\nbody\n" {
			t.Fatalf("a.md=%q", got)
		}
		if got, want := names(t, scope), []string{memoryLegacyIndexBackup, "a.md"}; !slices.Equal(got, want) {
			t.Fatalf("scope holds %q, want %q", got, want)
		}
	})

	t.Run("two indexes differing in case", func(t *testing.T) {
		t.Parallel()
		env, scope := newMemoryMigrateScope(t)
		write(t, filepath.Join(scope, "MEMORY.md"), "- [a](a.md) — exact index\n")
		write(t, filepath.Join(scope, "memory.md"), "- [a](a.md) — other index\n- [b](b.md) — only in the other\n")
		if names(t, scope)[0] != "MEMORY.md" || len(names(t, scope)) != 2 {
			t.Skip("the filesystem is case-insensitive")
		}
		write(t, filepath.Join(scope, "a.md"), "a\n")
		write(t, filepath.Join(scope, "b.md"), "b\n")
		if err := migrateMemoryScope(env); err != nil {
			t.Fatal(err)
		}
		for rel, want := range map[string]string{
			"a.md":                         "---\ndescription: exact index\n---\na\n",
			"b.md":                         "---\ndescription: only in the other\n---\nb\n",
			memoryLegacyIndexBackup:        "- [a](a.md) — exact index\n",
			memoryLegacyIndexBackup + ".2": "- [a](a.md) — other index\n- [b](b.md) — only in the other\n",
		} {
			if got := read(t, filepath.Join(scope, rel)); got != want {
				t.Fatalf("%s=%q, want %q", rel, got, want)
			}
		}
		if got := len(names(t, scope)); got != 4 {
			t.Fatalf("scope holds %q, want the two pages and two backups", names(t, scope))
		}
	})
}

// A scope whose directory does not exist yet has nothing to migrate.
func TestMigrateMemoryScopeWithNoScopeDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	if err := os.Remove(filepath.Join(root, "memory", "personal")); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
}

// Two links naming one page in a case other than its own: the earlier line
// wins, as for any page named twice. A link naming the page exactly wins over
// an earlier one in another case.
func TestMigrateMemoryScopeLinkCasePrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, index, want string }{
		{"the earlier of two other-case links", "- [x](NOTES.md) — first line\n- [y](Notes.md) — second line\n", "first line"},
		{"an exact link over an earlier other-case one", "- [x](NOTES.md) — other case\n- [y](notes.md) — exact name\n", "exact name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, scope := newMemoryMigrateScope(t)
			if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte(tc.index), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scope, "notes.md"), []byte("body\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := migrateMemoryScope(env); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(filepath.Join(scope, "notes.md")); err != nil || string(raw) != "---\ndescription: "+tc.want+"\n---\nbody\n" {
				t.Fatalf("notes.md=%q, %v", raw, err)
			}
		})
	}
}

// Frontmatter the field editor can't extend in place (a flow mapping, a
// block ended by "...") is left as it is, like unreadable frontmatter, rather
// than written into a page that no longer reads.
func TestMigrateMemoryScopeLeavesFrontmatterItCannotExtend(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	pages := map[string]string{
		"flow.md":  "---\n{tags: [a]}\n---\nbody\n",
		"ended.md": "---\ntags: [a]\n...\n---\nbody\n",
	}
	for rel, body := range pages {
		if parsed := parseMemoryPage(rel, []byte(body), time.Time{}); parsed.Unreadable || parsed.HasDescription {
			t.Fatalf("%s should read, with no description: %+v", rel, parsed)
		}
		if err := os.WriteFile(filepath.Join(scope, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte("- [f](flow.md) — flow\n- [e](ended.md) — ended\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	for rel, body := range pages {
		if raw, err := os.ReadFile(filepath.Join(scope, rel)); err != nil || string(raw) != body {
			t.Fatalf("%s=%q, %v; want it unchanged", rel, raw, err)
		}
	}
}

// An index a concurrent run renamed after the listing is already migrated:
// the others still migrate and the run succeeds.
func TestMigrateLegacyMemoryIndexesSkipsAnIndexRenamedMeanwhile(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	if err := os.WriteFile(filepath.Join(scope, "memory.md"), []byte("- [a](a.md) — from the index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "a.md"), []byte("body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(scope, "MEMORY.renamed-meanwhile.md")
	if err := migrateLegacyMemoryIndexes(env, []string{gone, filepath.Join(scope, "memory.md")}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(scope, "a.md")); err != nil || string(raw) != "---\ndescription: from the index\n---\nbody\n" {
		t.Fatalf("a.md=%q, %v", raw, err)
	}
}

// A backup name held by an entry differing from it only in case is taken: on
// a case-insensitive filesystem the two are one file, so renaming onto the
// name would replace that backup.
func TestMigrateMemoryScopeTreatsACaseVariantBackupAsTaken(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	variant := filepath.Join(scope, strings.ToLower(memoryLegacyIndexBackup))
	if err := os.WriteFile(variant, []byte("the original index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte("- [a](a.md) — index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(variant); err != nil || string(raw) != "the original index\n" {
		t.Fatalf("the earlier backup was replaced: %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(scope, memoryLegacyIndexBackup+".2")); err != nil || string(raw) != "- [a](a.md) — index\n" {
		t.Fatalf("%s.2=%q, %v", memoryLegacyIndexBackup, raw, err)
	}
}

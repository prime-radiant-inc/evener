package execenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// writeControlPathTree lays out a file whose name holds a newline beside an
// ordinary one, both holding a match between two context lines.
func writeControlPathTree(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"bad\nname.md", "ok.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("before\nneedle\nafter"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// controlPathGrepCases are the results each output mode gives for
// writeControlPathTree, one per file: a name holding a control character is
// written as a JSON string, so every line belongs to one result (#4154). A
// context search's result is its group of lines.
var controlPathGrepCases = []struct {
	mode    string
	context int
	want    []string
}{
	{mode: "files_with_matches", want: []string{`"bad\nname.md"`, "ok.md"}},
	{mode: "content", want: []string{`"bad\nname.md":2:needle`, "ok.md:2:needle"}},
	{mode: "count", want: []string{`"bad\nname.md":1`, "ok.md:1"}},
	{mode: "content", context: 1, want: []string{
		`"bad\nname.md"-1-before` + "\n" + `"bad\nname.md":2:needle` + "\n" + `"bad\nname.md"-3-after`,
		"ok.md-1-before\nok.md:2:needle\nok.md-3-after",
	}},
}

func checkControlPathGrep(t *testing.T, arm string, env *LocalExecutionEnvironment, dir string) {
	t.Helper()
	for _, tc := range controlPathGrepCases {
		got, err := env.Grep(t.Context(), "needle", dir, "", false, 100, tc.mode, tc.context)
		if err != nil {
			t.Fatalf("%s: grep %s (context %d): %v", arm, tc.mode, tc.context, err)
		}
		// rg searches files in parallel, so results may come in either order.
		separator := "\n"
		if tc.context > 0 {
			separator = "\n--\n"
		}
		results := strings.Split(got, separator)
		slices.Sort(results)
		if !slices.Equal(results, tc.want) {
			t.Errorf("%s: grep %s (context %d) results = %q, want %q", arm, tc.mode, tc.context, results, tc.want)
		}
	}
}

func TestGrepQuotesAControlCharacterPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeControlPathTree(t, root)
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	checkControlPathGrep(t, "fallback", fallback, root)

	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed; cannot check the rg arm")
	}
	withRg := NewLocalExecutionEnvironment(root)
	defer withRg.Cleanup()
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	checkControlPathGrep(t, "ripgrep", withRg, root)
}

func TestSandboxGrepQuotesAControlCharacterPath(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	writeControlPathTree(t, worktree)
	checkControlPathGrep(t, "sandboxed", env, worktree)
}

func TestQuoteControlPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, want string }{
		{"a\nb.md", `"a\nb.md"`},
		{"del\x7f.md", `"del\u007f.md"`},
		{"<b>&\a.md", `"<b>&\u0007.md"`},
		{"bad\xff\n.md", `"bad\xff\n.md"`},
	} {
		if got := QuoteControlPath(tc.path); got != tc.want {
			t.Errorf("QuoteControlPath(%q) = %s, want %s", tc.path, got, tc.want)
		}
	}
}

func TestPathHasControl(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{"a\nb": true, "a\rb": true, "a\x7f": true, "a\x00": true, "a\tb": false, "plain.md": false, "é.md": false} {
		if got := PathHasControl(path); got != want {
			t.Errorf("PathHasControl(%q) = %v, want %v", path, got, want)
		}
	}
}

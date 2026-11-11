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

// grepPathCase is one output mode's expected results for a tree holding one
// unusual filename beside "ok.md". A context search's result is its group of
// lines; the other modes give one result per file.
type grepPathCase struct {
	mode    string
	context int
	want    []string
}

// writePathTree lays out name beside an ordinary file, both holding a match
// between two context lines.
func writePathTree(t *testing.T, root, name string) {
	t.Helper()
	for _, file := range []string{name, "ok.md"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("before\nneedle\nafter"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// controlPathGrepCases are the results each output mode gives for a name
// holding a control character: it is written as a JSON string, so every line
// belongs to one result (#4154).
var controlPathGrepCases = []grepPathCase{
	{mode: "files_with_matches", want: []string{`"bad\nname.md"`, "ok.md"}},
	{mode: "content", want: []string{`"bad\nname.md":2:needle`, "ok.md:2:needle"}},
	{mode: "count", want: []string{`"bad\nname.md":1`, "ok.md:1"}},
	{mode: "content", context: 1, want: []string{
		`"bad\nname.md"-1-before` + "\n" + `"bad\nname.md":2:needle` + "\n" + `"bad\nname.md"-3-after`,
		"ok.md-1-before\nok.md:2:needle\nok.md-3-after",
	}},
}

// colonPathGrepCases are the same modes for a name holding a colon, which is
// ambiguous in "path:line:text" output (#4526).
var colonPathGrepCases = []grepPathCase{
	{mode: "files_with_matches", want: []string{`"a:1:b.md"`, "ok.md"}},
	{mode: "content", want: []string{`"a:1:b.md":2:needle`, "ok.md:2:needle"}},
	{mode: "count", want: []string{`"a:1:b.md":1`, "ok.md:1"}},
	{mode: "content", context: 1, want: []string{
		`"a:1:b.md"-1-before` + "\n" + `"a:1:b.md":2:needle` + "\n" + `"a:1:b.md"-3-after`,
		"ok.md-1-before\nok.md:2:needle\nok.md-3-after",
	}},
}

func checkPathGrep(t *testing.T, arm string, env *LocalExecutionEnvironment, dir string, cases []grepPathCase) {
	t.Helper()
	for _, tc := range cases {
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

// checkGrepPathArms runs the tree through the fallback and, when installed,
// ripgrep.
func checkGrepPathArms(t *testing.T, name string, cases []grepPathCase) {
	t.Helper()
	root := t.TempDir()
	writePathTree(t, root, name)
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	checkPathGrep(t, "fallback", fallback, root, cases)

	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed; cannot check the rg arm")
	}
	withRg := NewLocalExecutionEnvironment(root)
	defer withRg.Cleanup()
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	checkPathGrep(t, "ripgrep", withRg, root, cases)
}

func TestGrepQuotesAControlCharacterPath(t *testing.T) {
	t.Parallel()
	checkGrepPathArms(t, "bad\nname.md", controlPathGrepCases)
}

func TestGrepQuotesAColonPath(t *testing.T) {
	t.Parallel()
	checkGrepPathArms(t, "a:1:b.md", colonPathGrepCases)
}

func TestSandboxGrepQuotesAControlCharacterPath(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	writePathTree(t, worktree, "bad\nname.md")
	checkPathGrep(t, "sandboxed", env, worktree, controlPathGrepCases)
}

func TestSandboxGrepQuotesAColonPath(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	writePathTree(t, worktree, "a:1:b.md")
	checkPathGrep(t, "sandboxed", env, worktree, colonPathGrepCases)
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

// Issue #4526: only a grep result's path is ambiguous when it holds a colon,
// so GrepPath quotes it. A glob match or directory entry keeps the ordinary
// one-line rendering: its output is a usable path with no following field, and
// quoting a drive letter or a colon in the search root would break it.
func TestGrepPathQuotesOnlyGrepColonPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, grep, oneLine string }{
		{"plain.md", "plain.md", "plain.md"},
		{"a:1:b.md", `"a:1:b.md"`, "a:1:b.md"},
		{`C:\repo\ok.md`, `"C:\\repo\\ok.md"`, `C:\repo\ok.md`},
		{"a\nb.md", `"a\nb.md"`, `"a\nb.md"`},
		{"é.md", "é.md", "é.md"},
	} {
		if got := GrepPath(tc.path); got != tc.grep {
			t.Errorf("GrepPath(%q) = %s, want %s", tc.path, got, tc.grep)
		}
		if got := OneLinePath(tc.path); got != tc.oneLine {
			t.Errorf("OneLinePath(%q) = %s, want %s", tc.path, got, tc.oneLine)
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

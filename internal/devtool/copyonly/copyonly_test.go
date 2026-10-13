package copyonly

import (
	"go/parser"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// scanFixture scans one test file's source with prod as the production
// corpus and one sentence as the text assets, and returns its targets.
func scanFixture(t *testing.T, path, src string, prod ...string) []Target {
	t.Helper()
	s := newScanner(strings.Join(prod, "\n"), "Always use the tools you have.")
	r, err := s.scanGo(path, src, false)
	if err != nil {
		t.Fatalf("scanGo(%s): %v", path, err)
	}
	var targets []Target
	for _, c := range r.copyOnly {
		if c.Purpose == PurposeCopy {
			targets = append(targets, Target{File: c.File, Test: c.Test, Line: c.Line})
		}
	}
	return targets
}

const goFlagSrc = `package x

func TestUsageNamesTheFlags(t *testing.T) {
	out, err := run("--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Usage: evener") {
		t.Fatalf("usage = %q", out)
	}
}

func TestRejectsEmptyName(t *testing.T) {
	_, err := create("")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestCountsRows(t *testing.T) {
	rows := load()
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
}
`

// TestTargets pins which Go tests are copy-only targets, including the cases
// a line-based scanner gets wrong. The TypeScript cases live with the
// TypeScript scanner in cmd/evener-hub/frontend/src/dev/copyonly.
func TestTargets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		path string
		src  string
		prod []string
		want []string
	}{
		{
			name: "go: setup error guards are not claims",
			path: "a_test.go", src: goFlagSrc,
			prod: []string{"Usage: evener", "name is required"},
			want: []string{"TestUsageNamesTheFlags"},
		},
		{
			name: "go: expecting an error is a real claim",
			path: "a_test.go", src: goFlagSrc,
			prod: []string{"name is required"},
		},
		{
			name: "go: an expected error in the same condition is a behavior claim",
			path: "a_test.go",
			src: `package x
func TestRejectsEmptyTarget(t *testing.T) {
	if _, err := configure(""); err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatal(err)
	}
}
`,
			prod: []string{"target is required"},
		},
		{
			name: "go: braces inside strings do not merge the next test",
			path: "a_test.go",
			src: "package x\n" +
				"func TestRefusesLiteralHeader(t *testing.T) {\n" +
				"\t_, err := add(`x-api-key: ${PORTKEY`)\n" +
				"\tif err == nil {\n\t\tt.Fatal(\"want refusal\")\n\t}\n" +
				"\tcfg := \"{ unterminated\"\n\t_ = cfg\n}\n\n" +
				"func TestHelpShowsUsage(t *testing.T) {\n" +
				"\tif !strings.Contains(help(), \"Usage: evener\") {\n\t\tt.Fatal(\"no usage\")\n\t}\n}\n",
			prod: []string{"Usage: evener"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: a count in the condition is a behavior claim",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTwoRows(t *testing.T) {
	out := render()
	if len(rows(out)) != 2 || !strings.Contains(out, "Ready") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Ready"},
		},
		{
			name: "go: the name, comments and diagnostics are not fixture inputs",
			path: "a_test.go",
			src: `package x
func TestRenderShowsNoSessions(t *testing.T) {
	// The empty view says "no sessions yet".
	out := render(nil)
	if !strings.Contains(out, "no sessions yet") {
		t.Fatalf("want \"no sessions yet\", got %q", out)
	}
}
`,
			prod: []string{"no sessions yet"},
			want: []string{"TestRenderShowsNoSessions"},
		},
		{
			name: "go: a variable comparison in the condition is a behavior claim",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out, status := run("--help")
	if status != expectedStatus || !strings.Contains(out, "Usage: evener") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener"},
		},
		{
			name: "go: operators inside a string do not split the condition",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out, status := run("--help")
	if !strings.Contains(out, "Usage: evener && more") || status != expectedStatus {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener && more"},
		},
		{
			name: "go: a string holding an operator is still one copy term",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out := run("--help")
	if !strings.Contains(out, "Usage: evener && more") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener && more"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: a machine-token comparison is not a copy term",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	status, out := run()
	if status != "running" || !strings.Contains(out, "Usage: evener") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener", "running"},
		},
		{
			name: "go: a semicolon in a trailing comment does not split the condition",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out := help()
	if !strings.Contains(out, "Usage: evener") { // check; wording
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: text supplied in an if initializer is fixture",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheLabel(t *testing.T) {
	if got := render("Try again"); got != "Try again" {
		t.Fatalf("got %q", got)
	}
}
`,
			prod: []string{"Try again"},
		},
		{
			name: "go: only condition literals are compared with supplied text",
			path: "a_test.go",
			src: `package x
func TestRenderShowsEmpty(t *testing.T) {
	if got := render("empty"); got != "No sessions yet" {
		t.Fatalf("got %q", got)
	}
}
`,
			prod: []string{"No sessions yet", "empty"},
			want: []string{"TestRenderShowsEmpty"},
		},
		{
			name: "go: a contract-shaped literal is not copy",
			path: "a_test.go",
			src: `package x
func TestStatusShowsRunning(t *testing.T) {
	if got := status(); got != "session_running" {
		t.Fatal(got)
	}
}
`,
			prod: []string{"session_running"},
		},
		{
			name: "go: a regexp match against copy is a copy claim",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	if !regexp.MustCompile(` + "`Usage of evener: \\w+`" + `).MatchString(help()) {
		t.Fatal("no usage")
	}
}
`,
			prod: []string{"Usage of evener: run"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: a raw-string fixture with braces does not hide the next test",
			path: "a_test.go",
			src: "package x\n" +
				"func TestParsesConfig(t *testing.T) {\n" +
				"\tcfg := `{\n  \"a\": {\n    \"b\": 1\n`\n" +
				"\tif parse(cfg) == nil {\n\t\tt.Fatal(\"nil\")\n\t}\n}\n\n" +
				"func TestHelpShowsUsage(t *testing.T) {\n" +
				"\tif !strings.Contains(help(), \"Usage: evener\") {\n\t\tt.Fatal(\"no usage\")\n\t}\n}\n",
			prod: []string{"Usage: evener"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: a behavior term on the condition's next line is a behavior claim",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out, status := run("--help")
	if !strings.Contains(out, "Usage: evener") ||
		status != expectedStatus {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener"},
		},
		{
			name: "go: a copy check split across lines is a copy claim",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out := run("--help")
	if !strings.Contains(out,
		"Usage: evener") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: an error guard with an initializer is setup, not a claim",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out, err := run("--help")
	if err := check(out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Usage: evener") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: a concatenation with supplied input checks that the input is kept",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheGreeting(t *testing.T) {
	name := "Ada"
	if render(name) != "Hello "+name {
		t.Fatal(render(name))
	}
}
`,
			prod: []string{"Hello %s"},
		},
		{
			name: "go: a concatenation with any non-literal operand is a behavior check",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheGreeting(t *testing.T) {
	if !strings.Contains(render(), "Hello "+user()+"!") {
		t.Fatal(render())
	}
}
`,
			prod: []string{"Hello %s!"},
		},
		{
			name: "go: an all-literal concatenation is copy",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheGreeting(t *testing.T) {
	if render() != "Hello, "+"world" {
		t.Fatal(render())
	}
}
`,
			prod: []string{"Hello, world"},
			want: []string{"TestRenderShowsTheGreeting"},
		},
		{
			name: "go: a literal from a text asset is copy",
			path: "a_test.go",
			src: `package x
func TestPromptMentionsTools(t *testing.T) {
	if !strings.Contains(prompt(), "use the tools you have") {
		t.Fatal("missing")
	}
}
`,
			want: []string{"TestPromptMentionsTools"},
		},
		{
			name: "go: a behavior term under a negated compound is a behavior claim",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out, status := run("--help")
	if !(status == expectedStatus && strings.Contains(out, "Usage: evener")) {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener"},
		},
		{
			name: "go: copy terms under a negated compound are still copy claims",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	out := run("--help")
	if !(strings.Contains(out, "Usage: evener") || !(out != "Try again")) {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Usage: evener", "Try again"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: a numeric comparison before a text comparison is behavior",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheLabel(t *testing.T) {
	n, out := render()
	if matches(n == 2, out == "Try again") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Try again"},
		},
		{
			name: "go: a numeric comparison after a text comparison is behavior",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheLabel(t *testing.T) {
	n, out := render()
	if matches(out == "Try again", n == 2) {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Try again"},
		},
		{
			name: "go: a nested variable comparison after a text comparison is behavior",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheLabel(t *testing.T) {
	status, out := render()
	if matches(out == "Try again", status == expectedStatus) {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Try again"},
		},
		{
			name: "go: a nested variable comparison before a text comparison is behavior",
			path: "a_test.go",
			src: `package x
func TestRenderShowsTheLabel(t *testing.T) {
	status, out := render()
	if matches(status == expectedStatus, out == "Try again") {
		t.Fatal(out)
	}
}
`,
			prod: []string{"Try again"},
		},
		{
			name: "go: a subtest's name is not a fixture input",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	t.Run("Usage: evener", func(t *testing.T) {
		if !strings.Contains(help(), "Usage: evener") {
			t.Fatal("no usage")
		}
	})
}
`,
			prod: []string{"Usage: evener"},
			want: []string{"TestHelpShowsUsage"},
		},
		{
			name: "go: text a subtest passes to the code under test is supplied",
			path: "a_test.go",
			src: `package x
func TestHelpShowsUsage(t *testing.T) {
	t.Run("Usage: evener", func(t *testing.T) {
		out := render("Usage: evener")
		if !strings.Contains(out, "Usage: evener") {
			t.Fatal("no usage")
		}
	})
}
`,
			prod: []string{"Usage: evener"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, target := range scanFixture(t, tc.path, tc.src, tc.prod...) {
				got = append(got, target.Test)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("targets = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTargetsReportTheLineTheTestStartsOn(t *testing.T) {
	t.Parallel()
	goTargets := scanFixture(t, "a_test.go", goFlagSrc, "Usage: evener")
	if len(goTargets) != 1 || goTargets[0].Line != 3 {
		t.Fatalf("go targets = %+v, want one starting on line 3", goTargets)
	}
}

func TestRegexCore(t *testing.T) {
	t.Parallel()
	for rx, want := range map[string]string{
		`Retry[)]`:             "Retry",
		`Usage: \w+ evener`:    "Usage: ",
		`no sessions yet\.`:    "no sessions yet.",
		`^(a|b) longer phrase`: " longer phrase",
	} {
		if got := regexCore(rx); got != want {
			t.Errorf("regexCore(%q) = %q, want %q", rx, got, want)
		}
	}
}

// TestConcatenationPrefixIgnoresGrouping checks that a concatenation ending
// in a value gives the same known literal prefix however it is parenthesized.
func TestConcatenationPrefixIgnoresGrouping(t *testing.T) {
	t.Parallel()
	prefix := func(src string) (string, bool) {
		t.Helper()
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("ParseExpr(%s): %v", src, err)
		}
		got := comparisons(e)
		if len(got) != 1 {
			t.Fatalf("comparisons(%s) = %+v, want one", src, got)
		}
		return got[0].lit, got[0].partial
	}
	leftLit, leftPartial := prefix(`strings.Contains(out, "launchconfig:" + " mkdir " + dir)`)
	rightLit, rightPartial := prefix(`strings.Contains(out, "launchconfig:" + (" mkdir " + dir))`)
	if rightLit != leftLit || !leftPartial || !rightPartial {
		t.Fatalf("parenthesized prefix = %q (partial %v), left-associative = %q (partial %v); want equal and partial",
			rightLit, rightPartial, leftLit, leftPartial)
	}
}

func TestContractShaped(t *testing.T) {
	t.Parallel()
	for lit, want := range map[string]bool{
		"ab":              true,  // too short to be copy
		"123":             true,  // no letters
		"session_running": true,  // identifier
		"--json":          true,  // flag
		"Welcome":         false, // a capitalized word is a label
		"No sessions yet": false,
		"cat > file":      true, // shell
		"export type X":   true, // code
		"<tag attr=":      true, // markup
	} {
		if got := contractShaped(lit); got != want {
			t.Errorf("contractShaped(%q) = %v, want %v", lit, got, want)
		}
	}
}

// TestScanReadsTheRepositoryCorpus runs Scan over a throwaway git repository
// and checks which files it reads as tests, as production source, and as text
// assets, and that it reads only tracked files.
func TestScanReadsTheRepositoryCorpus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goTest := func(name, phrase string) string {
		return "func " + name + "(t *testing.T) {\n\tif !strings.Contains(out(), \"" + phrase + "\") {\n\t\tt.Fatal(1)\n\t}\n}\n"
	}

	write("pkg/x.go", "package x\nconst a = \"Go phrase here\"\n")
	write("pkg/x_test.go", "package x\n"+goTest("TestShowsGoPhrase", "Go phrase here")+goTest("TestShowsAssetPhrase", "asset phrase here"))
	write("pkg/prompt.md", "An asset phrase here.\n")
	write("pkg/fixture_test.go", "package x\n"+goTest("TestShowsTestOnlyPhrase", "only in tests here"))
	write("docs/guide.md", "a docs phrase here\n")
	write("pkg/doc_test.go", "package x\n"+goTest("TestShowsDocsPhrase", "a docs phrase here"))
	// TypeScript tests belong to the frontend's own audit.
	write("cmd/evener-hub/frontend/src/Empty.tsx", "export const Empty = () => <p>Nothing to show</p>;\n")
	write("cmd/evener-hub/frontend/src/Empty.test.tsx",
		"test(\"shows nothing\", () => {\n  expect(screen.getByText(\"Nothing to show\")).toBeTruthy();\n});\n")

	write("pkg/untracked_test.go", "package x\n"+goTest("TestShowsUntrackedPhrase", "Go phrase here"))
	for _, args := range [][]string{{"init", "-q"}, {"add", "--", "."}, {"rm", "-q", "--cached", "--", "pkg/untracked_test.go"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	targets, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []Target{
		{File: "pkg/x_test.go", Test: "TestShowsGoPhrase", Line: 2},
		{File: "pkg/x_test.go", Test: "TestShowsAssetPhrase", Line: 7},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("Scan = %+v\nwant %+v", targets, want)
	}
}

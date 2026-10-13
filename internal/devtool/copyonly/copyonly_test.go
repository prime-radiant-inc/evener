package copyonly

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// scanFixture scans one test file's source with prod as both production
// corpora and one sentence as the text assets.
func scanFixture(t *testing.T, path, src string, prod ...string) []Target {
	t.Helper()
	s := newScanner(strings.Join(prod, "\n"), strings.Join(prod, "\n"), "Always use the tools you have.")
	targets, err := s.scanFile(path, src)
	if err != nil {
		t.Fatalf("scanFile(%s): %v", path, err)
	}
	return targets
}

func tsBlockNames(src string) []string {
	var names []string
	for _, b := range tsTestBlocks(src) {
		names = append(names, b.name)
	}
	return names
}

const tsFlagSrc = `import { render, screen } from "@testing-library/react";

test("shows the empty title", () => {
  render(<Empty />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});

test("starting a session sends turn/start", async () => {
  render(<Composer />);
  await user.click(screen.getByRole("button", { name: "Send" }));
  expect(fake.calls).toHaveLength(1);
  expect(screen.getByText("Sending…")).toBeTruthy();
});

it.each([1, 2])("renders hint %s", (n) => {
  render(<Hint n={n} />);
  expect(screen.queryByText("Try again")).not.toBeNull();
});

test("no assertions at all", () => {
  render(<Thing />);
});
`

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

// TestTargets ports every case of tools/copy-oracle/test_inventory.py, plus
// the cases the Go AST settles that the line-based scanner could not.
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
			name: "ts: only tests whose every check is copy",
			path: "a.test.tsx", src: tsFlagSrc,
			prod: []string{"No sessions yet", "Sending…", "Try again"},
			want: []string{"shows the empty title", "renders hint %s"},
		},
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
			name: "ts: text the test feeds in is fixture, not copy",
			path: "a.test.tsx",
			src: `test("edit_file body renders the diff lines", () => {
  const args = JSON.stringify({ old_string: "old line", new_string: "new line" });
  render(<Body args={args} />);
  expect(screen.getByText("old line")).toBeTruthy();
  expect(screen.getByText("new line")).toBeTruthy();
});
`,
			prod: []string{"old line", "new line"},
		},
		{
			name: "ts: text used only to locate an attribute check is not a copy claim",
			path: "a.test.tsx",
			src: `test("scopes the dark pane", () => {
  render(<ThemeFlip />);
  expect(screen.getByText("Dark").parentElement?.getAttribute("data-theme")).toBe("dark");
});
`,
			prod: []string{"Dark"},
		},
		{
			name: "ts: a behavior-named test with only a copy oracle is not a target",
			path: "a.test.ts",
			src: `test("resetGeneration rejects invalidation waiters", async () => {
  r.resetGeneration("h");
  expect(await settled).toMatch(/generation mismatch/);
});
`,
			prod: []string{"generation mismatch"},
		},
		{
			name: "ts: the same copy-only body under a wording name is a target",
			path: "a.test.ts",
			src: `test("shows the generation mismatch", async () => {
  r.resetGeneration("h");
  expect(await settled).toMatch(/generation mismatch/);
});
`,
			prod: []string{"generation mismatch"},
			want: []string{"shows the generation mismatch"},
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
			name: "ts: a count on the same line is a behavior claim",
			path: "a.test.tsx",
			src: `test("renders Recommended in both themes", () => {
  render(<Specimen />);
  expect(screen.getAllByText("Recommended")).toHaveLength(THEMES);
});
`,
			prod: []string{"Recommended"},
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
			name: "ts: the name is not a fixture input",
			path: "a.test.tsx",
			src: `test("renders No sessions yet", () => {
  render(<Empty />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});
`,
			prod: []string{"No sessions yet"},
			want: []string{"renders No sessions yet"},
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
			name: "ts: expression-bodied tests do not swallow their neighbours",
			path: "a.test.tsx",
			src: `test("shows the hint", () => expect(screen.getByText("Try again")).toBeTruthy());

test("sends the request", async () => {
  await send();
  expect(fake.calls).toHaveLength(1);
});

test("renders the label", () =>
  expect(screen.getByText("Retry")).toBeTruthy());
`,
			prod: []string{"Try again", "Retry"},
			want: []string{"shows the hint", "renders the label"},
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
			name: "ts: a paren in a regex literal does not end a test early",
			path: "a.test.tsx",
			src: `test("shows the retry label", async () => {
  expect(screen.getByText(/Retry[)]/)).toBeTruthy();
  await send();
  expect(fake.calls).toHaveLength(1);
});
`,
			prod: []string{"Retry"},
		},
		{
			name: "ts: a paren in a regex literal does not absorb the next test",
			path: "a.test.tsx",
			src: `test("shows the open paren label", () => {
  expect(screen.getByText(/Open[(]/)).toBeTruthy();
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
			prod: []string{"Open", "Try again"},
			want: []string{"shows the open paren label", "renders the hint"},
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
			name: "ts: inline JSX closing tags do not merge neighbouring tests",
			path: "a.test.tsx",
			src: `test("renders the body", () => render(<div>body</div>));

test("shows the hint", () => {
  render(<span>/tmp/path</span>);
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
			prod: []string{"Try again"},
			want: []string{"shows the hint"},
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
			name: "ts: an imported copy constant is a copy claim",
			path: "a.test.tsx",
			src: `import { EMPTY_MESSAGE } from "./Empty";

test("shows the empty message", () => {
  expect(screen.getByText(EMPTY_MESSAGE)).toBeTruthy();
});
`,
			want: []string{"shows the empty message"},
		},
		{
			name: "ts: a literal found only in tests is fixture",
			path: "a.test.tsx",
			src: `test("shows the empty title", () => {
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});
`,
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
			name: "ts: a commented-out check is not a check",
			path: "a.test.tsx",
			src: `test("shows the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
  // expect(fake.calls).toHaveLength(1);
});
`,
			prod: []string{"Try again"},
			want: []string{"shows the hint"},
		},
		{
			name: "ts: a test.each table spread over lines supplies its rows",
			path: "a.test.tsx",
			src: `test.each([
  ["Retry"],
])("renders the %s label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`,
			prod: []string{"Retry"},
		},
		{
			name: "ts: a one-line test.each table supplies its rows",
			path: "a.test.tsx",
			src: `test.each([["Retry"]])("renders the %s label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`,
			prod: []string{"Retry"},
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
		{
			name: "ts: a multi-line declaration's name is not a fixture input",
			path: "a.test.tsx",
			src: `test(
  "renders Try again",
  () => {
    expect(screen.getByText("Try again")).toBeTruthy();
  },
);
`,
			prod: []string{"Try again"},
			want: []string{"renders Try again"},
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
	ts := scanFixture(t, "a.test.tsx", tsFlagSrc, "No sessions yet")
	if len(ts) != 1 || ts[0].Line != 3 {
		t.Fatalf("ts targets = %+v, want one starting on line 3", ts)
	}
	goTargets := scanFixture(t, "a_test.go", goFlagSrc, "Usage: evener")
	if len(goTargets) != 1 || goTargets[0].Line != 3 {
		t.Fatalf("go targets = %+v, want one starting on line 3", goTargets)
	}
}

// TestTSTestBlocks pins the block boundaries: each case once merged two
// neighbouring tests or split one.
func TestTSTestBlocks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "inline JSX closing tag",
			src: `test("renders the body", () => render(<div>body</div>));

test("shows the hint", () => {
  render(<span>/tmp/path</span>);
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
			want: []string{"renders the body", "shows the hint"},
		},
		{
			name: "apostrophe in JSX text",
			src: `test("renders the advice", () => render(<span>Don't retry</span>));

test("shows the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
			want: []string{"renders the advice", "shows the hint"},
		},
		{
			name: "string after a keyword",
			src: `test("shows the retry label", () => {
  const label = () => { return "Retry("; };
  expect(screen.getByText(label())).toBeTruthy();
  expect(fake.calls).toHaveLength(1);
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
			want: []string{"shows the retry label", "renders the hint"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tsBlockNames(tc.src); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("blocks = %q, want %q", got, tc.want)
			}
		})
	}
	for _, kw := range []string{"as", "satisfies"} {
		src := `test("shows the retry label", () => {
  const label = value ` + kw + ` "Retry(";
  expect(fake.calls).toHaveLength(1);
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`
		want := []string{"shows the retry label", "renders the hint"}
		if got := tsBlockNames(src); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: blocks = %q, want %q", kw, got, want)
		}
	}
}

// TestTSDeclarationFormsGiveTheSameTargets pins that formatting alone can't
// hide a copy-only test: a declaration whose name starts on a later line, and
// a test.each table spread over several lines, give the same targets and the
// same boundaries as their one-line forms.
func TestTSDeclarationFormsGiveTheSameTargets(t *testing.T) {
	t.Parallel()
	const neighbours = `
test("sends the request", async () => {
  await send();
  expect(fake.calls).toHaveLength(1);
});

test("shows the label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`
	const body = `expect(screen.getByText("Try again")).toBeTruthy();`
	forms := map[string]struct{ name, src string }{
		"one line": {"renders the hint", `test("renders the hint", () => {
  ` + body + `
});
`},
		"name on the next line": {"renders the hint", `test(
  "renders the hint",
  () => {
    ` + body + `
  },
);
`},
		"modifier, name on the next line": {"renders the hint", `it.only(
  "renders the hint",
  () => {
    ` + body + `
  },
);
`},
		"each on one line": {"renders hint %s", `it.each([1, 2])("renders hint %s", (n) => {
  ` + body + `
});
`},
		"each over several lines": {"renders hint %s", `test.each([
  [1, "one"],
  [2, "two"],
])(
  "renders hint %s",
  (n) => {
    ` + body + `
  },
);
`},
		"each table closing alone": {"renders hint %s", `test.each(
  cases
)("renders hint %s", (n) => {
  ` + body + `
});
`},
		"typed each": {"renders hint %s", `test.each<[number]>([[1], [2]])("renders hint %s", (n) => {
  ` + body + `
});
`},
		"nested generic each": {"renders hint %s", `it.each<Array<[string, number]>>([
  ["a", 1],
])(
  "renders hint %s",
  (n) => {
    ` + body + `
  },
);
`},
		"multi-line type arguments": {"renders hint %s", `test.each<
  [string, number]
>([
  ["a", 1],
])("renders hint %s", (n) => {
  ` + body + `
});
`},
		"function type in the type arguments": {"renders hint %s", `test.each<[(n: number) => string]>([[String]])("renders hint %s", (f) => {
  ` + body + `
});
`},
	}
	for form, tc := range forms {
		got := scanFixture(t, "a.test.tsx", tc.src+neighbours, "Try again", "Retry")
		var names []string
		for _, target := range got {
			names = append(names, target.Test)
		}
		if want := []string{tc.name, "shows the label"}; !reflect.DeepEqual(names, want) {
			t.Errorf("%s: targets = %q, want %q", form, names, want)
		}
		if len(got) > 0 && got[0].Line != 1 {
			t.Errorf("%s: first target starts on line %d, want 1", form, got[0].Line)
		}
	}
}

// TestTSCountAssertionsAreBehavior pins that a count of elements found by
// their text, in any of its spellings, is a behavior check, as is any numeric
// matcher.
func TestTSCountAssertionsAreBehavior(t *testing.T) {
	t.Parallel()
	for _, check := range []string{
		`expect(screen.getAllByText("Recommended")).toHaveLength(2);`,
		`expect(screen.getAllByText("Recommended").length).toBe(2);`,
		`expect(screen.getAllByText("Recommended").length).toEqual(2);`,
		`expect(screen.queryAllByText("Recommended").length).toBeGreaterThanOrEqual(1);`,
		`expect(rowsMatching(screen.getByText("Recommended"))).toBe(2);`,
		`expect(rowsMatching(screen.getByText("Recommended"))).toStrictEqual(-1);`,
	} {
		src := "test(\"renders Recommended in both themes\", () => {\n  render(<Specimen />);\n  " + check + "\n});\n"
		if got := scanFixture(t, "a.test.tsx", src, "Recommended"); len(got) != 0 {
			t.Errorf("%s: targets = %+v, want none", check, got)
		}
	}
	// The same test with a copy-only check is a target, so the cases above
	// pass for the count and not for some other reason.
	src := "test(\"renders Recommended in both themes\", () => {\n  render(<Specimen />);\n  expect(screen.getByText(\"Recommended\")).toBeTruthy();\n});\n"
	if got := scanFixture(t, "a.test.tsx", src, "Recommended"); len(got) != 1 {
		t.Errorf("copy-only control: targets = %+v, want one", got)
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
	tsTest := func(name, phrase string) string {
		return "test(\"" + name + "\", () => {\n  expect(screen.getByText(\"" + phrase + "\")).toBeTruthy();\n});\n"
	}

	write("pkg/x.go", "package x\nconst a = \"Go phrase here\"\n")
	write("pkg/x_test.go", "package x\n"+goTest("TestShowsGoPhrase", "Go phrase here")+goTest("TestShowsAssetPhrase", "asset phrase here"))
	write("pkg/prompt.md", "An asset phrase here.\n")
	write("pkg/fixture_test.go", "package x\n"+goTest("TestShowsTestOnlyPhrase", "only in tests here"))
	write("docs/guide.md", "a docs phrase here\n")
	write("pkg/doc_test.go", "package x\n"+goTest("TestShowsDocsPhrase", "a docs phrase here"))

	front := "cmd/evener-hub/frontend/src/"
	write(front+"Empty.tsx", "export const Empty = () => <p>Nothing to show</p>;\n")
	write(front+"Empty.test.tsx", tsTest("shows nothing", "Nothing to show"))
	write(front+"helper.d.ts", "declare const x: \"Declared phrase\";\n")
	write(front+"Declared.test.tsx", tsTest("shows the declared phrase", "Declared phrase"))
	write(front+"testing/fake.ts", "export const f = \"Fake phrase\";\n")
	write(front+"Fake.test.tsx", tsTest("shows the fake phrase", "Fake phrase"))
	write("other/src/Other.tsx", "export const o = \"Other tree phrase\";\n")
	write("other/src/Other.test.tsx", tsTest("shows the other phrase", "Other tree phrase"))
	write("mobile-native/node_modules/dep/index.ts", "export const d = \"Vendored phrase\";\n")
	write("mobile-native/src/Dep.test.ts", tsTest("shows the vendored phrase", "Vendored phrase"))
	// mobile-native typechecks and tests the shared mobile/src tree, so its
	// tests are scanned and its sources are production copy for both trees.
	write("mobile/src/state/shared.ts", "export const s = \"Shared mobile phrase\";\n")
	write("mobile/src/state/shared.test.ts", tsTest("shows the shared phrase", "Shared mobile phrase"))
	write("mobile-native/src/Shared.test.ts", tsTest("shows the shared phrase", "Shared mobile phrase"))

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
		{File: "cmd/evener-hub/frontend/src/Empty.test.tsx", Test: "shows nothing", Line: 1},
		{File: "mobile-native/src/Shared.test.ts", Test: "shows the shared phrase", Line: 1},
		{File: "mobile/src/state/shared.test.ts", Test: "shows the shared phrase", Line: 1},
		{File: "pkg/x_test.go", Test: "TestShowsGoPhrase", Line: 2},
		{File: "pkg/x_test.go", Test: "TestShowsAssetPhrase", Line: 7},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("Scan = %+v\nwant %+v", targets, want)
	}
}

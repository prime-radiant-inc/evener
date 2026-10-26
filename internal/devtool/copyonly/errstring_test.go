package copyonly

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// gitRepo writes files (slash path to body) into a new git checkout and
// tracks them.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "--", "."}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

const errStringSrc = `package x

func TestRejectsEmptyName(t *testing.T) {
	_, err := create("")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatal(err)
	}
}

func TestRejectsBlankName(t *testing.T) {
	_, err := create(" ")
	if !strings.Contains(err.Error(), "name is required") {
		t.Fatal(err)
	}
}

func TestMatchesTheSentinel(t *testing.T) {
	_, err := create("")
	if !errors.Is(err, ErrNameRequired) {
		t.Fatal(err)
	}
}

func TestEchoesTheNameItWasGiven(t *testing.T) {
	_, err := create("only in this test")
	if !strings.Contains(err.Error(), "only in this test") {
		t.Fatal(err)
	}
}

func TestHelpMentionsUsage(t *testing.T) {
	if !strings.Contains(help(), "Usage: evener") {
		t.Fatal("help")
	}
}
`

func TestScanErrStringsCountsErrorMessageAssertionsPerFile(t *testing.T) {
	t.Parallel()
	root := gitRepo(t, map[string]string{
		"prod.go":       "package x\n\nvar _ = \"name is required\"\nvar _ = \"Usage: evener\"\n",
		"a_test.go":     errStringSrc,
		"sub/b_test.go": "package sub\n",
		"sub/c_test.go": strings.ReplaceAll(errStringSrc, "TestRejects", "TestAlsoRejects"),
	})
	got, err := ScanErrStrings(root)
	if err != nil {
		t.Fatalf("ScanErrStrings: %v", err)
	}
	// Two assertions on production error text count. A sentinel match, the
	// test's own echoed fixture and non-error output do not.
	want := map[string]int{"a_test.go": 2, "sub/c_test.go": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanErrStrings = %v, want %v", got, want)
	}
}

func TestReadErrStringBudgets(t *testing.T) {
	t.Parallel()
	got, err := ReadErrStringBudgets(strings.NewReader("file\tcount\na_test.go\t12\n\nb_test.go\t1\n"))
	if err != nil {
		t.Fatalf("ReadErrStringBudgets: %v", err)
	}
	want := []ErrStringBudget{{File: "a_test.go", Count: 12}, {File: "b_test.go", Count: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadErrStringBudgets = %+v, want %+v", got, want)
	}
}

func TestReadErrStringBudgetsRefusesMalformedRows(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"no header":      "a_test.go\t3\n",
		"wrong header":   "file\ttest\treason\n",
		"not a number":   "file\tcount\na_test.go\tmany\n",
		"zero":           "file\tcount\na_test.go\t0\n",
		"negative":       "file\tcount\na_test.go\t-2\n",
		"one column":     "file\tcount\na_test.go\n",
		"three columns":  "file\tcount\na_test.go\t3\twhy\n",
		"duplicate file": "file\tcount\na_test.go\t3\na_test.go\t2\n",
	} {
		if _, err := ReadErrStringBudgets(strings.NewReader(body)); err == nil {
			t.Errorf("%s: ReadErrStringBudgets accepted %q", name, body)
		}
	}
}

func TestCompareErrStrings(t *testing.T) {
	t.Parallel()
	counts := map[string]int{"same_test.go": 3, "grew_test.go": 5, "shrank_test.go": 1, "new_test.go": 2}
	budgets := []ErrStringBudget{
		{File: "same_test.go", Count: 3},
		{File: "grew_test.go", Count: 4},
		{File: "shrank_test.go", Count: 4},
		{File: "gone_test.go", Count: 2},
	}
	over, slack := CompareErrStrings(counts, budgets)
	wantOver := []ErrStringDelta{{File: "grew_test.go", Count: 5, Budget: 4}, {File: "new_test.go", Count: 2, Budget: 0}}
	wantSlack := []ErrStringDelta{{File: "gone_test.go", Count: 0, Budget: 2}, {File: "shrank_test.go", Count: 1, Budget: 4}}
	if !reflect.DeepEqual(over, wantOver) {
		t.Errorf("over = %+v, want %+v", over, wantOver)
	}
	if !reflect.DeepEqual(slack, wantSlack) {
		t.Errorf("slack = %+v, want %+v", slack, wantSlack)
	}
}

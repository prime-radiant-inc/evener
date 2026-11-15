package testredundancy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/gittest"
)

// committedFixture is the fixture runner with its package committed to a real
// repository: Mutate's restore is git's, so the test needs git's index.
func committedFixture(t *testing.T) (*Runner, string) {
	t.Helper()
	r, dir := fixtureRunner(t)
	gittest.Run(t, dir, "init", "-q")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-q", "-m", "fixture")
	return r, dir
}

// Each mutation is applied alone, judged by the tests it names, and restored:
// a mutation a named test fails on, or that hangs the binary, is caught; one
// they all pass is survived; and one that cannot be applied or built, selects
// no test or every test (before or after the mutation), or whose tests already
// fail on the unmutated source is invalid rather than caught.
func TestMutateJudgesEachMutationAndRestoresTheSource(t *testing.T) {
	r, dir := committedFixture(t)
	// Short enough that the init hang ends quickly, long enough for a
	// fixture test on a loaded host.
	r.testTimeout = 5 * time.Second
	t.Setenv("FIXTURE_FAIL", "1")
	src := filepath.Join(dir, "calc.go")
	original, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	muts := []Mutation{
		{Name: "zero-flips", File: "calc.go", Old: "return 0", New: "return 2", Run: "^TestSignZero$"},
		{Name: "positive-unchecked", File: "calc.go", Old: "return 1", New: "return 2", Run: "^TestSignPositiveUnchecked$"},
		{Name: "absent-text", File: "calc.go", Old: "return 42", New: "return 2", Run: "^TestSignZero$"},
		{Name: "does-not-build", File: "calc.go", Old: "return 0", New: "return zero", Run: "^TestSignZero$"},
		{Name: "no-tests", File: "calc.go", Old: "return 0", New: "return 2", Run: "^TestNoSuchTest$"},
		{Name: "fails-unmutated", File: "calc.go", Old: "return 0", New: "return 2", Run: "^(TestSignZero|TestFailsWhenAsked)$"},
		{Name: "selects-everything", File: "calc.go", Old: "return 0", New: "return 2", Run: ""},
		{Name: "hangs-in-init", File: "calc.go", Old: "// Abs returns", New: "func init() {\n\tfor {\n\t}\n}\n\n// Abs returns", Run: "^TestSignZero$"},
		{Name: "renames-the-test", File: "calc_test.go", Old: "func TestSignZero(", New: "func signZero(", Run: "^TestSignZero$"},
	}
	results, err := r.Mutate(t.Context(), muts)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	want := map[string]Outcome{
		"zero-flips":         OutcomeCaught,
		"positive-unchecked": OutcomeSurvived,
		"absent-text":        OutcomeInvalid,
		"does-not-build":     OutcomeInvalid,
		"no-tests":           OutcomeInvalid,
		"fails-unmutated":    OutcomeInvalid,
		"selects-everything": OutcomeInvalid,
		"hangs-in-init":      OutcomeCaught,
		"renames-the-test":   OutcomeInvalid,
	}
	for _, res := range results {
		if res.Outcome != want[res.Name] {
			t.Errorf("%s: %s, want %s (%s)", res.Name, res.Outcome, want[res.Name], res.Detail)
		}
	}
	if len(results) != len(muts) {
		t.Fatalf("got %d results for %d mutations", len(results), len(muts))
	}
	caught := results[0]
	if len(caught.Failed) != 1 || caught.Failed[0] != "TestSignZero" {
		t.Errorf("caught mutation names failing tests %v, want TestSignZero", caught.Failed)
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("source not restored after mutating:\n%s", after)
	}
}

// Mutate restores with a file-scoped git restore, which would discard
// uncommitted work, cannot bring back an untracked or ignored file, and puts
// back a symlink rather than the file it points to. Each such target is
// refused before any file changes.
func TestMutateRefusesFilesGitCannotRestore(t *testing.T) {
	cases := []struct {
		name string
		// prepare makes the target and returns the file whose content must
		// survive the refused mutation.
		prepare func(t *testing.T, dir string) (target, guarded string)
	}{
		{"modified", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "calc.go")
			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(src, append([]byte("// uncommitted edit\n"), data...), 0o644); err != nil {
				t.Fatal(err)
			}
			return "calc.go", src
		}},
		{"ignored", func(t *testing.T, dir string) (string, string) {
			extra := filepath.Join(dir, "extra.go")
			if err := os.WriteFile(extra, []byte("package calc\n\nfunc extra() int { return 0 }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("extra.go\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return "extra.go", extra
		}},
		{"symlink", func(t *testing.T, dir string) (string, string) {
			if err := os.Symlink("calc.go", filepath.Join(dir, "link.txt")); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, dir, "add", "link.txt")
			gittest.Run(t, dir, "commit", "-q", "-m", "link")
			return "link.txt", filepath.Join(dir, "calc.go")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, dir := committedFixture(t)
			target, guarded := tc.prepare(t, dir)
			before, err := os.ReadFile(guarded)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Mutate(t.Context(), []Mutation{{Name: "m", File: target, Old: "return 0", New: "return 2", Run: "^TestSignZero$"}})
			if !errors.Is(err, ErrNotRestorable) {
				t.Fatalf("Mutate of a %s file: %v, want ErrNotRestorable", tc.name, err)
			}
			after, err := os.ReadFile(guarded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("%s changed:\n%s", guarded, after)
			}
		})
	}
}

package testredundancy

import (
	"os"
	"path/filepath"
	"testing"

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
// a mutation a named test fails on is caught, one they all pass is survived,
// and one that cannot be applied or built, selects no test or every test, or
// whose tests already fail on the unmutated source is invalid rather than
// caught.
func TestMutateJudgesEachMutationAndRestoresTheSource(t *testing.T) {
	r, dir := committedFixture(t)
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
	if string(after) != string(original) {
		t.Fatalf("source not restored after mutating:\n%s", after)
	}
}

// Restoring is a file-scoped git restore, which would discard uncommitted work
// in that file, so a mutation of a modified file is refused before any change.
func TestMutateRefusesAModifiedFile(t *testing.T) {
	r, dir := committedFixture(t)
	src := filepath.Join(dir, "calc.go")
	edited := []byte("// uncommitted edit\n")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	edited = append(edited, data...)
	if err := os.WriteFile(src, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Mutate(t.Context(), []Mutation{{Name: "m", File: "calc.go", Old: "return 0", New: "return 2", Run: "^TestSignZero$"}})
	if err == nil {
		t.Fatal("Mutate accepted a file with uncommitted changes")
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(edited) {
		t.Fatalf("the uncommitted edit was lost:\n%s", after)
	}
}

// git restore cannot bring back a file git does not track, so a mutation of an
// untracked or ignored file is refused before any change.
func TestMutateRefusesAnUntrackedFile(t *testing.T) {
	r, dir := committedFixture(t)
	extra := filepath.Join(dir, "extra.go")
	original := []byte("package calc\n\nfunc extra() int { return 0 }\n")
	if err := os.WriteFile(extra, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("extra.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Mutate(t.Context(), []Mutation{{Name: "m", File: "extra.go", Old: "return 0", New: "return 2", Run: "^TestSignZero$"}})
	if err == nil {
		t.Fatal("Mutate accepted a file git does not track")
	}
	after, err := os.ReadFile(extra)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("the untracked file was changed:\n%s", after)
	}
}

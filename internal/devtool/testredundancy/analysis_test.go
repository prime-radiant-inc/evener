package testredundancy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// parseProfileText parses an inline coverage profile.
func parseProfileText(t *testing.T, text string) Profile {
	t.Helper()
	p, err := ParseProfile(strings.NewReader("mode: set\n" + text))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	return p
}

// A candidate's unique statements are the ones neither the rest of the package
// nor any other candidate reaches; a block two candidates share is unique to
// neither, so deleting both would need a separate decision.
func TestUniqueCoverageExcludesTheRestAndTheOtherCandidates(t *testing.T) {
	rest := parseProfileText(t, ""+
		"m/p/a.go:1.1,2.2 3 1\n"+
		"m/p/a.go:3.1,4.2 2 0\n"+
		"m/p/a.go:5.1,6.2 4 0\n"+
		"m/p/a.go:7.1,8.2 1 0\n")
	candidates := map[string]Profile{
		"TestOnlyRest":   parseProfileText(t, "m/p/a.go:1.1,2.2 3 1\nm/p/a.go:3.1,4.2 2 0\n"),
		"TestOwnsBlock":  parseProfileText(t, "m/p/a.go:1.1,2.2 3 1\nm/p/a.go:3.1,4.2 2 1\n"),
		"TestSharesOne":  parseProfileText(t, "m/p/a.go:5.1,6.2 4 1\n"),
		"TestSharesTwo":  parseProfileText(t, "m/p/a.go:5.1,6.2 4 1\nm/p/a.go:7.1,8.2 1 1\n"),
		"TestNoCoverage": parseProfileText(t, "m/p/a.go:1.1,2.2 3 0\n"),
	}
	got := UniqueCoverage(candidates, rest)

	want := map[string]int{
		"TestOnlyRest":   0,
		"TestNoCoverage": 0,
		"TestSharesOne":  0,
		"TestOwnsBlock":  2,
		"TestSharesTwo":  1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for _, u := range got {
		if u.Stmts != want[u.Name] {
			t.Errorf("%s: %d unique statements, want %d (%+v)", u.Name, u.Stmts, want[u.Name], u.Blocks)
		}
	}
	owns := got[slices.IndexFunc(got, func(u Unique) bool { return u.Name == "TestOwnsBlock" })]
	if len(owns.Blocks) != 1 || owns.Blocks[0] != (BlockRef{File: "m/p/a.go", StartLine: 3, StartCol: 1, EndLine: 4, EndCol: 2, Stmts: 2}) {
		t.Errorf("TestOwnsBlock blocks = %+v, want the one block at 3.1,4.2", owns.Blocks)
	}
	// Rows with the fewest unique statements come first: they are the deletions.
	for i := 1; i < len(got); i++ {
		if got[i-1].Stmts > got[i].Stmts {
			t.Fatalf("rows not ordered by unique statements: %+v", got)
		}
	}
}

// Two blocks on the same lines with different columns are different blocks:
// a one-line `if x { return }` holds two.
func TestProfileKeepsBlocksThatDifferOnlyInColumns(t *testing.T) {
	p := parseProfileText(t, "m/p/a.go:1.5,1.20 1 1\nm/p/a.go:1.22,1.30 1 0\n")
	if p.Total() != 2 || p.Covered() != 1 {
		t.Fatalf("total %d covered %d, want 2 and 1", p.Total(), p.Covered())
	}
}

// The pair ranking puts the test whose coverage the other contains on the
// removable side, and orders by containment before assertion similarity.
func TestRankPairsOrdersByContainmentThenAssertions(t *testing.T) {
	cov := map[string]Profile{
		"TestSmall":    parseProfileText(t, "m/p/a.go:1.1,2.2 1 1\n"),
		"TestBig":      parseProfileText(t, "m/p/a.go:1.1,2.2 1 1\nm/p/a.go:3.1,4.2 1 1\n"),
		"TestElse":     parseProfileText(t, "m/p/a.go:5.1,6.2 1 1\n"),
		"TestElseCopy": parseProfileText(t, "m/p/a.go:5.1,6.2 1 1\n"),
	}
	asserts := map[string][]string{
		"TestSmall":    {"if got != _"},
		"TestBig":      {"if got != _", "if err != nil"},
		"TestElse":     {"if x"},
		"TestElseCopy": {"if x"},
	}
	pairs := RankPairs(cov, asserts)
	if len(pairs) < 2 {
		t.Fatalf("got %d pairs, want at least 2: %+v", len(pairs), pairs)
	}
	top := pairs[0]
	if top.Containment != 1 || top.AssertSimilarity != 1 {
		t.Fatalf("top pair = %+v, want the identical TestElse pair", top)
	}
	second := pairs[1]
	if second.Removable != "TestSmall" || second.Keeper != "TestBig" || second.Containment != 1 || second.AssertSimilarity != 0.5 {
		t.Fatalf("second pair = %+v, want TestSmall contained in TestBig with similarity 0.5", second)
	}
	for _, p := range pairs {
		if p.Containment == 0 {
			t.Fatalf("pairs with no shared coverage are listed: %+v", p)
		}
	}
}

// Assertions are the conditions guarding a t.Fatal/t.Error call plus the calls
// that hand the test's *testing.T to a helper, with literals normalized so two
// tests that check the same thing against different values compare equal.
func TestAssertionsReadsGuardedFailuresAndHelperCalls(t *testing.T) {
	dir := t.TempDir()
	src := `package p

import "testing"

func TestChecks(t *testing.T) {
	got := f(2)
	if got != 4 {
		t.Fatalf("got %d", got)
	}
	requireEqual(t, got, 4)
	if got > 0 {
		_ = got
	}
}

func TestSilent(t *testing.T) {
	_ = f(3)
}

func helperNotATest(t *testing.T) {
	if true {
		t.Fatal()
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "p_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Assertions(dir)
	if err != nil {
		t.Fatalf("Assertions: %v", err)
	}
	if len(got["TestChecks"]) != 2 {
		t.Fatalf("TestChecks assertions = %q, want the guarded failure and the helper call", got["TestChecks"])
	}
	if len(got["TestSilent"]) != 0 {
		t.Fatalf("TestSilent assertions = %q, want none", got["TestSilent"])
	}
	if _, ok := got["helperNotATest"]; ok {
		t.Fatalf("a helper was read as a test: %q", got)
	}
	// The same check against another literal normalizes to the same assertion.
	other := strings.ReplaceAll(src, "got != 4", "got != 9")
	if err := os.WriteFile(filepath.Join(dir, "p_test.go"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Assertions(dir)
	if err != nil {
		t.Fatalf("Assertions: %v", err)
	}
	if !slices.Equal(again["TestChecks"], got["TestChecks"]) {
		t.Fatalf("literal change altered assertions: %q vs %q", again["TestChecks"], got["TestChecks"])
	}
}

package evener_test

import (
	"bytes"
	"os"
	"testing"

	"primeradiant.com/evener/internal/devtool/copyonly"
)

const copyOnlyAllowlist = "internal/devtool/copyonly/allowlist.tsv"

// TestNoNewCopyOnlyTests is the ratchet behind the testing doc's "Copy Is Not
// a Test Oracle" for Go: a test that exists only to check wording (its name
// says it shows, renders or says something, and its every check compares
// output with production copy) may not land unless the allowlist names it with
// a reason. The allowlist should only shrink: an entry whose test is
// converted, deleted or renamed is stale and must go, so the list stays a true
// record of what is still kept and why. TypeScript tests have the same audit
// in cmd/evener-hub/frontend/src/dev/copyonly/copyonlyAudit.test.ts.
func TestNoNewCopyOnlyTests(t *testing.T) {
	t.Parallel()
	targets, err := copyonly.Scan(".")
	if err != nil {
		t.Fatalf("copyonly.Scan: %v", err)
	}
	body, err := os.ReadFile(copyOnlyAllowlist)
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	allowed, err := copyonly.ReadAllowlist(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", copyOnlyAllowlist, err)
	}

	unlisted, stale := copyonly.Compare(targets, allowed)
	for _, target := range unlisted {
		t.Errorf("%s:%d: test %q only checks wording. Assert the behavior the copy is for instead "+
			"(docs/developing-evener/testing.md, \"Copy Is Not a Test Oracle\"), or add it to %s with the reason it is kept",
			target.File, target.Line, target.Test, copyOnlyAllowlist)
	}
	for _, entry := range stale {
		t.Errorf("%s lists %s %q, which is no longer a copy-only test; remove the stale entry",
			copyOnlyAllowlist, entry.File, entry.Test)
	}
}

const errStringAllowlist = "internal/devtool/copyonly/errstring_allowlist.tsv"

// TestNoNewErrorMessageAssertions is the ratchet for tests that check an
// error's text against a phrase from production source instead of matching a
// sentinel, type or code. Each test file may hold no more such checks than
// its row in the allowlist, and a file with no row may hold none. The counts
// only go down: a file that holds fewer than its row must lower or remove the
// row, so the list stays a true record of what is left to convert.
func TestNoNewErrorMessageAssertions(t *testing.T) {
	t.Parallel()
	counts, err := copyonly.ScanErrStrings(".")
	if err != nil {
		t.Fatalf("copyonly.ScanErrStrings: %v", err)
	}
	body, err := os.ReadFile(errStringAllowlist)
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	budgets, err := copyonly.ReadErrStringBudgets(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", errStringAllowlist, err)
	}

	over, slack := copyonly.CompareErrStrings(counts, budgets)
	for _, d := range over {
		t.Errorf("%s checks error text against production copy %d time(s), over its budget of %d. "+
			"Match a sentinel with errors.Is, a type with errors.As, or a code instead "+
			"(docs/developing-evener/testing.md, \"Copy Is Not a Test Oracle\")",
			d.File, d.Count, d.Budget)
	}
	for _, d := range slack {
		t.Errorf("%s now holds %d error-text checks, under its budget of %d; set the count in %s to %d (remove the row at 0)",
			d.File, d.Count, d.Budget, errStringAllowlist, d.Count)
	}
}

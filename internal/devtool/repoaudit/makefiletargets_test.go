package repoaudit

import (
	"slices"
	"strings"
	"testing"
)

// TestEveryRuleHasARecipeCatchesTrailingComment pins the first hollow-gate
// shape rulesWithoutRecipesInLines must not read as "has prerequisites": a
// rule line whose only text after the colon is a "#…" comment. This is
// exactly the shape a rebase conflict resolution left behind on
// lint-fuzz-registry, and `make` runs it as "Nothing to be done" — a gate
// that reports green while checking nothing.
func TestEveryRuleHasARecipeCatchesTrailingComment(t *testing.T) {
	t.Parallel()
	lines := strings.Split("lint-fuzz-registry: # TODO restore after the rebase\n", "\n")
	empty, total := rulesWithoutRecipesInLines(lines)
	if total != 1 {
		t.Fatalf("expected 1 rule declaration parsed, got %d", total)
	}
	if !slices.Contains(empty, "lint-fuzz-registry") {
		t.Fatalf("expected lint-fuzz-registry to be reported as recipe-less (a trailing comment "+
			"is not a prerequisite), got %v", empty)
	}
}

// TestEveryRuleHasARecipeCatchesSemicolonOnlyRecipe pins the second
// hollow-gate shape: a rule line ending in a bare ";" with nothing after
// it. GNU make reads ";" as introducing an inline recipe, and an inline
// recipe with no text is empty — the same "Nothing to be done" outcome as
// no recipe at all — but the raw text after the colon (" ;") is non-empty,
// so a verbatim emptiness test misreads it as a prerequisite list.
func TestEveryRuleHasARecipeCatchesSemicolonOnlyRecipe(t *testing.T) {
	t.Parallel()
	lines := strings.Split("lint-fuzz-registry: ;\n", "\n")
	empty, total := rulesWithoutRecipesInLines(lines)
	if total != 1 {
		t.Fatalf("expected 1 rule declaration parsed, got %d", total)
	}
	if !slices.Contains(empty, "lint-fuzz-registry") {
		t.Fatalf("expected lint-fuzz-registry to be reported as recipe-less (a bare \";\" is an "+
			"empty inline recipe, not a prerequisite), got %v", empty)
	}
}

// TestEveryRuleHasARecipeAllowsRealSemicolonRecipe guards the fix above
// against overcorrecting: a rule whose inline recipe after ";" actually has
// content must NOT be reported as recipe-less.
func TestEveryRuleHasARecipeAllowsRealSemicolonRecipe(t *testing.T) {
	t.Parallel()
	lines := strings.Split("lint-fuzz-registry: ; echo hi\n", "\n")
	empty, total := rulesWithoutRecipesInLines(lines)
	if total != 1 {
		t.Fatalf("expected 1 rule declaration parsed, got %d", total)
	}
	if slices.Contains(empty, "lint-fuzz-registry") {
		t.Fatalf("lint-fuzz-registry has a real inline recipe after \";\" and should not be "+
			"reported as recipe-less, got %v", empty)
	}
}

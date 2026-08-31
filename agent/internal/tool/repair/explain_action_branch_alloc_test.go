package repair

import (
	"testing"
)

// The branch detection runs on every explained schema error, including MCP
// tool schemas where no action selector exists and it never fires. On such a
// schema it must not allocate: the selector walk checks the required list
// and enum for presence without materializing []string copies (finding #3
// from the #626 review; round 3 extended this to the enum-membership check,
// which now uses listContains instead of asStringSlice).
func TestActionSelector_NoAllocationsOnSelectorlessSchema(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
		"required":   []string{"file_path"},
	}
	args := map[string]any{}
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = actionSelector(params, args)
	})
	if allocs > 0 {
		t.Fatalf("actionSelector allocated %v per run on a selector-less schema; want 0", allocs)
	}
}

// The enum-membership check inside namedBranch must also be
// allocation-free: a schema with a selector and a tagged property whose tag
// is not in the enum (the guard's not-firing branch) walks the enum without
// materializing a []string copy (round 3, finding E2).
func TestNamedBranch_NoAllocationsWhenTagOutsideEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []any{"view", "append", "update"}},
			"slices": map[string]any{
				"type":        "array",
				"description": "For large files read in slices: line ranges.",
			},
		},
		"required": []string{"action"},
	}
	args := map[string]any{"action": "view"}
	// The container path must use the display form ("slices[0]", as
	// resolveSchemaErrorContainer's formatPath renders it) — a JSON-Pointer
	// form like "slices/0" parses as a single property name in
	// pathRootProperty, finds no such property, and exits at the empty-tag
	// check before the enum walk the test exists to measure.
	allocs := testing.AllocsPerRun(100, func() {
		_, _, _ = namedBranch(params, args, "slices[0]")
	})
	if allocs > 0 {
		t.Fatalf("namedBranch allocated %v per run when the tag is outside the enum; want 0", allocs)
	}
}

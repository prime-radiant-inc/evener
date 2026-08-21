package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestRenderWideTableForTargetsWithFields is the populated shape: a target
// carrying all four structured fields renders as one row of the six-column
// wide table, command wrapped in backticks as `make <name>`, with the
// summary in the second column (ruling R25 — spec §3's own example predates
// it and shows five).
func TestRenderWideTableForTargetsWithFields(t *testing.T) {
	targets := []Target{{
		Name:      "lint",
		Summary:   "Go lint, formatting, tagged floors, generated outputs, and secrets.",
		Proves:    "TOML naming; gofmt over every tracked .go file.",
		Trigger:   "required CI; local pre-merge.",
		Requires:  "golangci-lint, gitleaks.",
		FailsWhen: "any member of LINT_TARGETS exits nonzero.",
	}}
	got := Render(targets)
	want := "| Command | Summary | What it proves | Trigger | Requires | Fails when |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| `make lint` | Go lint, formatting, tagged floors, generated outputs, and secrets. | TOML naming; gofmt over every tracked .go file. | required CI; local pre-merge. | golangci-lint, gitleaks. | any member of LINT_TARGETS exits nonzero. |"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderWideTablePublishesTheSummary is ruling R25's regression guard,
// stated on its own rather than left implicit in the shape assertion above.
// Before R25 the wide table carried only the four structured fields, so a
// gate target's summary reached no doc and lint-generated could not see it
// drift: mutating coverage-floor's summary left every generated region
// byte-identical and `make lint` passed. Keep the summary in a published
// column, or that hole reopens for every fielded target in the repository.
func TestRenderWideTablePublishesTheSummary(t *testing.T) {
	targets := []Target{{
		Name:    "coverage-floor",
		Summary: "The repo's one coverage ratchet.",
		Proves:  "Per-module statement coverage against its floor.",
	}}
	got := Render(targets)
	// Both assertions are load-bearing. Presence alone would still pass if a
	// hasFields() regression demoted a fielded target into the COMPACT list,
	// where its summary is published but its four structured fields are not
	// — the summary would be there, in the wrong table. Pin the wide header
	// too, so this test can only pass when a fielded target renders wide AND
	// carries its summary.
	if !strings.Contains(got, wideTableHeader) {
		t.Fatalf("a target with fields did not render in the wide table:\n%s", got)
	}
	if !strings.Contains(got, "The repo's one coverage ratchet.") {
		t.Fatalf("wide table dropped the summary, so nothing gates it:\n%s", got)
	}
}

// TestRenderCompactListForSummaryOnlyTargets: a target with only a summary
// (no structured fields) must not get four empty wide-table cells (spec
// §3). It renders as a two-column Command/Summary list instead. No wide
// table exists here for it to be "other" than, so the family gets no
// "Other targets" subheading — see
// TestRenderMixedFamilyPutsCompactListAfterWideTable for the shape where
// the heading is warranted.
func TestRenderCompactListForSummaryOnlyTargets(t *testing.T) {
	targets := []Target{{
		Name:    "clean",
		Summary: "Remove the built binaries from the repo root.",
	}}
	got := Render(targets)
	want := "| Command | Summary |\n" +
		"| --- | --- |\n" +
		"| `make clean` | Remove the built binaries from the repo root. |"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderMixedFamilyPutsCompactListAfterWideTable: a family with both
// shapes puts every fielded target in the wide table (in file order),
// followed by a blank line, then the "Other targets" section for every
// summary-only target (also in file order) — never mixed into the same
// table, and never appearing as empty cells in the wide one.
func TestRenderMixedFamilyPutsCompactListAfterWideTable(t *testing.T) {
	targets := []Target{
		{Name: "build", Summary: "Build the binary.", Proves: "It compiles.", Trigger: "CI.", Requires: "Go.", FailsWhen: "Build fails."},
		{Name: "clean", Summary: "Remove the built binary."},
	}
	got := Render(targets)
	want := "| Command | Summary | What it proves | Trigger | Requires | Fails when |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| `make build` | Build the binary. | It compiles. | CI. | Go. | Build fails. |\n" +
		"\n" +
		"### Other targets\n\n" +
		"| Command | Summary |\n" +
		"| --- | --- |\n" +
		"| `make clean` | Remove the built binary. |"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderAllTargetsLackFieldsEmitsNoEmptyWideTable pins spec §3's
// sharpest edge: a family whose targets ALL lack structured fields emits
// only the compact list, never an empty (header-only) wide table above it
// — and, since there is no wide table for it to be "other" than, no
// "Other targets" subheading either. make/repo.mk turned out to be exactly
// this case for real once annotated: every target there is a summary-only
// repo chore, none a gate, so its README.md region is the compact list with
// no heading at all.
func TestRenderAllTargetsLackFieldsEmitsNoEmptyWideTable(t *testing.T) {
	targets := []Target{
		{Name: "fuzz-ledger", Summary: "Pretty-print the triage ledger."},
		{Name: "fuzz-nightly", Summary: "Run the unbounded coverage-guided search."},
	}
	got := Render(targets)
	// The compact list's header is a PREFIX of the wide table's since R25 put
	// Summary second in both ("| Command | Summary |" vs
	// "| Command | Summary | What it proves | …"), so tell them apart on the
	// wide table's full header rather than on a shared prefix.
	if strings.HasPrefix(got, "| Command | Summary | What it proves |") {
		t.Fatalf("got an empty wide table header when no target has fields:\n%s", got)
	}
	if strings.Contains(got, "Other targets") {
		t.Fatalf("got an \"Other targets\" heading with no wide table for it to be other than:\n%s", got)
	}
	want := "| Command | Summary |\n" +
		"| --- | --- |\n" +
		"| `make fuzz-ledger` | Pretty-print the triage ledger. |\n" +
		"| `make fuzz-nightly` | Run the unbounded coverage-guided search. |"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderEscapesPipeInCellText: no annotation in make/*.mk currently
// contains a literal "|", but a table cell that did would otherwise split
// into extra (broken) columns. Defend against that rather than assume it
// never happens.
func TestRenderEscapesPipeInCellText(t *testing.T) {
	targets := []Target{{
		Name:    "build",
		Summary: "Build A | B.",
	}}
	got := Render(targets)
	want := "| Command | Summary |\n" +
		"| --- | --- |\n" +
		"| `make build` | Build A \\| B. |"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderEmptyTargetsIsEmptyString: no real family is ever empty, but
// Render should not panic or emit stray headings for a zero-target input.
func TestRenderEmptyTargetsIsEmptyString(t *testing.T) {
	if got := Render(nil); got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

const testDocTemplate = `# Some Family

Hand-written prose above the region, including a pipe | for good measure.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/%s.mk, then run ` + "`make generate`" + `. -->
%s<!-- END GENERATED -->

More hand-written prose below the region.
`

// TestRewriteRegionReplacesOnlyMarkedSpan is the property that matters most
// per task-11-brief.md: everything outside the BEGIN/END markers, including
// the family-specific marker lines themselves, survives byte-for-byte.
func TestRewriteRegionReplacesOnlyMarkedSpan(t *testing.T) {
	doc := fmt.Appendf(nil, testDocTemplate, "building", "")
	got, err := RewriteRegion(doc, "building", "| Command |\n| --- |\n| `make build` | ... |")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Appendf(nil, testDocTemplate, "building", "| Command |\n| --- |\n| `make build` | ... |\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRewriteRegionOnEmptyRegionInsertsBody matches the six real docs'
// current committed state: BEGIN immediately followed by END, nothing
// between them yet.
func TestRewriteRegionOnEmptyRegionInsertsBody(t *testing.T) {
	doc := fmt.Appendf(nil, testDocTemplate, "linting", "")
	got, err := RewriteRegion(doc, "linting", "body line one\nbody line two")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Appendf(nil, testDocTemplate, "linting", "body line one\nbody line two\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRewriteRegionReplacingExistingBodyIsIdempotentInputShape: calling
// RewriteRegion again on output that already carries a body (rather than
// the pristine empty-region form) still replaces only the marked span, not
// just the empty case.
func TestRewriteRegionReplacingExistingBodyIsIdempotentInputShape(t *testing.T) {
	doc := fmt.Appendf(nil, testDocTemplate, "coverage", "stale body\n")
	got, err := RewriteRegion(doc, "coverage", "fresh body")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Appendf(nil, testDocTemplate, "coverage", "fresh body\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRewriteRegionMissingMarkerPairIsAnError: a doc with no GENERATED
// marker at all for the requested family is a hard error, not a silent
// no-op — task-11-brief.md Step 1.
func TestRewriteRegionMissingMarkerPairIsAnError(t *testing.T) {
	doc := []byte("# A doc with no marked region at all.\n")
	if _, err := RewriteRegion(doc, "building", "body"); err == nil {
		t.Fatal("expected an error: doc has no marked region")
	}
}

// TestRewriteRegionFamilyMismatchIsAnError: a marker present in the doc but
// naming a different family than the one requested must not be silently
// treated as a match — this is what would let a stale or misspelled marker
// vanish undetected.
func TestRewriteRegionFamilyMismatchIsAnError(t *testing.T) {
	doc := fmt.Appendf(nil, testDocTemplate, "testing", "")
	if _, err := RewriteRegion(doc, "building", "body"); err == nil {
		t.Fatal("expected an error: the doc's marker names make/testing.mk, not make/building.mk")
	}
}

// TestRewriteRegionUnterminatedRegionIsAnError: a BEGIN marker with no
// matching END before EOF is a hard error — task-11-brief.md Step 1.
func TestRewriteRegionUnterminatedRegionIsAnError(t *testing.T) {
	doc := []byte("<!-- BEGIN GENERATED: make targets. Edit make/building.mk, then run `make generate`. -->\nno end marker here\n")
	if _, err := RewriteRegion(doc, "building", "body"); err == nil {
		t.Fatal("expected an error: no END GENERATED marker before EOF")
	}
}

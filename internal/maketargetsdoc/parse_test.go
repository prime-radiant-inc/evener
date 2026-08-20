package maketargetsdoc

import "testing"

// TestParseFamilySummaryOnly is the minimal shape: a one-line summary and
// nothing else. Required by task-10-brief.md Step 1.
func TestParseFamilySummaryOnly(t *testing.T) {
	got, err := ParseFamily([]byte("## Remove the built binaries.\nclean:\n\trm -f evener\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "clean" || got[0].Summary != "Remove the built binaries." {
		t.Fatalf("got %+v", got)
	}
}

// TestParseFamilyAllFourFields uses the exact example from spec §2, so the
// parser is checked against the grammar's own reference block rather than a
// paraphrase of it.
func TestParseFamilyAllFourFields(t *testing.T) {
	src := "## Go lint, formatting, tagged floors, generated outputs, and secrets.\n" +
		"## proves: TOML naming; gofmt over every tracked .go file; the evenerfuzz and\n" +
		"##   eval compile floors; the internal-type check; golangci-lint across every\n" +
		"##   workspace module.\n" +
		"## trigger: required CI; local pre-merge.\n" +
		"## requires: golangci-lint, gitleaks.\n" +
		"## fails-when: any member of LINT_TARGETS exits nonzero.\n" +
		"lint: $(LINT_TARGETS)\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d targets, want 1: %+v", len(got), got)
	}
	want := Target{
		Name:      "lint",
		Summary:   "Go lint, formatting, tagged floors, generated outputs, and secrets.",
		Proves:    "TOML naming; gofmt over every tracked .go file; the evenerfuzz and eval compile floors; the internal-type check; golangci-lint across every workspace module.",
		Trigger:   "required CI; local pre-merge.",
		Requires:  "golangci-lint, gitleaks.",
		FailsWhen: "any member of LINT_TARGETS exits nonzero.",
	}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
}

// TestParseFamilyContinuationJoinsField pins the "single joining space"
// behaviour for a field continuation on its own, separate from the
// multi-continuation case above.
func TestParseFamilyContinuationJoinsField(t *testing.T) {
	src := "## Lint everything.\n## requires: golangci-lint,\n##   gitleaks.\nlint:\n\t@true\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "golangci-lint, gitleaks."
	if len(got) != 1 || got[0].Requires != want {
		t.Fatalf("got %+v, want Requires %q", got, want)
	}
}

// TestParseFamilyMultiLineSummary pins the "single joining space" behaviour
// for the summary itself, before any field line appears.
func TestParseFamilyMultiLineSummary(t *testing.T) {
	src := "## First line of the summary\n##   continues onto a second line.\nclean:\n\trm -f evener\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "First line of the summary continues onto a second line."
	if len(got) != 1 || got[0].Summary != want {
		t.Fatalf("got %+v, want Summary %q", got, want)
	}
}

// TestParseFamilyUnknownKeyIsAnError catches a typo'd key ("trigers" for
// "trigger") failing the build rather than silently vanishing from the
// generated output.
func TestParseFamilyUnknownKeyIsAnError(t *testing.T) {
	_, err := ParseFamily([]byte("## Lint.\n## trigers: always\nlint:\n\t@true\n"))
	if err == nil {
		t.Fatal("expected an error for the misspelled key 'trigers'")
	}
}

// TestParseFamilyRejectsBlockAboveTargetSpecificVariable: install-home and
// install-system each open with `name: VAR := value` before the line that
// carries the real prerequisites. A block sitting directly above the
// variable line documents an assignment, not the rule, so it must fail
// rather than silently attach to the wrong line.
//
// The fixture stops right after the variable line, on purpose: with a
// trailing `install-home: install` rule line (as an earlier version of this
// test had), removing the targetSpecificVariable guard would still make the
// test fail, but for the wrong reason — that later rule line would then
// have nothing pending above it and fail as "no annotation" instead. This
// isolated shape fails only if the guard itself fires.
func TestParseFamilyRejectsBlockAboveTargetSpecificVariable(t *testing.T) {
	src := "## Install into the home prefix.\ninstall-home: PREFIX := $(HOME)/.local\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: the block sits above a variable assignment, not the rule")
	}
}

// TestParseFamilyAnnotatesRealRuleLineAfterVariableLine is the shape
// TestParseFamilyRejectsBlockAboveTargetSpecificVariable's error guards:
// once the block moves to sit directly above the line that actually carries
// the prerequisites, parsing must succeed, and the leading variable line
// (with nothing pending above it) must not itself be an error.
func TestParseFamilyAnnotatesRealRuleLineAfterVariableLine(t *testing.T) {
	src := "install-home: PREFIX := $(HOME)/.local\n## Install into the home prefix.\ninstall-home: install\n\t@true\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "install-home" || got[0].Summary != "Install into the home prefix." {
		t.Fatalf("got %+v", got)
	}
}

// TestParseFamilyRejectsBlockSeparatedByBlankLine covers the make/linting.mk
// shape today: lint-naming's explanatory comment is separated from its rule
// by a blank line. A blank line is a non-comment line, so the block above it
// must not silently attach to the rule below.
func TestParseFamilyRejectsBlockSeparatedByBlankLine(t *testing.T) {
	src := "## Enforce naming.\n\nlint-naming:\n\t@true\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: a blank line separates the block from its rule")
	}
}

// TestParseFamilyRuleWithNoAnnotationIsAnError: every rule needs a summary
// (spec §2, "Required for every target"); a rule with nothing above it at
// all must fail rather than publish an empty row.
func TestParseFamilyRuleWithNoAnnotationIsAnError(t *testing.T) {
	_, err := ParseFamily([]byte("clean:\n\trm -f evener\n"))
	if err == nil {
		t.Fatal("expected an error: the rule has no ## annotation above it")
	}
}

// TestParseFamilyPhonyDirectiveIsNotARule: every real make/*.mk file opens
// with a `.PHONY: ...` line before any annotation exists. The parser must
// not treat it as an unannotated rule (which would make every real family
// file unparseable) or as a valid annotation target.
func TestParseFamilyPhonyDirectiveIsNotARule(t *testing.T) {
	src := ".PHONY: clean\n\n## Remove build artifacts.\nclean:\n\trm -f evener\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "clean" {
		t.Fatalf("got %+v", got)
	}
}

// TestParseFamilyMultipleTargetsInFileOrder exercises a realistic family-file
// preamble (.PHONY, a blank line, a plain variable assignment) ahead of two
// annotated targets, and pins that ParseFamily returns targets in file
// order, per its documented contract.
func TestParseFamilyMultipleTargetsInFileOrder(t *testing.T) {
	src := ".PHONY: build clean\n\n" +
		"PREFIX ?= $(HOME)/.local\n\n" +
		"## Build the binary.\n" +
		"build:\n" +
		"\tgo build -o evener .\n\n" +
		"## Remove the built binary.\n" +
		"clean:\n" +
		"\trm -f evener\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "build" || got[1].Name != "clean" {
		t.Fatalf("got %+v", got)
	}
}

// TestParseFamilyContinuationWithNothingToContinueIsAnError: a continuation
// line is only meaningful once a summary or field line exists to extend. As
// the first line of a block it has nothing to attach to.
func TestParseFamilyContinuationWithNothingToContinueIsAnError(t *testing.T) {
	src := "##   stray continuation\ntarget:\n\t@true\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: a continuation line opened the block")
	}
}

// TestParseFamilyProseAfterFieldLineIsAnError: the summary is defined as the
// LEADING run of ## lines with no key: prefix (spec §2). Plain text that
// shows up after a field has already started is neither a valid
// continuation (wrong spacing) nor part of the leading run, so it must fail
// rather than being silently folded into whichever field was last active.
func TestParseFamilyProseAfterFieldLineIsAnError(t *testing.T) {
	src := "## Summary.\n## trigger: CI.\n## More prose that is not a field.\ntarget:\n\t@true\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: prose line follows a field line")
	}
}

// TestParseFamilyDuplicateFieldKeyIsAnError: each field fills exactly one
// Target string field, so a key repeated in one block would otherwise
// silently overwrite or merge two unrelated statements.
func TestParseFamilyDuplicateFieldKeyIsAnError(t *testing.T) {
	src := "## Summary.\n## trigger: CI.\n## trigger: also local.\ntarget:\n\t@true\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: 'trigger' appears twice in the same block")
	}
}

// TestParseFamilyDanglingBlockAtEOFIsAnError: a block with nothing beneath
// it at all is separated from a rule by the strongest possible non-comment
// "line" — the end of the file — so it must fail like any other
// non-contiguous block rather than being silently dropped.
func TestParseFamilyDanglingBlockAtEOFIsAnError(t *testing.T) {
	_, err := ParseFamily([]byte("## Orphaned block with no rule below it.\n"))
	if err == nil {
		t.Fatal("expected an error: the block is never attached to a rule")
	}
}

// TestParseFamilyMalformedHashHashSpacingIsAnError checks the three ways a
// ## line's spacing can fail to match either the summary/field form (##
// plus exactly one space) or the continuation form (## plus exactly three
// spaces): no space, two spaces, and nothing at all after the ##.
func TestParseFamilyMalformedHashHashSpacingIsAnError(t *testing.T) {
	cases := []string{
		"##no space at all\n",
		"##  two spaces\n",
		"##\n",
	}
	for _, block := range cases {
		src := block + "target:\n\t@true\n"
		if _, err := ParseFamily([]byte(src)); err == nil {
			t.Fatalf("expected an error for block %q", block)
		}
	}
}

// TestParseFamilyPlainHashCommentDoesNotBreakContiguity: spec §2 breaks
// contiguity on "any non-comment line", and a plain "#" line is still a
// comment — just not a published one. Real family files (make/fuzzing.mk,
// most of make/testing.mk and make/building.mk) already carry multi-line
// "#" rationale directly above the rule, so a "##" summary placed above
// that rationale (a reasonable "headline first" reading of the grammar)
// must still parse, and the "#" content must not leak into the summary.
func TestParseFamilyPlainHashCommentDoesNotBreakContiguity(t *testing.T) {
	src := "## Lint everything.\n" +
		"# lint-naming enforces snake_case across every TOML file in the repo.\n" +
		"# See docs/developing-evener/linting.md for the full rationale.\n" +
		"lint-naming:\n\t@true\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "Lint everything."
	if len(got) != 1 || got[0].Name != "lint-naming" || got[0].Summary != want {
		t.Fatalf("got %+v, want Summary %q and the # lines absent from it", got, want)
	}
}

// TestParseFamilyCRLFLineEndings: strings.TrimRight(line, "\n") leaves a
// trailing "\r" on CRLF input, which survives into the summary and, on a
// continuation join, ends up embedded mid-string rather than at either end
// where it might go unnoticed.
func TestParseFamilyCRLFLineEndings(t *testing.T) {
	src := "## First line\r\n##   continues here.\r\nclean:\r\n\trm -f evener\r\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "First line continues here."
	if len(got) != 1 || got[0].Name != "clean" || got[0].Summary != want {
		t.Fatalf("got %+v, want Summary %q (no embedded \\r)", got, want)
	}
}

// TestParseFamilyFieldKeyWithColonButNoSpaceIsAnError: "## trigger:" with
// nothing after the colon misses fieldLine's required ": " and, before this
// test's fix, silently became summary prose reading "trigger:" instead of
// erroring. "## trigger: " (colon, space, then nothing) is a different,
// already-correct shape — an explicit empty field — and must keep parsing
// without error; TestParseFamilyAllFourFields and friends already exercise
// the populated form, so this test only needs the broken one plus that one
// contrast case.
func TestParseFamilyFieldKeyWithColonButNoSpaceIsAnError(t *testing.T) {
	src := "## Lint everything.\n## trigger:\nlint:\n\t@true\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: 'trigger:' has a colon but no value")
	}
}

// TestParseFamilyFieldKeyWithColonAndTrailingSpaceIsAnEmptyField pins the
// contrast case named in TestParseFamilyFieldKeyWithColonButNoSpaceIsAnError's
// comment: a trailing space after the colon is a deliberate empty value, not
// an error.
func TestParseFamilyFieldKeyWithColonAndTrailingSpaceIsAnEmptyField(t *testing.T) {
	src := "## Lint everything.\n## trigger: \nlint:\n\t@true\n"
	got, err := ParseFamily([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Trigger != "" {
		t.Fatalf("got %+v, want an empty Trigger and no error", got)
	}
}

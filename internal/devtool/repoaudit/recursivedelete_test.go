package repoaudit

import "testing"

// recursiveDeleteVerdict runs the full predicate over one already
// continuation-stripped line for tests: does any command on the line invoke
// a recursive rm fed by a variable, and does any command invoke a recursive
// rm this predicate cannot fully read. It exists so the predicate's coverage
// is provable directly, without needing a Makefile or script fixture; the
// two real audits keep their own per-command loops (makefile.go,
// scriptmktemp.go) because their allowlist bookkeeping needs to
// tell which specific command on a multi-delete line matched.
func recursiveDeleteVerdict(line, nestOpen string, continues bool) (flagged, unreadable bool) {
	commands := recursiveDeleteCommands(line, nestOpen)
	for c, command := range commands {
		args, isRM := rmArguments(command)
		if !isRM {
			continue
		}
		if continues && c == len(commands)-1 {
			unreadable = true
			continue
		}
		operands, recursive := recursiveDelete(args)
		if !recursive {
			continue
		}
		if len(operands) == 0 {
			unreadable = true
			continue
		}
		for _, operand := range operands {
			if operandComesFromVariable(operand) {
				flagged = true
			}
		}
	}
	return flagged, unreadable
}

// TestRecursiveDeletePredicateCatchesBothLineages is the union proof for
// issue #153: every mutation either the old scripts detector
// (recursiveDeleteTakingVariable, now retired) or the old Makefile detector
// defended against still fires against the one predicate both audits now
// share.
func TestRecursiveDeletePredicateCatchesBothLineages(t *testing.T) {
	t.Parallel()

	const shellOpen = "$("
	const makeOpen = "$$("

	flaggedCases := []struct {
		name     string
		line     string
		nestOpen string
	}{
		// Gap 1 (issue #153): make recipe-line prefixes. The old scripts
		// scanner required a boundary character immediately before "rm "
		// drawn from a fixed set that did not include '@', '-', or '+', so
		// these three read as no match at all.
		{"make silent prefix @rm", `@rm -rf $$dir`, makeOpen},
		{"make ignore-errors prefix -rm", `-rm -rf $$dir`, makeOpen},
		{"make always-run prefix +rm", `+rm -rf $$dir`, makeOpen},
		// Gap 2 (issue #153): GNU make's built-in $(RM) (defined as `rm -f`)
		// and the brace spelling ${RM}. The old scripts scanner searched for
		// the literal substring "rm ", which neither of these contains.
		{"make builtin $(RM)", `$(RM) -rf $$dir`, makeOpen},
		{"make builtin ${RM}", `${RM} -rf $$dir`, makeOpen},
		// Preserved from both lineages: the trap spelling that deleted a
		// checkout in kata 5hs2, a backtick command substitution, and an
		// absolute path invocation.
		{"quoted trap spelling", `trap 'rm -rf $dir' EXIT`, shellOpen},
		{"backtick command substitution", "x=`rm -rf $y`", shellOpen},
		{"absolute path invocation", `/bin/rm -rf $x`, shellOpen},
		{"double-quoted rm", `"rm" -rf $x`, shellOpen},
		{"delete hidden behind &&", `echo start && rm -rf $x`, shellOpen},
		{"delete hidden behind ||", `mkdir -p "$x" || rm -rf $x`, shellOpen},
		{"delete hidden behind ;", `cd /tmp; rm -rf $x`, shellOpen},
		// PR #276 review finding: the old scripts scanner's boundary-char set
		// included '(' and '&' — a bare subshell with no space after the
		// paren, and rm glued directly to a backgrounding '&', both still
		// invoked rm. namesRM's Trim-cutset approach dropped both when the
		// scripts audit moved onto it.
		{"bare subshell with no space after the paren", `(rm -rf "$SOME_VAR")`, shellOpen},
		{"rm glued to a backgrounding &", `sleep 1 &rm -rf $x`, shellOpen},
		// PR #276 review finding: real, but previously unproven by any
		// committed poison case. strings.Fields splits on any whitespace,
		// including a tab, so `rm` glued to its flags by a tab rather than a
		// space is still its own field.
		{"rm glued to its flags by a tab", "rm\t-rf $x", shellOpen},
	}
	for _, tc := range flaggedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			flagged, unreadable := recursiveDeleteVerdict(tc.line, tc.nestOpen, false)
			if !flagged {
				t.Errorf("recursiveDeleteVerdict(%q) = flagged=false, want flagged=true", tc.line)
			}
			if unreadable {
				t.Errorf("recursiveDeleteVerdict(%q) = unreadable=true, want false (this line is fully readable)", tc.line)
			}
		})
	}

	unreadableCases := []struct {
		name      string
		line      string
		nestOpen  string
		continues bool
	}{
		// Gap 3 (issue #153): a delete split across a backslash-continued
		// line. The Makefile detector already refused this shape
		// ("last-segment-on-continued-line refusal" in the issue); the old
		// scripts scanner had no continuation awareness at all and would
		// have silently passed a script that split its flags or path onto
		// the next physical line.
		{"rm as last command on a continued Makefile recipe line", `rm -rf`, makeOpen, true},
		{"rm as last command on a continued script line", `rm -rf`, shellOpen, true},
		// Bonus closure (documented as an accepted gap in docs/developing-evener/testing.md
		// for the old scripts scanner): a recursive rm with no operand at
		// all, because its target arrives over a pipe rather than as a
		// command-line argument. Unifying on the Makefile detector's
		// zero-operand refusal closes this for scripts too.
		{"recursive rm with target piped in via xargs", `find . -name '*.tmp' -print0 | xargs -0 rm -rf`, shellOpen, false},
	}
	for _, tc := range unreadableCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, unreadable := recursiveDeleteVerdict(tc.line, tc.nestOpen, tc.continues)
			if !unreadable {
				t.Errorf("recursiveDeleteVerdict(%q, continues=%v) = unreadable=false, want true", tc.line, tc.continues)
			}
		})
	}

	// A line does not become unreadable just by virtue of continuing: only
	// the LAST command on a continued line is unreadable, because "||"/";"
	// close the delete before the continuation does. Confirm a delete
	// earlier on a continued line still reads as flagged (readable, and
	// variable-fed) rather than merely "not unreadable".
	t.Run("delete followed by more commands on a continued line stays flagged and readable", func(t *testing.T) {
		t.Parallel()
		flagged, unreadable := recursiveDeleteVerdict(`rm -rf $x; echo done`, shellOpen, true)
		if !flagged {
			t.Error("want flagged=true for a readable delete earlier on a continued line")
		}
		if unreadable {
			t.Error("want unreadable=false: the delete is not the last command on the line")
		}
	})

	safeCases := []struct {
		name     string
		line     string
		nestOpen string
	}{
		{"no rm at all", `echo hello`, shellOpen},
		{"word merely containing rm is not a word boundary match", `confirm -rf $x`, shellOpen},
		{"non-recursive delete of a variable path", `rm "$x"`, shellOpen},
		{"recursive delete of an author-owned literal path", `rm -rf ./dist`, shellOpen},
		{"docker --rm flag is not an rm invocation on its own", `docker run --rm alpine true`, shellOpen},
	}
	for _, tc := range safeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			flagged, unreadable := recursiveDeleteVerdict(tc.line, tc.nestOpen, false)
			if flagged {
				t.Errorf("recursiveDeleteVerdict(%q) = flagged=true, want false", tc.line)
			}
			if unreadable {
				t.Errorf("recursiveDeleteVerdict(%q) = unreadable=true, want false", tc.line)
			}
		})
	}
}

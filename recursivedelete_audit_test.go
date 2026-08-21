package evener_test

import (
	"strings"
	"testing"
)

// This file holds the delete-safety predicate shared by
// TestNoScriptFeedsVariableToRecursiveDelete (scriptmktemp_audit_test.go) and
// TestNoMakefileRecipeFeedsVariableToRecursiveDelete (makefile_audit_test.go).
//
// The rule both audits hold is the one from kata 5hs2: a recursive delete
// whose path arrives in a variable deletes whatever that variable happens to
// hold, and an empty or clobbered expansion turns a scratch cleanup into a
// delete of something a person would miss. A script and a Makefile recipe
// are both shell underneath, so the shape of the hazard — and the shape of
// the detector — is the same in both; only the file-walking, the allowlist
// bookkeeping, and the failure-message attribution stay separate per caller,
// deliberately: see each audit's own file.
//
// Before this file, each audit carried its own copy of the detector, and
// they had drifted. The scripts copy (a single-pass textual scan for the
// substring "rm ") missed make's `@`/`-`/`+` recipe-line prefixes,
// `$(RM)`/`${RM}`, and a delete split across a backslash-continued line — all
// of which the Makefile copy (split into commands, then into whitespace
// fields) caught. This file is their union: every mutation either lineage's
// test suite defended against still fires here. See issue #153.

// recursiveDeleteLineText trims one physical line — a Makefile recipe line or
// a shell script line, the continuation convention is identical in both — to
// the shell text it contributes, and reports whether make or the shell will
// join the NEXT physical line onto this one.
//
// The continuation backslash has to go before anything counts words: left in
// place it is a bare `\` field, which would read as the path operand of an
// `rm -rf \` whose real target sits on the next line — exactly the shape a
// caller must refuse rather than silently pass.
func recursiveDeleteLineText(line string) (text string, continues bool) {
	trimmed := strings.TrimSpace(line)
	continues = strings.HasSuffix(trimmed, `\`)
	return strings.TrimSpace(strings.TrimSuffix(trimmed, `\`)), continues
}

// recursiveDeleteCommands splits one line's shell text into the separate
// commands it holds, so a delete hiding behind `&&`, `||`, `|`, or `;` is
// examined on its own rather than as a tail of whatever ran before it, and so
// a delete wrapped in a nested command substitution — a backtick, or however
// this dialect opens one — is examined instead of read as an operand of
// whatever encloses it.
//
// nestOpen is the one place the two dialects differ. A shell script opens a
// command substitution with "$(". A Makefile recipe cannot spell it that
// way: make eats the first dollar sign in `$(...)`, so a recipe writes
// "$$(" to leave the shell one dollar to open its own substitution with, and
// "$(" alone in recipe text is make's OWN variable expansion
// (`$(EVENER_DIST_BIN_DIR)`), which must not split here or the variable
// reference would be torn out of the very operand this predicate exists to
// look at. Bare braces (`${...}`) are never a separator, for the same
// reason, in both dialects.
//
// A trailing "# comment" on an already-split command is stripped too — make
// passes recipe text to the shell verbatim, so the shell's own comment rule
// applies there as much as it does in a script. It is stripped per command,
// after splitting, so a literal "#" earlier on the line cannot truncate a
// different command's text.
func recursiveDeleteCommands(line, nestOpen string) []string {
	const sep = "\x00"
	// "||" is listed before "|", and nestOpen before either dollar-bearing
	// form, so the longer operator wins when both match at the same index:
	// strings.NewReplacer prefers the pattern given first on a tie.
	segments := strings.Split(strings.NewReplacer(
		"&&", sep, "||", sep, "|", sep, ";", sep, nestOpen, sep, "`", sep,
	).Replace(line), sep)
	for i, segment := range segments {
		if before, _, found := strings.Cut(segment, " #"); found {
			segments[i] = before
		}
	}
	return segments
}

// namesRM reports whether a command word invokes rm.
//
// A prefixed or quoted spelling still names the same weapon: make's `@rm`
// and `-rm` recipe-line prefixes, the `'rm` that opens `trap 'rm -rf …'`,
// and a fully quoted `"rm"`. The trap spelling matters most — an EXIT trap
// holding a recursive delete is what deleted a checkout in kata 5hs2 — so it
// must not read as an ordinary word.
//
// Two more glued spellings reach rm with no space at all: a bare subshell
// with nothing between the paren and the command, `(rm -rf "$x")`, and rm
// backgrounded with nothing between it and the `&`, `sleep 1 &rm -rf $x`.
// (PR #276 review: the pre-union scripts scanner's character-boundary check
// already covered both — the field-based rewrite lost them until now.)
//
// Two indirect spellings reach rm without naming it directly. `$(RM)` is
// defined by GNU make itself (as `rm -f`) and needs no declaration here to
// work, and any absolute path ends in `/rm`. Both are ordinary things to
// write, so both have to count.
func namesRM(field string) bool {
	word := strings.Trim(field, `@-+'"(&`)
	return word == "rm" || word == "$(RM)" || word == "${RM}" || strings.HasSuffix(word, "/rm")
}

// rmArguments returns the words following an `rm` command word, and reports
// whether the command invokes rm at all.
//
// Finding rm is deliberately separate from deciding the delete is recursive.
// A command may put the command word on one physical line and its flags on
// the next (see recursiveDeleteLineText), and a scan that only recognized an
// already-recursive delete would not see `rm \` as anything at all — so the
// caller could not refuse it.
func rmArguments(command string) ([]string, bool) {
	fields := strings.Fields(command)
	for i, f := range fields {
		if namesRM(f) {
			return fields[i+1:], true
		}
	}
	return nil, false
}

// recursiveDelete classifies rm's arguments into the paths it would delete
// and whether any flag among them makes the delete recursive.
func recursiveDelete(args []string) (operands []string, recursive bool) {
	for _, f := range args {
		switch {
		case f == "--recursive":
			recursive = true
		case strings.HasPrefix(f, "--"):
			// Any other long option, including the bare `--` terminator.
		case len(f) > 1 && strings.HasPrefix(f, "-"):
			// A short-flag bundle: -r, -R, -rf, -fr all delete recursively.
			if strings.ContainsAny(f, "rR") {
				recursive = true
			}
		default:
			operands = append(operands, strings.Trim(f, `'"`))
		}
	}
	return operands, recursive
}

// operandComesFromVariable reports whether a recursive delete's target is
// decided at run time rather than written out by the author.
//
// A Makefile recipe reaches the shell after make has expanded it, so three
// spellings put a run-time value in an operand there: `$(VAR)` and `${VAR}`
// are make's own, and `$$name` is a shell variable make passes through. A
// script operand does the same with `$VAR`, `${VAR}`, or `$(cmd)`. Every one
// of them can expand to the empty string, or to a path nobody reviewed. Any
// `$` therefore counts — a Makefile recipe spells a literal dollar `$$`, so
// there is no unambiguous literal dollar to exempt there either.
func operandComesFromVariable(operand string) bool {
	return strings.Contains(operand, "$")
}

// recursiveDeleteUnreadableReason explains, for both audits, why a caller
// must refuse a recursive delete it cannot fully read rather than trying to
// classify it. The reason is dialect-independent: it is about what the scan
// itself cannot see, not about which file it is scanning.
const recursiveDeleteUnreadableReason = "this delete either runs on past the end of its line or names no " +
	"path on it, so its flags or its target arrive somewhere nothing here can tie them back " +
	"to the rm and no review can say what it deletes; keep the whole delete — rm, flags and " +
	"path — on one line:\n  %s"

// recursiveDeleteVerdict runs the full predicate over one already
// continuation-stripped line for tests: does any command on the line invoke
// a recursive rm fed by a variable, and does any command invoke a recursive
// rm this predicate cannot fully read. It exists so the predicate's coverage
// is provable directly, without needing a Makefile or script fixture; the
// two real audits keep their own per-command loops (makefile_audit_test.go,
// scriptmktemp_audit_test.go) because their allowlist bookkeeping needs to
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

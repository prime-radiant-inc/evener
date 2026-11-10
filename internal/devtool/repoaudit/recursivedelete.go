package repoaudit

import (
	"strings"
)

// This file holds the delete-safety predicate shared by
// noScriptFeedsVariableToRecursiveDelete (scriptmktemp.go) and
// noMakefileRecipeFeedsVariableToRecursiveDelete (makefile.go).
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

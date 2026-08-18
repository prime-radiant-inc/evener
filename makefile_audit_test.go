package serf_test

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// recipeLine trims one physical Makefile line to the shell text it contributes,
// dropping make's trailing line-continuation backslash.
//
// The backslash has to go before anything counts words: left in place it is a
// bare `\` field, which would read as the path operand of a `rm -rf \` whose
// real target sits on the next line — exactly the shape the audit must refuse.
func recipeLine(line string) string {
	trimmed := strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimSuffix(trimmed, `\`))
}

// recipeCommands splits one Makefile line into the shell commands it holds, so
// a delete hiding behind `||` or `;` is examined on its own rather than as a
// tail of whatever ran before it.
//
// The two spellings that open a nested command are separators too: a backtick,
// and the `$$(` that is how a recipe writes shell command substitution (make
// eats the first dollar, so `$(...)` alone is make's own expansion, never a
// shell command). Without them a delete wrapped in one reads as an operand of
// whatever encloses it and is never examined -- `trap` is caught only because
// it leaves rm as a bare word.
//
// Bare braces and parentheses are deliberately NOT separators. `${TMPDIR:-/tmp}`
// and `$(SERF_DIST_BIN_DIR)` carry them, and splitting there would tear the
// variable reference out of the very operand this audit exists to look at.
func recipeCommands(line string) []string {
	const sep = "\x00"
	// `||` is listed before `|`, and `$$(` before either dollar-bearing form, so
	// the longer operator wins when both match at the same index:
	// strings.NewReplacer prefers the pattern given first on a tie.
	return strings.Split(strings.NewReplacer(
		"&&", sep, "||", sep, "|", sep, ";", sep, "$$(", sep, "`", sep,
	).Replace(line), sep)
}

// namesRM reports whether a command word invokes rm.
//
// A prefixed or quoted spelling still names the same weapon: make's `@rm` and
// `-rm`, the `'rm` that opens `trap 'rm -rf …'`, and a fully quoted `"rm"`. The
// trap spelling matters most — an EXIT trap holding a recursive delete is what
// deleted a checkout in kata 5hs2 — so it must not read as an ordinary word.
//
// Two indirect spellings reach rm without naming it directly. `$(RM)` is
// defined by GNU make itself (as `rm -f`) and needs no declaration here to
// work, and any absolute path ends in `/rm`. Both are ordinary things to write,
// so both have to count.
func namesRM(field string) bool {
	word := strings.Trim(field, `@-+'"`)
	return word == "rm" || word == "$(RM)" || word == "${RM}" || strings.HasSuffix(word, "/rm")
}

// rmArguments returns the words following an `rm` command word, and reports
// whether the command invokes rm at all.
//
// Finding rm is deliberately separate from deciding the delete is recursive.
// A recipe may put the command on one physical line and its flags on the next,
// and a scan that only recognized an already-recursive delete would not see
// `rm \` as anything at all — so the caller could not refuse it.
func rmArguments(command string) ([]string, bool) {
	fields := strings.Fields(command)
	for i, f := range fields {
		if namesRM(f) {
			return fields[i+1:], true
		}
	}
	return nil, false
}

// recursiveDelete classifies rm's arguments into the paths it would delete and
// whether any flag among them makes the delete recursive.
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
// decided at run time rather than written out by the recipe's author.
//
// A recipe reaches the shell after make has expanded it, so three spellings put
// a run-time value in an operand: `$(VAR)` and `${VAR}` are make's own, and
// `$$name` is a shell variable make passes through. Every one of them can
// expand to the empty string, or to a path nobody reviewed. Any `$` therefore
// counts — make spells a literal dollar `$$` as well, so there is no
// unambiguous literal dollar to exempt.
func operandComesFromVariable(operand string) bool {
	return strings.Contains(operand, "$")
}

// makefileVariableFedDeletes are the recipe lines that hand a variable to a
// recursive delete today, each mapped to the reason it survives review.
//
// The list is the finding, not an exemption: it should only ever get shorter,
// and removing an entry after converting its recipe to a path the recipe owns
// outright is the intended lifecycle. Keying on the whole line is deliberate.
// It makes each site unique, and it means any edit to a listed line — even a
// reworded message beside the delete — fails the audit and forces someone to
// re-read the delete in that diff. Keys are the line as recipeLine normalizes
// it, so they carry no trailing continuation backslash.
var makefileVariableFedDeletes = map[string]string{
	`rm -rf "$$dir" || finish_status=1;`: "test-web's log directory: minted on the same recipe by a checked " +
		`mktemp -d "${TMPDIR:-/tmp}/serf-test-web.XXXXXX" || exit 1, ` +
		"so $$dir is either a fresh temp directory or the recipe already exited",
	`rm -rf "$$dir" || { finish_status=1; printf 'full logs: %s\n' "$$dir" >&2; };`: "test-web-browser's log directory: " +
		`minted on the same recipe by a checked mktemp -d "${TMPDIR:-/tmp}/serf-test-web-browser.XXXXXX" || exit 1`,
	`rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"`: "dist's own output paths, both rooted at DIST_DIR " +
		"(default `dist`) and named for the build's GOOS/GOARCH. This is the weakest entry of the three: " +
		"`make dist DIST_DIR=` roots both operands at `/` instead, and nothing in the recipe refuses that",
}

// TestNoMakefileRecipeFeedsVariableToRecursiveDelete holds the delete-safety
// rule over the Makefile, which is where the repository's remaining
// variable-fed recursive deletes live.
//
// The hazard is the one from kata 5hs2: a recursive delete whose path arrives
// in a variable deletes whatever that variable happens to hold, and an empty
// expansion turns a scratch cleanup into a delete of something a person would
// miss. Makefile:232 records the same lesson from the other side — the
// selftest-lib suite exists to prove no selftest can be made to delete the
// checkout.
//
// Two limits are worth stating, because a passing run does not cover them. The
// scan is textual, so `find … -exec rm -rf {} +` reads as a delete of the
// literal `{}` and slips through. And the pin cannot see one blessed delete
// swapped for a different one on a line whose text is unchanged. Read the
// deletes in any diff that touches this file's Makefile lines.
//
// The scan is also per physical line, but that is handled rather than merely
// admitted: a delete running past the end of its line is refused outright, so a
// continuation hides nothing.
func TestNoMakefileRecipeFeedsVariableToRecursiveDelete(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	var unswept, unreadable []string
	matched := map[string]int{}
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := recipeLine(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Whether make joins the NEXT physical line onto this one. It is read
		// before recipeLine strips the marker, and it is what makes a delete at
		// the end of this line unreviewable.
		continues := strings.HasSuffix(strings.TrimSpace(line), `\`)
		where := fmt.Sprintf("Makefile:%d: %s", i+1, trimmed)
		commands := recipeCommands(trimmed)
		for c, command := range commands {
			args, isRM := rmArguments(command)
			if !isRM {
				continue
			}
			// An rm that is the LAST command on a continued line runs on into
			// the next physical line, which may carry its recursion flag, its
			// path, or both. Neither this line nor the next says what gets
			// deleted, so the shape is refused outright rather than
			// allowlisted: a delete that cannot be read cannot be reviewed.
			//
			// A delete followed by more commands on the same line is safe from
			// this: `||` and `;` end the rm before the continuation does, which
			// is why the blessed sites at Makefile:77 and :121 stay green.
			if continues && c == len(commands)-1 {
				unreadable = append(unreadable, where)
				continue
			}
			operands, recursive := recursiveDelete(args)
			if !recursive {
				continue
			}
			// A recursive delete with no path at all takes its target from a
			// pipe (`xargs rm -rf`), which is equally unreadable.
			if len(operands) == 0 {
				unreadable = append(unreadable, where)
				continue
			}
			fromVariable := false
			for _, operand := range operands {
				if operandComesFromVariable(operand) {
					fromVariable = true
				}
			}
			if !fromVariable {
				continue
			}
			if _, allowed := makefileVariableFedDeletes[trimmed]; allowed {
				matched[trimmed]++
				continue
			}
			unswept = append(unswept, where)
		}
	}

	sort.Strings(unswept)
	for _, o := range unswept {
		t.Errorf("this recipe hands a variable to a recursive delete, so an empty or "+
			"unexpected expansion deletes whatever the expansion names (kata 5hs2 lost a "+
			"checkout that way). Give the delete a path the recipe owns outright, or add "+
			"the line to makefileVariableFedDeletes with the reason it is safe. If this is "+
			"a listed line that was only reworded, update its entry — the audit keys on the "+
			"whole line so that the delete gets re-read:\n  %s", o)
	}

	sort.Strings(unreadable)
	for _, o := range unreadable {
		t.Errorf("this delete either runs on past the end of its line or names no path on "+
			"it, so its flags or its target arrive somewhere nothing here can tie them back "+
			"to the rm and no review can say what it deletes; keep the whole delete — rm, "+
			"flags and path — on one line:\n  %s", o)
	}

	stale := make([]string, 0, len(makefileVariableFedDeletes))
	for line := range makefileVariableFedDeletes {
		stale = append(stale, line)
	}
	sort.Strings(stale)
	for _, line := range stale {
		switch matched[line] {
		case 1:
		case 0:
			t.Errorf("makefileVariableFedDeletes lists a line that no longer feeds a variable "+
				"to a recursive delete — delete the entry, whose stated reason was %q:\n  %s",
				makefileVariableFedDeletes[line], line)
		default:
			t.Errorf("one makefileVariableFedDeletes entry now blesses %d deletes, so a copy of "+
				"a reviewed line entered the Makefile without review of its own; give each site "+
				"its own entry:\n  %s", matched[line], line)
		}
	}
}

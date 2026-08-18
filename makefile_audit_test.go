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
// Braces and parentheses are deliberately NOT separators. `${TMPDIR:-/tmp}` and
// `$(SERF_DIST_BIN_DIR)` carry them, and splitting there would tear the variable
// reference out of the very operand this audit exists to look at.
func recipeCommands(line string) []string {
	const sep = "\x00"
	// `||` is listed before `|` so the two-character operator wins when both
	// match at the same index: strings.NewReplacer prefers the pattern given
	// first on a tie.
	return strings.Split(strings.NewReplacer(
		"&&", sep, "||", sep, "|", sep, ";", sep,
	).Replace(line), sep)
}

// recursiveDeleteOperands returns the paths a recursive `rm` in this command
// would delete, and reports whether the command is a recursive delete at all.
//
// A prefixed or quoted spelling still names the same weapon: make's `@rm` and
// `-rm`, and the `'rm` that opens `trap 'rm -rf …'`. The trap spelling matters
// most — an EXIT trap holding a recursive delete is what deleted a checkout in
// kata 5hs2 — so it must not read as an ordinary word. Quotes are trimmed from
// both ends, so a fully quoted `"rm"` is recognized too.
func recursiveDeleteOperands(command string) (operands []string, isRecursiveDelete bool) {
	fields := strings.Fields(command)
	at := -1
	for i, f := range fields {
		if strings.Trim(f, `@-+'"`) == "rm" {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, false
	}
	recursive := false
	for _, f := range fields[at+1:] {
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
	if !recursive {
		return nil, false
	}
	return operands, true
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
// scan is textual and per physical line, so `find … -exec rm -rf {} +` reads as
// a delete of the literal `{}` and slips through. And the pin cannot see one
// blessed delete swapped for a different one on a line whose text is unchanged.
// Read the deletes in any diff that touches this file's Makefile lines.
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
		where := fmt.Sprintf("Makefile:%d: %s", i+1, trimmed)
		for _, command := range recipeCommands(trimmed) {
			operands, isDelete := recursiveDeleteOperands(command)
			if !isDelete {
				continue
			}
			// A recursive delete with no path on its own line takes its target
			// from a continuation line or from a pipe, so this audit cannot see
			// what it deletes. That shape is refused outright rather than
			// allowlisted: an unreadable delete cannot be reviewed.
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
		t.Errorf("this recursive delete names no path on its own line, so it takes its "+
			"target from a continuation line or from a pipe and nothing here can review "+
			"what it deletes; put the path on the same line as the rm:\n  %s", o)
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

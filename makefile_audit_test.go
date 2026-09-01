package evener_test

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// The rm-detection predicate this audit walks the Makefile with —
// recursiveDeleteLineText, recursiveDeleteCommands, namesRM, rmArguments,
// recursiveDelete, operandComesFromVariable — is shared with the scripts
// audit's TestNoScriptFeedsVariableToRecursiveDelete and lives in
// recursivedelete_audit_test.go; see that file's header for why (issue #153).

// makefileVariableFedDeletes are the recipe lines that hand a variable to a
// recursive delete today, each mapped to the reason it survives review.
//
// The list is the finding, not an exemption: it should only ever get shorter,
// and removing an entry after converting its recipe to a path the recipe owns
// outright is the intended lifecycle. Keying on the whole line is deliberate.
// It makes each site unique, and it means any edit to a listed line — even a
// reworded message beside the delete — fails the audit and forces someone to
// re-read the delete in that diff. Keys are the line as recursiveDeleteLineText
// normalizes it, so they carry no trailing continuation backslash.
var makefileVariableFedDeletes = map[string]string{}

// TestNoMakefileRecipeFeedsVariableToRecursiveDelete holds the delete-safety
// rule over the Makefile, which is where the repository's remaining
// variable-fed recursive deletes live.
//
// The hazard is the one from kata 5hs2: a recursive delete whose path arrives
// in a variable deletes whatever that variable happens to hold, and an empty
// expansion turns a scratch cleanup into a delete of something a person would
// miss.
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
//
// The scan walks makefileSourcePaths one file at a time, rather than joining
// every file's body into one string first, so a failure's citation is
// path:line for the file that actually holds the delete — matching the
// pattern scriptmktemp_audit_test.go's recursiveDeleteOffenders already uses
// for the equivalent scripts/ audit. Scanning file-by-file also means the
// last line of one file's content can never be mistaken for a continuation
// of, or fused onto, the first line of the next file: each file's line index
// and its "does this line continue" check are entirely its own.
func TestNoMakefileRecipeFeedsVariableToRecursiveDelete(t *testing.T) {
	t.Parallel()

	var unswept, unreadable []string
	matched := map[string]int{}
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			// continues reports whether make joins the NEXT physical line onto
			// this one, which is what makes a delete at the end of this line
			// unreviewable.
			trimmed, continues := recursiveDeleteLineText(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			where := fmt.Sprintf("%s:%d: %s", path, i+1, trimmed)
			commands := recursiveDeleteCommands(trimmed, "$$(")
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
				// this: `||` and `;` end the rm before the continuation does.
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
		t.Errorf(recursiveDeleteUnreadableReason, o)
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

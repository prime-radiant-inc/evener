package serf_test

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// This audit is the Makefile half of the rule scriptmktemp_audit_test.go
// enforces over scripts/: no recursive delete may take an argument a caller
// can clobber. It shares that file's recursiveDeleteTakingVariable rather than
// carrying its own matcher — a second, weaker copy of a security predicate is
// how a banned shape ends up blessed by the audit that was supposed to find
// it. (Measured against the copy this file used to hold: `rm -fr`, `rm -Rf`,
// `rm --recursive`, and any second delete after a `;` all read as clean.)
//
// What differs here is the REPORTING, not the matching: a Makefile offender is
// named by its recipe and the variable it feeds, because that is what a reader
// has to go and look at, and because a recipe is the unit a human blesses.

// makefileRecipeAllowedSites are the Makefile recipes carrying variable-fed
// recursive deletes that have been audited and blessed.
//
// These are the sites this audit documents as safe. Each entry must include:
// - The exact file (Makefile)
// - The recipe name
// - The variable(s) involved
// - Brief justification
//
// The list is pinned per entry, not by total: a new site fails as an offender,
// and a blessed site the Makefile no longer contains fails as a stale entry.
// Comparing totals instead would let one site that grew a second delete cancel
// out another that disappeared. Removing an entry after converting the recipe
// to a safe shape is the intended lifecycle.
var makefileRecipeAllowedSites = []string{
	"Makefile:test-web:$$dir",         // Line 77: dir=$(mktemp -d ...) || exit 1; rm -rf "$$dir"
	"Makefile:test-web-browser:$$dir", // Line 121: dir=$(mktemp -d ...) || exit 1; rm -rf "$$dir"
	"Makefile:dist:SERF_DIST_BIN_DIR", // Line 163: rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"
	"Makefile:dist:SERF_DIST_ARCHIVE", // Line 163: rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"
}

// TestNoMakefileRecipeFeedsVariableToRecursiveDelete audits Makefile recipe
// lines for variable-fed recursive deletes and keeps the audited set pinned.
//
// The rule: a recursive delete that feeds a variable is dangerous because:
//  1. The variable could be empty, causing rm -rf "" to delete from cwd
//  2. The variable could be wrong or misconfigured
//  3. The variable could be set by user input or an earlier failure
//
// Safe alternatives:
//  1. rm -rf literal/hardcoded/path only
//  2. mkdir -p $TMPDIR/owned-by-us && rm -rf $TMPDIR/owned-by-us/$subdir (mkdir-owned)
//  3. Guard through a script that uses scratch-lib or covscratch-lib patterns
//
// The audit is pinned per site: a new one requires explicit review, and after
// converting a recipe to a safe shape, deleting the entry is what proves the
// conversion took.
func TestNoMakefileRecipeFeedsVariableToRecursiveDelete(t *testing.T) {
	t.Parallel()

	// Read Makefile
	body, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	var offenders []string
	seenSites := make(map[string]bool)

	inRecipe := ""
	for i, line := range strings.Split(string(body), "\n") {
		lineNum := i + 1

		// Detect recipe start (target: prerequisite)
		// A recipe line starts with a tab after the target line
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			// Not a recipe line, could be a target
			if strings.Contains(line, ":") && !strings.HasPrefix(line, "#") {
				parts := strings.Split(line, ":")
				if len(parts) > 0 {
					inRecipe = strings.TrimSpace(parts[0])
				}
			}
			continue
		}

		if inRecipe == "" {
			continue
		}

		// Extract the actual recipe content (strip leading whitespace)
		recipeContent := strings.TrimPrefix(line, "\t")
		recipeContent = strings.TrimPrefix(recipeContent, " ")

		// Skip comments and empty lines
		trimmed := strings.TrimSpace(recipeContent)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if recursiveDeleteTakingVariable(trimmed) {
			// Extract variable references as they appear, normalizing Make variables
			// Shell: $$var stays as $$var (Makefile escaping)
			// Make: $(VAR) becomes VAR, ${VAR} becomes VAR
			varPattern := regexp.MustCompile(`\$\$(\w+)|\$\{(\w+)\}|\$\((\w+)\)`)
			matches := varPattern.FindAllStringSubmatch(trimmed, -1)
			var vars []string
			seen := make(map[string]bool)
			for _, match := range matches {
				var varRef string
				if match[1] != "" {
					// $$var -> $$var (keep as-is for shell escaping)
					varRef = "$$" + match[1]
				} else if match[2] != "" {
					// ${VAR} -> VAR (Make variable)
					varRef = match[2]
				} else if match[3] != "" {
					// $(VAR) -> VAR (Make variable)
					varRef = match[3]
				}
				if varRef != "" && !seen[varRef] {
					vars = append(vars, varRef)
					seen[varRef] = true
				}
			}

			for _, varName := range vars {
				entry := fmt.Sprintf("Makefile:%s:%s", inRecipe, varName)
				if slices.Contains(makefileRecipeAllowedSites, entry) {
					seenSites[entry] = true
				} else {
					offenders = append(offenders,
						fmt.Sprintf("Makefile:%d:%s: %s (variable: %s)",
							lineNum, inRecipe, trimmed, varName))
				}
			}
		}
	}

	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("This Makefile recipe feeds a variable to a recursive delete. "+
			"Convert to a safe shape (hardcoded path, mkdir-owned directory, "+
			"or guard through a script using scratch-lib), then remove from the "+
			"audit allowlist:\n  %s", o)
	}

	// Every blessed site must still exist, checked one entry at a time. A
	// comparison of totals would be satisfied by any set of the right size, so
	// one recipe growing a second variable-fed delete would cover for another
	// site disappearing, and the stale entry left behind is then a standing
	// blessing for whatever reoccupies that recipe name later.
	var stale []string
	for _, entry := range makefileRecipeAllowedSites {
		if !seenSites[entry] {
			stale = append(stale, entry)
		}
	}
	sort.Strings(stale)
	for _, entry := range stale {
		t.Errorf("makefileRecipeAllowedSites blesses %s, but the Makefile no longer feeds that "+
			"variable to a recursive delete there. If the recipe was converted to a safe shape, "+
			"delete the entry; if the recipe was renamed, the entry has to follow it, or it goes "+
			"on blessing a recipe nobody reviewed.", entry)
	}
}

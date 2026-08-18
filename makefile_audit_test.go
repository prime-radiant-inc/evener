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

// variableFedRecursiveDelete reports whether a Makefile recipe line contains
// an rm -rf / rm -r / rm -fr command whose argument expands a variable.
//
// Safe patterns:
//   - rm -rf literal/path
//   - rm -rf -- "$(VAR)" with "$(VAR)" verified set beforehand
//   - rm -rf inside scripts/ guarded by scratch-lib
//
// Unsafe patterns:
//   - rm -rf $$var (shell variable, could be empty or wrong)
//   - rm -rf "$(VAR)" without verification (Make variable, could be empty)
//   - rm -rf with variable in glob or glob expansion
func variableFedRecursiveDelete(line string) bool {
	// Stop at comment boundary
	if idx := strings.Index(line, "#"); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)

	// Look for rm -r variants
	rmIdx := strings.Index(line, "rm -r")
	if rmIdx < 0 {
		return false
	}

	// Extract the rm -r... command up to end or pipe/semicolon/and/or/redirect
	rmCmd := line[rmIdx:]
	for _, term := range []string{"|", ";", "&&", "||", ">", "<", "&"} {
		if idx := strings.Index(rmCmd, term); idx >= 0 {
			rmCmd = rmCmd[:idx]
		}
	}
	rmCmd = strings.TrimSpace(rmCmd)

	// Check if the arguments to rm contain variable expansion
	// Shell: $$var or $var
	// Make: $(VAR) or ${VAR}
	// Patterns to catch:
	//   - "$$dir" or $$dir
	//   - "$(VAR)" or $(VAR)
	//   - "${VAR}" or ${VAR}
	//   - "$1" $@ etc (positional or special params)

	// Very simple check: does it contain $ followed by { or ( or $ or a letter/digit/underscore?
	varPattern := regexp.MustCompile(`\$[\$({]|[$][a-zA-Z_][a-zA-Z0-9_]*|\$[@*?#\-]`)
	return varPattern.MatchString(rmCmd)
}

// makefileRecipeAllowedSites are the Makefile recipes carrying variable-fed
// recursive deletes that have been audited and blessed.
//
// These are the sites this audit documents as safe. Each entry must include:
// - The exact file (Makefile)
// - The recipe name
// - The variable(s) involved
// - Brief justification
//
// The list is count-pinned: if a new site appears, the audit fails with a count
// mismatch. This forces explicit review of each site. Removing an entry after
// converting the recipe to a safe shape is the intended lifecycle.
var makefileRecipeAllowedSites = []string{
	"Makefile:test-web:$$dir",           // Line 77: dir=$(mktemp -d ...) || exit 1; rm -rf "$$dir"
	"Makefile:test-web-browser:$$dir",   // Line 121: dir=$(mktemp -d ...) || exit 1; rm -rf "$$dir"
	"Makefile:dist:SERF_DIST_BIN_DIR",   // Line 163: rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"
	"Makefile:dist:SERF_DIST_ARCHIVE",   // Line 163: rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"
}

// TestNoMakefileRecipeFeedsVariableToRecursiveDelete audits Makefile recipe
// lines for variable-fed recursive deletes and keeps the audited set pinned.
//
// The rule: a recursive delete that feeds a variable is dangerous because:
//   1. The variable could be empty, causing rm -rf "" to delete from cwd
//   2. The variable could be wrong or misconfigured
//   3. The variable could be set by user input or an earlier failure
//
// Safe alternatives:
//   1. rm -rf literal/hardcoded/path only
//   2. mkdir -p $TMPDIR/owned-by-us && rm -rf $TMPDIR/owned-by-us/$subdir (mkdir-owned)
//   3. Guard through a script that uses scratch-lib or covscratch-lib patterns
//
// The audit is count-pinned: new entries require explicit review. After
// converting a recipe to a safe shape, delete the entry and the count will
// validate the conversion.
func TestNoMakefileRecipeFeedsVariableToRecursiveDelete(t *testing.T) {
	t.Parallel()

	// Read Makefile
	body, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	var offenders []string
	var seenSites []string
	seenSiteMap := make(map[string]bool)

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

		if variableFedRecursiveDelete(trimmed) {
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
					seenSiteMap[entry] = true
					seenSites = append(seenSites, entry)
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

	// Check that all blessed sites were found
	sort.Strings(makefileRecipeAllowedSites)
	sort.Strings(seenSites)
	if len(seenSites) != len(makefileRecipeAllowedSites) {
		t.Errorf("Makefile audit saw %d blessed sites, but allowlist has %d. "+
			"The Makefile may have changed, or blessed sites were removed. "+
			"Current count: %d, expected: %d\n"+
			"Blessed sites:\n%s\n"+
			"Found sites:\n%s",
			len(seenSites), len(makefileRecipeAllowedSites),
			len(seenSites), len(makefileRecipeAllowedSites),
			formatList(makefileRecipeAllowedSites),
			formatList(seenSites))
	}
}

func formatList(items []string) string {
	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, "  %s\n", item)
	}
	return b.String()
}

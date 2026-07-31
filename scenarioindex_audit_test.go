package serf_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// test/scenarios/INDEX.md is the navigation entry point into the card corpus:
// an agent picking work reads the index, not a directory listing. That makes
// it the one file in the corpus that rots silently — a card added without an
// index line is simply invisible, and a card deleted leaves an entry pointing
// at nothing. Kata qaxx found 40 of 131 cards unindexed and two entries naming
// files that no longer exist. These two tests make both halves of that drift
// impossible: the corpus and its index have to agree in both directions.
//
// The index names a card as a bare backticked filename (`worktree-resume-reentry.md`).
// Prose spans that are not card references — doc paths like
// `docs/tools/transcripts.md`, or a glob over a family like `web-*-image.md` —
// carry a `/` or a `*` and so are not card names by this rule.
var scenarioIndexCardRefRE = regexp.MustCompile("`([A-Za-z0-9][A-Za-z0-9._-]*\\.md)`")

// scenarioIndexExempt are the files under test/scenarios/ that are not cards
// and so have no entry in the index. Everything else in that directory does.
var scenarioIndexExempt = map[string]string{
	"INDEX.md":     "the index itself",
	"README.md":    "the runbook explaining how to execute a card",
	"_template.md": "the blank a new card is copied from",
}

// TestScenarioIndexListsEveryCard is the "nothing is invisible" half: every
// card on disk has an entry in INDEX.md. The check is for the backticked
// filename rather than a bare substring on purpose — `doctor-forensics.md` is
// a substring of `serf-doctor-forensics.md`, so a substring match would report
// a card as indexed when only its longer-named sibling is.
func TestScenarioIndexListsEveryCard(t *testing.T) {
	index := readScenarioIndex(t)
	var missing []string
	for _, name := range scenarioCardNames(t) {
		if !strings.Contains(index, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d scenario card(s) are missing from %s, so nobody navigating "+
			"the index can find them. Add a one-line entry for each under the "+
			"group it belongs to, summarising what the card's own \"What this "+
			"covers\" says:\n%s",
			len(missing), filepath.Join(scenarioDir, "INDEX.md"), strings.Join(missing, "\n"))
	}
}

// TestScenarioIndexReferencesOnlyRealCards is the "nothing is a ghost" half:
// every card filename INDEX.md names still exists. A retired card that keeps
// its entry sends a reader to a 404; naming it in a historical aside ("replaces
// the old X") does the same, so write those as a bare slug without the `.md`.
func TestScenarioIndexReferencesOnlyRealCards(t *testing.T) {
	index := readScenarioIndex(t)
	seen := map[string]bool{}
	var ghosts []string
	for i, line := range strings.Split(index, "\n") {
		for _, m := range scenarioIndexCardRefRE.FindAllStringSubmatch(line, -1) {
			name := m[1]
			if _, ok := scenarioIndexExempt[name]; ok {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			if _, err := os.Stat(filepath.Join(scenarioDir, name)); err != nil {
				ghosts = append(ghosts, filepath.Join(scenarioDir, "INDEX.md")+":"+
					strconv.Itoa(i+1)+": "+name)
			}
		}
	}
	if len(ghosts) > 0 {
		sort.Strings(ghosts)
		t.Fatalf("%s names %d card file(s) that do not exist. Drop the entry if "+
			"the card was retired, or, if the mention is a historical aside, "+
			"write the slug without the `.md` so it does not read as a link:\n%s",
			filepath.Join(scenarioDir, "INDEX.md"), len(ghosts), strings.Join(ghosts, "\n"))
	}
}

// scenarioCardNames returns the base names of every card under test/scenarios/,
// which is scenarioCardFiles minus the runbook, the template, the index itself,
// and the docs page that helper folds in for the port and $HOME audits.
func scenarioCardNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, path := range scenarioCardFiles(t) {
		if filepath.Dir(path) != scenarioDir {
			continue
		}
		name := filepath.Base(path)
		if _, ok := scenarioIndexExempt[name]; ok {
			continue
		}
		names = append(names, name)
	}
	return names
}

func readScenarioIndex(t *testing.T) string {
	t.Helper()
	path := filepath.Join(scenarioDir, "INDEX.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

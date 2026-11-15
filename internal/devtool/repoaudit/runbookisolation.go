package repoaudit

import (
	"os"
	"path/filepath"
	"strings"
)

// runbooksRouteIsolationThroughTheHelper keeps the runbooks and scenario
// cards isolating HOME through e2e_isolate_home. A hand-rolled list of unsets
// drifts from the harnesses' list; one that omits
// EVENER_PROVIDERS_CONFIG/EVENER_CREDENTIALS_CONFIG lets an operator with those
// exported load real providers and load or claim real state.
//
// The rule is per block, not per file: any block that sets up an isolated HOME
// (it sources the helper or assigns HOME) must call e2e_isolate_home, so a
// file-global mention cannot satisfy it. Cards that deliberately run against
// the real HOME or real credentials carry a named exemption below; the audit
// fails if an exemption names a card that no longer exists.
func runbooksRouteIsolationThroughTheHelper(a *audit) {
	docs := []string{
		"docs/developing-evener/testing.md",
		"docs/developing-evener/agentic-testing.md",
	}
	scenarios, err := filepath.Glob("test/scenarios/*.md")
	if err != nil {
		a.Fatalf("glob test/scenarios/*.md: %v", err)
	}
	docs = append(docs, scenarios...)
	if len(scenarios) == 0 {
		a.Fatal("no scenario cards found; the audit would silently scan nothing")
	}

	seen := map[string]bool{}
	for _, doc := range docs {
		key := scenarioExemptionKey(doc)
		if _, exempt := scenarioExemption(doc); exempt {
			seen[key] = true
			continue
		}
		fences, err := ReadMarkdownFences(doc)
		if err != nil {
			a.Fatal(err)
		}
		for _, fence := range fences {
			text := strings.Join(fence.Lines, "\n")
			if !isolationSite(text) {
				continue
			}
			if !HasCommand(text, "e2e_isolate_home") {
				a.Errorf("%s sets up an isolated HOME without routing through e2e_isolate_home; "+
					"hand-rolled unsets drift from the e2e harnesses' list (the #2040 defect):\n%s",
					doc, text)
			}
		}
		for _, line := range nonFencedLines(a, doc) {
			inner, ok := nonFencedIsolationRecipe(line.text)
			if !ok || !isolationSite(inner) {
				continue
			}
			if !HasCommand(inner, "e2e_isolate_home") {
				a.Errorf("%s:%d states a non-fenced isolation recipe without routing through "+
					"e2e_isolate_home (the #2040 defect):\n  %s", doc, line.n, strings.TrimSpace(line.text))
			}
		}
	}
	for doc, reason := range scenarioIsolationExemptions {
		if !seen[doc] {
			a.Errorf("scenarioIsolationExemptions names %s, which is not a scenario card being "+
				"scanned — delete the stale entry", doc)
		}
		if strings.TrimSpace(reason) == "" {
			a.Errorf("scenarioIsolationExemptions names %s with no reason", doc)
		}
	}
}

// isolationSite reports whether a fenced block tries to point evener at a
// scratch HOME: it sources the isolation helper or assigns HOME. Prose and
// samples that do neither are not isolation sites.
func isolationSite(text string) bool {
	return strings.Contains(text, "scripts/lib/e2e-lib.sh") || hasHomeAssignment(text)
}

// hasHomeAssignment reports whether a non-comment line assigns HOME. The match
// is boundary-aware so XDG_STATE_HOME (and HOMEDRIVE/HOMEPATH) — whose names
// merely end in HOME — do not count as the isolation this audit looks for.
func hasHomeAssignment(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for i := 0; i+len("HOME=") <= len(trimmed); i++ {
			if trimmed[i:i+len("HOME=")] != "HOME=" {
				continue
			}
			if i == 0 || !identByte(trimmed[i-1]) {
				return true
			}
		}
	}
	return false
}

func identByte(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// numberedLine is a non-fenced source line with its 1-based line number.
type numberedLine struct {
	n    int
	text string
}

// nonFencedLines returns every line of path that lies outside a fenced block.
// Some cards state their isolation recipe as a prose bullet rather than a
// fenced block, and a fenced-only audit would miss it. Unterminated fences are
// already fatal in ReadMarkdownFences.
func nonFencedLines(a *audit, path string) []numberedLine {
	body, err := os.ReadFile(path)
	if err != nil {
		a.Fatalf("read %s: %v", path, err)
	}
	var out []numberedLine
	inFence := false
	lineNo := 0
	for line := range strings.SplitSeq(string(body), "\n") {
		lineNo++
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out = append(out, numberedLine{n: lineNo, text: line})
		}
	}
	return out
}

// nonFencedIsolationRecipe returns the shell of a prose list item that is
// entirely one backticked command, e.g. "- `export HOME="$run/home"`". The
// whole-item-is-a-command shape is what separates a copyable recipe from a
// sentence that merely names a variable; the latter is not returned.
func nonFencedIsolationRecipe(line string) (string, bool) {
	s := strings.TrimSpace(line)
	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(s, marker) {
			s = strings.TrimSpace(s[len(marker):])
			break
		}
	}
	if !strings.HasPrefix(s, "`") || !strings.HasSuffix(s, "`") || len(s) < 2 {
		return "", false
	}
	inner := s[1 : len(s)-1]
	if strings.Contains(inner, "`") {
		return "", false
	}
	return inner, true
}

// scenarioIsolationExemptions names scenario cards that deliberately set HOME
// their own way and must not be routed through e2e_isolate_home, each with its
// reason. An entry is earned by the card's own stated intent, never to silence
// a finding:
//
//   - compact-tool-pins-note-and-persists.md and compact-note-survives-resume.md
//     deliberately symlink the operator's real provider credentials into a
//     throwaway HOME and write a bespoke hub.toml under the legacy ~/.evener
//     layout; e2e_isolate_home's EVENER_PROVIDERS_CONFIG/EVENER_CREDENTIALS_CONFIG
//     unsets are the opposite of what those cards exist to prove.
var scenarioIsolationExemptions = map[string]string{
	"test/scenarios/compact-tool-pins-note-and-persists.md": "deliberately symlinks the operator's real provider credentials into a throwaway HOME",
	"test/scenarios/compact-note-survives-resume.md":        "inherits compact-tool-pins-note-and-persists.md's real-credential setup",
}

// scenarioExemptionKey normalizes a path to the slash form the exemption map is
// keyed by. filepath.Glob builds its results with filepath.Join, so on Windows
// it hands back `test\scenarios\...`; without this the lookup would miss, the
// two exempt cards would be reported as violations, and the stale-exemption
// check would fire on both entries.
func scenarioExemptionKey(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// scenarioExemption reports the reason path is exempt, matching either
// separator form so the audit still runs (and still exempts) on Windows.
func scenarioExemption(path string) (string, bool) {
	reason, ok := scenarioIsolationExemptions[scenarioExemptionKey(path)]
	return reason, ok
}

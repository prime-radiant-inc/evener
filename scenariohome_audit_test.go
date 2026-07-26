package serf_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// scenarioHomeApprovedFiles lists every scenario card (or runbook) allowed to
// mention a real, un-isolated `~/.serf` / `$HOME/.serf` path with no isolation
// marker of its own (see scenarioFileHasHomeIsolationMarker below). Kata 93f5
// found that TestScenarioCardsNeverTargetJessesHubPort's literal-9180 check
// misses a materially different hazard: a card that never names port 9180 can
// still read or write Jesse's real ~/.serf directly, because THAT is what
// makes the flock, the auth token, and the session history shared, not the
// port. Every entry here is a reviewed reason, not a rubber stamp — see kata
// `keyb` for the two cards that need the real $HOME for OAuth and what they
// are and are not allowed to touch as a result.
var scenarioHomeApprovedFiles = map[string]string{
	"test/scenarios/web-goal-set-and-complete.md": "documented OAuth-footgun " +
		"exception (needs already-signed-in OpenAI OAuth, which only exists " +
		"under the real $HOME) — hardened per kata keyb: pre-checks the " +
		"~/.serf/hub.lock flock before starting, and isolates session " +
		"history via XDG_STATE_HOME even though $HOME itself is real.",
	"test/scenarios/sidecar-approval-broker-communicate.md": "same OAuth-footgun " +
		"exception and same kata-keyb hardening (flock pre-check + " +
		"XDG_STATE_HOME session-history isolation) as web-goal-set-and-complete.md.",
	"test/scenarios/credentials-page-displays-sources.md": "same OAuth-footgun " +
		"exception (the credentials page's OAuth-sourced row needs a real " +
		"signed-in OpenAI session) — this is the canonical example " +
		"docs/agentic-testing.md points to for the pattern. Hardened per " +
		"kata 93f5: backs up ~/.serf/credentials.toml before the dual-layer " +
		"Sharp-edges edit and restores it in Cleanup, and no longer " +
		"truncates the file with a bare `>`.",
	"test/scenarios/ask-restart-rederive.md": "Pre-state explicitly reuses " +
		"ask-web-answer.md's hub and shell state (\"same as ask-web-answer.md\") " +
		"rather than repeating its own export — safe as long as that card's " +
		"isolated $HOME export (fixed under kata 93f5) is still the one live " +
		"in the same shell when this card's `~/.serf/run/*.json` glob runs.",
	"test/scenarios/index-sidebar-lists-projects.md": "a read-only regression " +
		"baseline explicitly written to run against Jesse's own real dev hub " +
		"(\"On this dev box there's plenty\", a hardcoded /home/jesse path) — " +
		"never spawns a session or writes anything. Filed under kata 93f5 as " +
		"a candidate to reconsider (should this even live under test/scenarios/ " +
		"as an agent-runnable card, versus a manual dev checklist?), not fixed.",
	"test/scenarios/model-picker-fresh-install-no-recent.md": "Pre-state already " +
		"isolates $HOME/XDG_STATE_HOME (\"Hermetic $HOME/$XDG_STATE_HOME scratch " +
		"dirs\") — the ~/.serf mention is a one-time READ, copying real " +
		"providers.toml/credentials.toml INTO that isolated home so a provider " +
		"enumerates live, matching docs/agentic-testing.md's own sanctioned " +
		"\"copy in a scratch credentials.toml first\" recipe. Never writes to " +
		"the real ~/.serf.",
	"test/scenarios/model-switch-providers-live.md": "every ~/.serf mention is " +
		"either the standard \"so the live ~/.serf/providers.toml is untouched\" " +
		"warning (this card isolates via SERF_PROVIDERS_CONFIG) or retrospective " +
		"prose in a dated Results section reporting what a past run found on " +
		"that host — neither is an instruction to touch the real path now.",
	"test/scenarios/reasoning-effort-providers.md": "the remaining mention " +
		"(after kata 93f5 removed the KIMI_KEY-from-real-credentials.toml read) " +
		"is the standard \"so the live ~/.serf/providers.toml is untouched\" " +
		"warning — this card isolates via SERF_PROVIDERS_CONFIG.",
	"test/scenarios/worktree-create-and-orient.md": "the isolation recipe here " +
		"is READ-ONLY config symlinked from ~/.serf (providers.toml/" +
		"credentials.toml/auth-token) into an isolated SERF_STATE_DIR, mutable " +
		"state kept separate — never writes to the real ~/.serf. Doesn't say " +
		"\"isolated\" verbatim, which is why the marker below misses it.",
	"test/scenarios/worktree-ergonomics-findings.md": "same read-only " +
		"symlinked-config pattern as worktree-create-and-orient.md.",
}

// scenarioHomeOwnIsolationMarker matches a card performing its own isolation
// (`export HOME=`, `HOME=$(mktemp`) or pointing at the shared convention
// ("Setup checklist"). scenarioMentionsIsolatedHome (below) separately checks
// for the third, harder-to-regex form: "isolated ... $HOME" with arbitrary
// words in between (e.g. "Isolated fake `$HOME`", "a fresh `$HOME` (isolated
// ~/.serf)"). A card matching none of these AND not in
// scenarioHomeApprovedFiles is exactly the shape kata 93f5 found: a real
// ~/.serf reference with nothing establishing it is actually isolated.
var scenarioHomeOwnIsolationMarker = regexp.MustCompile(
	`(?i)export HOME=|HOME=\$\(mktemp|Setup checklist`,
)

var scenarioHomePathPattern = regexp.MustCompile(`~/\.serf|\$\{?HOME\}?/\.serf`)

// scenarioIsolatedHomeWindow is how close "isolated" and "HOME" have to
// appear (in bytes) to count as the same phrase, e.g. "Isolated fake `$HOME`
// per attempt" or "a fresh $HOME (isolated ~/.serf)". Generous on purpose —
// this only needs to rule out false negatives on prose that already reads as
// a human explaining the precondition; scenarioHomeApprovedFiles is the
// backstop for anything this misses.
const scenarioIsolatedHomeWindow = 60

func scenarioMentionsIsolatedHome(content string) bool {
	lower := strings.ToLower(content)
	start := 0
	for {
		idx := strings.Index(lower[start:], "isolat")
		if idx < 0 {
			return false
		}
		idx += start
		windowEnd := idx + scenarioIsolatedHomeWindow
		if windowEnd > len(lower) {
			windowEnd = len(lower)
		}
		if strings.Contains(lower[idx:windowEnd], "home") {
			return true
		}
		start = idx + 1
	}
}

// TestScenarioCardsNeverReferenceRealHomeWithoutIsolation is the structural
// half of kata 93f5: TestScenarioCardsNeverTargetJessesHubPort only catches a
// card that names port 9180. A card that never mentions the port can still
// share the flock, the auth token, and the session history by referencing
// `~/.serf` (or `$HOME/.serf`) with nothing in the file establishing that
// $HOME is actually isolated for that run. A future 43rd card that does this
// fails here unless it's added to scenarioHomeApprovedFiles above, which
// forces a human to read it and say why.
func TestScenarioCardsNeverReferenceRealHomeWithoutIsolation(t *testing.T) {
	files := scenarioCardFiles(t)
	var findings []string
	for _, path := range files {
		if _, ok := scenarioHomeApprovedFiles[path]; ok {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		content := string(raw)
		if !scenarioHomePathPattern.MatchString(content) {
			continue
		}
		if scenarioHomeOwnIsolationMarker.MatchString(content) || scenarioMentionsIsolatedHome(content) {
			continue
		}
		findings = append(findings, path)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("scenario cards must not reference a real ~/.serf or $HOME/.serf "+
			"path without something in the same file establishing that $HOME is "+
			"isolated for the run (an own `export HOME=`/`HOME=$(mktemp` line, a "+
			"pointer to the Setup checklist, or an explicit \"isolated ... $HOME\" "+
			"note) — the port is not the hazard here, the shared $HOME is (kata "+
			"93f5). If this file genuinely needs the real $HOME (the OAuth-footgun "+
			"exception, kata keyb) or isolates by some other established pattern, "+
			"add it to scenarioHomeApprovedFiles with a reviewed reason:\n%s",
			strings.Join(findings, "\n"))
	}
}

// TestScenarioHomeApprovedFilesStillExist guards the allowlist itself, same
// spirit as TestScenarioPortAllowlistEntriesActuallyExist: an entry for a
// deleted or renamed card is dead weight that stops meaning anything.
func TestScenarioHomeApprovedFilesStillExist(t *testing.T) {
	for path := range scenarioHomeApprovedFiles {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("scenarioHomeApprovedFiles entry %s: %v", path, err)
		}
	}
}

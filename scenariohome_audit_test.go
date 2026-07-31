package serf_test

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioHomeApprovedFiles lists every scenario card (or runbook) allowed to
// mention a real, un-isolated serf state path (see scenarioHomePathPattern)
// with no isolation marker of its own (see scenarioHomeOwnIsolationMarker
// below). Kata 93f5
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
	"test/scenarios/ask-subagent-invisible.md": "same shape as " +
		"ask-restart-rederive.md: Pre-state is \"Hub + credentials as " +
		"ask-web-answer.md\", so the `~/.local/state/serf/projects` find in step 2 " +
		"resolves against that card's isolated $HOME export — safe exactly as " +
		"long as it is still the one live in the same shell (kata c0e1 widened " +
		"the audit to the XDG state root, which is what surfaced this card).",
	"test/scenarios/ask-noninteractive-invisible.md": "same reused-hub reason as " +
		"ask-subagent-invisible.md. Its second half runs a one-shot `serf` CLI " +
		"rather than the hub, and that run writes a transcript under whatever " +
		"$HOME the shell carries — which is the isolated one when the card is run " +
		"as written, in ask-web-answer.md's shell.",
	"test/scenarios/model-picker-recent-reflects-last-5-global.md": "continues " +
		"model-picker-fresh-install-no-recent.md's hermetic hub (\"state " +
		"accumulates across cards 2-5\"), which isolates $HOME/XDG_STATE_HOME; the " +
		"one `~/.local/state/serf/auth/openai.json` mention is the same one-time " +
		"READ its sibling is allowlisted for — an OAuth token copied INTO the " +
		"hermetic home so a real provider enumerates. Never writes the real path.",
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
// ("Setup checklist", "an isolated hub" — the runbook's own name for a hub
// started under a throwaway HOME, which is the only way to start a second one
// at all: the flock path is not overridable, docs/testing.md "A Disposable Hub
// Needs Its Own HOME"). scenarioMentionsIsolatedHome (below) separately checks
// for the harder-to-regex form: "isolated ... $HOME" with arbitrary words in
// between (e.g. "Isolated fake `$HOME`", "a fresh `$HOME` (isolated
// ~/.serf)"). A card matching none of these AND not in
// scenarioHomeApprovedFiles is exactly the shape kata 93f5 found: a real
// ~/.serf reference with nothing establishing it is actually isolated.
var scenarioHomeOwnIsolationMarker = regexp.MustCompile(
	`(?i)export HOME=|HOME=\$\(mktemp|Setup checklist|isolated hub`,
)

// scenarioStateRootIsolationMarker matches the other established way to keep a
// card off the real XDG state root: relocating the state root itself and
// leaving $HOME alone. This counts only for `~/.local/state/serf` mentions —
// the `~/.serf` root does NOT move with XDG_STATE_HOME or SERF_STATE_DIR, so a
// card naming that one still needs a real $HOME isolation marker.
//
// It has to match an assignment (`export XDG_STATE_HOME="$run/state"`, an
// inline `XDG_STATE_HOME="$TH/…"` prefix, `env:{SERF_STATE_DIR:$state}`) or
// the flag, never a bare mention of the name: cli-help.md quotes
// `SERF_STATE_DIR` as one line of `serf-tui --help` output, which established
// nothing, and the first version of this pattern handed that card a free pass
// on the whole state root.
var scenarioStateRootIsolationMarker = regexp.MustCompile(
	`(?:XDG_STATE_HOME|SERF_STATE_DIR)\s*[:=]|--state-dir[ =]`,
)

// scenarioHomeAnchor matches every way a card can name Jesse's real home
// directory: the shell's own `~` / `$HOME` / `${HOME}`, or a literal
// `/home/<user>` / `/Users/<user>` path typed out in full.
//
// Serf keeps real state under two roots, and a card can share either one.
// `~/.serf` (scenarioHubStatePathPattern) holds the hub flock, the auth token,
// credentials.toml, providers.toml and the daemon rendezvous files;
// `~/.local/state/serf` (scenarioXDGStatePathPattern — the XDG state root,
// used whenever XDG_STATE_HOME is unset) holds the OAuth records, session
// metadata and transcripts. They are separate patterns because they are
// isolated by different means, not because one is safer.
//
// Kata c0e1: the pattern used to be `~/\.serf|\$\{?HOME\}?/\.serf`, which
// spelled out one anchor and one root, so the whole rest of the cross product
// walked straight past the net — `~/.local/state/serf/auth/openai.json` (a
// card reading Jesse's live OAuth record) and `/home/jesse/.serf/run/*.json`
// (a card globbing his live rendezvous files) both named a real state root
// and neither matched. Anchor and root are separate alternations now, so a new
// spelling of either half is one entry rather than a new blind spot.
const (
	scenarioHomeAnchor  = `(?:~|\$\{?HOME\}?|/home/[^/\s]+|/Users/[^/\s]+)`
	scenarioLiteralHome = `(?:/home|/Users)/[^/\s]+`
	scenarioStateRoots  = `(?:\.serf|\.local/state/serf)`
)

var (
	scenarioHubStatePathPattern = regexp.MustCompile(scenarioHomeAnchor + `/\.serf`)
	scenarioXDGStatePathPattern = regexp.MustCompile(scenarioHomeAnchor + `/\.local/state/serf`)

	// scenarioLiteralHomePathPattern is the subset of the two above that no
	// amount of isolation can save: a state path written as a literal absolute
	// home path. `export HOME=$(mktemp -d)` redirects `~/.serf` and
	// `$HOME/.local/state/serf`; it cannot redirect `/home/jesse/.serf/run`,
	// which still names the one real directory whatever the card did to its
	// environment.
	scenarioLiteralHomePathPattern = regexp.MustCompile(scenarioLiteralHome + `/` + scenarioStateRoots)
)

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
		windowEnd := min(idx+scenarioIsolatedHomeWindow, len(lower))
		if strings.Contains(lower[idx:windowEnd], "home") {
			return true
		}
		start = idx + 1
	}
}

// TestScenarioCardsNeverReferenceRealHomeWithoutIsolation is the structural
// half of kata 93f5: TestScenarioCardsNeverTargetJessesHubPort only catches a
// card that names port 9180. A card that never mentions the port can still
// share the flock, the auth token, the OAuth record, and the session history
// by referencing a real state root (`~/.serf` or `~/.local/state/serf`, under
// any spelling of home) with nothing in the file establishing that $HOME is
// actually isolated for that run. A future 43rd card that does this fails
// here unless it's added to scenarioHomeApprovedFiles above, which forces a
// human to read it and say why.
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
		homeIsolated := scenarioHomeOwnIsolationMarker.MatchString(content) ||
			scenarioMentionsIsolatedHome(content)
		switch {
		case scenarioHubStatePathPattern.MatchString(content) && !homeIsolated:
			findings = append(findings, path+" (~/.serf — needs an isolated $HOME)")
		case scenarioXDGStatePathPattern.MatchString(content) && !homeIsolated &&
			!scenarioStateRootIsolationMarker.MatchString(content):
			findings = append(findings, path+" (~/.local/state/serf — needs an isolated $HOME or state root)")
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("scenario cards must not reference a real serf state path "+
			"(`~/.serf` or `~/.local/state/serf`, under any spelling of home) "+
			"without something in the same file establishing that $HOME is "+
			"isolated for the run (an own `export HOME=`/`HOME=$(mktemp` line, a "+
			"pointer to the Setup checklist, or an explicit \"isolated ... $HOME\" "+
			"note) — the port is not the hazard here, the shared $HOME is (kata "+
			"93f5). If this file genuinely needs the real $HOME (the OAuth-footgun "+
			"exception, kata keyb) or isolates by some other established pattern, "+
			"add it to scenarioHomeApprovedFiles with a reviewed reason:\n%s",
			strings.Join(findings, "\n"))
	}
}

// TestScenarioCardsNeverHardcodeALiteralHomePath is the half of kata c0e1 that
// no isolation marker can talk its way out of. `export HOME=$(mktemp -d)`
// redirects every `~/…` and `$HOME/…` in the card that follows it, so those
// spellings are safe once a card isolates. A literal `/home/jesse/.serf/run`
// or `/Users/jesse/.local/state/serf` is not a spelling of "this run's home"
// at all — it names Jesse's one real directory, and keeps naming it however
// carefully the rest of the card sets up its scratch $HOME. There is
// deliberately no allowlist: a card that wants to *warn* about the real state
// root can say `~/.serf`, which reads better and cannot be executed by
// accident.
func TestScenarioCardsNeverHardcodeALiteralHomePath(t *testing.T) {
	files := scenarioCardFiles(t)
	var findings []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !scenarioLiteralHomePathPattern.MatchString(line) {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("scenario cards must never hardcode a literal home path to a "+
			"serf state root — an isolated $HOME cannot redirect it, so the card "+
			"reads or writes Jesse's live flock, OAuth record, rendezvous files "+
			"or transcripts no matter what its Pre-state set up (kata c0e1). "+
			"Write the path against the run's own scratch home instead "+
			"(`$HOME/.serf/…`, `\"$run/home\"/.serf/…`), or use `~/.serf` if the "+
			"line is prose warning a reader away from the real thing:\n%s",
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

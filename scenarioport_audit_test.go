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

// scenarioPortAllowedMentions lists the exact (file, line-substring) pairs
// where the literal digits 9180 may appear in a scenario card or its runbook.
// Every entry here is a sentence that warns an executing agent away from
// Jesse's real hub, never one that hands it out as a target. Adding a new
// row is how a card earns the right to say "9180" at all — it forces the
// line to be typed out here and reviewed, rather than merely dropped into
// a Pre-state bullet by convention. See kata 66mb.
var scenarioPortAllowedMentions = map[string][]string{
	"test/scenarios/README.md": {
		"Never Jesse's real hub or his port `9180`",
	},
	"docs/agentic-testing.md": {
		"Never Jesse's real hub, never his port `9180`",
		"the doc's old literal `9180` would frequently fail to bind",
		// The two entries that used to sit here named a hand-picked free
		// port. 68fm replaced that whole recipe: a hub now binds
		// 127.0.0.1:0 and reports back the port the kernel gave it, so
		// there is no port for a human to pick or to collide on. These
		// are the warnings that survived that rewrite.
		"never port `9180`, as usual",
		"checklist, never Jesse's real `9180`",
		"`pgrep -f 'serf-hub.*:9180'` (matches however the real hub was",
	},
	"test/scenarios/spawn-empty-prompt-starts-dormant.md": {
		"never Jesse's port 9180",
	},
	"test/scenarios/cost-estimate-display-and-gating.md": {
		"never Jesse's real `9180`",
	},
	"test/scenarios/ended-session-metrics-tui-and-web.md": {
		"non-`9180` port — never Jesse's",
	},
	"test/scenarios/web-goal-set-and-complete.md": {
		"default `0.0.0.0:9180` may host an unrelated",
		"pgrep -f 'serf-hub.*:9180' >/dev/null && \\",
		"{ echo \"Jesse's real hub is running on 9180 — this card cannot start until it stops (flock at ~/.serf/hub.lock)\" >&2; exit 1; }",
	},
	"test/scenarios/job-restart-durability.md": {
		"never Jesse's real hub on `9180`",
	},
	"test/scenarios/recursion-coordinator-fanout.md": {
		"never Jesse's real hub on `9180`",
	},
	"test/scenarios/recursion-deaf-coordinator-drivedown.md": {
		"never Jesse's real hub on `9180`",
	},
	"test/scenarios/attention-needs-you-end-to-end.md": {
		"never Jesse's real `9180`",
	},
	"test/scenarios/sidecar-approval-broker-communicate.md": {
		"never Jesse's real hub on `9180`",
		"holds the flock — check `pgrep -f 'serf-hub.*:9180'` first rather than",
	},
	"test/scenarios/job-watch-caller-send-no-deadlock.md": {
		"never Jesse's real hub on `9180`",
	},
	"test/scenarios/dev-fix-broken-script.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/dev-hello-script.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/dev-plugin-superpowers-brainstorming.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/meta-flush-on-completion.md": {
		"never `9180`",
	},
	"test/scenarios/transcript-endpoint-url.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-interrupt-live-turn.md": {
		"never Jesse's port `9180`",
		"never Jesse's real `9180`",
	},
	"test/scenarios/tui-paste-image-from-clipboard.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-paste-image-path.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-queue-then-completes.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-queue-then-drain-as-steer.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-steer-live-turn.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-steer-success-reconciles.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/tui-workspace-navigation.md": {
		"never Jesse's port `9180`",
	},
	"test/scenarios/web-drag-drop-image.md":            {"never `9180`, Jesse's real one"},
	"test/scenarios/web-file-picker-image.md":          {"never `9180`, Jesse's real one"},
	"test/scenarios/web-paste-image-from-clipboard.md": {"never `9180`, Jesse's real one"},
	"test/scenarios/web-queue-then-completes.md":       {"never `9180`,"},
	"test/scenarios/web-queue-then-drain-as-steer.md":  {"never `9180`, Jesse's"},
	"test/scenarios/web-steer-in-idle-fails-fast.md":   {"never `9180`,"},
	"test/scenarios/web-steer-live-turn.md":            {"never `9180`,"},
	"test/scenarios/web-steer-success-reconciles.md":   {"never `9180`,"},
	"test/scenarios/workspace-title-bar-actions.md":    {"never `9180`,"},
}

// TestScenarioCardsNeverTargetJessesHubPort is the structural half of kata
// 66mb's fix. 42 scenario cards under test/scenarios/ used to instruct an
// executing agent to spawn or curl against 127.0.0.1:9180 / 0.0.0.0:9180 —
// Jesse's own live serf-hub, host-wide flock'd at ~/.serf/hub.lock. This
// test makes that class of mistake fail the build instead of relying on
// authors remembering the convention: any future card (or edit to an
// existing one) that names port 9180 fails here unless the exact line is
// added to scenarioPortAllowedMentions above, which requires a human to
// read it and agree it's a warning, not an instruction.
func TestScenarioCardsNeverTargetJessesHubPort(t *testing.T) {
	files := scenarioCardFiles(t)
	var findings []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		allowed := scenarioPortAllowedMentions[path]
		for i, line := range strings.Split(string(raw), "\n") {
			if !containsWholePort9180(line) {
				continue
			}
			if lineIsAllowed(line, allowed) {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("scenario cards must never target Jesse's live hub on port 9180 "+
			"(host-wide flock'd at ~/.serf/hub.lock) — use an isolated $HOME and a "+
			"free port instead (see docs/agentic-testing.md \"Setup checklist\"). "+
			"If this line is a genuine warning against 9180 rather than an "+
			"instruction to use it, add it to scenarioPortAllowedMentions:\n%s",
			strings.Join(findings, "\n"))
	}
}

// scenarioLiteralHostPortPattern matches an address a card typed out with the
// port already decided: `127.0.0.1:9280`, `http://127.0.0.1:9186/api/spawn`,
// `localhost:9187`, `0.0.0.0:9180`. Port `0` is the whole point of the
// convention and is deliberately matched-then-allowed below rather than
// excluded here, so the allowance is visible next to the rule.
//
// A port written as a variable (`127.0.0.1:$PORT`) or a placeholder
// (`127.0.0.1:<random-unused-port>`) has no digits and never matches: those
// are cards deriving the port from the hub that bound it, which is exactly
// what this audit is pushing every card towards.
var scenarioLiteralHostPortPattern = regexp.MustCompile(`(?:127\.0\.0\.1|0\.0\.0\.0|localhost):([0-9]+)`)

// scenarioLiteralHostPortAllowedMentions is the allowlist for the rule below.
// Unlike scenarioPortAllowedMentions — which is a large list of prose warning
// agents away from 9180 — this one exists to stay empty or near it: a card
// with a documented ruling behind its literal port, and nothing else. Cite the
// ruling in the comment; a bare entry is a rubber stamp.
var scenarioLiteralHostPortAllowedMentions = map[string][]string{
	// Kata keyb ruled that this card keeps the REAL $HOME: its subject is a
	// goal turn under an already-signed-in OpenAI OAuth session, and that
	// state only exists under the real user state home. A hub on the real
	// $HOME contends for Jesse's own `~/.serf/hub.lock`, so the card has to
	// name the address his hub actually listens on in order to warn about it.
	// This is the sentence that does the warning; the card's own launch four
	// lines further down still binds 127.0.0.1:0 like every other card.
	"test/scenarios/web-goal-set-and-complete.md": {
		"The default `0.0.0.0:9180` may host an unrelated",
	},
}

// TestScenarioCardsNeverNameALiteralHostPort closes the gap
// TestScenarioCardsNeverTargetJessesHubPort left open: it checks one literal,
// 9180, so a card could pick any other number and pass. Cards did — 9280,
// 9186, 9187, 9331 — and the ones that shared a number shared it on purpose
// ("reuse that hub if it's still running on 127.0.0.1:9280"), which turned a
// hand-picked port into a rendezvous protocol and a collision domain: two
// agents running the same card set at once fight over one listener.
//
// The convention that replaced it (kata 68fm, docs/agentic-testing.md's Setup
// checklist) is that nobody picks a port at all — the hub binds
// 127.0.0.1:0, logs what the kernel gave it, and every consumer reads that
// back. A sibling card that wants the same hub takes the run directory
// (`$SERF_E2E_RUN`) and re-derives the port from `$run/hub.log`. So a literal
// port in a card is, by construction, either a stale pre-68fm recipe or a
// private rendezvous convention — both of which this test rejects.
func TestScenarioCardsNeverNameALiteralHostPort(t *testing.T) {
	files := scenarioCardFiles(t)
	var findings []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		allowed := scenarioLiteralHostPortAllowedMentions[path]
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range scenarioLiteralHostPortPattern.FindAllStringSubmatch(line, -1) {
				if m[1] == "0" {
					continue // the sanctioned kernel-assigned form
				}
				if lineIsAllowed(line, allowed) {
					continue
				}
				findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				break
			}
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("scenario cards must never name a host:port with the port already "+
			"decided — bind `127.0.0.1:0` and read the port back from the hub's own "+
			"`listening on` line instead (docs/agentic-testing.md \"Setup checklist\"), "+
			"and hand it to a sibling card through `$SERF_E2E_RUN`/`$run/hub.log` "+
			"rather than by agreeing on a number. A hand-picked port is a collision "+
			"domain: two agents running these cards at once contend for one listener. "+
			"If a line genuinely needs to name a port because a documented ruling "+
			"forces it, add it to scenarioLiteralHostPortAllowedMentions and cite the "+
			"ruling:\n%s",
			strings.Join(findings, "\n"))
	}
}

// TestScenarioPortAllowlistEntriesActuallyExist guards both port allowlists:
// every configured (file, substring) pair must still be findable in the
// named file, so a rewritten warning silently drops its exemption instead of
// the exemption rotting into a stale, unchecked entry.
func TestScenarioPortAllowlistEntriesActuallyExist(t *testing.T) {
	for _, allowlist := range []map[string][]string{
		scenarioPortAllowedMentions,
		scenarioLiteralHostPortAllowedMentions,
	} {
		for path, substrs := range allowlist {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("allowlisted file %s does not exist: %v", path, err)
			}
			content := string(raw)
			for _, sub := range substrs {
				if !strings.Contains(content, sub) {
					t.Errorf("%s: allowlisted substring no longer present, remove or fix the entry: %q", path, sub)
				}
			}
		}
	}
}

// scenarioDir is the one directory the scenario audits read, relative to the
// repo root that `go test` runs them from.
const scenarioDir = "test" + string(filepath.Separator) + "scenarios"

func scenarioCardFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	entries, err := os.ReadDir(scenarioDir)
	if err != nil {
		t.Fatalf("reading %s: %v", scenarioDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		files = append(files, filepath.Join(scenarioDir, e.Name()))
	}
	files = append(files, "docs/agentic-testing.md")
	sort.Strings(files)
	return files
}

// containsWholePort9180 reports whether line contains the digits 9180 as a
// standalone token — not as part of a longer number like 19180 or 91800,
// which are unrelated ports used elsewhere in the suite.
func containsWholePort9180(line string) bool {
	const needle = "9180"
	rest := line
	for {
		idx := strings.Index(rest, needle)
		if idx < 0 {
			return false
		}
		before := byte(0)
		if idx > 0 {
			before = rest[idx-1]
		}
		after := byte(0)
		if idx+len(needle) < len(rest) {
			after = rest[idx+len(needle)]
		}
		if !isDigit(before) && !isDigit(after) {
			return true
		}
		rest = rest[idx+len(needle):]
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func lineIsAllowed(line string, allowed []string) bool {
	for _, sub := range allowed {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

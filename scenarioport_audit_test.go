package serf_test

import (
	"os"
	"path/filepath"
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
		"Pick a free port — never 9180",
		"never bind port `9180`",
		"checklist, never Jesse's real `9180`",
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

// TestScenarioPortAllowlistEntriesActuallyExist guards the allowlist itself:
// every configured (file, substring) pair must still be findable in the
// named file, so a rewritten warning silently drops its exemption instead of
// the exemption rotting into a stale, unchecked entry.
func TestScenarioPortAllowlistEntriesActuallyExist(t *testing.T) {
	for path, substrs := range scenarioPortAllowedMentions {
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

func scenarioCardFiles(t *testing.T) []string {
	t.Helper()
	const scenarioDir = "test" + string(filepath.Separator) + "scenarios"
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

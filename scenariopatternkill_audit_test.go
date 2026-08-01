package serf_test

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioPatternKillPattern matches the two commands that kill by name rather
// than by pid: `pkill -f serf-hub`, `killall serf`. Both signal every process
// on the box whose command line matches, so a card that runs one reaches
// straight into another agent's run — the hub it is mid-scenario against, the
// login flow it is waiting on, the producer whose output it is about to read.
// `pgrep` is deliberately absent: reading which processes exist is fine, and
// kata pcev's ruling on auth-device-poll-concurrent.md is exactly that — read
// the pgrep, don't clear it.
var scenarioPatternKillPattern = regexp.MustCompile(`\b(?:pkill|killall)\b`)

// scenarioPatternKillAllowedMentions lists the exact (file, line-substring)
// pairs where a card may write `pkill` or `killall` at all. Two kinds of row:
//
//   - the warnings, which are most of them. Thirteen cards and the runbook
//     already carry hand-written "never `pkill -f`, it takes out every
//     concurrent agent's hub" prose, and that volume is what asked for this
//     check — it is a convention every author re-derived by reading a
//     neighbouring card.
//   - a kill whose pattern cannot match anything but this run, with the
//     ruling that says so cited.
//
// Kata 4k48 framed the choice: a verbatim allowlist costs an entry per warning
// and re-breaks whenever one is reworded, while the cheap heuristic — allow
// `pkill` on a line that also says "never" or "takes out" — passes any real
// instruction that happens to share a line with the word. The verbatim list
// wins because the churn is loud and lands in a human's hands, and the
// heuristic's failure is silent.
var scenarioPatternKillAllowedMentions = map[string][]string{
	"docs/agentic-testing.md": {
		"instead of by `pkill -f`. Those are the same two files",
		"(not `pkill -f`, which would also kill any other concurrent agent's hub)",
		"# `pkill -f serf-hub-test` pattern match, which would also kill any other",
	},
	"test/scenarios/ask-noninteractive-invisible.md": {
		"`pkill -f serf-hub`, which takes out every other concurrent agent's hub too",
	},
	"test/scenarios/ask-restart-rederive.md": {
		"never a `pkill -f serve` pattern, which would also kill a concurrent agent's",
	},
	"test/scenarios/ask-subagent-invisible.md": {
		"`pkill -f serf-hub`, which takes out every other concurrent agent's hub too",
	},
	"test/scenarios/ask-tui-answer.md": {
		"Never `pkill -f serf-hub`, which takes out every",
	},
	"test/scenarios/attention-needs-you-end-to-end.md": {
		"never `pkill -f serf-hub`, which would also kill a",
	},
	"test/scenarios/auth-device-poll-concurrent.md": {
		"`pkill -f 'serf openai login'`, which would also kill a",
		"`pkill -f 'serf openai login'` pattern, which would also kill a",
	},
	"test/scenarios/cli-device-code-flow.md": {
		"exists for). Never `pkill -f",
	},
	"test/scenarios/compact-note-survives-resume.md": {
		"by name — never `pkill -f",
	},
	"test/scenarios/compact-tool-pins-note-and-persists.md": {
		"`pkill -f serf-hub` also kills every *other* concurrent",
		"and a `pkill -f` whose pattern appears in your",
	},
	"test/scenarios/meta-flush-on-completion.md": {
		// The one sanctioned kill in the corpus, and the reason this audit
		// cannot be a blanket ban. The pattern carries the session id, so it
		// matches this run's daemon and no other agent's: kata pcev ruled it
		// "session-scoped and fine" and left it alone; kata 4k48 restated that
		// it must survive whatever rule replaced the hand-written warnings.
		"`pkill -f 'serf serve.*<session_id>'` — kill mid-life.",
	},
	"scripts/disk-reclaim-selftest.sh": {
		// The comment over `probe_run_id`, which is where the rule below is
		// written down for the next author of this script.
		"# by its COMMAND LINE with `pgrep -f` / `pkill -f`. That command line therefore",
		// Cleanup for the run's OWN stall probe, after the assertion that it
		// should already be dead has failed. The pattern is what makes this safe:
		// the probe's duration carries this run's zero-padded pid, so it cannot
		// match a concurrent selftest's probe. Before kata qw8e both runs spawned
		// a fixed `sleep 987654` and this line reaped the other run's probe while
		// that run was still polling for it.
		`pkill -f "$stall_probe"`,
		`pkill -f "$report_stall_probe"`,
	},
	"test/scenarios/model-switch-providers-live.md": {
		"never a `pkill -f 'serf serve'` pattern, which would also kill a",
	},
	"test/scenarios/spawn-failure-ux-post-ws5.md": {
		"`pkill -f serf-hub` pattern, which would take out any concurrent",
	},
	"test/scenarios/spawn-keyboard-contract.md": {
		"never `pkill -f serf-hub`,",
	},
	"test/scenarios/tui-goal-set-and-complete.md": {
		"# by pid: `pkill -f serf-hub` would take out",
	},
}

// TestNoCardOrScriptPatternKillsAProcess makes the corpus's most-repeated
// convention mechanical. Thirteen cards plus docs/agentic-testing.md warn in
// prose against `pkill -f`, and prose is why two cards shipped the instruction
// anyway (kata pcev) and went unnoticed until an unrelated kata happened to
// read them.
//
// A pattern kill is the process-side twin of the two collisions this repo
// already fails the build over: a fixed port is one listener two agents fight
// for, a fixed /tmp path is one file, and `pkill -f serf-hub` is every hub on
// the box at once. Kill by a pid the card recorded — `$HUBPID`,
// `$run/hub.pid`, `$tmpdir/login.pid` — per docs/agentic-testing.md's Setup
// checklist.
//
// scripts/*.sh is the second corpus, added by kata qw8e. A script has the same
// reach as a card and gets run more often, and disk-reclaim-selftest.sh was
// carrying the class live: it killed its stall probe by the fixed pattern
// `sleep 987654`, which two concurrent selftests both spawn, so one run's
// cleanup reaped the other's probe while that run was still polling for it.
func TestNoCardOrScriptPatternKillsAProcess(t *testing.T) {
	var findings []string
	scriptMatches := 0
	for _, path := range append(scenarioCardFiles(t), auditedShellScripts(t)...) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		allowed := scenarioPatternKillAllowedMentions[path]
		for i, line := range strings.Split(string(raw), "\n") {
			if !scenarioPatternKillPattern.MatchString(line) {
				continue
			}
			if isAuditedShellScript(path) {
				scriptMatches++
			}
			if lineIsAllowed(line, allowed) {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	// Clean corpus and dead needle are the same green, and only a floor on
	// matches tells them apart (scenariofixture_audit_test.go). scripts/ carries
	// the two sanctioned kills below and the comment that explains them, so zero
	// is not "the scripts are clean" — it is "this audit stopped reading them".
	if scriptMatches == 0 {
		t.Fatalf("the pattern-kill needle matched nothing across %s/*.sh, where "+
			"disk-reclaim-selftest.sh reaps its own stall probes with `pkill -f`. "+
			"Zero matches means the pattern or the file set is dead and the script "+
			"half of this audit is checking nothing", auditedShellScriptDirs)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card or a script in %s must never kill a process by "+
			"name — `pkill -f` and `killall` signal every match on the box, so it "+
			"reaches into any concurrent agent's run: their hub mid-scenario, their "+
			"login flow mid-poll, the producer whose output they are about to read "+
			"(kata pcev). Kill by a pid the run recorded instead (`$HUBPID`, "+
			"`$run/hub.pid`, `$tmpdir/login.pid` — docs/agentic-testing.md's Setup "+
			"checklist). If the line WARNS against a pattern kill rather than "+
			"performing one, or the pattern provably carries something unique to "+
			"this run, add it to scenarioPatternKillAllowedMentions and say why:\n%s",
			auditedShellScriptDirs, strings.Join(findings, "\n"))
	}
}

// TestScenarioPatternKillPatternMatchesTheShapesItClaims keeps the audit above
// falsifiable. With the corpus clean it passes whether the rule works or the
// rule matches nothing at all, and those two readings are the same green. The
// positives are the instructions this class actually shipped — pcev's two login
// cards, and the `pkill` in job-restart-durability.md's Cleanup that this audit
// found on its first run; the negatives are the convention that replaced them.
func TestScenarioPatternKillPatternMatchesTheShapesItClaims(t *testing.T) {
	byPattern := []string{
		"   pkill -f 'serf openai login'",
		"  daemon dies (SIGPIPE); verify with `pgrep -f TICK_` and `pkill` if",
		"killall serf-hub",
		`pkill -9 -f "serf serve"`,
	}
	for _, line := range byPattern {
		if !scenarioPatternKillPattern.MatchString(line) {
			t.Errorf("pattern kill not detected: %q", line)
		}
	}
	byPID := []string{
		`kill "$HUBPID"; rm -rf "$run"`,
		"kill $PRODUCER 2>/dev/null",
		`kill "$(cat "$run/hub.pid")"`,
		"  pgrep -f 'serf-hub.*:9180' >/dev/null && \\",
	}
	for _, line := range byPID {
		if scenarioPatternKillPattern.MatchString(line) {
			t.Errorf("kill by pid (or a pgrep read) reported as a pattern kill: %q", line)
		}
	}
}

// TestScenarioPatternKillAllowlistEntriesActuallyExist is the guard that makes
// the verbatim allowlist affordable: a reworded warning loses its exemption
// here, loudly, in one place, instead of leaving a row that exempts nothing and
// is never reread.
func TestScenarioPatternKillAllowlistEntriesActuallyExist(t *testing.T) {
	var stale []string
	for path, substrs := range scenarioPatternKillAllowedMentions {
		raw, err := os.ReadFile(path)
		if err != nil {
			stale = append(stale, path+" (file does not exist)")
			continue
		}
		for _, sub := range substrs {
			if !strings.Contains(string(raw), sub) {
				stale = append(stale, path+": "+sub)
			}
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("scenarioPatternKillAllowedMentions has %d entry/entries that no "+
			"longer match anything. Drop the entry, or update it to the line as it "+
			"reads now:\n%s", len(stale), strings.Join(stale, "\n"))
	}
}

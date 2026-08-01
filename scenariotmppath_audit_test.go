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

// scenarioFixedTmpPathPattern matches a path under the shared /tmp whose first
// segment a card typed out: `/tmp/serf-doctor`, `/tmp/login-out.txt`,
// `/tmp/eff-cfg/providers.toml`, `/tmp/serf-e2e-*`. Two agents running that
// card at the same time resolve the same string, which is the whole hazard —
// one's build overwrites the other's binary mid-run, one's Cleanup deletes the
// fixture the other is about to paste (kata k2rx).
//
// The capture is the path itself, so the one sanctioned spelling can be
// matched-then-allowed at the point of use below rather than hidden in this
// regexp: `mktemp -d /tmp/serf-hook-scenario.XXXXXX` names /tmp and is still
// unique per run, because mktemp re-rolls the X's.
var scenarioFixedTmpPathPattern = regexp.MustCompile("/tmp/([^\\s\"'`)\\]}]+)")

// scenarioMktempTemplateMarker is the run of X's mktemp(1) replaces with random
// characters. A /tmp path containing it is not a fixed path: `mktemp -d
// /tmp/serf-hook-scenario.XXXXXX` gives every concurrent run its own directory,
// and an observed error message quoting `/tmp/serf-tui-imgpath-XXXX` is naming
// that same per-run shape with the random part elided.
const scenarioMktempTemplateMarker = "XXX"

// scenarioFixedTmpPathAllowedMentions lists the exact (file, line-substring)
// pairs where a fixed /tmp path may still appear. Every entry is a line that
// NAMES such a path without telling an executing agent to use one — the four
// shapes kata 2g2t's sweep deliberately left in place, plus the warnings that
// sweep itself wrote:
//
//   - a warning against a fixed path or a /tmp glob ("never a fixed
//     `/tmp/serf-doctor` that a second card would overwrite"),
//   - a past-tense record of what an earlier run did, which rewriting would
//     falsify,
//   - an observed payload or error message quoting a path whose random part is
//     elided,
//   - a string that merely contains "/tmp/" and creates no file at all — the
//     localStorage key in spawn-stale-model-cleared.md.
//
// Adding a row is how a line earns the right to say `/tmp/<name>` at all: it
// has to be typed out here and read by a human first. That cost is the point.
// The three hand sweeps this audit replaces (k2rx, 8b8w's 17 cards, 2g2t's 12)
// each re-derived the same judgement from scratch and the third still shipped
// short by two.
var scenarioFixedTmpPathAllowedMentions = map[string][]string{
	"docs/agentic-testing.md": {
		// The Setup checklist's own warnings, inside the recipe every card
		// copies from.
		"not a fixed /tmp/serf-hub-test that a second concurrent build would",
		"one lives under its own $run dir instead of a fixed /tmp/serf-hub-test)",
		"not a `rm -rf /tmp/serf-e2e-*` glob",
		// An invented path in a sentence about tmux send-keys parsing: the
		// point is that the string contains a `/`, and nothing creates it.
		"a literal `/tmp/foo/AGENTS.md` containing",
	},
	// scripts/*.sh, added to this audit by kata qw8e. Only two rows, because a
	// script has no prose to warn in: both of these are scripts ABOUT the debris
	// in /tmp rather than scripts that put any there.
	"scripts/reclaim-test-debris.sh": {
		// The one-off GOCACHE this script exists to DELETE (kata r07s left it
		// behind). Naming it is the whole job, no run creates it, and two
		// concurrent reclaims of one already-absent directory collide over
		// nothing.
		"2) /tmp/serf-gocache-k3",
		"gocache_debris='/tmp/serf-gocache-k3'",
	},
	"scripts/report-tmp-debris.sh": {
		// A dated measurement in the header — what 120 `/tmp/serf*` entries
		// weighed on 2026-07-30. This script reports and never writes.
		"8.4G across 120 `/tmp/serf*`",
	},
	"test/scenarios/auth-device-poll-concurrent.md": {
		"fixed `/tmp/login-out.txt`, which a second agent running this card",
	},
	"test/scenarios/compact-note-survives-resume.md": {
		"an `rm -rf /tmp/serf-sc-home-*` glob (it deletes every other concurrent run of",
	},
	"test/scenarios/hooks-claude-compat-matcher.md": {
		"fixed `/tmp/serf` a second concurrent run would overwrite mid-test",
		// The comment naming the mktemp'd directory this card cleans up. The
		// template itself (line 57) needs no entry — the X's exempt it.
		"# the dir is self-contained under /tmp/serf-hook-scenario.*",
	},
	"test/scenarios/model-picker-badges-match-catalog-data.md": {
		// Dated Results prose reporting what a past run rebuilt. Rewriting it
		// to today's convention would falsify the record.
		"Rebuilt `/tmp/serf-hub` and restarted the hub to",
	},
	"test/scenarios/model-switch-providers-live.md": {
		"never a `/tmp/serf-e2e-msw-*`",
	},
	"test/scenarios/reasoning-effort-providers.md": {
		"never a fixed `/tmp/serf-eff` +",
		"`/tmp/eff-cfg` pair, which a card running beside this one would overwrite",
	},
	"test/scenarios/serf-doctor-forensics.md": {
		"`/tmp/serf-doctor` that a second card running at the same time would",
	},
	"test/scenarios/spawn-stale-model-cleared.md": {
		// A localStorage KEY whose text happens to contain a project path. No
		// file is created, and three assertions read this exact string.
		`"serf-hub.spawn-defaults./tmp/some-project",`,
		`perProject: localStorage.getItem("serf-hub.spawn-defaults./tmp/some-project"),`,
		`localStorage.removeItem("serf-hub.spawn-defaults./tmp/some-project");`,
	},
	"test/scenarios/transcript-find-by-query-content-search.md": {
		"`/tmp/serf` that a second card running at the same time would",
	},
	"test/scenarios/transcript-find-scope-all-projects.md": {
		"`/tmp/serf` that a second card running at the same time would",
	},
	"test/scenarios/transcript-read-jsonl-debug-hatch.md": {
		"`/tmp/serf` that a second card running at the same time would",
	},
	"test/scenarios/transcript-read-outline-range-expand-turn.md": {
		"`/tmp/serf` that a second card running at the same time would",
	},
	"test/scenarios/tui-goal-set-and-complete.md": {
		"never a fixed `/tmp/serf-hub-test` a second",
	},
	"test/scenarios/tui-paste-image-path.md": {
		"fixed `/tmp/serf-e2e-test-image.png` that a second agent's Cleanup",
	},
	"test/scenarios/tui-steer-success-reconciles.md": {
		"never a fixed `/tmp/pane-*.txt`",
	},
	"test/scenarios/web-file-picker-image.md": {
		// An observed request payload. The directory it names was mktemp'd by
		// the run that produced it; the random part is elided with `…`.
		`payload: {"files": ["/tmp/serf-e2e-img-…/red.png"]}`,
	},
	"test/scenarios/web-goal-set-and-complete.md": {
		"never a `/tmp/serf-e2e-*` glob",
	},
	"test/scenarios/web-model-switch-mid-session.md": {
		"`/tmp/…` name a second concurrent run would clobber (kata `k2rx`)",
	},
	"test/scenarios/worktree-create-and-orient.md": {
		"never a fixed `/tmp/serf-wt` that a card running beside this one would",
	},
	"test/scenarios/worktree-ergonomics-findings.md": {
		// Past-tense Reproduction section: where an earlier run's harness and
		// transcripts actually lived.
		"`/tmp/wt-scen-run1/` (`run.sh`, `matrix.sh`, `inspect.py`, `analyze.sh`)",
	},
}

// TestNoCardOrScriptNamesAFixedTmpPath is the structural check the fourth
// member of this hazard family never had. Ports (9180, literal host:port,
// hand-picked placeholder) and homes (real state root, literal home path, hub
// launch without HOME) each fail the build; a card writing to a fixed path
// under the shared /tmp was swept by hand three times instead — k2rx, then
// 8b8w's 17 port-driven cards, then 2g2t's 12.
//
// Hand sweeps do not hold. 2g2t's candidate list was itself the union of two
// earlier enumerations and still missed two live offenders
// (job-restart-durability.md's `cp "$JOBS" /tmp/jobs-before-restart.jsonl` and
// tui-paste-image-path.md's fixed PNG fixture), and its own sweep of
// serf-doctor-forensics.md left three `/tmp/serf-doctor` invocations behind in
// steps 5 and 6 — which is what this test found on its first run (kata xvb2).
//
// scripts/*.sh is the second corpus, added by kata qw8e: the audit stopped at
// the cards while the scripts agents run every day carried the same class
// unchecked, and two live instances were sitting there — fuzz-triage's stub
// flake counter defaulting to a shared `/tmp/stubgo.cnt` whose odd/even parity
// decides the verdict, and fuzz-coverage-global's `RAPID_FAILFILE=/tmp/ambient`.
// Shell is the easier half of the corpus: a script has no prose, so a `/tmp`
// path in one is nearly always an instruction rather than a warning about one.
func TestNoCardOrScriptNamesAFixedTmpPath(t *testing.T) {
	var findings []string
	scriptMatches := 0
	for _, path := range append(scenarioCardFiles(t), auditedShellScripts(t)...) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		allowed := scenarioFixedTmpPathAllowedMentions[path]
		for i, line := range strings.Split(string(raw), "\n") {
			if !scenarioNamesAFixedTmpPath(line) {
				continue
			}
			if filepath.Dir(path) == scriptDir {
				scriptMatches++
			}
			if lineIsAllowed(line, allowed) {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped reaching that corpus, and the two are the same green
	// (scenariofixture_audit_test.go). The cards cannot go quiet — their
	// allowlist is 30 entries deep — but scripts/ carries only the handful
	// below, so the script half gets the floor.
	if scriptMatches == 0 {
		t.Fatalf("the fixed-/tmp needle matched nothing across %s/*.sh. Two scripts "+
			"exist to talk about /tmp (reclaim-test-debris.sh names the one-off "+
			"GOCACHE debris it removes, report-tmp-debris.sh sizes `/tmp/serf*`), so "+
			"zero matches means the pattern or the file set is dead and the script "+
			"half of this audit is checking nothing", scriptDir)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card or a script in %s must never name a fixed path "+
			"under the shared /tmp — two agents running it at once resolve the same "+
			"string, so one's build overwrites the other's binary mid-run and one's "+
			"Cleanup deletes the fixture the other is about to read (kata k2rx). "+
			"Name every artifact from the run's own directory instead: "+
			"`run=$(mktemp -d -t serf-e2e-XXXXXX)` and then `\"$run/…\"`, per "+
			"docs/agentic-testing.md's Setup checklist. If the line NAMES a fixed "+
			"path without instructing anyone to use one — a warning against it, a "+
			"past run's record, an observed payload with the random part elided — "+
			"add it to scenarioFixedTmpPathAllowedMentions with the reason:\n%s",
			scriptDir, strings.Join(findings, "\n"))
	}
}

// scenarioNamesAFixedTmpPath is the rule itself, shared by the corpus audit and
// the pattern-level test below so the two cannot drift: a line names a fixed
// /tmp path when it writes one whose characters are all decided in advance.
func scenarioNamesAFixedTmpPath(line string) bool {
	for _, m := range scenarioFixedTmpPathPattern.FindAllStringSubmatch(line, -1) {
		if strings.Contains(m[1], scenarioMktempTemplateMarker) {
			continue // mktemp's own template — re-rolled per run
		}
		return true
	}
	return false
}

// TestScenarioFixedTmpPathPatternMatchesTheShapesItClaims keeps the audit above
// falsifiable. Now that the corpus is clean, that audit passes both when the
// rule works and when the rule has quietly stopped matching anything — the two
// readings are indistinguishable from a green run. These cases separate them,
// and they are drawn from the shapes the three hand sweeps actually found (and,
// in the first negative case, from the one spelling the Setup checklist asks
// for).
func TestScenarioFixedTmpPathPatternMatchesTheShapesItClaims(t *testing.T) {
	fixed := []string{
		"- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).",
		"   (`cp \"$JOBS\" /tmp/jobs-before-restart.jsonl`) and note the last",
		`   /tmp/serf-doctor jobs "$SID" --state-dir "$SCR"`,
		`   tmux capture-pane -t "$TMUX_SESSION" -p > /tmp/pane-pending.txt`,
		"   convert -size 64x64 xc:red /tmp/serf-e2e-test-image.png",
		"   rm -rf /tmp/serf-e2e-* # a shared prefix is still a shared path",
	}
	for _, line := range fixed {
		if !scenarioNamesAFixedTmpPath(line) {
			t.Errorf("fixed /tmp path not detected: %q", line)
		}
	}
	perRun := []string{
		"run=$(mktemp -d -t serf-e2e-XXXXXX)",
		`go build -o "$run/serf-doctor" ./cmd/serf-doctor`,
		"WORK=$(mktemp -d /tmp/serf-hook-scenario.XXXXXX)",
		"  `read /tmp/serf-tui-imgpath-XXXX: is a directory`; a harmless",
		"- The run directory mktemp made under /tmp holds every artifact.",
	}
	for _, line := range perRun {
		if scenarioNamesAFixedTmpPath(line) {
			t.Errorf("per-run path reported as fixed: %q", line)
		}
	}
}

// TestScenarioFixedTmpPathAllowlistEntriesActuallyExist keeps the carve-outs
// honest, same spirit as TestScenarioPortAllowlistEntriesActuallyExist: a
// reworded warning must drop its exemption loudly here rather than leave a
// stale row nobody rereads. That noise is the price of a verbatim allowlist,
// and it is cheaper than the alternative — a heuristic that reads the sentence
// around the path would pass a real instruction that happens to say "never".
func TestScenarioFixedTmpPathAllowlistEntriesActuallyExist(t *testing.T) {
	var stale []string
	for path, substrs := range scenarioFixedTmpPathAllowedMentions {
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
		t.Fatalf("scenarioFixedTmpPathAllowedMentions has %d entry/entries that no "+
			"longer match anything. Drop the entry, or update it to the line as it "+
			"reads now:\n%s", len(stale), strings.Join(stale, "\n"))
	}
}

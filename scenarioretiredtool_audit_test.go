package serf_test

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioRetiredJobTools names model-facing job tools that no longer exist in
// the tool registry, mapped to the surviving surface a card should teach
// instead. A card that calls one of these does not fail loudly at the step that
// names it — the agent executing the card gets `unknown tool: "<name>"` and
// improvises, so the run produces a plausible transcript that proves nothing.
// Kata fd8n found 17 cards plus docs/agentic-testing.md still teaching
// job_read_output five weeks after cf84923c6 unregistered it.
var scenarioRetiredJobTools = map[string]string{
	"job_read_output": "unregistered by cf84923c6 (2026-06-23). Supervision " +
		"reads are `job_status`; job output is read with " +
		"`read_transcript(transcript_ref=\"job:<job_id>\")`, which is also the " +
		"call an observer spends its watch read grant on",
}

// scenarioRetiredToolAllowedMentions lists the exact (file, line-substring)
// pairs where a retired tool name may still appear. The only sanctioned reason
// is a dated "Verified live" results block reporting what a past run actually
// called: rewriting that sentence to name today's tool would falsify the
// record. An instruction to an executing agent never qualifies.
var scenarioRetiredToolAllowedMentions = map[string][]string{
	"test/scenarios/job-watch-passive-observer-noop-filter.md": {
		"Parent still used `job_list` and `job_read_output` after successful",
	},
}

// TestScenarioCardsNeverCallARetiredJobTool keeps the card corpus executable:
// every tool a card tells an agent to call has to be one the agent will
// actually be handed. The check runs over scenarioCardFiles, so it covers
// docs/agentic-testing.md as well as the cards themselves.
func TestScenarioCardsNeverCallARetiredJobTool(t *testing.T) {
	var findings []string
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for name := range scenarioRetiredJobTools {
				if !strings.Contains(line, name) {
					continue
				}
				if scenarioRetiredToolMentionAllowed(path, line) {
					continue
				}
				findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		var guidance []string
		for name, replacement := range scenarioRetiredJobTools {
			guidance = append(guidance, name+" — "+replacement)
		}
		sort.Strings(guidance)
		t.Fatalf("scenario cards and docs/agentic-testing.md must not name a job "+
			"tool that is no longer registered — an agent executing the step gets "+
			"`unknown tool` and improvises, so the run looks like evidence and is "+
			"not (kata fd8n):\n%s\n\nRetired tools and what to teach instead:\n%s",
			strings.Join(findings, "\n"), strings.Join(guidance, "\n"))
	}
}

// TestScenarioRetiredToolAllowlistEntriesActuallyExist keeps the carve-outs
// honest: an entry whose file or sentence has since changed silently widens
// the exemption to nothing at all, and nobody notices until a real violation
// slips through the same path.
func TestScenarioRetiredToolAllowlistEntriesActuallyExist(t *testing.T) {
	var stale []string
	for path, lines := range scenarioRetiredToolAllowedMentions {
		raw, err := os.ReadFile(path)
		if err != nil {
			stale = append(stale, path+" (file does not exist)")
			continue
		}
		for _, allowed := range lines {
			if !strings.Contains(string(raw), allowed) {
				stale = append(stale, path+": "+allowed)
			}
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("scenarioRetiredToolAllowedMentions has %d entry/entries that no "+
			"longer match anything. Drop the entry, or update it to the line as it "+
			"reads now:\n%s", len(stale), strings.Join(stale, "\n"))
	}
}

func scenarioRetiredToolMentionAllowed(path, line string) bool {
	for _, allowed := range scenarioRetiredToolAllowedMentions[path] {
		if strings.Contains(line, allowed) {
			return true
		}
	}
	return false
}

package serf_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// pairs where a retired tool name may still appear. Two reasons are sanctioned,
// and both name the retired tool as retired:
//
//   - a dated "Verified live" results block reporting what a past run actually
//     called — rewriting that sentence to name today's tool would falsify the
//     record;
//   - a line whose subject IS the removal: it states the live name, then names
//     the retired one to say what became of it. The web-UI renderer registry
//     keys a descriptor on a tool NAME, and it still carries the retired name as
//     a defensive alias, so the parity checklist has to be able to say which
//     name is live and which is only an alias.
//
// An instruction to call the tool never qualifies under either.
var scenarioRetiredToolAllowedMentions = map[string][]string{
	"test/scenarios/job-watch-passive-observer-noop-filter.md": {
		"Parent still used `job_list` and `job_read_output` after successful",
	},
	"docs/web-ui/parity/parity-m4-transcript.md": {
		"The legacy renderer was keyed on `job_read_output`, unregistered since cf84923c6",
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

// jobControlContract is the evergreen job-control contract, the one document
// that is allowed to name a retired tool — in the entry whose whole job is
// recording the removal.
const jobControlContract = "docs/job-control.md"

// TestJobControlContractNamesARetiredToolOnlyAsRemoved is the doc half of kata
// fd8n. docs/job-control.md described job_read_output as live contract for five
// weeks after it was unregistered — a whole `### job_read_output` section, its
// paging rules, and ~50 references written as though a model could call it.
// Recording the removal is the doc's job; describing the tool as reachable is
// the rot. The section heading is the boundary between the two.
func TestJobControlContractNamesARetiredToolOnlyAsRemoved(t *testing.T) {
	raw, err := os.ReadFile(jobControlContract)
	if err != nil {
		t.Fatalf("reading %s: %v", jobControlContract, err)
	}
	var findings []string
	heading := ""
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			heading = strings.TrimSpace(line)
		}
		for name := range scenarioRetiredJobTools {
			if !strings.Contains(line, name) {
				continue
			}
			if heading == "### No `"+name+"`" {
				continue
			}
			findings = append(findings, jobControlContract+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("%s may name a retired job tool only under its own "+
			"\"### No `<tool>`\" removed-tools entry, which exists to record the "+
			"removal. Everywhere else the contract must describe the surface a "+
			"model can actually reach (kata fd8n):\n%s",
			jobControlContract, strings.Join(findings, "\n"))
	}
}

// retiredToolDocRoots are the two trees read as current instruction: docs/
// tells a contributor or an agent which tool to reach for, and tools/ holds the
// harnesses that drive real sessions plus the prompts they send. A retired tool
// name in either is the same failure the card corpus has — the reader reaches
// for a tool that is not there.
var retiredToolDocRoots = []string{"docs", "tools"}

// retiredToolLiveDocExtensions are the file kinds the sweep reads. The .yaml
// half is not decoration: a fluency probe's prompt is sent verbatim to a live
// model, so a probe naming a retired tool is a stronger version of the same
// bug — the model is told to make a call that returns `unknown tool`, and the
// probe's own expectations then grade the improvisation that follows.
var retiredToolLiveDocExtensions = map[string]bool{".md": true, ".yaml": true, ".yml": true}

// retiredToolHistoricalDocDirs are trees kept precisely to preserve a past
// state. docs/web-ui/history holds the pre-rewrite mockups and the plan written
// against them; rewriting those to today's tool names would falsify the record.
var retiredToolHistoricalDocDirs = []string{
	filepath.Join("docs", "web-ui", "history"),
}

// TestLiveDocsNeverTeachARetiredJobTool extends the card corpus's rule to the
// evergreen docs and the live fluency probes. Kata eb5m found five documents
// outside kata fd8n's scope still naming job_read_output — an architecture
// invariant, a fluency metric, a "use a subagent when" capability list, a
// probe-coverage table, and a web-UI renderer parity item — and the sweep it
// asked for turned up two more consequential ones the kata had not seen: the
// jobs.control_lifecycle probe told a live model to call the retired tool, and
// job_watch.observer_callback spent a forbidden_calls slot on it.
func TestLiveDocsNeverTeachARetiredJobTool(t *testing.T) {
	var findings []string
	for _, path := range retiredToolLiveDocFiles(t) {
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
		t.Fatalf("evergreen docs must describe the tool surface a model can "+
			"actually reach; a retired name here teaches a reader to build on or "+
			"call a tool that is not registered (kata eb5m):\n%s\n\nRetired tools "+
			"and what to teach instead:\n%s",
			strings.Join(findings, "\n"), strings.Join(guidance, "\n"))
	}
}

// modelFacingToolDefinitions declares every tool schema a provider can be
// handed. A constructor here is model-facing contract text: its Description is
// sent verbatim to the model as the tool's instructions.
const modelFacingToolDefinitions = "agent/internal/tool/definitions.go"

// TestToolDefinitionsNeverDeclareARetiredJobTool is the source half of the rule
// the doc and card sweeps enforce. A retired tool's constructor is invisible to
// every other guard: the registry check only sees what is registered, and the
// definition-enumerating tests keep the constructor compiling, so a full
// pre-removal contract can sit in the schema file describing paging knobs and
// wait semantics no model can reach.
func TestToolDefinitionsNeverDeclareARetiredJobTool(t *testing.T) {
	raw, err := os.ReadFile(modelFacingToolDefinitions)
	if err != nil {
		t.Fatalf("reading %s: %v", modelFacingToolDefinitions, err)
	}
	var findings []string
	for i, line := range strings.Split(string(raw), "\n") {
		for name := range scenarioRetiredJobTools {
			if !strings.Contains(line, name) {
				continue
			}
			findings = append(findings, modelFacingToolDefinitions+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		var guidance []string
		for name, replacement := range scenarioRetiredJobTools {
			guidance = append(guidance, name+" — "+replacement)
		}
		sort.Strings(guidance)
		t.Fatalf("%s must not name a job tool that is no longer registered — the "+
			"constructor and its Description read as live contract to anyone "+
			"editing the tool surface (kata 0fyd):\n%s\n\nRetired tools and what "+
			"to describe instead:\n%s",
			modelFacingToolDefinitions, strings.Join(findings, "\n"), strings.Join(guidance, "\n"))
	}
}

// retiredToolLiveDocFiles walks the evergreen files under retiredToolDocRoots.
// It skips two kinds of file for the same reason the mention allowlist skips a
// "Verified live" results block: a dated record says what was true on its date,
// and a history tree exists to hold what used to be. docs/job-control.md is
// skipped too — TestJobControlContractNamesARetiredToolOnlyAsRemoved owns it,
// with the sharper heading-scoped rule the removed-tools entry needs.
func retiredToolLiveDocFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, root := range retiredToolDocRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if isRetiredToolHistoricalDocDir(path) {
					return fs.SkipDir
				}
				return nil
			}
			if !retiredToolLiveDocExtensions[filepath.Ext(path)] || datedRecordFilename(d.Name()) {
				return nil
			}
			if filepath.ToSlash(path) == jobControlContract {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	if len(files) == 0 {
		t.Fatalf("no evergreen docs found under %v — the audit would pass vacuously", retiredToolDocRoots)
	}
	sort.Strings(files)
	return files
}

func isRetiredToolHistoricalDocDir(path string) bool {
	return slices.Contains(retiredToolHistoricalDocDirs, path)
}

// datedRecordFilename reports whether a markdown file name starts with a
// YYYY-MM-DD- stamp. Every spec, plan, research note, and run report in this
// repo carries one, and that stamp is what marks it as a record of a decision
// rather than the contract in force today.
func datedRecordFilename(name string) bool {
	const stamp = "2026-01-01-"
	if len(name) < len(stamp) {
		return false
	}
	for i, want := range "dddd-dd-dd-" {
		switch want {
		case 'd':
			if name[i] < '0' || name[i] > '9' {
				return false
			}
		default:
			if name[i] != '-' {
				return false
			}
		}
	}
	return true
}

func scenarioRetiredToolMentionAllowed(path, line string) bool {
	for _, allowed := range scenarioRetiredToolAllowedMentions[path] {
		if strings.Contains(line, allowed) {
			return true
		}
	}
	return false
}

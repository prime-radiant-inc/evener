package serf_test

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/transcript"
)

// TestScenarioTranscriptFixturesCarryTheFormatVersion keeps a hand-written
// transcript fixture readable by the binaries the card then runs against it.
// Every semantic reader validates the v2 boundary before it reads a single
// entry (transcript.ValidateHeader), so a fixture header without
// `format_version` fails the step that reads it with `unsupported transcript
// format` and exit 1 — no counting, no assertion, nothing to interpret.
// Kata 09ft: serf-doctor-forensics.md's step 4 could not run at all for
// exactly this reason, and the card looked fine because the fixture parses as
// JSON and every other step reads jobs.jsonl instead.
func TestScenarioTranscriptFixturesCarryTheFormatVersion(t *testing.T) {
	want := `"format_version":` + strconv.Itoa(transcript.FormatVersion)
	var findings []string
	matched := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, `"kind":"header"`) {
				continue
			}
			matched++
			if strings.Contains(line, want) {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	// A corpus audit is green either because the corpus is clean or because
	// its needle stopped matching anything; only a floor on matches tells the
	// two apart. The corpus carries hand-written header fixtures today, so
	// zero matches means the detector broke, not that the fixtures left.
	if matched == 0 {
		t.Fatalf("the header-line needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card's transcript-header fixture must carry %s — "+
			"serf's readers validate the v2 boundary before any entry, so a "+
			"header without it fails every transcript-reading step with "+
			"`unsupported transcript format` and the card's assertion never "+
			"runs (kata 09ft):\n%s", want, strings.Join(findings, "\n"))
	}
}

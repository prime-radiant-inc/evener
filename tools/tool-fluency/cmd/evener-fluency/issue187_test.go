package main

import (
	"flag"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestProbeMetricsComputedFromTranscripts pins #187's first sub-claim: the
// probe manifest's metrics block must not be parsed-and-dropped dead weight.
// The runner must actually compute the named metrics from the run's
// transcripts so a caller comparing prompting arms reads them out of
// result.json instead of hand-reading transcripts.
func TestProbeMetricsComputedFromTranscripts(t *testing.T) {
	stateDir := t.TempDir()
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("read_file", `{"file_path":"configpath/resolve.go"}`),
				fluencyToolCall("grep_files", `{"pattern":"ResolveProvidersConfig"}`),
			},
		}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				// Repeated read of the SAME file with the same arguments:
				// the churn signal the nbcf eval exists to measure.
				fluencyToolCall("read_file", `{"file_path":"configpath/resolve.go"}`),
				// First test run arrives after the investigation phase.
				fluencyToolCall("shell", `{"command":"go test ./configpath/..."}`),
			},
		}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				// First source edit lands AFTER the first test run.
				fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
			},
		}),
	}
	writeFluencyTranscript(t, stateDir, "02wMz5Txv1C3Hut0M8GCeB", turns)

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}

	if got.InvestigativeCallCount != 3 {
		t.Fatalf("investigative calls = %d, want 3 (2 in round 1 + 1 repeated read in round 2)", got.InvestigativeCallCount)
	}
	if got.RepeatedReadOrGrepCount != 1 {
		t.Fatalf("repeated read/grep = %d, want 1 (second read of the same file with identical arguments)", got.RepeatedReadOrGrepCount)
	}
	if got.FirstTestRunRound != 2 {
		t.Fatalf("first test run round = %d, want 2", got.FirstTestRunRound)
	}
	if got.FirstSourceEditRound != 3 {
		t.Fatalf("first source edit round = %d, want 3", got.FirstSourceEditRound)
	}
	if got.PrematureFixBeforeRedTest {
		t.Fatal("premature fix flagged, but the edit came after the first test run")
	}
}

// TestProbeMetricsPrematureFixFlagged covers the failure mode #187 names: a
// model that edits source before ever running a test. The flag must fire so
// "passed within N rounds" alone can no longer hide the churn.
func TestProbeMetricsPrematureFixFlagged(t *testing.T) {
	stateDir := t.TempDir()
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
			},
		}),
	}
	writeFluencyTranscript(t, stateDir, "02wMz5Txv1C3Hut0M8GCeB", turns)

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}
	if !got.PrematureFixBeforeRedTest {
		t.Fatal("premature fix = false, want true (source edit with no prior test run)")
	}
	if got.FirstTestRunRound != 0 {
		t.Fatalf("first test run round = %d, want 0 (no test ran)", got.FirstTestRunRound)
	}
}

// TestProbeMetricsProseMentionIsNotExecution pins the triage risk that a
// substring match can mistake prose for test execution: the text "go test"
// appearing in assistant prose must not count as a test-run round.
func TestProbeMetricsProseMentionIsNotExecution(t *testing.T) {
	stateDir := t.TempDir()
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "I will run go test ./configpath/... after the fix."},
				fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
			},
		}),
	}
	writeFluencyTranscript(t, stateDir, "02wMz5Txv1C3Hut0M8GCeB", turns)

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}
	if got.FirstTestRunRound != 0 {
		t.Fatalf("first test run round = %d, want 0 (prose mention is not execution)", got.FirstTestRunRound)
	}
	if !got.PrematureFixBeforeRedTest {
		t.Fatal("premature fix = false, want true (edit landed with no real test run)")
	}
}

// TestProbeMetricsWantedByManifest verifies the manifest-driven reporting
// half: only the metrics a probe names in its `metrics:` block are reported
// on the result, and an unknown name in a manifest is a load-time error
// rather than a silently dropped key (the original defect — a declared
// metric with no consumer).
func TestProbeMetricsWantedByManifest(t *testing.T) {
	dir := t.TempDir()
	metricsYAML := `schema: 1
id: metrics_probe
prompt: p
metrics:
  max_tool_calls: 40
  wants_investigative_call_count: true
  wants_repeated_read_or_grep_count: true
  wants_tool_round_of_first_test_run: true
  wants_tool_round_of_first_source_edit: true
  wants_premature_fix_before_red_test_flag: true
`
	mustWrite(t, filepath.Join(dir, "metrics.yaml"), metricsYAML)
	probes, err := loadProbes(dir, "all")
	if err != nil {
		t.Fatalf("loadProbes: %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %#v, want 1", probes)
	}
	probe := probes[0]

	stateDir := t.TempDir()
	writeFluencyTranscript(t, stateDir, "02wMz5Txv1C3Hut0M8GCeB", []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("read_file", `{"file_path":"a.go"}`),
				fluencyToolCall("shell", `{"command":"go test ./..."}`),
			},
		}),
	})

	res := probeResult{Probe: probe.ID, CanonicalToolCounts: map[string]int{}, ModelToolCounts: map[string]int{}}
	applyProbeMetrics(&res, probe, stateDir)

	if res.Metrics == nil {
		t.Fatal("result metrics = nil, want the manifest-named metrics computed and reported")
	}
	if _, ok := res.Metrics["investigative_call_count"]; !ok {
		t.Fatalf("metrics = %v, want investigative_call_count reported (manifest asked for it)", res.Metrics)
	}
	if _, ok := res.Metrics["repeated_read_or_grep_count"]; !ok {
		t.Fatalf("metrics = %v, want repeated_read_or_grep_count reported", res.Metrics)
	}
	if _, ok := res.Metrics["tool_round_of_first_test_run"]; !ok {
		t.Fatalf("metrics = %v, want tool_round_of_first_test_run reported", res.Metrics)
	}
	if _, ok := res.Metrics["tool_round_of_first_source_edit"]; !ok {
		t.Fatalf("metrics = %v, want tool_round_of_first_source_edit reported", res.Metrics)
	}
	if _, ok := res.Metrics["premature_fix_before_red_test_flag"]; !ok {
		t.Fatalf("metrics = %v, want premature_fix_before_red_test_flag reported", res.Metrics)
	}
	if res.Metrics["investigative_call_count"] != 1 {
		t.Fatalf("investigative_call_count = %v, want 1", res.Metrics["investigative_call_count"])
	}
	if res.Metrics["tool_round_of_first_test_run"] != 1 {
		t.Fatalf("tool_round_of_first_test_run = %v, want 1", res.Metrics["tool_round_of_first_test_run"])
	}
	// max_tool_calls is a manifest-local threshold, not a computed metric.
	if _, ok := res.Metrics["max_tool_calls"]; ok {
		t.Fatalf("metrics = %v, max_tool_calls is a threshold not a reportable metric", res.Metrics)
	}
}

// TestLoadProbesRejectsUnknownMetric pins the other half of the dead-field
// defect: an unknown metric name must fail at load time, not parse into a
// map nobody reads.
func TestLoadProbesRejectsUnknownMetric(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "bad.yaml"), "schema: 1\nid: bad\nprompt: p\nmetrics:\n  not_a_real_metric: true\n")
	if _, err := loadProbes(dir, "all"); err == nil || !strings.Contains(err.Error(), "not_a_real_metric") {
		t.Fatalf("loadProbes err = %v, want unknown-metric error naming the key", err)
	}
}

// TestMaxRoundsFlagIsAuthoritative pins #187's second sub-claim: the
// documented `--max-rounds` value must govern the runner, in both
// harnesses, instead of a hardcoded 80 that ignores the flag.
func TestMaxRoundsFlagIsAuthoritative(t *testing.T) {
	// The CLI harness must forward the configured round limit, not 80.
	cfg := runConfig{model: "openai/m", maxRounds: 40, reasoningEffort: "high"}
	probe := probeFile{Prompt: "p"}
	res := probeResult{WorkDir: "/work", StateDir: "/state"}

	args := cliProbeArgs(cfg, probe, res)
	value := ""
	for i, a := range args {
		if a == "--max-rounds" && i+1 < len(args) {
			value = args[i+1]
		}
	}
	if value != "40" {
		t.Fatalf("--max-rounds forwarded %q, want \"40\" (flag must be authoritative)", value)
	}

	// The live harness must derive its session config from the same flag,
	// not from a hardcoded 80: buildLiveSessionConfig is the seam both the
	// live path and this test go through.
	sessCfg := buildLiveSessionConfig(cfg, "/state")
	if sessCfg.MaxToolRoundsPerInput != 40 {
		t.Fatalf("live MaxToolRoundsPerInput = %d, want 40 (flag must be authoritative)", sessCfg.MaxToolRoundsPerInput)
	}

	// The default when the flag is unset must be the parsed flag default,
	// and both harnesses must agree with it.
	var parsed runConfig
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	defineRunFlags(fs, &parsed)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if parsed.maxRounds != defaultMaxRounds {
		t.Fatalf("default maxRounds = %d, want %d", parsed.maxRounds, defaultMaxRounds)
	}
	defCfg := runConfig{model: "openai/m", maxRounds: defaultMaxRounds, reasoningEffort: "high"}
	defArgs := cliProbeArgs(defCfg, probe, res)
	defValue := ""
	for i, a := range defArgs {
		if a == "--max-rounds" && i+1 < len(defArgs) {
			defValue = defArgs[i+1]
		}
	}
	if defValue != "80" {
		t.Fatalf("default --max-rounds forwarded %q, want \"80\" (prior hardcode as default)", defValue)
	}
	if got := buildLiveSessionConfig(defCfg, "/state").MaxToolRoundsPerInput; got != 80 {
		t.Fatalf("default live MaxToolRoundsPerInput = %d, want 80", got)
	}
}

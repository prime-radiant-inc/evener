package main

import (
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// rootMeta writes the session meta that marks sid as the root (non-subagent)
// session, so metrics scope to it as computeProbeMetrics expects.
func rootMeta(t *testing.T, stateDir, sid string) {
	t.Helper()
	meta := schema.SessionMeta{ID: sid, CreatedAt: time.Now().UTC()}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}

// subagentMeta marks sid as a delegate child session: metrics must skip it.
func subagentMeta(t *testing.T, stateDir, sid, parent string) {
	t.Helper()
	meta := schema.SessionMeta{ID: sid, ParentSessionID: parent, IsSubagent: true, CreatedAt: time.Now().UTC()}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}

// TestProbeMetricsComputedFromTranscripts pins #187's first sub-claim: the
// probe manifest's metrics block must not be parsed-and-dropped dead weight.
// The runner must actually compute the named metrics from the run's
// transcripts so a caller comparing prompting arms reads them out of
// result.json instead of hand-reading transcripts.
//
// Fixture names are ANTHROPIC-realistic (canonical tool names): shell,
// read_file, edit_file, communicate. The wire-name variants live in
// TestProbeMetricsProviderWireNames below.
func TestProbeMetricsComputedFromTranscripts(t *testing.T) {
	stateDir := t.TempDir()
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootMeta(t, stateDir, rootID)
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
				// Repeated read of the SAME file: the churn signal the
				// nbcf eval exists to measure.
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
	writeFluencyTranscript(t, stateDir, rootID, turns)

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}

	if got.InvestigativeCallCount != 3 {
		t.Fatalf("investigative calls = %d, want 3 (2 in round 1 + 1 repeated read in round 2)", got.InvestigativeCallCount)
	}
	if got.RepeatedReadOrGrepCount != 1 {
		t.Fatalf("repeated read/grep = %d, want 1 (second read of the same file)", got.RepeatedReadOrGrepCount)
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

// TestProbeMetricsProviderWireNames is the F1 regression: transcripts persist
// the provider-VISIBLE wire names, not canonical ones. An OpenAI run renames
// shell→exec_command and glob→find_files; a Gemini run renames shell→
// run_shell_command, grep→grep_search, list_dir→list_directory. Metrics must
// classify those identically to canonical names. Each subtest uses one
// provider's realistic name set — never a mix across providers.
func TestProbeMetricsProviderWireNames(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string // wire name as the provider emits it
		args       string
		toolCalls  int  // want investigative calls
		testRound  int  // want tool_round_of_first_test_run (0 = never)
		isShellFix bool // shell call driving an edit-with-test scenario
	}{
		// OpenAI responses API wire names.
		{name: "openai exec_command runs go test", toolName: "exec_command", args: `{"command":"go test ./..."}`, isShellFix: true},
		{name: "openai find_files is investigative", toolName: "find_files", args: `{"pattern":"*.go"}`, toolCalls: 1},
		{name: "openai grep_files is investigative", toolName: "grep_files", args: `{"pattern":"Resolve"}`, toolCalls: 1},
		// Gemini wire names.
		{name: "gemini run_shell_command runs go test", toolName: "run_shell_command", args: `{"command":"go test ./..."}`, isShellFix: true},
		{name: "gemini grep_search is investigative", toolName: "grep_search", args: `{"pattern":"Resolve"}`, toolCalls: 1},
		{name: "gemini list_directory is investigative", toolName: "list_directory", args: `{"path":"."}`, toolCalls: 1},
		// Canonical names (Anthropic/Kimi/GLM) still classify.
		{name: "anthropic shell runs go test", toolName: "shell", args: `{"command":"go test ./..."}`, isShellFix: true},
		{name: "anthropic read_file is investigative", toolName: "read_file", args: `{"file_path":"a.go"}`, toolCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			const rootID = "02wMz5Txv1C3Hut0M8GCeB"
			rootMeta(t, stateDir, rootID)
			var turns []schema.Turn
			if tt.isShellFix {
				// The named failure mode: edit BEFORE the first red test,
				// then the test run in a later round.
				turns = []schema.Turn{
					schema.NewTurn(schema.TurnAssistant, llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
						},
					}),
					schema.NewTurn(schema.TurnAssistant, llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							fluencyToolCall(tt.toolName, tt.args),
						},
					}),
				}
			} else {
				turns = []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						fluencyToolCall(tt.toolName, tt.args),
					},
				})}
			}
			writeFluencyTranscript(t, stateDir, rootID, turns)

			got, err := computeProbeMetrics(stateDir)
			if err != nil {
				t.Fatalf("computeProbeMetrics: %v", err)
			}
			if tt.isShellFix {
				if got.FirstTestRunRound != 2 {
					t.Fatalf("FirstTestRunRound = %d, want 2 (wire name %q must classify as the test run)", got.FirstTestRunRound, tt.toolName)
				}
				if !got.PrematureFixBeforeRedTest {
					t.Fatal("premature fix = false, want true (edit round 1 precedes the first test round 2)")
				}
			}
			if tt.toolCalls > 0 && got.InvestigativeCallCount != tt.toolCalls {
				t.Fatalf("investigative calls = %d, want %d (wire name %q must classify as investigative)", got.InvestigativeCallCount, tt.toolCalls, tt.toolName)
			}
		})
	}
}

// TestProbeMetricsPrematureFixFlagged covers both halves of the failure mode
// #187 names: a model that edits source before ever running a test, AND a
// model that runs its first test only AFTER an earlier edit (edit round 1,
// go test round 3). Both violate RED-then-fix discipline and must flag.
func TestProbeMetricsPrematureFixFlagged(t *testing.T) {
	t.Run("edit with no test at all", func(t *testing.T) {
		stateDir := t.TempDir()
		const rootID = "02wMz5Txv1C3Hut0M8GCeB"
		rootMeta(t, stateDir, rootID)
		turns := []schema.Turn{
			schema.NewTurn(schema.TurnAssistant, llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
				},
			}),
		}
		writeFluencyTranscript(t, stateDir, rootID, turns)

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
	})

	t.Run("edit round 1, go test round 3", func(t *testing.T) {
		stateDir := t.TempDir()
		const rootID = "02wMz5Txv1C3Hut0M8GCeB"
		rootMeta(t, stateDir, rootID)
		turns := []schema.Turn{
			schema.NewTurn(schema.TurnAssistant, llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
				},
			}),
			schema.NewTurn(schema.TurnAssistant, llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					fluencyToolCall("read_file", `{"file_path":"configpath/resolve.go"}`),
				},
			}),
			schema.NewTurn(schema.TurnAssistant, llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					fluencyToolCall("shell", `{"command":"go test ./configpath/..."}`),
				},
			}),
		}
		writeFluencyTranscript(t, stateDir, rootID, turns)

		got, err := computeProbeMetrics(stateDir)
		if err != nil {
			t.Fatalf("computeProbeMetrics: %v", err)
		}
		if !got.PrematureFixBeforeRedTest {
			t.Fatal("premature fix = false, want true (edit round 1 precedes the first test run round 3)")
		}
		if got.FirstTestRunRound != 3 {
			t.Fatalf("first test run round = %d, want 3", got.FirstTestRunRound)
		}
	})
}

// TestProbeMetricsProseMentionIsNotExecution pins the triage risk that a
// substring match can mistake prose for test execution: the text "go test"
// appearing in assistant prose must not count as a test-run round.
func TestProbeMetricsProseMentionIsNotExecution(t *testing.T) {
	stateDir := t.TempDir()
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootMeta(t, stateDir, rootID)
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "I will run go test ./configpath/... after the fix."},
				fluencyToolCall("edit_file", `{"file_path":"configpath/resolve.go"}`),
			},
		}),
	}
	writeFluencyTranscript(t, stateDir, rootID, turns)

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

// TestProbeMetricsIgnoresSubagentSessions pins the session scoping: delegate
// and observer children share the state dir but run their own rounds. Their
// tool calls must not fold into the root's round numbering.
func TestProbeMetricsIgnoresSubagentSessions(t *testing.T) {
	stateDir := t.TempDir()
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	const childID = "02wMz5Txv2enqVTitaig6F"
	rootMeta(t, stateDir, rootID)
	subagentMeta(t, stateDir, childID, rootID)

	// Root does one read and one test run.
	writeFluencyTranscript(t, stateDir, rootID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("read_file", `{"file_path":"a.go"}`),
				fluencyToolCall("shell", `{"command":"go test ./..."}`),
			},
		}),
	})
	// Child session does an edit that must NOT count toward the root's
	// metrics (or it would look like a premature fix).
	writeFluencyTranscript(t, stateDir, childID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("edit_file", `{"file_path":"a.go"}`),
			},
		}),
	})

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}
	if got.FirstSourceEditRound != 0 {
		t.Fatalf("first source edit round = %d, want 0 (child edit must not leak into root rounds)", got.FirstSourceEditRound)
	}
	if got.FirstTestRunRound != 1 {
		t.Fatalf("first test run round = %d, want 1", got.FirstTestRunRound)
	}
	if got.PrematureFixBeforeRedTest {
		t.Fatal("premature fix = true, want false (child's edit is not the root's)")
	}
}

// TestProbeMetricsRepeatKeyDiscriminates pins the F6 collision cases: the
// repeat key must distinguish tool shape and argument values, so a read_file
// of a.go is not a repeat of a list_dir of a.go, a grep of the same pattern
// in a different path is not a repeat, and paged reads at different offsets
// are not repeats.
func TestProbeMetricsRepeatKeyDiscriminates(t *testing.T) {
	stateDir := t.TempDir()
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootMeta(t, stateDir, rootID)
	writeFluencyTranscript(t, stateDir, rootID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				// read_file a.go (page 1)
				fluencyToolCall("read_file", `{"file_path":"a.go"}`),
				// list_dir a.go: different tool shape, same path — NOT a repeat.
				fluencyToolCall("list_dir", `{"path":"a.go"}`),
				// grep pattern X in path a: NOT a repeat of the reads.
				fluencyToolCall("grep_files", `{"pattern":"X","path":"a"}`),
				// grep pattern X in path b: different path — NOT a repeat.
				fluencyToolCall("grep_files", `{"pattern":"X","path":"b"}`),
				// read_file a.go offset 100: different page — NOT a repeat.
				fluencyToolCall("read_file", `{"file_path":"a.go","offset":100}`),
			},
		}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				// Same read as the first: THIS is a repeat.
				fluencyToolCall("read_file", `{"file_path":"a.go"}`),
				// Same grep as round 1's: THIS is a repeat.
				fluencyToolCall("grep_files", `{"pattern":"X","path":"a"}`),
			},
		}),
	})

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}
	if got.RepeatedReadOrGrepCount != 2 {
		t.Fatalf("repeated read/grep = %d, want 2 (same read_file and same grep only)", got.RepeatedReadOrGrepCount)
	}
}

// TestProbeMetricsCountsToolRounds pins the F5 round definition: a round is
// an assistant turn with at least one tool call. A text-only turn between
// two tool turns must not advance the count.
func TestProbeMetricsCountsToolRounds(t *testing.T) {
	stateDir := t.TempDir()
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootMeta(t, stateDir, rootID)
	writeFluencyTranscript(t, stateDir, rootID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("read_file", `{"file_path":"a.go"}`),
			},
		}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "thinking about it, no tools this turn"}},
		}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("shell", `{"command":"go test ./..."}`),
			},
		}),
	})

	got, err := computeProbeMetrics(stateDir)
	if err != nil {
		t.Fatalf("computeProbeMetrics: %v", err)
	}
	if got.FirstTestRunRound != 2 {
		t.Fatalf("first test run round = %d, want 2 (text-only turn must not count as a round)", got.FirstTestRunRound)
	}
}

// TestProbeMetricsTestCommandShapes pins the F9 acceptance: compound and
// env-prefixed commands count as test runs; plain `go vet` does not.
func TestProbeMetricsTestCommandShapes(t *testing.T) {
	for _, tt := range []struct {
		command string
		want    bool
	}{
		{"go test ./...", true},
		{"go test", true},
		{"cd pkg && go test ./...", true},
		{"FOO=1 go test ./...", true},
		{"cd pkg && go vet ./...", false},
		{"go vet ./...", false},
		{"echo hello", false},
	} {
		if got := isTestCommand(`{"command":` + quoteJSON(tt.command) + `}`); got != tt.want {
			t.Errorf("isTestCommand(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

// quoteJSON renders s as a JSON string literal (commands above contain no
// characters JSON escapes beyond the quotes this adds).
func quoteJSON(s string) string {
	return `"` + s + `"`
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
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootMeta(t, stateDir, rootID)
	writeFluencyTranscript(t, stateDir, rootID, []schema.Turn{
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
	for _, key := range []string{
		"investigative_call_count",
		"repeated_read_or_grep_count",
		"tool_round_of_first_test_run",
		"tool_round_of_first_source_edit",
		"premature_fix_before_red_test_flag",
	} {
		if _, ok := res.Metrics[key]; !ok {
			t.Fatalf("metrics = %v, want %s reported (manifest asked for it)", res.Metrics, key)
		}
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

// TestProbeMetricsSurfacesTranscriptError pins the F7 contract: a transcript
// that cannot be read must surface as a finding, never silently drop the
// metrics while the run could still report status "passed".
func TestProbeMetricsSurfacesTranscriptError(t *testing.T) {
	stateDir := t.TempDir()
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	rootMeta(t, stateDir, rootID)
	// Corrupt transcript: header present but entries unparseable.
	mustWrite(t, filepath.Join(stateDir, "sessions", rootID+".transcript.jsonl"), "not-json\n")

	res := probeResult{Probe: "p", CanonicalToolCounts: map[string]int{}, ModelToolCounts: map[string]int{}}
	probe := probeFile{Metrics: metricsSpec{WantsInvestigativeCallCount: true}}
	applyProbeMetrics(&res, probe, stateDir)

	if len(res.Findings) == 0 {
		t.Fatal("corrupt transcript produced no findings; metrics errors must surface, not vanish")
	}
	if res.Findings[0].Title != "phase metrics unavailable" {
		t.Fatalf("finding title = %q, want the metrics-unavailable finding", res.Findings[0].Title)
	}
}

// TestProbeMetricsEnforcesMaxToolCalls pins the F9 contract: the manifest's
// max_tool_calls threshold is enforced as a churn finding, not parsed and
// ignored.
func TestProbeMetricsEnforcesMaxToolCalls(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "p.yaml"), "schema: 1\nid: p\nprompt: x\nmetrics:\n  max_tool_calls: 2\n")
	probes, err := loadProbes(dir, "all")
	if err != nil {
		t.Fatalf("loadProbes: %v", err)
	}
	probe := probes[0]

	over := probeResult{Probe: "p", ModelToolCounts: map[string]int{"shell": 3}}
	applyProbeMetrics(&over, probe, t.TempDir())
	found := false
	for _, f := range over.Findings {
		if f.Title == "tool call budget exceeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("over-budget run produced no budget finding: %#v", over.Findings)
	}

	under := probeResult{Probe: "p", ModelToolCounts: map[string]int{"shell": 2}}
	applyProbeMetrics(&under, probe, t.TempDir())
	for _, f := range under.Findings {
		if f.Title == "tool call budget exceeded" {
			t.Fatalf("under-budget run produced a budget finding: %#v", under.Findings)
		}
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

// TestMaxRoundsRejectsNonPositive pins the F3 contract: 0 and negative
// values must be rejected up front with an explicit error, like
// --repetitions, rather than silently mapping to unlimited.
func TestMaxRoundsRejectsNonPositive(t *testing.T) {
	for _, bad := range []string{"0", "-1"} {
		err := runSuite([]string{"--max-rounds", bad})
		if err == nil {
			t.Fatalf("runSuite(--max-rounds %s) returned nil, want validation error", bad)
		}
		if !strings.Contains(err.Error(), "--max-rounds must be >= 1") {
			t.Fatalf("runSuite(--max-rounds %s) error = %v, want --max-rounds guidance", bad, err)
		}
	}
}

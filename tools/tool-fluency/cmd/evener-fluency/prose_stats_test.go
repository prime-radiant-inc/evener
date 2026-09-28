package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const (
	proseRootID  = "02wMz5Txv1C3Hut0M8GCeB"
	proseChildID = "02wMz5Txv2enqVTitaig6F"
)

func assistantTurn(parts ...llm.ContentPart) schema.Turn {
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: parts})
}

func textPart(s string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: s}
}

// writeProseRun writes a root session that says rootMessage through its
// result tool and a delegate that reports "Child report.".
func writeProseRun(t *testing.T, stateDir, rootMessage string) {
	t.Helper()
	rootMeta(t, stateDir, proseRootID)
	subagentMeta(t, stateDir, proseChildID, proseRootID)
	message, _ := json.Marshal(map[string]any{"message": rootMessage, "end_turn": true})
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(textPart("Looking at the tests."), fluencyToolCall("communicate", string(message))),
		// Arguments that were not valid JSON: the transcript keeps the raw text.
		assistantTurn(llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "call_bad", Name: "communicate", Arguments: json.RawMessage(`{}`), RawArguments: "{not json",
		}}),
	})
	writeFluencyTranscript(t, stateDir, proseChildID, []schema.Turn{
		assistantTurn(fluencyToolCall("communicate", `{"message":"Child report.","end_turn":true}`)),
	})
}

// TestExtractRunProseSplitsRootAndDelegateProse: the user sees only the
// root's result-tool messages; the whole run's prose adds visible assistant
// text and delegate reports; a call with unparseable arguments is skipped.
func TestExtractRunProseSplitsRootAndDelegateProse(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	writeProseRun(t, stateDir, "Fixed — see #12.")
	p, err := extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	if want := []string{"Fixed — see #12."}; !slices.Equal(p.ToUser, want) {
		t.Errorf("ToUser = %q, want %q", p.ToUser, want)
	}
	for _, want := range []string{"Looking at the tests.", "Fixed — see #12.", "Child report."} {
		if !slices.Contains(p.All, want) {
			t.Errorf("All = %q, missing %q", p.All, want)
		}
	}
	if len(p.All) != 3 {
		t.Errorf("All = %q, want exactly 3 pieces", p.All)
	}
}

// TestExtractRunProseCountsWhatTheAppShows: the user sees one message per
// call, the one the app shows: message, or output.message when message is
// empty. The whole run's prose still counts both, since output.message is
// writing the agent did.
func TestExtractRunProseCountsWhatTheAppShows(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootMeta(t, stateDir, proseRootID)
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(fluencyToolCall("communicate", `{"message":"Short note.","output":{"message":"Structured body."}}`)),
		assistantTurn(fluencyToolCall("communicate", `{"output":{"message":"Only in output."},"end_turn":true}`)),
	})
	p, err := extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	if want := []string{"Short note.", "Only in output."}; !slices.Equal(p.ToUser, want) {
		t.Errorf("ToUser = %q, want %q", p.ToUser, want)
	}
	if want := []string{"Short note.", "Structured body.", "Only in output."}; !slices.Equal(p.All, want) {
		t.Errorf("All = %q, want %q", p.All, want)
	}
}

func TestResultMessagesReadsBothFieldsOnce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args string
		want []string
	}{
		{`{"message":"a"}`, []string{"a"}},
		{`{"message":"a","output":{"message":"b"}}`, []string{"a", "b"}},
		{`{"message":"a","output":{"message":"a"}}`, []string{"a"}},
		{`{"message":"a","output":"plain"}`, []string{"a"}},
		{`{not json`, nil},
		// message absent, output given as a JSON-encoded string (the live
		// path's repairDefaultCommunicateEnvelope shape):
		// apptranscript.NormalizeCommunicateArguments promotes the string to
		// an object and copies its message up, the same normalization
		// shownMessage applies. Reading raw arguments here (skipping that
		// normalization) would decode output as a plain string and see no
		// message at all, though shownMessage/to_user would still find one.
		{`{"output":"{\"message\":\"Fixed the off-by-one.\"}"}`, []string{"Fixed the off-by-one."}},
	} {
		if got := resultMessages(c.args); !slices.Equal(got, c.want) {
			t.Errorf("resultMessages(%s) = %q, want %q", c.args, got, c.want)
		}
	}
}

// TestShownMessageAndResultMessagesAgreeOnStringValuedOutput: shownMessage
// (what "to_user" counts) and resultMessages (what "all" counts) must decode
// a call's arguments the same way, so a string-valued output cannot make
// to_user see a message that all misses.
func TestShownMessageAndResultMessagesAgreeOnStringValuedOutput(t *testing.T) {
	t.Parallel()
	const args = `{"output":"{\"message\":\"Fixed the off-by-one.\"}","end_turn":true}`
	const want = "Fixed the off-by-one."
	if got := shownMessage(args); got != want {
		t.Errorf("shownMessage(%s) = %q, want %q", args, got, want)
	}
	if got := resultMessages(args); !slices.Equal(got, []string{want}) {
		t.Errorf("resultMessages(%s) = %q, want %q", args, got, []string{want})
	}
}

// TestExtractRunProseHandlesStringValuedOutput: the same string-valued
// output shape, end to end through extractRunProse. Before decoding both
// "to_user" and "all" from one normalized decode, to_user counted the
// message (via shownMessage's normalization) while all missed it (via
// resultMessages' raw read).
func TestExtractRunProseHandlesStringValuedOutput(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootMeta(t, stateDir, proseRootID)
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(fluencyToolCall("communicate", `{"output":"{\"message\":\"Fixed the off-by-one.\"}","end_turn":true}`)),
	})
	p, err := extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	want := []string{"Fixed the off-by-one."}
	if !slices.Equal(p.ToUser, want) {
		t.Errorf("ToUser = %q, want %q", p.ToUser, want)
	}
	if !slices.Equal(p.All, want) {
		t.Errorf("All = %q, want %q", p.All, want)
	}
}

func TestSummarizeProseGroupsByLabelAndModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	writeProseRun(t, stateDir, "Done — see #12.")
	for rep, status := range map[int]string{1: "passed", 2: "failed"} {
		writeFluencyResult(t, dir, probeResult{Probe: "prose.bugfix-tally", Model: "m", Repetition: rep, Status: status, StateDir: stateDir})
	}
	stats, err := summarizeProse([]labeledDir{{Label: "baseline", Dir: dir}})
	if err != nil {
		t.Fatalf("summarizeProse: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want one row", stats)
	}
	s := stats[0]
	if s.Label != "baseline" || s.Model != "m" || s.Runs != 2 || s.Passed != 1 || s.Tasks != 1 || s.TasksAllPassed != 0 {
		t.Errorf("row = %+v, want baseline/m with 2 runs, 1 passed, 1 task, 0 all-passed", s)
	}
	if s.Messages != 2 || s.MedianMessageWords != 3 || s.ToUser.EmDashes != 2 || s.ToUser.OpaqueIDs != 2 {
		t.Errorf("row = %+v, want 2 messages of 3 words with 2 em dashes and 2 ids", s)
	}
	if s.All.Words <= s.ToUser.Words {
		t.Errorf("All.Words = %d, want more than ToUser.Words = %d", s.All.Words, s.ToUser.Words)
	}
}

// TestSummarizeProseCountsBlockedRuns: a run the gateway or the harness
// blocked says nothing about the prompt, so the table shows those runs apart
// from the passes and failures.
func TestSummarizeProseCountsBlockedRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	writeProseRun(t, stateDir, "Done.")
	for rep, status := range map[int]string{1: "passed", 2: "failed", 3: "blocked_infra"} {
		writeFluencyResult(t, dir, probeResult{Probe: "prose.smoke", Model: "m", Repetition: rep, Status: status, StateDir: stateDir})
	}
	stats, err := summarizeProse([]labeledDir{{Label: "v0", Dir: dir}})
	if err != nil {
		t.Fatalf("summarizeProse: %v", err)
	}
	if len(stats) != 1 || stats[0].Runs != 3 || stats[0].Passed != 1 || stats[0].Blocked != 1 {
		t.Fatalf("stats = %+v, want 3 runs with 1 passed and 1 blocked", stats)
	}
}

// TestSummarizeProseLeavesBlockedRunsOutOfTaskPasses: a blocked run says
// nothing about the prompt, so it neither fails its task nor makes a task
// count when no run of it was decided.
func TestSummarizeProseLeavesBlockedRunsOutOfTaskPasses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	writeProseRun(t, stateDir, "Done.")
	writeFluencyResult(t, dir, probeResult{Probe: "prose.smoke", Model: "m", Repetition: 1, Status: "passed", StateDir: stateDir})
	writeFluencyResult(t, dir, probeResult{Probe: "prose.smoke", Model: "m", Repetition: 2, Status: "blocked_infra", StateDir: stateDir})
	writeFluencyResult(t, dir, probeResult{Probe: "prose.git-greeting", Model: "m", Repetition: 1, Status: "blocked_infra", StateDir: stateDir})
	stats, err := summarizeProse([]labeledDir{{Label: "v0", Dir: dir}})
	if err != nil {
		t.Fatalf("summarizeProse: %v", err)
	}
	if len(stats) != 1 || stats[0].Tasks != 1 || stats[0].TasksAllPassed != 1 || stats[0].Blocked != 2 {
		t.Fatalf("stats = %+v, want 1 decided task that passed every decided run, and 2 blocked runs", stats)
	}
}

func TestParseLabeledNeedsBothParts(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "label", "=dir", "label="} {
		if _, _, err := parseLabeled(bad); err == nil {
			t.Errorf("parseLabeled(%q) succeeded, want an error", bad)
		}
	}
	if label, value, err := parseLabeled("v1-A=/tmp/x"); err != nil || label != "v1-A" || value != "/tmp/x" {
		t.Errorf("parseLabeled = %q, %q, %v", label, value, err)
	}
}

func TestProseSubcommandsRejectBadInput(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"prose-count"}, "prose-count FILE"},
		{[]string{"prose-stats"}, "at least one --results"},
		{[]string{"prose-stats", "--results", "nolabel"}, "want LABEL=VALUE"},
		{[]string{"prose-stats", "--results", "a=b", "--channel", "bogus"}, "--channel must be"},
	} {
		if err := run(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("run(%q) = %v, want an error containing %q", c.args, err, c.want)
		}
	}
}

// TestProseCommandsPrintTheirCounts runs both commands through run and reads
// what they print. It swaps os.Stdout, so it cannot run in parallel.
func TestProseCommandsPrintTheirCounts(t *testing.T) {
	dir := t.TempDir()
	section := filepath.Join(dir, "section.md")
	if err := os.WriteFile(section, []byte("It works — mostly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"prose-count", section}) })
	if want := section + "\twords=3 em_dashes=1 contrastive=0 bold_labels=0 headers=0 arrows=0 shouting=0 opaque_ids=0\n"; out != want {
		t.Errorf("prose-count printed %q, want %q", out, want)
	}

	results := filepath.Join(dir, "results")
	stateDir := filepath.Join(dir, "state")
	writeProseRun(t, stateDir, "Done — see #12.")
	writeFluencyResult(t, results, probeResult{Probe: "prose.smoke", Model: "m", Repetition: 1, Status: "passed", StateDir: stateDir})
	out = captureStdout(t, func() error { return run([]string{"prose-stats", "--results", "v1=" + results, "--json"}) })
	var stats []proseStats
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("prose-stats --json printed %q: %v", out, err)
	}
	if len(stats) != 1 || stats[0].Label != "v1" || stats[0].Messages != 1 {
		t.Errorf("prose-stats --json = %+v, want one v1 row with one message", stats)
	}
}

func TestRenderProseTableShowsTheChosenChannel(t *testing.T) {
	t.Parallel()
	stats := []proseStats{{
		Label: "v1", Model: "m", Runs: 1,
		ToUser: proseCounts{Words: 1000, EmDashes: 5},
		All:    proseCounts{Words: 2000, EmDashes: 30},
	}}
	for channel, want := range map[string]string{"to_user": "5.0", "all": "15.0"} {
		var buf bytes.Buffer
		if err := renderProseTable(&buf, stats, channel); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		// The data row's columns: label, model, runs, passed, blocked, tasks
		// all passed, messages per run, median message words, words, em
		// dashes per 1,000 words, and the other rates.
		row := strings.Fields(lines[len(lines)-1])
		if len(row) < 10 || row[9] != want {
			t.Errorf("channel %s: row = %q, want em dashes per 1k = %s", channel, row, want)
		}
	}
}

// writeFluencyResult writes res where a run under dir writes it:
// dir/<model>/<probe>/rep-NN/result.json.
func writeFluencyResult(t *testing.T, dir string, res probeResult) {
	t.Helper()
	if err := writeProbeResult(filepath.Join(dir, res.Model), res); err != nil {
		t.Fatal(err)
	}
}

// captureStdout returns what fn prints to os.Stdout and fails the test when
// fn returns an error.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	printed := make(chan []byte)
	go func() {
		data, _ := io.ReadAll(r)
		printed <- data
	}()
	stdout := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = stdout
	_ = w.Close()
	data := <-printed
	_ = r.Close()
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return string(data)
}

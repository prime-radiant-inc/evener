package msgrender

import (
	"strconv"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

func TestDiffBodyTintsAddLines(t *testing.T) {
	withTestColorProfile(t)
	diff := strings.Join([]string{
		"@@ -1,3 +1,3 @@",
		" context line",
		"-removed",
		"+added",
	}, "\n")
	got := diffBody(ToolArgs{}, diff, 60)

	// Lipgloss may combine fg+bg in one sequence (e.g. \x1b[38;2;...;48;2;...m)
	// or emit separate \x1b[48;...m sequences.
	hasBg := func(s string) bool {
		return strings.Contains(s, "\x1b[48") ||
			strings.Contains(s, ";48;") ||
			strings.Contains(s, ";48m")
	}

	// Locate the rendered lines by their visible text content.
	var addedLine, contextLine string
	for l := range strings.SplitSeq(got, "\n") {
		if strings.Contains(l, "+added") {
			addedLine = l
		}
		if strings.Contains(l, " context") {
			contextLine = l
		}
	}
	if addedLine == "" {
		t.Fatal("diffBody output missing '+added' line")
	}
	if !hasBg(addedLine) {
		t.Errorf("'+added' line should carry a background tint: %q", addedLine)
	}
	if contextLine == "" {
		t.Fatal("diffBody output missing context line")
	}
	if hasBg(contextLine) {
		t.Errorf("context line should NOT carry a background tint: %q", contextLine)
	}
}

func TestDiffBodyHandlesEmptyInput(t *testing.T) {
	got := diffBody(ToolArgs{}, "", 60)
	if got != "" {
		t.Errorf("diffBody on empty input should be empty; got %q", got)
	}
}

func TestFileBodyShowsFirstLines(t *testing.T) {
	lines := []string{}
	for i := 1; i <= 20; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	args := ToolArgs{"file_path": "x.txt"}
	got := fileBody(args, strings.Join(lines, "\n"), 60)
	if !strings.Contains(got, "line1") {
		t.Errorf("fileBody should contain first lines: %q", got)
	}
	if !strings.Contains(got, "show 15 more lines") && !strings.Contains(got, "more lines") {
		t.Errorf("fileBody should show truncation hint: %q", got)
	}
}

func TestTaskListBodyRendersPerTaskRows(t *testing.T) {
	// task_list output is JSON-shaped: array of {name, status}.
	output := `[
		{"name":"Understand task","status":"done"},
		{"name":"Do the work","status":"in_progress"},
		{"name":"Verify","status":"pending"}
	]`
	got := taskListBody(ToolArgs{}, output, 60)
	for _, want := range []string{"Understand task", "Do the work", "Verify", "[✓]", "[ ]"} {
		if !strings.Contains(got, want) {
			t.Errorf("taskListBody missing %q in: %q", want, got)
		}
	}
}

func TestDelegateBodyShowsSummaryWhenChildUnavailable(t *testing.T) {
	args := ToolArgs{"job_id": "job_01NONEXISTENT", "status": "completed"}
	got := delegateBody(args, "", 60)
	if !strings.Contains(got, "completed") || strings.Contains(got, "turns") {
		t.Errorf("delegateBody should show status without obsolete turn count: %q", got)
	}
}

func TestDelegateBodyHandlesNarrowWidth(t *testing.T) {
	args := ToolArgs{"job_id": "job_01ABCD"}
	got := delegateBody(args, "", 10)
	if got == "" {
		t.Errorf("delegateBody should return non-empty output at narrow width")
	}
}

func TestDelegateBodyKeepsSameOwnerJobSuffixesDistinct(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	first := delegateBody(ToolArgs{"job_id": "job_" + owner + "_000000000001"}, "", 80)
	second := delegateBody(ToolArgs{"job_id": "job_" + owner + "_000000000002"}, "", 80)
	if first == second || !strings.Contains(first, "000000000001") || !strings.Contains(second, "000000000002") {
		t.Fatalf("delegate labels = %q, %q; want distinct complete suffixes", first, second)
	}
}

func TestSubagentRunBodyKeepsSameOwnerJobSuffixesDistinct(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	first := SubagentRunBody(transcript.SubagentRunInfo{JobID: "job_" + owner + "_000000000001"}, 80)
	second := SubagentRunBody(transcript.SubagentRunInfo{JobID: "job_" + owner + "_000000000002"}, 80)
	if first == second || !strings.Contains(first, "000000000001") || !strings.Contains(second, "000000000002") {
		t.Fatalf("subagent labels = %q, %q; want distinct complete suffixes", first, second)
	}
}

func TestShellBodyHighlightsOutput(t *testing.T) {
	withTestColorProfile(t)
	got := ShellBody(ToolArgs{"command": "ls"}, "file1.go\nfile2.go\nfile3.go", 60)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "$ ls") {
		t.Errorf("ShellBody should include the styled command prompt: %q", got)
	}
	if !strings.Contains(got, "file1.go") {
		t.Errorf("ShellBody should include output content: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("ShellBody should emit ANSI escapes with TrueColor profile: %q", got)
	}
}

func TestShellBodyFormatsAndHighlightsCommand(t *testing.T) {
	withTestColorProfile(t)
	command := "cd /tmp && echo \"a;b\"; printf '%s\\n' \"$HOME\" | tee out"
	got := ShellBody(ToolArgs{"command": command}, "ok", 60)
	plain := ansiPattern.ReplaceAllString(got, "")
	want := "$ cd /tmp && \n  echo \"a;b\"; \n  printf '%s\\n' \"$HOME\" | \n  tee out\n"
	if !strings.Contains(plain, want) {
		t.Fatalf("ShellBody command layout = %q, want command block %q", plain, want)
	}
	if strings.Index(plain, "tee out") > strings.Index(plain, "ok") {
		t.Fatalf("command must precede output: %q", plain)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("formatted command should use Chroma styling: %q", got)
	}
}

func TestShellBodyRendersCommandOnlyWithoutTrimmingWhitespace(t *testing.T) {
	withTestColorProfile(t)
	command := "  echo \"$HOME\"  "
	got := ShellBody(ToolArgs{"command": command}, "", 60)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "$   echo \"$HOME\"  ") {
		t.Fatalf("ShellBody command-only text = %q, want leading and trailing command whitespace", plain)
	}
}

func TestShellBodyFallsBackToFormattedPlainCommand(t *testing.T) {
	withTestColorProfile(t)
	previousLexer := getChromaLexer
	getChromaLexer = func(string) chroma.Lexer { return nil }
	t.Cleanup(func() {
		getChromaLexer = previousLexer
	})

	got := ShellBody(ToolArgs{"command": "echo \"a;b\"; printf '%s' \"$HOME\""}, "", 60)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "$ echo \"a;b\"; \n  printf '%s' \"$HOME\"") {
		t.Fatalf("ShellBody fallback lost formatted command source: %q", plain)
	}
}

func TestWebSearchBodyPassesThroughUnchanged(t *testing.T) {
	output := strings.Join([]string{
		"Result 1 title — https://a.com",
		"Result 2 title — https://b.com",
	}, "\n")
	got := webSearchBody(ToolArgs{}, output, 60)
	if got != output {
		t.Errorf("webSearchBody should pass through unchanged:\n got  %q\n want %q", got, output)
	}
}

func TestRenderSubagentRailConsolidates(t *testing.T) {
	withTestColorProfile(t)
	runs := []transcript.SubagentRunInfo{
		{JobID: "j1", Task: "port webhook verification", Status: "running"},
		{JobID: "j2", Task: "trace retry callers", Status: "running"},
		{JobID: "j3", Task: "update docs", Status: "completed"},
		{JobID: "j4", Task: "audit deps", Status: "failed", Reason: "2 high CVEs"},
	}
	out := RenderSubagentRail(runs, 80)
	// Header tallies the workers.
	for _, want := range []string{"Subagents", "2 running", "1 done", "1 failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("header missing %q: %q", want, out)
		}
	}
	// Running entries are listed; the failure surfaces with its reason.
	for _, want := range []string{"port webhook verification", "trace retry callers", "audit deps", "2 high CVEs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %q", want, out)
		}
	}
	// The settled pile folds to a count — the done entry's name is NOT listed.
	if strings.Contains(out, "update docs") {
		t.Fatalf("done should fold to a count, not list 'update docs': %q", out)
	}
	if !strings.Contains(out, "✓ 1 done") {
		t.Fatalf("done count missing: %q", out)
	}
}

func TestSubagentRailClass_Exhausted(t *testing.T) {
	if got := subagentRailClass("exhausted"); got != "failed" {
		t.Fatalf("exhausted rail class = %q, want failed", got)
	}
	withTestColorProfile(t)
	run := transcript.SubagentRunInfo{
		JobID:  "job_exhausted",
		Task:   "bounded work",
		Status: "exhausted",
		Reason: "tool_round_budget_exhausted",
	}
	body := SubagentRunBody(run, 80)
	if !strings.Contains(body, "exhausted") {
		t.Fatalf("subagent body did not retain exhausted status: %q", body)
	}
	rail := RenderSubagentRail([]transcript.SubagentRunInfo{run}, 80)
	if !strings.Contains(rail, "1 failed") || !strings.Contains(rail, "tool_round_budget_exhausted") {
		t.Fatalf("exhausted rail did not retain terminal non-success reason: %q", rail)
	}
	if strings.Contains(rail, "running") || strings.Contains(rail, "1 done") {
		t.Fatalf("exhausted rail rendered as running or successful: %q", rail)
	}
}

func TestRenderSubagentRailNamesBackgroundShellByCommand(t *testing.T) {
	withTestColorProfile(t)
	out := RenderSubagentRail([]transcript.SubagentRunInfo{
		{JobID: "jbg", JobType: "shell", Background: true, Command: "go test ./... -count=1", Status: "running"},
	}, 80)
	if !strings.Contains(out, "go test ./... -count=1") {
		t.Fatalf("a background shell should be named by its command: %q", out)
	}
}

func TestRenderSubagentRailShowsLiveActivity(t *testing.T) {
	withTestColorProfile(t)
	out := RenderSubagentRail([]transcript.SubagentRunInfo{
		{JobID: "j1", Task: "port webhook", Status: "running", Activity: "shell: go test ./...", Steps: 3},
	}, 80)
	if !strings.Contains(out, "shell: go test ./...") || !strings.Contains(out, "· 3") {
		t.Fatalf("running row should show the live activity + step count: %q", out)
	}
}

func TestRenderSubagentRailKeepsTiedDoneRowVisible(t *testing.T) {
	withTestColorProfile(t)
	out := RenderSubagentRail([]transcript.SubagentRunInfo{
		{JobID: "j1", Task: "port webhook", Status: "completed", Headline: "go test passed · 4ad69c0"},
		{JobID: "j2", Task: "trace callers", Status: "completed"},
	}, 80)
	if !strings.Contains(out, "port webhook") || !strings.Contains(out, "go test passed · 4ad69c0") {
		t.Fatalf("a tied done row should stay visible with its headline: %q", out)
	}
	if strings.Contains(out, "trace callers") {
		t.Fatalf("a plain done row should fold, not list 'trace callers': %q", out)
	}
	if !strings.Contains(out, "✓ 2 done") {
		t.Fatalf("tally should count both done: %q", out)
	}
}

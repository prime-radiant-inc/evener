package msgrender

import (
	"strconv"
	"strings"
	"testing"
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
	// Each + line should carry a background tint; we can detect via ANSI bg escape.
	// Lipgloss may combine fg+bg in one sequence (e.g. \x1b[38;2;...;48;2;...m)
	// or emit separate \x1b[48;...m sequences.
	hasBg := strings.Contains(got, "\x1b[48") ||
		strings.Contains(got, ";48;") ||
		strings.Contains(got, ";48m")
	if !hasBg {
		t.Errorf("diffBody should set background on +/− lines: %q", got)
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

func TestSubagentBodyShowsSummaryWhenChildUnavailable(t *testing.T) {
	args := ToolArgs{"agent_id": "01NONEXISTENT", "turns_used": float64(3)}
	got := subagentBody(args, "", 60)
	if !strings.Contains(got, "turns") {
		t.Errorf("subagentBody should show turn summary: %q", got)
	}
}

func TestSubagentBodyHandlesNarrowWidth(t *testing.T) {
	args := ToolArgs{"agent_id": "01ABCD"}
	got := subagentBody(args, "", 10)
	if strings.Contains(got, "panic") {
		t.Errorf("subagentBody should not panic at narrow width")
	}
}

func TestShellBodyHighlightsOutput(t *testing.T) {
	got := ShellBody(ToolArgs{"command": "ls"}, "file1.go\nfile2.go\nfile3.go", 60)
	if got == "" {
		t.Errorf("ShellBody should return non-empty for non-empty output")
	}
}

func TestWebSearchBodyFormatsResults(t *testing.T) {
	output := strings.Join([]string{
		"Result 1 title — https://a.com",
		"Result 2 title — https://b.com",
	}, "\n")
	got := webSearchBody(ToolArgs{}, output, 60)
	if !strings.Contains(got, "Result 1") || !strings.Contains(got, "Result 2") {
		t.Errorf("webSearchBody should include results: %q", got)
	}
}

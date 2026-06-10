package toolsummary

import (
	"strings"
	"testing"
)

func TestSummarizeTool_Shell_WithPurpose(t *testing.T) {
	desc, _ := SummarizeTool("shell", `{"command":"ls -la /tmp","purpose":"list temp files"}`)
	if desc != "list temp files" {
		t.Errorf("got %q", desc)
	}
}

func TestSummarizeTool_Shell_WithLegacyDescription(t *testing.T) {
	desc, _ := SummarizeTool("shell", `{"command":"ls -la /tmp","description":"list temp files"}`)
	if desc != "list temp files" {
		t.Errorf("got %q", desc)
	}
}

func TestSummarizeTool_Shell_MultiLine(t *testing.T) {
	cmd := "line one\nline two\nline three"
	desc, detail := SummarizeTool("shell", `{"command":"`+strings.ReplaceAll(cmd, "\n", `\n`)+`"}`)
	if desc != "line one" {
		t.Errorf("desc: got %q, want first line", desc)
	}
	if !strings.Contains(detail, "line two") {
		t.Errorf("detail should contain full command, got %q", detail)
	}
}

func TestSummarizeTool_Shell_Short(t *testing.T) {
	desc, detail := SummarizeTool("shell", `{"command":"go build ./..."}`)
	if desc != "go build ./..." {
		t.Errorf("got %q", desc)
	}
	if detail != "" {
		t.Errorf("short single-line should have no detail, got %q", detail)
	}
}

func TestSummarizeTool_Shell_LongSingleLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	desc, detail := SummarizeTool("shell", `{"command":"`+long+`"}`)
	if !strings.HasSuffix(desc, "…") {
		t.Errorf("long desc should be truncated: %q", desc)
	}
	if !strings.Contains(detail, long) {
		t.Errorf("detail should contain full command")
	}
}

func TestSummarizeTool_ReadFile(t *testing.T) {
	tests := []struct {
		json string
		want string
	}{
		{`{"file_path":"/foo/bar/baz.go"}`, "…/bar/baz.go"},
		{`{"file_path":"/foo/bar/baz.go","offset":100}`, "…/bar/baz.go :100+"},
		{`{"file_path":"/foo/bar/baz.go","limit":50}`, "…/bar/baz.go :50"},
		{`{"file_path":"/foo/bar/baz.go","offset":100,"limit":50}`, "…/bar/baz.go :100+50"},
	}
	for _, tt := range tests {
		desc, _ := SummarizeTool("read_file", tt.json)
		if desc != tt.want {
			t.Errorf("read_file %s → %q, want %q", tt.json, desc, tt.want)
		}
	}
}

func TestSummarizeTool_WriteFile(t *testing.T) {
	desc, _ := SummarizeTool("write_file", `{"file_path":"/a/b/c.go","content":"line1\nline2\nline3"}`)
	if !strings.Contains(desc, "c.go") {
		t.Errorf("missing filename: %q", desc)
	}
	if !strings.Contains(desc, "3 lines") {
		t.Errorf("missing line count: %q", desc)
	}
}

func TestSummarizeTool_EditFile_DescAndDiff(t *testing.T) {
	desc, detail := SummarizeTool("edit_file", `{"file_path":"/a/b/c.go","old_string":"func foo()","new_string":"func bar()"}`)
	if !strings.Contains(desc, "c.go") {
		t.Errorf("desc missing filename: %q", desc)
	}
	// detail should be a unified diff
	if !strings.Contains(detail, "-func foo()") {
		t.Errorf("detail missing removed line: %q", detail)
	}
	if !strings.Contains(detail, "+func bar()") {
		t.Errorf("detail missing added line: %q", detail)
	}
}

func TestSummarizeTool_Glob(t *testing.T) {
	desc, _ := SummarizeTool("glob", `{"pattern":"**/*.go","path":"/some/dir"}`)
	if !strings.Contains(desc, "**/*.go") {
		t.Errorf("missing pattern: %q", desc)
	}
	if !strings.Contains(desc, "dir") {
		t.Errorf("missing path: %q", desc)
	}
}

func TestSummarizeTool_Grep(t *testing.T) {
	desc, _ := SummarizeTool("grep", `{"pattern":"func.*Error","path":"/src"}`)
	if !strings.Contains(desc, "func.*Error") {
		t.Errorf("missing pattern: %q", desc)
	}
}

func TestSummarizeTool_TaskList_View(t *testing.T) {
	desc, detail := SummarizeTool("task_list", `{"action":"view"}`)
	if desc != "view" {
		t.Errorf("got %q", desc)
	}
	if detail != "" {
		t.Errorf("view should have no detail: %q", detail)
	}
}

func TestSummarizeTool_TaskList_Append(t *testing.T) {
	desc, detail := SummarizeTool("task_list", `{"action":"append","tasks":[{"description":"do thing A","prompt":"do A fully"},{"description":"do thing B","prompt":"do B fully"}]}`)
	if desc != "append 2 tasks" {
		t.Errorf("desc: got %q", desc)
	}
	if !strings.Contains(detail, "do thing A") {
		t.Errorf("detail missing task A: %q", detail)
	}
	if !strings.Contains(detail, "do thing B") {
		t.Errorf("detail missing task B: %q", detail)
	}
}

func TestSummarizeTool_TaskList_Update(t *testing.T) {
	desc, detail := SummarizeTool("task_list", `{"action":"update","updates":[{"id":1,"status":"done"},{"id":2,"status":"inProgress"}]}`)
	if desc != "update 2 tasks" {
		t.Errorf("desc: got %q", desc)
	}
	if !strings.Contains(detail, "done") {
		t.Errorf("detail missing status: %q", detail)
	}
	if !strings.Contains(detail, "inProgress") {
		t.Errorf("detail missing status: %q", detail)
	}
	if !strings.Contains(detail, "✓") {
		t.Errorf("detail missing done icon: %q", detail)
	}
	if !strings.Contains(detail, "→") {
		t.Errorf("detail missing in_progress icon: %q", detail)
	}
}

func TestSummarizeTool_WebSearch(t *testing.T) {
	desc, _ := SummarizeTool("web_search", `{"query":"golang context timeout"}`)
	if !strings.Contains(desc, "golang context timeout") {
		t.Errorf("got %q", desc)
	}
}

func TestSummarizeTool_Delegate(t *testing.T) {
	desc, _ := SummarizeTool("delegate", `{"task":"Explore the codebase and find all usages of the Foo interface"}`)
	if !strings.Contains(desc, "Explore") {
		t.Errorf("got %q", desc)
	}
}

func TestSummarizeTool_Communicate(t *testing.T) {
	desc, _ := SummarizeTool("communicate", `{"message":"Building..."}`)
	if !strings.Contains(desc, "Building") {
		t.Errorf("got %q", desc)
	}
}

func TestSummarizeTool_Fallback(t *testing.T) {
	desc, _ := SummarizeTool("unknown_tool", `{"foo":"bar","num":42}`)
	if !strings.Contains(desc, "bar") && !strings.Contains(desc, "42") {
		t.Errorf("fallback should show short values: %q", desc)
	}
}

func TestSummarizeTool_Empty(t *testing.T) {
	desc, detail := SummarizeTool("shell", "")
	if desc != "" || detail != "" {
		t.Errorf("empty args should return empty: %q %q", desc, detail)
	}
}

func TestSummarizeTool_InvalidJSON(t *testing.T) {
	desc, _ := SummarizeTool("shell", "not json")
	if desc != "not json" {
		t.Errorf("invalid JSON should return raw: %q", desc)
	}
}

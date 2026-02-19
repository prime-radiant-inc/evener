package main

import (
	"strings"
	"testing"
)

func TestSummarizeArgs_Shell(t *testing.T) {
	got := summarizeArgs("shell", `{"command":"ls -la /tmp","description":"list temp files"}`)
	if got != "list temp files" {
		t.Errorf("got %q", got)
	}
	got = summarizeArgs("shell", `{"command":"go build ./..."}`)
	if got != "go build ./..." {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeArgs_ShellTruncates(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := summarizeArgs("shell", `{"command":"`+long+`"}`)
	if len(got) > 83 { // 80 chars + "…"
		t.Errorf("not truncated: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("missing ellipsis: %q", got)
	}
}

func TestSummarizeArgs_ShellMultiline(t *testing.T) {
	got := summarizeArgs("shell", `{"command":"line one\nline two"}`)
	if strings.Contains(got, "\n") {
		t.Errorf("should not contain newline: %q", got)
	}
	if !strings.Contains(got, "line one") {
		t.Errorf("should contain first line: %q", got)
	}
}

func TestSummarizeArgs_ReadFile(t *testing.T) {
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
		got := summarizeArgs("read_file", tt.json)
		if got != tt.want {
			t.Errorf("read_file %s → %q, want %q", tt.json, got, tt.want)
		}
	}
}

func TestSummarizeArgs_WriteFile(t *testing.T) {
	got := summarizeArgs("write_file", `{"file_path":"/a/b/c.go","content":"line1\nline2\nline3"}`)
	if !strings.Contains(got, "c.go") {
		t.Errorf("missing filename: %q", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("missing line count: %q", got)
	}
}

func TestSummarizeArgs_EditFile(t *testing.T) {
	got := summarizeArgs("edit_file", `{"file_path":"/a/b/c.go","old_string":"func foo()","new_string":"func bar()"}`)
	if !strings.Contains(got, "c.go") {
		t.Errorf("missing filename: %q", got)
	}
	if !strings.Contains(got, "func foo()") {
		t.Errorf("missing old_string preview: %q", got)
	}
}

func TestSummarizeArgs_Glob(t *testing.T) {
	got := summarizeArgs("glob", `{"pattern":"**/*.go","path":"/some/dir"}`)
	if !strings.Contains(got, "**/*.go") {
		t.Errorf("missing pattern: %q", got)
	}
	if !strings.Contains(got, "dir") {
		t.Errorf("missing path: %q", got)
	}
}

func TestSummarizeArgs_Grep(t *testing.T) {
	got := summarizeArgs("grep", `{"pattern":"func.*Error","path":"/src"}`)
	if !strings.Contains(got, "func.*Error") {
		t.Errorf("missing pattern: %q", got)
	}
}

func TestSummarizeArgs_TaskList(t *testing.T) {
	tests := []struct {
		json string
		want string
	}{
		{`{"action":"view"}`, "view"},
		{`{"action":"append","tasks":[{},{}]}`, "append 2 tasks"},
		{`{"action":"update","updates":[{},{},{}]}`, "update 3 tasks"},
	}
	for _, tt := range tests {
		got := summarizeArgs("task_list", tt.json)
		if got != tt.want {
			t.Errorf("task_list %s → %q, want %q", tt.json, got, tt.want)
		}
	}
}

func TestSummarizeArgs_WebSearch(t *testing.T) {
	got := summarizeArgs("web_search", `{"query":"golang context timeout"}`)
	if !strings.Contains(got, "golang context timeout") {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeArgs_SpawnAgent(t *testing.T) {
	got := summarizeArgs("spawn_agent", `{"task":"Explore the codebase and find all usages of the Foo interface"}`)
	if !strings.Contains(got, "Explore") {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeArgs_Communicate(t *testing.T) {
	got := summarizeArgs("communicate", `{"action":"status","message":"Building..."}`)
	if !strings.Contains(got, "status") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "Building") {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeArgs_Fallback(t *testing.T) {
	got := summarizeArgs("unknown_tool", `{"foo":"bar","num":42}`)
	if !strings.Contains(got, "bar") && !strings.Contains(got, "42") {
		t.Errorf("fallback should show short values: %q", got)
	}
}

func TestSummarizeArgs_Empty(t *testing.T) {
	got := summarizeArgs("shell", "")
	if got != "" {
		t.Errorf("empty args should return empty: %q", got)
	}
}

func TestSummarizeArgs_InvalidJSON(t *testing.T) {
	got := summarizeArgs("shell", "not json")
	if got != "not json" {
		t.Errorf("invalid JSON should return raw: %q", got)
	}
}

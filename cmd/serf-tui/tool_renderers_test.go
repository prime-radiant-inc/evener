package main

import (
	"strings"
	"testing"
	"time"
)

func TestRendererRegistryHasReadFile(t *testing.T) {
	r, ok := lookupToolRenderer("read_file")
	if !ok {
		t.Fatalf("no renderer for read_file")
	}
	args := toolArgsFromJSON(`{"file_path":"handlers/signup.go"}`)
	if r.Verb(args) != "read" {
		t.Errorf("read_file verb = %q; want 'read'", r.Verb(args))
	}
	if r.Target(args) != "handlers/signup.go" {
		t.Errorf("read_file target = %q; want 'handlers/signup.go'", r.Target(args))
	}
}

func TestReadFileResultLineCount(t *testing.T) {
	r, ok := lookupToolRenderer("read_file")
	if !ok {
		t.Fatalf("no renderer for read_file")
	}
	args := ToolArgs{}
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"empty", "", "0 lines"},
		{"single line no newline", "hello", "1 line"},
		{"single line with trailing newline", "hello\n", "1 line"},
		{"two lines", "hello\nworld", "2 lines"},
		{"two lines with trailing newline", "hello\nworld\n", "2 lines"},
		{"error", "", "error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errStr := ""
			if tc.want == "error" {
				errStr = "read failed"
			}
			got := r.Result(args, tc.output, errStr, 0)
			if got != tc.want {
				t.Errorf("read_file Result(%q) = %q; want %q", tc.output, got, tc.want)
			}
		})
	}
}

func TestRendererRegistryFallback(t *testing.T) {
	// Unknown tool gets fallback renderer.
	r, ok := lookupToolRenderer("totally_unknown_tool")
	if !ok {
		t.Fatalf("no fallback renderer")
	}
	args := toolArgsFromJSON(`{"x":"y"}`)
	if r.Verb(args) == "" {
		t.Errorf("fallback verb is empty")
	}
}

func TestRendererRegistryMCPFallback(t *testing.T) {
	r, ok := lookupToolRenderer("linear__search")
	if !ok {
		t.Fatalf("no MCP fallback renderer")
	}
	args := toolArgsFromJSON(`{"query":"foo"}`)
	if r.Verb(args) != "linear" {
		t.Errorf("MCP verb = %q; want 'linear'", r.Verb(args))
	}
	_ = time.Second // satisfy unused import on dev iteration
}

func TestShellRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("shell")
	args := toolArgsFromJSON(`{"command":"ls -la","purpose":"List home dir"}`)
	if r.Verb(args) != "shell" {
		t.Errorf("shell verb = %q", r.Verb(args))
	}
	if !strings.Contains(r.Target(args), "ls") {
		t.Errorf("shell target should contain command: %q", r.Target(args))
	}
}

func TestGrepRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("grep")
	args := toolArgsFromJSON(`{"pattern":"foo","path":"src/"}`)
	if r.Verb(args) != "grep" {
		t.Errorf("grep verb = %q", r.Verb(args))
	}
	if !strings.Contains(r.Target(args), "foo") {
		t.Errorf("grep target should contain pattern: %q", r.Target(args))
	}
	result := r.Result(args, "match1\nmatch2\nmatch3", "", 0)
	if !strings.Contains(result, "3") {
		t.Errorf("grep result should count hits: %q", result)
	}
}

func TestGlobRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("glob")
	args := toolArgsFromJSON(`{"pattern":"**/*.go"}`)
	if r.Verb(args) != "glob" {
		t.Errorf("glob verb = %q", r.Verb(args))
	}
	// Result counts entries, not newlines.
	tests := []struct {
		output string
		want   string
	}{
		{"", "0 matches"},
		{"a.go", "1 matches"},
		{"a.go\nb.go", "2 matches"},
		{"a.go\nb.go\nc.go", "3 matches"},
	}
	for _, tc := range tests {
		got := r.Result(args, tc.output, "", 0)
		if got != tc.want {
			t.Errorf("glob Result(%q) = %q; want %q", tc.output, got, tc.want)
		}
	}
}

func TestListDirRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("list_dir")
	args := toolArgsFromJSON(`{"path":"/tmp"}`)
	if r.Verb(args) != "ls" {
		t.Errorf("list_dir verb = %q", r.Verb(args))
	}
	if r.Target(args) != "/tmp" {
		t.Errorf("list_dir target = %q", r.Target(args))
	}
	// Result parses JSON array of DirEntry.
	tests := []struct {
		output string
		want   string
	}{
		{`[]`, "0 entries"},
		{`[{"name":"file.txt","is_dir":false}]`, "1 entries"},
		{`[{"name":"a","is_dir":true},{"name":"b","is_dir":false},{"name":"c","is_dir":false}]`, "3 entries"},
	}
	for _, tc := range tests {
		got := r.Result(args, tc.output, "", 0)
		if got != tc.want {
			t.Errorf("list_dir Result(%q) = %q; want %q", tc.output, got, tc.want)
		}
	}
}

func TestEditFileRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("edit_file")
	args := toolArgsFromJSON(`{"file_path":"src/main.go"}`)
	if r.Verb(args) != "edit" {
		t.Errorf("edit_file verb = %q", r.Verb(args))
	}
	if !r.ExpandedByDefault {
		t.Errorf("edit_file should default to expanded")
	}
}

func TestWriteFileRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("write_file")
	args := toolArgsFromJSON(`{"file_path":"src/new.go"}`)
	if r.Verb(args) != "write" {
		t.Errorf("write_file verb = %q", r.Verb(args))
	}
}

func TestApplyPatchRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("apply_patch")
	args := toolArgsFromJSON(`{"patch":"--- a/x\n+++ b/x\n@@ ..."}`)
	if r.Verb(args) != "patch" {
		t.Errorf("apply_patch verb = %q", r.Verb(args))
	}
}

func TestApplyPatchRendererUnifiedDiff(t *testing.T) {
	r, _ := lookupToolRenderer("apply_patch")
	args := ToolArgs{"patch": "--- a/src/main.go\n+++ b/src/main.go\n@@ -1,3 +1,4 @@\n+// new\n"}
	if got := r.Target(args); got != "src/main.go" {
		t.Errorf("apply_patch unified-diff target = %q; want src/main.go", got)
	}
}

func TestApplyPatchRendererV4a(t *testing.T) {
	r, _ := lookupToolRenderer("apply_patch")
	tests := []struct {
		name   string
		patch  string
		want   string
	}{
		{
			name:  "Update File",
			patch: "*** Begin Patch\n*** Update File: src/foo.go\n@@ -1,3 +1,4 @@\n+// new\n*** End Patch",
			want:  "src/foo.go",
		},
		{
			name:  "Add File",
			patch: "*** Begin Patch\n*** Add File: cmd/bar/main.go\n+package main\n*** End Patch",
			want:  "cmd/bar/main.go",
		},
		{
			name:  "Delete File",
			patch: "*** Begin Patch\n*** Delete File: old/legacy.go\n*** End Patch",
			want:  "old/legacy.go",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := ToolArgs{"patch": tc.patch}
			if got := r.Target(args); got != tc.want {
				t.Errorf("apply_patch v4a target = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestWebFetchRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("web_fetch")
	args := toolArgsFromJSON(`{"url":"https://example.com"}`)
	if r.Verb(args) != "fetch" || r.Target(args) != "https://example.com" {
		t.Errorf("web_fetch wrong: verb=%q target=%q", r.Verb(args), r.Target(args))
	}
}

func TestWebSearchRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("web_search")
	args := toolArgsFromJSON(`{"query":"foo bar"}`)
	if r.Verb(args) != "search" || r.Target(args) != "foo bar" {
		t.Errorf("web_search wrong: verb=%q target=%q", r.Verb(args), r.Target(args))
	}
}

func TestSpawnAgentRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("spawn_agent")
	args := toolArgsFromJSON(`{"task":"do something useful"}`)
	if r.Verb(args) != "spawn" {
		t.Errorf("spawn_agent verb = %q", r.Verb(args))
	}
	if !strings.Contains(r.Target(args), "do something") {
		t.Errorf("spawn_agent target should include task: %q", r.Target(args))
	}
}

func TestResumeAgentRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("resume_agent")
	args := toolArgsFromJSON(`{"agent_id":"01ABCD"}`)
	if r.Verb(args) != "resume" {
		t.Errorf("resume_agent verb = %q", r.Verb(args))
	}
}

func TestWaitRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("wait")
	if r.Verb(toolArgsFromJSON(`{}`)) != "wait" {
		t.Errorf("wait verb wrong")
	}
}

func TestCloseAgentRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("close_agent")
	if r.Verb(toolArgsFromJSON(`{}`)) != "close" {
		t.Errorf("close_agent verb wrong")
	}
}

func TestTaskListRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("task_list")
	args := toolArgsFromJSON(`{}`)
	if r.Verb(args) != "tasks" {
		t.Errorf("task_list verb = %q", r.Verb(args))
	}
	if !r.ExpandedByDefault {
		t.Errorf("task_list should default expanded")
	}
}

func TestUseSkillRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("use_skill")
	// Primary key: skill_name (current tool schema).
	args := toolArgsFromJSON(`{"skill_name":"brainstorming"}`)
	if r.Verb(args) != "skill" {
		t.Errorf("use_skill verb = %q", r.Verb(args))
	}
	if r.Target(args) != "brainstorming" {
		t.Errorf("use_skill target (skill_name) = %q", r.Target(args))
	}
	// Legacy fallback: name key.
	argsLegacy := toolArgsFromJSON(`{"name":"debugging"}`)
	if r.Target(argsLegacy) != "debugging" {
		t.Errorf("use_skill target (legacy name) = %q", r.Target(argsLegacy))
	}
}

func TestMCPFallbackTargetIncludesFirstArgs(t *testing.T) {
	r, _ := lookupToolRenderer("linear__search")
	args := toolArgsFromJSON(`{"query":"oncall","filter":"open"}`)
	target := r.Target(args)
	if !strings.Contains(target, "search") {
		t.Errorf("MCP target should include operation: %q", target)
	}
	if !strings.Contains(target, "oncall") {
		t.Errorf("MCP target should include first string arg: %q", target)
	}
}

func TestUnknownToolHasJSONBody(t *testing.T) {
	r, _ := lookupToolRenderer("unknown_tool_xyz")
	if r.Body == nil {
		t.Errorf("unknown tool renderer should have jsonBody")
	}
}

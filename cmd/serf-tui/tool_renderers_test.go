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
	args := toolArgsFromJSON(`{"name":"brainstorming"}`)
	if r.Verb(args) != "skill" {
		t.Errorf("use_skill verb = %q", r.Verb(args))
	}
	if r.Target(args) != "brainstorming" {
		t.Errorf("use_skill target = %q", r.Target(args))
	}
}

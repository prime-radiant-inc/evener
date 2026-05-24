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

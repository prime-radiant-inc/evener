package main

import (
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

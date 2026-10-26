package main

import (
	"bytes"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
)

// TestPrintServeSandboxLineNonEmpty covers the non-empty path.
func TestPrintServeSandboxLineNonEmpty(t *testing.T) {
	var buf bytes.Buffer
	printServeSandboxLine(&buf, "sandbox: enabled")
	if buf.String() != "sandbox: enabled\n" {
		t.Fatalf("output = %q, want 'sandbox: enabled\\n'", buf.String())
	}
}

// TestPrintServeSandboxLineEmpty covers the empty path.
func TestPrintServeSandboxLineEmpty(t *testing.T) {
	var buf bytes.Buffer
	printServeSandboxLine(&buf, "")
	if buf.String() != "" {
		t.Fatalf("output = %q, want empty", buf.String())
	}
}

// TestReportServeResumeOverridden covers the overridden path.
func TestReportServeResumeOverridden(t *testing.T) {
	var buf bytes.Buffer
	meta := schema.SessionMeta{ID: "session-1", TurnCount: 5}
	model := cmdutil.ModelRef{Provider: "openai", Model: "gpt-4"}
	reportServeResume(&buf, meta, model, "oldprov", "oldmodel", true)
	if !bytes.Contains(buf.Bytes(), []byte("resumed with model override")) {
		t.Fatalf("output should contain override message: %q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("gpt-4")) {
		t.Fatalf("output should contain model name: %q", buf.String())
	}
}

// TestReportServeResumeNotOverridden covers the non-overridden path.
func TestReportServeResumeNotOverridden(t *testing.T) {
	var buf bytes.Buffer
	meta := schema.SessionMeta{ID: "session-2", TurnCount: 10}
	model := cmdutil.ModelRef{Provider: "openai", Model: "gpt-4"}
	reportServeResume(&buf, meta, model, "", "", false)
	if !bytes.Contains(buf.Bytes(), []byte("resumed (10 turns)")) {
		t.Fatalf("output should contain turn count: %q", buf.String())
	}
}

// TestMapServePendingEscalations covers the happy path.
func TestMapServePendingEscalations(t *testing.T) {
	data := []events.SandboxEscalationRequestedData{
		{EscalationID: "esc-1", Mode: "write", Tool: "edit_file", Kind: "path", DeniedPath: "/etc/passwd"},
		{EscalationID: "esc-2", Mode: "exec", Tool: "exec_command", Kind: "command", Command: "rm -rf /", PartiallyRan: true},
	}
	result := mapServePendingEscalations(data)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	if result[0].EscalationID != "esc-1" || result[0].Tool != "edit_file" {
		t.Fatalf("result[0] = %+v", result[0])
	}
	if result[1].PartiallyRan != true {
		t.Fatalf("result[1].PartiallyRan = %v, want true", result[1].PartiallyRan)
	}
}

// TestMapServePendingEscalationsEmpty covers the empty-input path.
func TestMapServePendingEscalationsEmpty(t *testing.T) {
	result := mapServePendingEscalations(nil)
	if len(result) != 0 {
		t.Fatalf("len = %d, want 0", len(result))
	}
}

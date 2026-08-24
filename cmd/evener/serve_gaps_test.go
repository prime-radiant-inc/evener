package main

import (
	"bytes"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
)

// TestCloneServeBoolNonNil covers the non-nil path in cloneServeBool.
func TestCloneServeBoolNonNil(t *testing.T) {
	v := true
	clone := cloneServeBool(&v)
	if clone == nil || *clone != true {
		t.Fatalf("cloneServeBool(&true) = %v, want &true", clone)
	}
	// Mutating the clone should not affect the original.
	*clone = false
	if v != true {
		t.Fatalf("original was mutated: v = %v, want true", v)
	}
}

// TestCloneServeBoolNil covers the nil path.
func TestCloneServeBoolNil(t *testing.T) {
	if clone := cloneServeBool(nil); clone != nil {
		t.Fatalf("cloneServeBool(nil) = %v, want nil", clone)
	}
}

// TestCloneServeInt64NonNil covers the non-nil path in cloneServeInt64.
func TestCloneServeInt64NonNil(t *testing.T) {
	v := int64(42)
	clone := cloneServeInt64(&v)
	if clone == nil || *clone != 42 {
		t.Fatalf("cloneServeInt64(&42) = %v, want &42", clone)
	}
	*clone = 99
	if v != 42 {
		t.Fatalf("original was mutated: v = %d, want 42", v)
	}
}

// TestCloneServeInt64Nil covers the nil path.
func TestCloneServeInt64Nil(t *testing.T) {
	if clone := cloneServeInt64(nil); clone != nil {
		t.Fatalf("cloneServeInt64(nil) = %v, want nil", clone)
	}
}

// TestCloneServeUsageNonNil covers the non-nil path in cloneServeUsage.
func TestCloneServeUsageNonNil(t *testing.T) {
	v := appwire.EvenerUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	clone := cloneServeUsage(&v)
	if clone == nil || clone.InputTokens != 100 || clone.OutputTokens != 50 || clone.TotalTokens != 150 {
		t.Fatalf("cloneServeUsage = %+v, want %+v", clone, v)
	}
	clone.InputTokens = 999
	if v.InputTokens != 100 {
		t.Fatalf("original was mutated: InputTokens = %d, want 100", v.InputTokens)
	}
}

// TestCloneServeUsageNil covers the nil path.
func TestCloneServeUsageNil(t *testing.T) {
	if clone := cloneServeUsage(nil); clone != nil {
		t.Fatalf("cloneServeUsage(nil) = %v, want nil", clone)
	}
}

// TestCloneServeWorktreeNonNil covers the non-nil path in cloneServeWorktree.
func TestCloneServeWorktreeNonNil(t *testing.T) {
	v := appwire.JobActivityWorktree{Branch: "test-branch", Dirty: true}
	clone := cloneServeWorktree(&v)
	if clone == nil || clone.Branch != "test-branch" || clone.Dirty != true {
		t.Fatalf("cloneServeWorktree = %+v, want %+v", clone, v)
	}
	clone.Branch = "mutated"
	if v.Branch != "test-branch" {
		t.Fatalf("original was mutated: Branch = %q, want %q", v.Branch, "test-branch")
	}
}

// TestCloneServeWorktreeNil covers the nil path.
func TestCloneServeWorktreeNil(t *testing.T) {
	if clone := cloneServeWorktree(nil); clone != nil {
		t.Fatalf("cloneServeWorktree(nil) = %v, want nil", clone)
	}
}

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

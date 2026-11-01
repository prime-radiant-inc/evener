package agent

import (
	"strings"
	"testing"
)

// TestMarshalDelegateCreateResult_ProgressiveTruncation covers the
// progressive field-dropping branches (lines 209-226) by using a maxChars
// small enough to force each truncation step.
func TestMarshalDelegateCreateResult_ProgressiveTruncation(t *testing.T) {
	t.Parallel()
	out := delegateCreateResult{
		DelegateID:     "dlg_123",
		ChildSessionID: "sess_123",
		Type:           "general",
		Status:         "created",
		Model:          strings.Repeat("m", 100),
		Sandbox:        &delegateSandboxReport{Mode: strings.Repeat("s", 100)},
		Worktree:       &delegateWorktreeReport{Branch: strings.Repeat("w", 100)},
		Warnings:       []string{strings.Repeat("z", 100)},
		StartError:     strings.Repeat("e", 100),
	}

	t.Run("fits without truncation", func(t *testing.T) {
		got, err := marshalDelegateCreateResult(out, 10000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "dlg_123") {
			t.Fatalf("expected delegate id in output: %s", got)
		}
	})

	t.Run("truncation drops StartError first", func(t *testing.T) {
		// Use a maxChars just large enough to fit once StartError is dropped.
		full, _ := marshalDelegateCreateResult(out, 10000)
		withoutError := out
		withoutError.StartError = ""
		withoutErr, _ := marshalDelegateCreateResult(withoutError, 10000)
		// Pick maxChars between the two sizes to force StartError to be dropped.
		maxChars := (len(withoutErr) + len(full)) / 2
		if maxChars >= len(full) {
			maxChars = len(withoutErr) + 1
		}
		got, err := marshalDelegateCreateResult(out, maxChars)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, strings.Repeat("e", 10)) {
			t.Fatalf("expected StartError to be dropped: %s", got)
		}
	})

	t.Run("truncation drops Warnings second", func(t *testing.T) {
		withoutError := out
		withoutError.StartError = ""
		withoutWarnings := withoutError
		withoutWarnings.Warnings = nil
		withoutWarn, _ := marshalDelegateCreateResult(withoutWarnings, 10000)
		withErr, _ := marshalDelegateCreateResult(withoutError, 10000)
		maxChars := (len(withoutWarn) + len(withErr)) / 2
		if maxChars >= len(withErr) {
			maxChars = len(withoutWarn) + 1
		}
		got, err := marshalDelegateCreateResult(out, maxChars)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, strings.Repeat("z", 10)) {
			t.Fatalf("expected Warnings to be dropped: %s", got)
		}
	})

	t.Run("truncation drops Worktree third", func(t *testing.T) {
		withoutError := out
		withoutError.StartError = ""
		withoutError.Warnings = nil
		withoutWorktree := withoutError
		withoutWorktree.Worktree = nil
		withoutWT, _ := marshalDelegateCreateResult(withoutWorktree, 10000)
		withWarn, _ := marshalDelegateCreateResult(withoutError, 10000)
		maxChars := (len(withoutWT) + len(withWarn)) / 2
		if maxChars >= len(withWarn) {
			maxChars = len(withoutWT) + 1
		}
		got, err := marshalDelegateCreateResult(out, maxChars)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, strings.Repeat("w", 10)) {
			t.Fatalf("expected Worktree to be dropped: %s", got)
		}
	})

	t.Run("truncation drops Model fourth", func(t *testing.T) {
		withoutError := out
		withoutError.StartError = ""
		withoutError.Warnings = nil
		withoutError.Worktree = nil
		withoutModel := withoutError
		withoutModel.Model = ""
		withoutM, _ := marshalDelegateCreateResult(withoutModel, 10000)
		withWT, _ := marshalDelegateCreateResult(withoutError, 10000)
		maxChars := (len(withoutM) + len(withWT)) / 2
		if maxChars >= len(withWT) {
			maxChars = len(withoutM) + 1
		}
		got, err := marshalDelegateCreateResult(out, maxChars)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, strings.Repeat("m", 10)) {
			t.Fatalf("expected Model to be dropped: %s", got)
		}
	})

	t.Run("truncation drops Sandbox last", func(t *testing.T) {
		withoutError := out
		withoutError.StartError = ""
		withoutError.Warnings = nil
		withoutError.Worktree = nil
		withoutError.Model = ""
		withoutSandbox := withoutError
		withoutSandbox.Sandbox = nil
		withoutSB, _ := marshalDelegateCreateResult(withoutSandbox, 10000)
		withModel, _ := marshalDelegateCreateResult(withoutError, 10000)
		maxChars := (len(withoutSB) + len(withModel)) / 2
		if maxChars >= len(withModel) {
			maxChars = len(withoutSB) + 1
		}
		got, err := marshalDelegateCreateResult(out, maxChars)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, strings.Repeat("s", 10)) {
			t.Fatalf("expected Sandbox to be dropped: %s", got)
		}
	})
}

// TestResultToolName covers the resultToolName helper.
func TestResultToolName(t *testing.T) {
	t.Parallel()
	t.Run("default", func(t *testing.T) {
		s := &Session{}
		if got := s.resultToolName(); got != "communicate" {
			t.Fatalf("expected 'communicate', got %q", got)
		}
	})
	t.Run("custom", func(t *testing.T) {
		s := &Session{cfg: SessionConfig{ResultToolName: "custom_tool"}}
		if got := s.resultToolName(); got != "custom_tool" {
			t.Fatalf("expected 'custom_tool', got %q", got)
		}
	})
}

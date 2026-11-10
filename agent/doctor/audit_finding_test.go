package doctor

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A finding's signature is runbook, category, slugified title, and the ISO
// week the audit ran, so a rerun the same week dedups against itself.
func TestAuditSignature(t *testing.T) {
	t.Parallel()
	sig := auditSignature("fixture", "timeout", "Run-timeout jobs", time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	// 2026-08-22 is ISO week 34 of 2026.
	if sig != "fixture:timeout:run-timeout-jobs:2026-W34" {
		t.Fatalf("signature = %q, want fixture:timeout:run-timeout-jobs:2026-W34", sig)
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"Run-timeout jobs", "run-timeout-jobs"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"Multiple   spaces & symbols!", "multiple-spaces-symbols"},
		{"", ""},
		{"---dashes---", "dashes"},
		{"!!!@@@###", ""},
		{"!!!hello", "hello"},
		{"hello world!!!", "hello-world"},
		{"test 123", "test-123"},
	} {
		if got := slugify(tc.input); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAppendUniqueString(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		list []string
		v    string
		want []string
	}{
		{"new value appended", []string{"a", "b"}, "c", []string{"a", "b", "c"}},
		{"duplicate left out", []string{"a", "b", "c"}, "b", []string{"a", "b", "c"}},
		{"empty list", nil, "a", []string{"a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendUniqueString(tc.list, tc.v); !slices.Equal(got, tc.want) {
				t.Fatalf("appendUniqueString(%v, %q) = %v, want %v", tc.list, tc.v, got, tc.want)
			}
		})
	}
}

func TestConditionsSummary(t *testing.T) {
	t.Parallel()
	conds := []auditCondition{
		{Metric: "jobs.run_timeout", Op: ">=", Value: 5},
		{Metric: "longest_identical_run.length", Op: ">", Value: 3},
	}
	if got, want := conditionsSummary(conds), "jobs.run_timeout >= 5 && longest_identical_run.length > 3"; got != want {
		t.Fatalf("conditionsSummary = %q, want %q", got, want)
	}
	if got := conditionsSummary(nil); got != "" {
		t.Fatalf("empty conditions = %q, want empty", got)
	}
}

// RenderAudit surfaces every part of the result: the summary rows, each
// manual step, each unreadable session, and each finding.
func TestRenderAuditShowsEveryPartOfTheResult(t *testing.T) {
	t.Parallel()
	out := RenderAudit(AuditResult{
		Runbook:         "runbook-sentinel",
		SessionsChecked: 1,
		Findings: []Finding{{
			Signature: "signature-sentinel", Severity: "high", Category: "cat", Title: "finding-title-sentinel",
			Description:  "desc",
			SuggestedFix: SuggestedFix{Type: "diagnosis"},
		}},
		Summary:    []AuditSummaryRow{{Title: "summary-title-sentinel", Severity: "high", Sessions: 1}},
		Manual:     []string{"manual-step-sentinel"},
		Unreadable: []UnreadableSession{{SessionID: "session-sentinel", TranscriptRef: "local:session-sentinel", Error: "error-sentinel"}},
	})
	for _, want := range []string{"runbook-sentinel", "summary-title-sentinel", "manual-step-sentinel", "session-sentinel", "error-sentinel", "signature-sentinel"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered audit missing %q:\n%s", want, out)
		}
	}
}

func TestRunAudit_SweepListError(t *testing.T) {
	t.Parallel()
	// A file (not a directory) as state base makes the --since sweep's
	// session listing fail, and the audit reports that failure.
	rb := mustParseFixtureRunbook(t)
	tmpFile := t.TempDir() + "/notadir"
	writeFile(t, tmpFile, "x")
	if _, err := RunAudit(context.Background(), tmpFile, rb, AuditOpts{Since: time.Hour}); err == nil {
		t.Fatal("want error from ListSessions sweep on invalid state base")
	}
}

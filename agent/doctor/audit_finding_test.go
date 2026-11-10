package doctor

import (
	"slices"
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

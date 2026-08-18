package report

import (
	"strings"
	"testing"
)

func TestCategoryVocabulary(t *testing.T) {
	// The five failure classes are the vocabulary humans and log scrapers
	// read a run's failure by; the strings are load-bearing output contract.
	cases := []struct {
		cat  Category
		want string
	}{
		{Setup, "setup"},
		{NotChecked, "not-checked"},
		{Findings, "findings"},
		{ResultsLost, "results-lost"},
		{Interrupted, "interrupted"},
	}
	for _, c := range cases {
		if got := c.cat.String(); got != c.want {
			t.Errorf("Category(%d).String() = %q, want %q", int(c.cat), got, c.want)
		}
	}
}

func TestPassSummaryShape(t *testing.T) {
	var out strings.Builder
	r := New(&out, "lint")
	r.Pass("7 modules, 3s")
	if got, want := out.String(), "PASS lint (7 modules, 3s)\n"; got != want {
		t.Errorf("Pass wrote %q, want %q", got, want)
	}
	if !r.Summarized() {
		t.Error("Summarized() = false after Pass")
	}
}

func TestFailSummaryShape(t *testing.T) {
	cases := []struct {
		cat    Category
		detail string
		want   string
	}{
		{Findings, "2/4 modules: identifier llm", "FAIL lint (findings: 2/4 modules: identifier llm)\n"},
		{Setup, "LINT_PARALLEL must be a positive integer without leading zeroes", "FAIL lint (setup: LINT_PARALLEL must be a positive integer without leading zeroes)\n"},
		{NotChecked, "3 modules: . agent llm", "FAIL lint (not-checked: 3 modules: . agent llm)\n"},
		{ResultsLost, "5 modules: one two three four five", "FAIL lint (results-lost: 5 modules: one two three four five)\n"},
		{Interrupted, "SIGTERM", "FAIL lint (interrupted: SIGTERM)\n"},
	}
	for _, c := range cases {
		var out strings.Builder
		r := New(&out, "lint")
		r.Fail(c.cat, c.detail)
		if got := out.String(); got != c.want {
			t.Errorf("Fail(%s, %q) wrote %q, want %q", c.cat, c.detail, got, c.want)
		}
		if !r.Summarized() {
			t.Errorf("Summarized() = false after Fail(%s)", c.cat)
		}
	}
}

func TestSecondSummaryPanics(t *testing.T) {
	// Exactly one summary line per run is the discipline this package
	// exists to enforce; a second summary is a control-flow bug in the
	// tool, and it must fail loudly in that tool's tests, not ship.
	var out strings.Builder
	r := New(&out, "lint")
	r.Pass("ok")
	defer func() {
		if recover() == nil {
			t.Error("second summary did not panic")
		}
	}()
	r.Fail(Setup, "again")
}

func TestReplayFencesUnitLog(t *testing.T) {
	var out strings.Builder
	Replay(&out, "identifier", strings.NewReader("stdout:identifier\nstderr:identifier\n"))
	want := "----- identifier -----\nstdout:identifier\nstderr:identifier\n"
	if got := out.String(); got != want {
		t.Errorf("Replay wrote %q, want %q", got, want)
	}
}

func TestRetainedPointerShape(t *testing.T) {
	var out strings.Builder
	RetainedPointer(&out, "/tmp/evener-module-lint.abc123")
	if got, want := out.String(), "full logs: /tmp/evener-module-lint.abc123\n"; got != want {
		t.Errorf("RetainedPointer wrote %q, want %q", got, want)
	}
}

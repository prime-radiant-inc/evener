package doctor

import (
	"testing"
)

// TestMetricSourceResolve_TruncationWarnings covers the truncation_warnings
// metric path (audit.go:373-377) including the error path for extra segments.
func TestMetricSourceResolve_TruncationWarnings(t *testing.T) {
	t.Parallel()
	m := metricSource{health: HealthResult{TruncationWarnings: 3}}
	got, err := m.resolve("truncation_warnings")
	if err != nil || got != 3 {
		t.Fatalf("truncation_warnings: got=%v err=%v", got, err)
	}
	// With extra path segment -> error.
	_, err = m.resolve("truncation_warnings.extra")
	if err == nil {
		t.Fatal("extra segment should error")
	}
}

// TestMetricSourceResolve_StaleNotifications covers the stale_notifications
// metric path (audit.go:378-382).
func TestMetricSourceResolve_StaleNotifications(t *testing.T) {
	t.Parallel()
	m := metricSource{health: HealthResult{StaleNotifications: 5}}
	got, err := m.resolve("stale_notifications")
	if err != nil || got != 5 {
		t.Fatalf("stale_notifications: got=%v err=%v", got, err)
	}
	_, err = m.resolve("stale_notifications.extra")
	if err == nil {
		t.Fatal("extra segment should error")
	}
}

// TestMetricSourceResolve_UserCorrections covers the user_corrections metric
// path (audit.go:383-387).
func TestMetricSourceResolve_UserCorrections(t *testing.T) {
	t.Parallel()
	m := metricSource{health: HealthResult{UserCorrections: 2}}
	got, err := m.resolve("user_corrections")
	if err != nil || got != 2 {
		t.Fatalf("user_corrections: got=%v err=%v", got, err)
	}
	_, err = m.resolve("user_corrections.extra")
	if err == nil {
		t.Fatal("extra segment should error")
	}
}

// TestMetricSourceResolve_Steering covers the steering metric path
// (audit.go:388-392) including the empty-key error.
func TestMetricSourceResolve_SteeringCov(t *testing.T) {
	t.Parallel()
	m := metricSource{health: HealthResult{Steering: map[string]int{"restart": 1}}}
	got, err := m.resolve("steering.restart")
	if err != nil || got != 1 {
		t.Fatalf("steering.restart: got=%v err=%v", got, err)
	}
	// Absent key reads as zero.
	got, err = m.resolve("steering.unknown")
	if err != nil || got != 0 {
		t.Fatalf("absent steering key: got=%v err=%v", got, err)
	}
	// Empty key -> error.
	_, err = m.resolve("steering")
	if err == nil {
		t.Fatal("steering without kind should error")
	}
}

// TestMetricSourceResolve_ToolCallsCov covers the tool_calls metric path
// (audit.go:393-397).
func TestMetricSourceResolve_ToolCallsCov(t *testing.T) {
	t.Parallel()
	m := metricSource{health: HealthResult{ToolCalls: map[string]int{"exec_command": 10}}}
	got, err := m.resolve("tool_calls.exec_command")
	if err != nil || got != 10 {
		t.Fatalf("tool_calls.exec_command: got=%v err=%v", got, err)
	}
	// Absent tool reads as zero.
	got, err = m.resolve("tool_calls.unknown_tool")
	if err != nil || got != 0 {
		t.Fatalf("absent tool: got=%v err=%v", got, err)
	}
	// Empty key -> error.
	_, err = m.resolve("tool_calls")
	if err == nil {
		t.Fatal("tool_calls without tool should error")
	}
}

// TestMetricSourceResolve_APILogNotLoaded covers the error when apilog
// totals are requested but not loaded (audit.go:426-427).
func TestMetricSourceResolve_APILogNotLoaded(t *testing.T) {
	t.Parallel()
	m := metricSource{haveAPILog: false}
	_, err := m.resolve("apilog.calls")
	if err == nil {
		t.Fatal("apilog not loaded should error")
	}
}

// TestMetricSourceResolve_APIHealthNotLoaded covers the error when apilog
// health is requested but not loaded (audit.go:406-407).
func TestMetricSourceResolve_APIHealthNotLoaded(t *testing.T) {
	t.Parallel()
	m := metricSource{haveAPIHealth: false}
	_, err := m.resolve("apilog.recorded_empty")
	if err == nil {
		t.Fatal("apilog health not loaded should error")
	}
}

// TestMetricSourceResolve_JobsZeroOutputTerminalExtra covers the error path
// for extra path segments on jobs.zero_output_terminal (audit.go:357-358).
func TestMetricSourceResolve_JobsZeroOutputTerminalExtra(t *testing.T) {
	t.Parallel()
	m := metricSource{health: HealthResult{Jobs: JobsHealth{ZeroOutputTerminal: 1}}}
	_, err := m.resolve("jobs.zero_output_terminal.extra")
	if err == nil {
		t.Fatal("extra segments on zero_output_terminal should error")
	}
}

// TestParseAuditFenceEmptyList covers the empty-list case in parseAuditFence
// (audit.go:252-254) where YAML decodes successfully but the audit list is
// empty — should return found=false.
func TestParseAuditFenceEmptyList(t *testing.T) {
	t.Parallel()
	// YAML with no audit entries -> found=false.
	_, found := parseAuditFence("audit: []")
	if found {
		t.Fatal("empty audit list should not be found")
	}
	// YAML with non-audit content -> found=false.
	_, found = parseAuditFence("other: value")
	if found {
		t.Fatal("non-audit YAML should not be found")
	}
}

// TestParseRunbookDefaultStepReset covers the default case in ParseRunbook's
// line loop (audit.go:228-230) where a non-indented, non-bullet line resets
// the open step.
func TestParseRunbookDefaultStepReset(t *testing.T) {
	t.Parallel()
	content := []byte(`# Runbook: step-reset-test

## HEALTHY
- No issues.

## INSPECT
` + "```" + `
evener-doctor transcript <selector> --health --json
` + "```" + `

## CLASSIFY
` + "```" + `yaml
audit:
  - title: "Test check"
    severity: low
    category: test
    metric: truncation_warnings
    op: ">="
    value: 1
` + "```" + `

Some prose line that is not a bullet.

- A manual step after prose.
`)
	rb, err := ParseRunbook("step-reset-test", content)
	if err != nil {
		t.Fatalf("ParseRunbook: %v", err)
	}
	if len(rb.ManualSteps) != 1 {
		t.Fatalf("manual steps = %d, want 1: %+v", len(rb.ManualSteps), rb.ManualSteps)
	}
	if rb.ManualSteps[0] != "A manual step after prose." {
		t.Fatalf("manual step = %q, want 'A manual step after prose.'", rb.ManualSteps[0])
	}
}

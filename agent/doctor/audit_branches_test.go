package doctor

import (
	"strings"
	"testing"
	"time"
)

func TestCompareMetric_Bool(t *testing.T) {
	t.Parallel()
	// == on boolean metrics.
	if ok, err := compareMetric(true, "==", true); err != nil || !ok {
		t.Fatalf("true == true: ok=%v err=%v", ok, err)
	}
	if ok, err := compareMetric(true, "!=", false); err != nil || !ok {
		t.Fatalf("true != false: ok=%v err=%v", ok, err)
	}
	if ok, err := compareMetric(true, "==", false); err != nil || ok {
		t.Fatalf("true == false: ok=%v err=%v", ok, err)
	}
	// Type mismatch: non-bool value against a boolean metric.
	if _, err := compareMetric(true, "==", 1); err == nil {
		t.Fatal("bool metric vs int value should error")
	}
	// Invalid operator for boolean.
	if _, err := compareMetric(true, ">=", true); err == nil {
		t.Fatal(">= should be invalid for boolean metric")
	}
}

func TestCompareMetric_String(t *testing.T) {
	t.Parallel()
	if ok, err := compareMetric("shell", "==", "shell"); err != nil || !ok {
		t.Fatalf("string ==: ok=%v err=%v", ok, err)
	}
	if ok, err := compareMetric("shell", "!=", "read"); err != nil || !ok {
		t.Fatalf("string !=: ok=%v err=%v", ok, err)
	}
	if ok, err := compareMetric("shell", "==", "read"); err != nil || ok {
		t.Fatalf("string == mismatch: ok=%v err=%v", ok, err)
	}
	// Type mismatch.
	if _, err := compareMetric("shell", "==", 1); err == nil {
		t.Fatal("string metric vs int value should error")
	}
	// Invalid operator for string.
	if _, err := compareMetric("shell", ">=", "shell"); err == nil {
		t.Fatal(">= should be invalid for string metric")
	}
}

func TestCompareMetric_Int(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		actual any
		op     string
		want   any
		expect bool
	}{
		{5, ">=", 3, true},
		{5, ">", 5, false},
		{5, "<=", 5, true},
		{5, "<", 3, false},
		{5, "==", 5, true},
		{5, "!=", 3, true},
	} {
		ok, err := compareMetric(tc.actual, tc.op, tc.want)
		if err != nil {
			t.Fatalf("compareMetric(%v, %q, %v): err=%v", tc.actual, tc.op, tc.want, err)
		}
		if ok != tc.expect {
			t.Errorf("compareMetric(%v, %q, %v) = %v, want %v", tc.actual, tc.op, tc.want, ok, tc.expect)
		}
	}
	// Int with float64 value.
	if ok, err := compareMetric(5, ">=", 3.0); err != nil || !ok {
		t.Fatalf("int >= float64: ok=%v err=%v", ok, err)
	}
	// Int with int64 value.
	if ok, err := compareMetric(5, "==", int64(5)); err != nil || !ok {
		t.Fatalf("int == int64: ok=%v err=%v", ok, err)
	}
	// Type mismatch: non-numeric value.
	if _, err := compareMetric(5, ">=", "string"); err == nil {
		t.Fatal("int metric vs string value should error")
	}
}

func TestCompareMetric_UnsupportedType(t *testing.T) {
	t.Parallel()
	if _, err := compareMetric([]int{1}, "==", []int{1}); err == nil {
		t.Fatal("unsupported type should error")
	}
}

func TestCompareMetric_InvalidOperator(t *testing.T) {
	t.Parallel()
	if _, err := compareMetric(5, "~=", 3); err == nil {
		t.Fatal("invalid operator should error")
	}
}

func TestToFloat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		v    any
		want float64
	}{
		{int(5), 5},
		{int64(5), 5},
		{float64(5.5), 5.5},
	} {
		got, err := toFloat(tc.v)
		if err != nil || got != tc.want {
			t.Errorf("toFloat(%v) = %v, %v; want %v, nil", tc.v, got, err, tc.want)
		}
	}
	if _, err := toFloat("string"); err == nil {
		t.Error("toFloat(string) should error")
	}
}

func TestRunbookNeedsAPILog(t *testing.T) {
	t.Parallel()
	// A runbook with an apilog.calls metric needs apilog.
	rb := Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.calls", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPILog() {
		t.Error("runbook with apilog.calls should need apilog")
	}
	// apilog.errors needs apilog (not a health metric).
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.errors", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPILog() {
		t.Error("runbook with apilog.errors should need apilog")
	}
	// apilog.avg_latency_ms needs apilog.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.avg_latency_ms", Op: ">=", Value: 100}},
	}}}
	if !rb.needsAPILog() {
		t.Error("runbook with apilog.avg_latency_ms should need apilog")
	}
	// apilog.empties needs apilog.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.empties", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPILog() {
		t.Error("runbook with apilog.empties should need apilog")
	}
	// apilog.recorded_empty does NOT need apilog (it's a health metric).
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.recorded_empty", Op: ">=", Value: 1}},
	}}}
	if rb.needsAPILog() {
		t.Error("runbook with apilog.recorded_empty should NOT need apilog")
	}
	// apilog.errors_by_class.permanent does NOT need apilog.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.errors_by_class.permanent", Op: ">=", Value: 1}},
	}}}
	if rb.needsAPILog() {
		t.Error("runbook with apilog.errors_by_class.* should NOT need apilog")
	}
	// Non-apilog metric does not need apilog.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "jobs.run_timeout", Op: ">=", Value: 1}},
	}}}
	if rb.needsAPILog() {
		t.Error("runbook with jobs.* should NOT need apilog")
	}
	// Empty runbook does not need apilog.
	if (Runbook{}).needsAPILog() {
		t.Error("empty runbook should not need apilog")
	}
}

func TestRunbookNeedsAPIHealth(t *testing.T) {
	t.Parallel()
	// apilog.recorded_empty needs health.
	rb := Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.recorded_empty", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPIHealth() {
		t.Error("runbook with apilog.recorded_empty should need health")
	}
	// apilog.retry_storm_groups needs health.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.retry_storm_groups", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPIHealth() {
		t.Error("runbook with apilog.retry_storm_groups should need health")
	}
	// apilog.unsettled_groups needs health.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.unsettled_groups", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPIHealth() {
		t.Error("runbook with apilog.unsettled_groups should need health")
	}
	// apilog.errors_by_class.* needs health.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.errors_by_class.timeout", Op: ">=", Value: 1}},
	}}}
	if !rb.needsAPIHealth() {
		t.Error("runbook with apilog.errors_by_class.* should need health")
	}
	// Non-health metric does not need health.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "jobs.run_timeout", Op: ">=", Value: 1}},
	}}}
	if rb.needsAPIHealth() {
		t.Error("runbook with jobs.* should NOT need health")
	}
	// apilog.calls does not need health.
	rb = Runbook{Checks: []AuditCheck{{
		Conditions: []auditCondition{{Metric: "apilog.calls", Op: ">=", Value: 1}},
	}}}
	if rb.needsAPIHealth() {
		t.Error("runbook with apilog.calls should NOT need health")
	}
}

func TestMetricSourceResolve_UnknownNamespace(t *testing.T) {
	t.Parallel()
	src := metricSource{}
	if _, err := src.resolve("unknown.metric"); err == nil {
		t.Error("unknown namespace should error")
	}
}

func TestMetricSourceResolve_Jobs(t *testing.T) {
	t.Parallel()
	src := metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 5}, ZeroOutputTerminal: 3}}}
	// jobs.<reason> resolves from the map.
	if v, err := src.resolve("jobs.run_timeout"); err != nil || v != 5 {
		t.Errorf("jobs.run_timeout = %v, %v; want 5, nil", v, err)
	}
	// jobs.zero_output_terminal resolves as scalar.
	if v, err := src.resolve("jobs.zero_output_terminal"); err != nil || v != 3 {
		t.Errorf("jobs.zero_output_terminal = %v, %v; want 3, nil", v, err)
	}
	// jobs with no rest -> error.
	if _, err := src.resolve("jobs"); err == nil {
		t.Error("jobs with no path should error")
	}
	// jobs.zero_output_terminal.<x> -> error (trailing junk).
	if _, err := src.resolve("jobs.zero_output_terminal.x"); err == nil {
		t.Error("jobs.zero_output_terminal.x should error")
	}
}

func TestMetricSourceResolve_LongestIdenticalRun(t *testing.T) {
	t.Parallel()
	src := metricSource{health: HealthResult{LongestIdenticalRun: IdenticalRun{Length: 4, AllErrors: true, Tool: "shell"}}}
	if v, err := src.resolve("longest_identical_run.length"); err != nil || v != 4 {
		t.Errorf("length = %v, %v; want 4", v, err)
	}
	if v, err := src.resolve("longest_identical_run.errors"); err != nil || v != true {
		t.Errorf("errors = %v, %v; want true", v, err)
	}
	if v, err := src.resolve("longest_identical_run.all_errors"); err != nil || v != true {
		t.Errorf("all_errors = %v, %v; want true", v, err)
	}
	if v, err := src.resolve("longest_identical_run.tool"); err != nil || v != "shell" {
		t.Errorf("tool = %v, %v; want shell", v, err)
	}
	if _, err := src.resolve("longest_identical_run.unknown"); err == nil {
		t.Error("unknown longest_identical_run.* should error")
	}
}

func TestMetricSourceResolve_SteeringAndToolCalls(t *testing.T) {
	t.Parallel()
	src := metricSource{health: HealthResult{
		Steering:  map[string]int{"correction": 2},
		ToolCalls: map[string]int{"shell": 10, "read_file": 5},
	}}
	if v, err := src.resolve("steering.correction"); err != nil || v != 2 {
		t.Errorf("steering.correction = %v, %v; want 2", v, err)
	}
	if v, err := src.resolve("tool_calls.shell"); err != nil || v != 10 {
		t.Errorf("tool_calls.shell = %v, %v; want 10", v, err)
	}
	// Absent key reads as zero (legitimate, not an error).
	if v, err := src.resolve("tool_calls.unknown"); err != nil || v != 0 {
		t.Errorf("tool_calls.unknown = %v, %v; want 0", v, err)
	}
	// steering with no rest -> error.
	if _, err := src.resolve("steering"); err == nil {
		t.Error("steering with no kind should error")
	}
	// tool_calls with no rest -> error.
	if _, err := src.resolve("tool_calls"); err == nil {
		t.Error("tool_calls with no tool should error")
	}
}

func TestMetricSourceResolve_ToolErrors(t *testing.T) {
	t.Parallel()
	src := metricSource{health: HealthResult{
		ToolErrors: map[string]map[string]int{"shell": {"timeout": 3, "permission": 1}},
	}}
	if v, err := src.resolve("tool_errors.shell.timeout"); err != nil || v != 3 {
		t.Errorf("tool_errors.shell.timeout = %v, %v; want 3", v, err)
	}
	// Absent class reads as zero.
	if v, err := src.resolve("tool_errors.shell.unknown"); err != nil || v != 0 {
		t.Errorf("tool_errors.shell.unknown = %v, %v; want 0", v, err)
	}
	// Missing class -> error.
	if _, err := src.resolve("tool_errors.shell"); err == nil {
		t.Error("tool_errors without class should error")
	}
	// Missing tool -> error.
	if _, err := src.resolve("tool_errors."); err == nil {
		t.Error("tool_errors with empty tool should error")
	}
}

func TestMetricSourceResolve_APILogTotals(t *testing.T) {
	t.Parallel()
	src := metricSource{apilog: APILogTotals{Calls: 100, Empties: 5, Errors: 3, AvgLatencyMs: 250}, haveAPILog: true}
	if v, err := src.resolve("apilog.calls"); err != nil || v != 100 {
		t.Errorf("apilog.calls = %v, %v; want 100", v, err)
	}
	if v, err := src.resolve("apilog.empties"); err != nil || v != 5 {
		t.Errorf("apilog.empties = %v, %v; want 5", v, err)
	}
	if v, err := src.resolve("apilog.errors"); err != nil || v != 3 {
		t.Errorf("apilog.errors = %v, %v; want 3", v, err)
	}
	if v, err := src.resolve("apilog.avg_latency_ms"); err != nil || v != 250 {
		t.Errorf("apilog.avg_latency_ms = %v, %v; want 250", v, err)
	}
	if _, err := src.resolve("apilog.unknown"); err == nil {
		t.Error("unknown apilog.* should error")
	}
	// Without haveAPILog, accessing apilog.* totals errors.
	srcNoLog := metricSource{haveAPILog: false}
	if _, err := srcNoLog.resolve("apilog.calls"); err == nil {
		t.Error("apilog.calls without loaded totals should error")
	}
}

func TestMetricSourceResolve_APIHealth(t *testing.T) {
	t.Parallel()
	src := metricSource{
		apiHealth:     APIHealthResult{RecordedEmpty: 2, RetryStormGroups: 1, UnsettledGroups: 3, ErrorsByClass: map[string]int{"permanent": 4, "timeout": 7}},
		haveAPIHealth: true,
	}
	if v, err := src.resolve("apilog.recorded_empty"); err != nil || v != 2 {
		t.Errorf("apilog.recorded_empty = %v, %v; want 2", v, err)
	}
	if v, err := src.resolve("apilog.retry_storm_groups"); err != nil || v != 1 {
		t.Errorf("apilog.retry_storm_groups = %v, %v; want 1", v, err)
	}
	if v, err := src.resolve("apilog.unsettled_groups"); err != nil || v != 3 {
		t.Errorf("apilog.unsettled_groups = %v, %v; want 3", v, err)
	}
	if v, err := src.resolve("apilog.errors_by_class.permanent"); err != nil || v != 4 {
		t.Errorf("apilog.errors_by_class.permanent = %v, %v; want 4", v, err)
	}
	// Absent class reads as zero.
	if v, err := src.resolve("apilog.errors_by_class.unknown"); err != nil || v != 0 {
		t.Errorf("apilog.errors_by_class.unknown = %v, %v; want 0", v, err)
	}
	// Empty class -> error.
	if _, err := src.resolve("apilog.errors_by_class."); err == nil {
		t.Error("apilog.errors_by_class. (empty class) should error")
	}
	// Unknown apilog health metric.
	if _, err := src.resolve("apilog.unknown_health"); err == nil {
		t.Error("unknown apilog health metric should error")
	}
	// Without haveAPIHealth, accessing health metrics errors.
	srcNoHealth := metricSource{haveAPIHealth: false}
	if _, err := srcNoHealth.resolve("apilog.recorded_empty"); err == nil {
		t.Error("apilog.recorded_empty without loaded health should error")
	}
}

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
	} {
		if got := slugify(tc.input); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAppendUniqueString(t *testing.T) {
	t.Parallel()
	list := []string{"a", "b"}
	list = appendUniqueString(list, "c")
	if len(list) != 3 || list[2] != "c" {
		t.Fatalf("list = %v, want [a b c]", list)
	}
	// Duplicate is not appended.
	list = appendUniqueString(list, "a")
	if len(list) != 3 {
		t.Fatalf("list = %v, want no duplicate", list)
	}
}

func TestConditionsSummary(t *testing.T) {
	t.Parallel()
	conds := []auditCondition{
		{Metric: "jobs.run_timeout", Op: ">=", Value: 5},
		{Metric: "longest_identical_run.length", Op: ">=", Value: 3},
	}
	got := conditionsSummary(conds)
	want := "jobs.run_timeout >= 5 && longest_identical_run.length >= 3"
	if got != want {
		t.Fatalf("conditionsSummary = %q, want %q", got, want)
	}
	// Empty conditions.
	if got := conditionsSummary(nil); got != "" {
		t.Fatalf("empty conditions = %q, want empty", got)
	}
}

func TestAuditCheckEvaluate(t *testing.T) {
	t.Parallel()
	// A check that trips when the condition holds.
	check := AuditCheck{
		Conditions: []auditCondition{
			{Metric: "jobs.run_timeout", Op: ">=", Value: 5},
		},
	}
	src := metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 10}}}}
	tripped, err := check.evaluate(src)
	if err != nil || !tripped {
		t.Fatalf("evaluate: tripped=%v err=%v, want true nil", tripped, err)
	}
	// Does not trip when condition fails.
	src = metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 1}}}}
	tripped, err = check.evaluate(src)
	if err != nil || tripped {
		t.Fatalf("evaluate: tripped=%v err=%v, want false nil", tripped, err)
	}
	// Zero-value source: jobs.<reason> resolves to 0 (nil map read), not an
	// error, so evaluate returns false, nil.
	src = metricSource{}
	tripped, err = check.evaluate(src)
	if err != nil || tripped {
		t.Fatalf("evaluate on zero source: tripped=%v err=%v, want false nil", tripped, err)
	}
	// An unknown namespace metric does error from resolve.
	badMetricCheck := AuditCheck{
		Conditions: []auditCondition{{Metric: "unknown.metric", Op: ">=", Value: 1}},
	}
	if _, err := badMetricCheck.evaluate(src); err == nil {
		t.Fatal("evaluate with unknown namespace should error")
	}
	// Error from compareMetric (type mismatch) propagates with metric name.
	badCheck := AuditCheck{
		Conditions: []auditCondition{{Metric: "longest_identical_run.length", Op: "==", Value: "not-a-number"}},
	}
	src = metricSource{health: HealthResult{LongestIdenticalRun: IdenticalRun{Length: 3}}}
	_, err = badCheck.evaluate(src)
	if err == nil || !strings.Contains(err.Error(), "metric longest_identical_run.length") {
		t.Fatalf("evaluate error = %v, want metric name in error", err)
	}
}

func TestParseRunbook_MissingTitleErrors(t *testing.T) {
	t.Parallel()
	bad := "## CLASSIFY\n```yaml\naudit:\n  - severity: high\n    category: timeout\n    metric: jobs.run_timeout\n    op: \">=\"\n    value: 5\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for missing title")
	}
}

func TestParseRunbook_InvalidSuggestedFixErrors(t *testing.T) {
	t.Parallel()
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: high\n    category: timeout\n    suggested_fix: unknown\n    metric: jobs.run_timeout\n    op: \">=\"\n    value: 5\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for invalid suggested_fix")
	}
}

func TestParseRunbook_MissingMetricErrors(t *testing.T) {
	t.Parallel()
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: high\n    category: timeout\n    op: \">=\"\n    value: 5\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for missing metric")
	}
}

func TestParseRunbook_BothMetricAndAllErrors(t *testing.T) {
	t.Parallel()
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: high\n    category: timeout\n    metric: jobs.run_timeout\n    op: \">=\"\n    value: 5\n    all:\n      - metric: longest_identical_run.length\n        op: \">=\"\n        value: 3\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for both metric and all")
	}
}

func TestParseRunbook_AllConditionMissingMetricErrors(t *testing.T) {
	t.Parallel()
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: high\n    category: timeout\n    all:\n      - op: \">=\"\n        value: 3\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for all condition missing metric")
	}
}

func TestRenderAudit_FindingsWithManual(t *testing.T) {
	t.Parallel()
	res := AuditResult{
		Runbook:         "test",
		SessionsChecked: 1,
		Findings: []Finding{{
			Signature: "sig", Severity: "high", Category: "cat", Title: "title",
			Description: "desc",
			SuggestedFix: SuggestedFix{Type: "diagnosis"},
		}},
		Summary: []AuditSummaryRow{{Title: "title", Severity: "high", Sessions: 1}},
		Manual:  []string{"Check manually"},
	}
	out := RenderAudit(res)
	for _, want := range []string{"test", "sessions_checked=1", "findings=1", "high", "title", "manual step", "Check manually", "signature"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered audit missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAudit_NoFindingsHealthy(t *testing.T) {
	t.Parallel()
	res := AuditResult{Runbook: "test", SessionsChecked: 0}
	out := RenderAudit(res)
	if !strings.Contains(out, "no findings — healthy") {
		t.Errorf("rendered audit with no findings should say healthy:\n%s", out)
	}
}

func TestRenderAudit_UnreadableSessions(t *testing.T) {
	t.Parallel()
	res := AuditResult{
		Runbook:    "test",
		Unreadable: []UnreadableSession{{SessionID: "sid", TranscriptRef: "local:sid", Error: "boom"}},
	}
	out := RenderAudit(res)
	if !strings.Contains(out, "could not be read") || !strings.Contains(out, "sid") || !strings.Contains(out, "boom") {
		t.Errorf("rendered audit should include unreadable session:\n%s", out)
	}
}

func TestRunAudit_SweepListError(t *testing.T) {
	t.Parallel()
	// An invalid state base that ListSessions cannot traverse should surface
	// an error from the --since sweep path.
	rb := mustParseFixtureRunbook(t)
	// A file (not a directory) as state base causes ListSessions to fail.
	tmpFile := t.TempDir() + "/notadir"
	writeFile(t, tmpFile, "x")
	if _, err := RunAudit(tmpFile, rb, AuditOpts{Since: time.Hour}); err == nil {
		t.Fatal("want error from ListSessions sweep on invalid state base")
	}
}

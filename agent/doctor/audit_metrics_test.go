package doctor

import (
	"testing"
)

// compareMetric applies a runbook operator to a resolved metric: booleans and
// strings take only == and !=, numbers take every operator against any numeric
// runbook value, and a type or operator mismatch is an authoring error.
func TestCompareMetric(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		actual  any
		op      string
		want    any
		expect  bool
		wantErr bool
	}{
		{name: "bool ==", actual: true, op: "==", want: true, expect: true},
		{name: "bool == differs", actual: true, op: "==", want: false},
		{name: "bool !=", actual: true, op: "!=", want: false, expect: true},
		{name: "bool against a number", actual: true, op: "==", want: 1, wantErr: true},
		{name: "bool ordering", actual: true, op: ">=", want: true, wantErr: true},
		{name: "string ==", actual: "shell", op: "==", want: "shell", expect: true},
		{name: "string == differs", actual: "shell", op: "==", want: "read"},
		{name: "string !=", actual: "shell", op: "!=", want: "read", expect: true},
		{name: "string != same", actual: "shell", op: "!=", want: "shell"},
		{name: "string against a number", actual: "shell", op: "==", want: 1, wantErr: true},
		{name: "string ordering", actual: "shell", op: ">", want: "read", wantErr: true},
		{name: ">= above", actual: 5, op: ">=", want: 3, expect: true},
		{name: ">= equal", actual: 5, op: ">=", want: 5, expect: true},
		{name: "> equal", actual: 5, op: ">", want: 5},
		{name: "> above", actual: 5, op: ">", want: 3, expect: true},
		{name: "<= equal", actual: 5, op: "<=", want: 5, expect: true},
		{name: "<= above", actual: 5, op: "<=", want: 3},
		{name: "< below", actual: 3, op: "<", want: 5, expect: true},
		{name: "< equal", actual: 5, op: "<", want: 5},
		{name: "< above", actual: 5, op: "<", want: 3},
		{name: "== equal", actual: 5, op: "==", want: 5, expect: true},
		{name: "== differs", actual: 5, op: "==", want: 3},
		{name: "!= differs", actual: 5, op: "!=", want: 3, expect: true},
		{name: "!= equal", actual: 5, op: "!=", want: 5},
		{name: "float64 value", actual: 5, op: ">=", want: 3.0, expect: true},
		{name: "int64 value", actual: 5, op: "==", want: int64(5), expect: true},
		{name: "non-numeric value", actual: 5, op: ">=", want: "not-a-number", wantErr: true},
		{name: "unknown operator", actual: 5, op: "~=", want: 3, wantErr: true},
		{name: "unsupported metric type", actual: []int{1}, op: "==", want: []int{1}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := compareMetric(tc.actual, tc.op, tc.want)
			if (err != nil) != tc.wantErr || ok != tc.expect {
				t.Fatalf("compareMetric(%v, %q, %v) = (%v, %v), want (%v, error %v)", tc.actual, tc.op, tc.want, ok, err, tc.expect, tc.wantErr)
			}
		})
	}
}

// TestMetricSourceResolveAnswersEveryAPIHealthMetricName guards the coupling
// the API-health switch deliberately lacks a default for: every path routed to
// APIHealthResult by needsAPIHealth must have its own case in resolve. A new
// name added without one falls through to the totals path, which this test
// catches by leaving haveAPILog false.
func TestMetricSourceResolveAnswersEveryAPIHealthMetricName(t *testing.T) {
	src := metricSource{haveAPIHealth: true}
	for path := range apiHealthMetricNames {
		if got, err := src.resolve(path); err != nil {
			t.Errorf("resolve(%q) = (%v, %v); every apiHealthMetricNames key needs an API-health case", path, got, err)
		}
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

// A runbook pays for the API-log totals decode only when a check reads a
// totals metric, and for the API-health decode only when a check reads a
// health metric.
func TestRunbookNeedsAPILogAndHealth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		metric              string
		wantLog, wantHealth bool
	}{
		{"apilog.calls", true, false},
		{"apilog.errors", true, false},
		{"apilog.avg_latency_ms", true, false},
		{"apilog.empties", true, false},
		{"apilog.recorded_empty", false, true},
		{"apilog.retry_storm_groups", false, true},
		{"apilog.unsettled_groups", false, true},
		{"apilog.errors_by_class.permanent", false, true},
		{"jobs.run_timeout", false, false},
	} {
		t.Run(tc.metric, func(t *testing.T) {
			rb := Runbook{Checks: []AuditCheck{{Conditions: []auditCondition{{Metric: tc.metric, Op: ">=", Value: 1}}}}}
			if rb.needsAPILog() != tc.wantLog || rb.needsAPIHealth() != tc.wantHealth {
				t.Fatalf("needsAPILog = %v, needsAPIHealth = %v; want %v, %v", rb.needsAPILog(), rb.needsAPIHealth(), tc.wantLog, tc.wantHealth)
			}
		})
	}
	if (Runbook{}).needsAPILog() || (Runbook{}).needsAPIHealth() {
		t.Error("an empty runbook needs neither decode")
	}
}

// resolve maps a dotted metric path to its value: an absent map key reads as
// zero, while an unknown namespace, a malformed path, or a metric whose source
// was not loaded is an error.
func TestMetricSourceResolve(t *testing.T) {
	t.Parallel()
	health := metricSource{health: HealthResult{
		Jobs:                JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 5}, ZeroOutputTerminal: 3},
		LongestIdenticalRun: IdenticalRun{Length: 4, AllErrors: true, Tool: "shell"},
		TruncationWarnings:  6,
		StaleNotifications:  7,
		UserCorrections:     8,
		Steering:            map[string]int{"correction": 2},
		ToolCalls:           map[string]int{"shell": 10},
		ToolErrors:          map[string]map[string]int{"shell": {"timeout": 9}},
	}}
	totals := metricSource{apilog: APILogTotals{Calls: 100, Empties: 11, Errors: 12, AvgLatencyMs: 250}, haveAPILog: true}
	apiHealth := metricSource{
		apiHealth:     APIHealthResult{RecordedEmpty: 13, RetryStormGroups: 14, UnsettledGroups: 15, ErrorsByClass: map[string]int{"permanent": 16}},
		haveAPIHealth: true,
	}
	for _, tc := range []struct {
		name    string
		src     metricSource
		path    string
		want    any
		wantErr bool
	}{
		{name: "unknown namespace", src: health, path: "unknown.metric", wantErr: true},
		{name: "jobs reason", src: health, path: "jobs.run_timeout", want: 5},
		{name: "jobs absent reason", src: health, path: "jobs.other", want: 0},
		{name: "jobs zero-output terminal", src: health, path: "jobs.zero_output_terminal", want: 3},
		{name: "bare jobs", src: health, path: "jobs", wantErr: true},
		{name: "jobs zero-output trailing path", src: health, path: "jobs.zero_output_terminal.x", wantErr: true},
		{name: "identical-run length", src: health, path: "longest_identical_run.length", want: 4},
		{name: "identical-run errors", src: health, path: "longest_identical_run.errors", want: true},
		{name: "identical-run all_errors", src: health, path: "longest_identical_run.all_errors", want: true},
		{name: "identical-run tool", src: health, path: "longest_identical_run.tool", want: "shell"},
		{name: "identical-run unknown field", src: health, path: "longest_identical_run.unknown", wantErr: true},
		{name: "truncation warnings", src: health, path: "truncation_warnings", want: 6},
		{name: "truncation warnings trailing path", src: health, path: "truncation_warnings.extra", wantErr: true},
		{name: "stale notifications", src: health, path: "stale_notifications", want: 7},
		{name: "stale notifications trailing path", src: health, path: "stale_notifications.extra", wantErr: true},
		{name: "user corrections", src: health, path: "user_corrections", want: 8},
		{name: "user corrections trailing path", src: health, path: "user_corrections.extra", wantErr: true},
		{name: "steering kind", src: health, path: "steering.correction", want: 2},
		{name: "steering absent kind", src: health, path: "steering.unknown", want: 0},
		{name: "bare steering", src: health, path: "steering", wantErr: true},
		{name: "tool calls", src: health, path: "tool_calls.shell", want: 10},
		{name: "tool calls absent tool", src: health, path: "tool_calls.unknown", want: 0},
		{name: "bare tool calls", src: health, path: "tool_calls", wantErr: true},
		{name: "tool errors", src: health, path: "tool_errors.shell.timeout", want: 9},
		{name: "tool errors absent class", src: health, path: "tool_errors.shell.unknown", want: 0},
		{name: "tool errors without a class", src: health, path: "tool_errors.shell", wantErr: true},
		{name: "tool errors empty tool", src: health, path: "tool_errors..class", wantErr: true},
		{name: "tool errors empty class", src: health, path: "tool_errors.tool.", wantErr: true},
		{name: "tool errors no path", src: health, path: "tool_errors.", wantErr: true},
		{name: "apilog calls", src: totals, path: "apilog.calls", want: 100},
		{name: "apilog empties", src: totals, path: "apilog.empties", want: 11},
		{name: "apilog errors", src: totals, path: "apilog.errors", want: 12},
		{name: "apilog avg latency", src: totals, path: "apilog.avg_latency_ms", want: 250},
		{name: "apilog unknown total", src: totals, path: "apilog.unknown", wantErr: true},
		{name: "apilog totals not loaded", src: metricSource{}, path: "apilog.calls", wantErr: true},
		{name: "apilog recorded empty", src: apiHealth, path: "apilog.recorded_empty", want: 13},
		{name: "apilog retry storm groups", src: apiHealth, path: "apilog.retry_storm_groups", want: 14},
		{name: "apilog unsettled groups", src: apiHealth, path: "apilog.unsettled_groups", want: 15},
		{name: "apilog errors by class", src: apiHealth, path: "apilog.errors_by_class.permanent", want: 16},
		{name: "apilog errors by absent class", src: apiHealth, path: "apilog.errors_by_class.unknown", want: 0},
		{name: "apilog errors by empty class", src: apiHealth, path: "apilog.errors_by_class.", wantErr: true},
		{name: "apilog unknown health metric", src: apiHealth, path: "apilog.unknown_metric", wantErr: true},
		{name: "apilog health not loaded", src: metricSource{}, path: "apilog.recorded_empty", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.src.resolve(tc.path)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
				t.Fatalf("resolve(%q) = (%v, %v), want (%v, error %v)", tc.path, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// A check trips only when every condition holds; a metric that cannot be
// resolved or compared fails the check loudly instead of reading as false.
func TestAuditCheckEvaluate(t *testing.T) {
	t.Parallel()
	check := AuditCheck{Conditions: []auditCondition{{Metric: "jobs.run_timeout", Op: ">=", Value: 5}}}
	for _, tc := range []struct {
		name    string
		check   AuditCheck
		src     metricSource
		tripped bool
		wantErr bool
	}{
		{name: "condition holds", check: check, src: metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 10}}}}, tripped: true},
		{name: "condition fails", check: check, src: metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 1}}}}},
		{name: "absent reason reads as zero", check: check, src: metricSource{}},
		{name: "zero meets a zero threshold", check: AuditCheck{Conditions: []auditCondition{{Metric: "apilog.calls", Op: ">=", Value: 0}}},
			src: metricSource{haveAPILog: true}, tripped: true},
		{name: "one of two conditions fails", check: AuditCheck{Conditions: []auditCondition{
			{Metric: "jobs.run_timeout", Op: ">=", Value: 5},
			{Metric: "longest_identical_run.length", Op: ">=", Value: 3},
		}}, src: metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{"run_timeout": 10}}}}},
		{name: "unknown namespace", check: AuditCheck{Conditions: []auditCondition{{Metric: "unknown.metric", Op: ">=", Value: 1}}}, wantErr: true},
		{name: "type mismatch", check: AuditCheck{Conditions: []auditCondition{{Metric: "longest_identical_run.length", Op: "==", Value: "not-a-number"}}},
			src: metricSource{health: HealthResult{LongestIdenticalRun: IdenticalRun{Length: 3}}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tripped, err := tc.check.evaluate(tc.src)
			if (err != nil) != tc.wantErr || tripped != tc.tripped {
				t.Fatalf("evaluate = (%v, %v), want (%v, error %v)", tripped, err, tc.tripped, tc.wantErr)
			}
		})
	}
}

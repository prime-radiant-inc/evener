package doctor

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// compareMetric: string type branches
// ---------------------------------------------------------------------------

func TestCompareMetricStringEqualMore(t *testing.T) {
	got, err := compareMetric("hello", "==", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected true for equal strings")
	}
}

func TestCompareMetricStringNotEqualMore(t *testing.T) {
	got, err := compareMetric("hello", "!=", "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected true for not-equal strings")
	}
}

func TestCompareMetricStringEqualFalseMore(t *testing.T) {
	got, err := compareMetric("hello", "==", "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("expected false for unequal strings with ==")
	}
}

func TestCompareMetricStringNotEqualFalseMore(t *testing.T) {
	got, err := compareMetric("hello", "!=", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("expected false for equal strings with !=")
	}
}

func TestCompareMetricStringInvalidOpMore(t *testing.T) {
	_, err := compareMetric("hello", ">", "world")
	if err == nil {
		t.Fatalf("expected error for invalid string operator")
	}
}

func TestCompareMetricStringNonStringValueMore(t *testing.T) {
	_, err := compareMetric("hello", "==", 42)
	if err == nil {
		t.Fatalf("expected error for non-string want with string actual")
	}
}

// ---------------------------------------------------------------------------
// metricSource.resolve: tool_errors path
// ---------------------------------------------------------------------------

func TestMetricSourceResolveToolErrorsMissingDot(t *testing.T) {
	m := metricSource{health: HealthResult{}}
	_, err := m.resolve("tool_errors.toolonly")
	if err == nil {
		t.Fatalf("expected error for tool_errors without class")
	}
}

func TestMetricSourceResolveToolErrorsEmptyTool(t *testing.T) {
	m := metricSource{health: HealthResult{}}
	_, err := m.resolve("tool_errors..class")
	if err == nil {
		t.Fatalf("expected error for empty tool name")
	}
}

func TestMetricSourceResolveToolErrorsEmptyClass(t *testing.T) {
	m := metricSource{health: HealthResult{}}
	_, err := m.resolve("tool_errors.tool.")
	if err == nil {
		t.Fatalf("expected error for empty class name")
	}
}

func TestMetricSourceResolveToolErrorsValid(t *testing.T) {
	m := metricSource{health: HealthResult{
		ToolErrors: map[string]map[string]int{
			"read_file": {"timeout": 3},
		},
	}}
	val, err := m.resolve("tool_errors.read_file.timeout")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 3 {
		t.Fatalf("expected 3, got %v", val)
	}
}

// ---------------------------------------------------------------------------
// compareMetric: int type with all operators
// ---------------------------------------------------------------------------

func TestCompareMetricIntGreaterThanOrEqualMore(t *testing.T) {
	got, err := compareMetric(5, ">=", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 5 >= 3 = true")
	}
}

func TestCompareMetricIntGreaterThanMore(t *testing.T) {
	got, err := compareMetric(5, ">", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 5 > 3 = true")
	}
}

func TestCompareMetricIntLessThanOrEqualMore(t *testing.T) {
	got, err := compareMetric(3, "<=", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 3 <= 5 = true")
	}
}

func TestCompareMetricIntLessThanMore(t *testing.T) {
	got, err := compareMetric(3, "<", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 3 < 5 = true")
	}
}

func TestCompareMetricIntEqualMore(t *testing.T) {
	// == is a valid op for int metrics (routes through compareFloat)
	got, err := compareMetric(5, "==", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("expected 5 == 3 = false")
	}
}

// ---------------------------------------------------------------------------
// compareMetric: float64/int64 want conversion
// ---------------------------------------------------------------------------

func TestCompareMetricIntWithFloat64WantMore(t *testing.T) {
	got, err := compareMetric(5, ">=", float64(3.0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 5 >= 3.0 = true")
	}
}

func TestCompareMetricIntWithInt64WantMore(t *testing.T) {
	got, err := compareMetric(5, ">=", int64(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 5 >= int64(3) = true")
	}
}

func TestCompareMetricIntWithInvalidWantMore(t *testing.T) {
	_, err := compareMetric(5, ">=", "not-a-number")
	if err == nil {
		t.Fatalf("expected error for non-numeric want")
	}
}

// ---------------------------------------------------------------------------
// compareMetric: unsupported actual type
// ---------------------------------------------------------------------------

func TestCompareMetricUnsupportedTypeMore(t *testing.T) {
	_, err := compareMetric([]int{1, 2}, "==", []int{1, 2})
	if err == nil {
		t.Fatalf("expected error for unsupported type")
	}
}

// ---------------------------------------------------------------------------
// compareFloat: all operators
// ---------------------------------------------------------------------------

func TestCompareFloatGreaterThanOrEqualMore(t *testing.T) {
	got, err := compareFloat(5.0, ">=", 5.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 5.0 >= 5.0 = true")
	}
}

func TestCompareFloatGreaterThanMore(t *testing.T) {
	got, err := compareFloat(5.0, ">", 3.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 5.0 > 3.0 = true")
	}
}

func TestCompareFloatLessThanOrEqualMore(t *testing.T) {
	got, err := compareFloat(3.0, "<=", 3.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 3.0 <= 3.0 = true")
	}
}

func TestCompareFloatLessThanMore(t *testing.T) {
	got, err := compareFloat(3.0, "<", 5.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected 3.0 < 5.0 = true")
	}
}

func TestCompareFloatInvalidOpMore(t *testing.T) {
	// == is valid for float; use a truly invalid operator
	_, err := compareFloat(3.0, "<>", 3.0)
	if err == nil {
		t.Fatalf("expected error for invalid float operator <>")
	}
}

// ---------------------------------------------------------------------------
// toFloat: all types
// ---------------------------------------------------------------------------

func TestToFloatIntMore(t *testing.T) {
	v, err := toFloat(42)
	if err != nil || v != 42.0 {
		t.Fatalf("toFloat(42) = %v err=%v", v, err)
	}
}

func TestToFloatInt64More(t *testing.T) {
	v, err := toFloat(int64(42))
	if err != nil || v != 42.0 {
		t.Fatalf("toFloat(int64(42)) = %v err=%v", v, err)
	}
}

func TestToFloatFloat64More(t *testing.T) {
	v, err := toFloat(42.5)
	if err != nil || v != 42.5 {
		t.Fatalf("toFloat(42.5) = %v err=%v", v, err)
	}
}

func TestToFloatInvalidTypeMore(t *testing.T) {
	_, err := toFloat("not a number")
	if err == nil {
		t.Fatalf("expected error for string")
	}
}

// ---------------------------------------------------------------------------
// appendUniqueString: duplicate detection
// ---------------------------------------------------------------------------

func TestAppendUniqueStringDuplicateMore(t *testing.T) {
	list := []string{"a", "b", "c"}
	result := appendUniqueString(list, "b")
	if len(result) != 3 {
		t.Fatalf("expected length 3, got %d", len(result))
	}
}

func TestAppendUniqueStringNewElementMore(t *testing.T) {
	list := []string{"a", "b", "c"}
	result := appendUniqueString(list, "d")
	if len(result) != 4 || result[3] != "d" {
		t.Fatalf("expected length 4 with 'd' at end, got %v", result)
	}
}

func TestAppendUniqueStringEmptyListMore(t *testing.T) {
	result := appendUniqueString(nil, "a")
	if len(result) != 1 || result[0] != "a" {
		t.Fatalf("expected ['a'], got %v", result)
	}
}

// ---------------------------------------------------------------------------
// slugify: edge cases
// ---------------------------------------------------------------------------

func TestSlugifyAllSpecialCharsMore(t *testing.T) {
	result := slugify("!!!@@@###")
	if result != "" {
		t.Fatalf("expected empty for all special chars, got %q", result)
	}
}

func TestSlugifyTrailingDashMore(t *testing.T) {
	result := slugify("hello world!!!")
	if result != "hello-world" {
		t.Fatalf("expected 'hello-world', got %q", result)
	}
}

func TestSlugifyLeadingSpecialCharsMore(t *testing.T) {
	result := slugify("!!!hello")
	if result != "hello" {
		t.Fatalf("expected 'hello', got %q", result)
	}
}

func TestSlugifyConsecutiveSpecialCharsMore(t *testing.T) {
	result := slugify("hello   world")
	if result != "hello-world" {
		t.Fatalf("expected 'hello-world', got %q", result)
	}
}

func TestSlugifyEmptyMore(t *testing.T) {
	if slugify("") != "" {
		t.Fatalf("expected empty for empty input")
	}
}

func TestSlugifyNumbersMore(t *testing.T) {
	result := slugify("test 123")
	if result != "test-123" {
		t.Fatalf("expected 'test-123', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// conditionsSummary: multiple conditions
// ---------------------------------------------------------------------------

func TestConditionsSummaryMultipleMore(t *testing.T) {
	conds := []auditCondition{
		{Metric: "apilog.errors", Op: ">=", Value: 10},
		{Metric: "apilog.calls", Op: ">", Value: 100},
	}
	result := conditionsSummary(conds)
	if result != "apilog.errors >= 10 && apilog.calls > 100" {
		t.Fatalf("conditionsSummary = %q", result)
	}
}

func TestConditionsSummaryEmptyMore(t *testing.T) {
	result := conditionsSummary(nil)
	if result != "" {
		t.Fatalf("expected empty for nil conditions, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// auditSignature: format verification
// ---------------------------------------------------------------------------

func TestAuditSignatureFormatMore(t *testing.T) {
	now := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	sig := auditSignature("myrunbook", "performance", "High Error Rate", now)
	if sig == "" {
		t.Fatalf("expected non-empty signature")
	}
	if !containsStr(sig, "myrunbook") {
		t.Fatalf("expected runbook in signature: %q", sig)
	}
	if !containsStr(sig, "performance") {
		t.Fatalf("expected category in signature: %q", sig)
	}
	if !containsStr(sig, "high-error-rate") {
		t.Fatalf("expected slugified title in signature: %q", sig)
	}
}

// ---------------------------------------------------------------------------
// truncate: edge cases
// ---------------------------------------------------------------------------

func TestTruncateShortStringMore(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Fatalf("expected 'hello' for short string")
	}
}

func TestTruncateExactLengthMore(t *testing.T) {
	if truncate("hello", 5) != "hello" {
		t.Fatalf("expected 'hello' for exact length")
	}
}

func TestTruncateLongStringMore(t *testing.T) {
	result := truncate("hello world this is long", 10)
	// truncate may add "..." so result can be slightly longer
	if len(result) > 13 {
		t.Fatalf("expected max ~13 chars (10 + ellipsis), got %d", len(result))
	}
	if !containsStr(result, "hello") {
		t.Fatalf("expected 'hello' in truncated result: %q", result)
	}
}

// ---------------------------------------------------------------------------
// RenderAudit: edge cases
// ---------------------------------------------------------------------------

func TestRenderAuditEmptyWithManualMore(t *testing.T) {
	r := AuditResult{
		Runbook: "test",
		Manual:  []string{"step 1", "step 2"},
	}
	out := RenderAudit(r)
	if !containsStr(out, "2 manual step(s)") {
		t.Fatalf("expected manual steps in output: %q", out)
	}
	if !containsStr(out, "step 1") {
		t.Fatalf("expected step 1 in output: %q", out)
	}
}

func TestRenderAuditWithUnreadableMore(t *testing.T) {
	r := AuditResult{
		Runbook: "test",
		Unreadable: []UnreadableSession{
			{SessionID: "sess_1", Error: "permission denied"},
		},
	}
	out := RenderAudit(r)
	if !containsStr(out, "could not be read") {
		t.Fatalf("expected 'could not be read' in output: %q", out)
	}
	if !containsStr(out, "permission denied") {
		t.Fatalf("expected error in output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// AuditCheck.evaluate: zero-value metric
// ---------------------------------------------------------------------------

func TestAuditCheckEvaluateZeroMetricMore(t *testing.T) {
	check := AuditCheck{
		Title: "test",
		Conditions: []auditCondition{
			{Metric: "apilog.calls", Op: ">=", Value: 0},
		},
	}
	source := metricSource{
		health:     HealthResult{},
		apilog:     APILogTotals{},
		haveAPILog: true,
	}
	got, err := check.evaluate(source)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// apilog.calls = 0, >= 0 is true
	if !got {
		t.Fatalf("expected 0 >= 0 = true")
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

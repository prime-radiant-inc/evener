package doctor

import (
	"testing"
)

// TestMetricSourceResolve_APIHealthUnknownMetric covers the "unknown metric"
// error path inside the apilog health metric resolver (audit.go:423).
func TestMetricSourceResolve_APIHealthUnknownMetric(t *testing.T) {
	t.Parallel()
	m := metricSource{
		haveAPIHealth: true,
		apiHealth:     APIHealthResult{},
	}
	_, err := m.resolve("apilog.unknown_metric")
	if err == nil {
		t.Fatal("unknown apilog metric should error")
	}
}

// TestMetricSourceResolve_APIHealthErrorsByClassEmpty covers the empty-class
// error path for apilog.errors_by_class (audit.go:418-419).
func TestMetricSourceResolve_APIHealthErrorsByClassEmpty(t *testing.T) {
	t.Parallel()
	m := metricSource{
		haveAPIHealth: true,
		apiHealth:     APIHealthResult{ErrorsByClass: map[string]int{}},
	}
	_, err := m.resolve("apilog.errors_by_class.")
	if err == nil {
		t.Fatal("empty class should error")
	}
}

// TestMetricSourceResolve_APIHealthErrorsByClass covers the happy path for
// apilog.errors_by_class.<class> (audit.go:421).
func TestMetricSourceResolve_APIHealthErrorsByClass(t *testing.T) {
	t.Parallel()
	m := metricSource{
		haveAPIHealth: true,
		apiHealth: APIHealthResult{
			ErrorsByClass: map[string]int{
				apiErrorClassQuota:     1,
				apiErrorClassPermanent: 2,
				apiErrorClassRetryable: 3,
			},
		},
	}
	for class, want := range m.apiHealth.ErrorsByClass {
		got, err := m.resolve("apilog.errors_by_class." + class)
		if err != nil || got != want {
			t.Errorf("apilog.errors_by_class.%s: got=%v err=%v, want=%v", class, got, err, want)
		}
	}
}

// TestMetricSourceResolve_APIHealthScalarMetrics covers the scalar apilog
// health metrics: recorded_empty, retry_storm_groups, unsettled_groups
// (audit.go:410-415).
func TestMetricSourceResolve_APIHealthScalarMetrics(t *testing.T) {
	t.Parallel()
	m := metricSource{
		haveAPIHealth: true,
		apiHealth: APIHealthResult{
			RecordedEmpty:    7,
			RetryStormGroups: 2,
			UnsettledGroups:  3,
		},
	}
	got, err := m.resolve("apilog.recorded_empty")
	if err != nil || got != 7 {
		t.Errorf("recorded_empty: got=%v err=%v, want 7", got, err)
	}
	got, err = m.resolve("apilog.retry_storm_groups")
	if err != nil || got != 2 {
		t.Errorf("retry_storm_groups: got=%v err=%v, want 2", got, err)
	}
	got, err = m.resolve("apilog.unsettled_groups")
	if err != nil || got != 3 {
		t.Errorf("unsettled_groups: got=%v err=%v, want 3", got, err)
	}
}

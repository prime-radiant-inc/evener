package main

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/hubapi"
)

// TestWorkspaceDataFromAppThreadCarriesWorkMetrics asserts that
// thread.Serf.{Usage,WorkMillis,ActiveTurnStartedAt} (WS2) flow onto
// WorkspaceData, the same way Goal already does (TestHubDetailFromAppThreadCarriesGoal
// in web_test.go covers the analogous hubapi.SessionDetail mapping).
func TestWorkspaceDataFromAppThreadCarriesWorkMetrics(t *testing.T) {
	usage := &appwire.SerfUsage{InputTokens: 12, OutputTokens: 34, CacheReadTokens: 5, TotalTokens: 46}
	wd := workspaceDataFromAppThread(appwire.Thread{
		ID:     "th_metrics",
		Source: "local",
		Status: appwire.ThreadStatus{Type: "idle"},
		Serf: appwire.SerfThread{
			Ref:                 "local:th_metrics",
			Usage:               usage,
			WorkMillis:          9000,
			ActiveTurnStartedAt: 1_700_000_000,
		},
	})
	if wd.WorkMillis != 9000 {
		t.Fatalf("WorkMillis=%d, want 9000", wd.WorkMillis)
	}
	if wd.ActiveTurnStartedAt != 1_700_000_000 {
		t.Fatalf("ActiveTurnStartedAt=%d, want 1700000000", wd.ActiveTurnStartedAt)
	}
	if wd.Usage == nil {
		t.Fatalf("Usage=nil, want %+v", usage)
	}
	if *wd.Usage != *usage {
		t.Fatalf("Usage=%+v, want %+v", wd.Usage, usage)
	}
}

// TestSerfUsageFromCumulative pins the nil-when-zero convention (mirrors
// serfUsageFromLLM in cmd/serf/serve.go): an all-zero CumulativeUsage — a
// fresh session or a meta written before WS2 — must map to a nil
// *appwire.SerfUsage so the usage cluster hides rather than rendering ↑0 ↓0.
func TestSerfUsageFromCumulative(t *testing.T) {
	if got := serfUsageFromCumulative(schema.CumulativeUsage{}); got != nil {
		t.Fatalf("serfUsageFromCumulative(zero) = %+v, want nil", got)
	}

	got := serfUsageFromCumulative(schema.CumulativeUsage{
		InputTokens:     100,
		OutputTokens:    50,
		CacheReadTokens: 10,
		TotalTokens:     150,
	})
	want := &appwire.SerfUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 150}
	if got == nil || *got != *want {
		t.Fatalf("serfUsageFromCumulative = %+v, want %+v", got, want)
	}
}

// TestHubUsageFromAppwire pins hubUsageFromAppwire's nil-safety (a thread with
// no token data carries a nil *appwire.SerfUsage) and its field-for-field
// mapping into hubapi's flattened Usage type.
func TestHubUsageFromAppwire(t *testing.T) {
	if got := hubUsageFromAppwire(nil); got != nil {
		t.Fatalf("hubUsageFromAppwire(nil) = %+v, want nil", got)
	}

	got := hubUsageFromAppwire(&appwire.SerfUsage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, TotalTokens: 6})
	want := &hubapi.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, TotalTokens: 6}
	if got == nil || *got != *want {
		t.Fatalf("hubUsageFromAppwire = %+v, want %+v", got, want)
	}
}

func TestStateLabel_UnifiedVocabulary(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       string
	}{
		{"active", false, "Working"},
		{"awaiting", false, "Your move"},
		{"awaiting", true, "Question waiting"},
		{"warning", false, "Warning"},
		{"errored", false, "Error"},
		{"idle", false, "Idle"},
	}
	for _, c := range cases {
		if got := stateLabel(c.state, c.askPending); got != c.want {
			t.Errorf("stateLabel(%q, %v) = %q, want %q", c.state, c.askPending, got, c.want)
		}
	}
}

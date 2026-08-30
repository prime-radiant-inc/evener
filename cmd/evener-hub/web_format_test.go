package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// TestActiveTurnRunningForReadsStartedAtAsMillis locks the wire unit for
// appwire.Turn.StartedAt: it is epoch-MILLISECONDS (the appprojector/apptranscript
// stamps emit UnixMilli, and the web reducer reads epoch-ms), so the
// server-rendered "running for" duration must read it via time.UnixMilli. Reading
// an ms value as seconds places the start ~55000 CE, a negative elapsed the
// compactDuration clamp floors to "1s" — never the true 2m span.
func TestActiveTurnRunningForReadsStartedAtAsMillis(t *testing.T) {
	startedMs := time.Now().Add(-2 * time.Minute).UnixMilli()
	thread := appwire.Thread{
		Turns: []appwire.Turn{{
			ID:        "turn_1",
			Status:    appwire.TurnStatusInProgress,
			StartedAt: &startedMs,
		}},
	}
	if got := activeTurnRunningFor(thread); got != "2m" {
		t.Fatalf("activeTurnRunningFor = %q, want \"2m\" (Turn.StartedAt is epoch-ms)", got)
	}
}

// TestWorkspaceDataFromAppThreadCarriesWorkMetrics asserts that
// thread.Evener.{Usage,WorkMillis,ActiveTurnStartedAt} (WS2) flow onto
// WorkspaceData alongside the other AppWire-derived session projection fields.
func TestWorkspaceDataFromAppThreadCarriesWorkMetrics(t *testing.T) {
	usage := &appwire.EvenerUsage{InputTokens: 12, OutputTokens: 34, CacheReadTokens: 5, TotalTokens: 46}
	wd := workspaceDataFromAppThread(appwire.Thread{
		ID:     "th_metrics",
		Source: "local",
		Status: appwire.ThreadStatus{Type: "idle"},
		Evener: appwire.EvenerThread{
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

// TestWorkspaceDataFromAppThread_CarriesCostEstimate verifies the
// remote/appwire workspace path renders the cost the daemon delivered on
// thread.Evener.Cost rather than re-deriving one of its own (spec §7.5: the
// daemon prices from its session's registry, which the web cannot see).
func TestWorkspaceDataFromAppThread_CarriesCostEstimate(t *testing.T) {
	wd := workspaceDataFromAppThread(appwire.Thread{
		ID:            "th_cost",
		Source:        "local",
		Status:        appwire.ThreadStatus{Type: "idle"},
		ModelProvider: "claude-opus-4-5",
		Evener: appwire.EvenerThread{
			Ref:   "local:th_cost",
			Usage: &appwire.EvenerUsage{InputTokens: 100_000, OutputTokens: 20_000},
			Cost:  "~$1.00",
		},
	})
	if wd.Cost != "~$1.00" {
		t.Fatalf("Cost = %q, want ~$1.00", wd.Cost)
	}
}

// TestWorkspaceDataFromAppThread_NoDaemonCostRendersNothing pins the flag-day
// rule (spec §14.1): a daemon that reported no cost leaves the chip empty; the
// web never invents one from a bundled pricing table.
func TestWorkspaceDataFromAppThread_NoDaemonCostRendersNothing(t *testing.T) {
	wd := workspaceDataFromAppThread(appwire.Thread{
		ID:            "th_cost",
		Source:        "local",
		Status:        appwire.ThreadStatus{Type: "idle"},
		ModelProvider: "claude-opus-4-5",
		Evener: appwire.EvenerThread{
			Ref:   "local:th_cost",
			Usage: &appwire.EvenerUsage{InputTokens: 100_000, OutputTokens: 20_000},
		},
	})
	if wd.Cost != "" {
		t.Fatalf("Cost = %q, want empty when the daemon reported none", wd.Cost)
	}
}

// TestEvenerUsageFromCumulative pins the nil-when-zero convention (mirrors
// evenerUsageFromLLM in cmd/evener/serve.go): an all-zero CumulativeUsage — a
// fresh session or a meta written before WS2 — must map to a nil
// *appwire.EvenerUsage so the usage cluster hides rather than rendering ↑0 ↓0.
func TestEvenerUsageFromCumulative(t *testing.T) {
	if got := evenerUsageFromCumulative(schema.CumulativeUsage{}); got != nil {
		t.Fatalf("evenerUsageFromCumulative(zero) = %+v, want nil", got)
	}

	got := evenerUsageFromCumulative(schema.CumulativeUsage{
		InputTokens:     100,
		OutputTokens:    50,
		CacheReadTokens: 10,
		TotalTokens:     150,
	})
	want := &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 150}
	if got == nil || *got != *want {
		t.Fatalf("evenerUsageFromCumulative = %+v, want %+v", got, want)
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

package launchconfig

import (
	"testing"
)

// TestMergeLayersMaxConcurrentDelegateTurns covers the MaxConcurrentDelegateTurns
// merge path (line 130-134).
func TestMergeLayersMaxConcurrentDelegateTurns(t *testing.T) {
	l := Layer{MaxConcurrentDelegateTurns: ptrInt(4)}
	got, diags := mergeLayers(map[LayerName]Layer{
		LayerLaunch: l,
	})
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got.Effective.MaxConcurrentDelegateTurns == nil || *got.Effective.MaxConcurrentDelegateTurns != 4 {
		t.Fatalf("MaxConcurrentDelegateTurns = %v, want 4", got.Effective.MaxConcurrentDelegateTurns)
	}
	if got.Provenance["max_concurrent_delegate_turns"] != LayerLaunch {
		t.Fatalf("provenance for max_concurrent_delegate_turns = %v, want launch", got.Provenance["max_concurrent_delegate_turns"])
	}
}

// TestMergeLayersMaxRetainedTerminal covers the MaxRetainedTerminal merge path
// (line 136-140).
func TestMergeLayersMaxRetainedTerminal(t *testing.T) {
	l := Layer{MaxRetainedTerminal: ptrInt(10)}
	got, diags := mergeLayers(map[LayerName]Layer{
		LayerLaunch: l,
	})
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got.Effective.MaxRetainedTerminal == nil || *got.Effective.MaxRetainedTerminal != 10 {
		t.Fatalf("MaxRetainedTerminal = %v, want 10", got.Effective.MaxRetainedTerminal)
	}
	if got.Provenance["max_retained_terminal"] != LayerLaunch {
		t.Fatalf("provenance for max_retained_terminal = %v, want launch", got.Provenance["max_retained_terminal"])
	}
}

// TestMergeLayersBothOptionalFields covers both fields set together.
func TestMergeLayersBothOptionalFields(t *testing.T) {
	l := Layer{MaxConcurrentDelegateTurns: ptrInt(2), MaxRetainedTerminal: ptrInt(5)}
	got, _ := mergeLayers(map[LayerName]Layer{
		LayerLaunch: l,
	})
	if got.Effective.MaxConcurrentDelegateTurns == nil || *got.Effective.MaxConcurrentDelegateTurns != 2 {
		t.Fatalf("MaxConcurrentDelegateTurns = %v, want 2", got.Effective.MaxConcurrentDelegateTurns)
	}
	if got.Effective.MaxRetainedTerminal == nil || *got.Effective.MaxRetainedTerminal != 5 {
		t.Fatalf("MaxRetainedTerminal = %v, want 5", got.Effective.MaxRetainedTerminal)
	}
}

func ptrInt(v int) *int { return &v }

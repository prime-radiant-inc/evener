package registry

import "testing"

// ReasoningEnabled: nil is "unknown", which is not disabled-from-reasoning.
func TestReasoningEnabled(t *testing.T) {
	t.Parallel()
	off := false
	on := true
	for _, tc := range []struct {
		name string
		caps Caps
		want bool
	}{
		{"nil verdict is unknown", Caps{}, true},
		{"explicit on", Caps{Reasoning: &on}, true},
		{"explicit off", Caps{Reasoning: &off}, false},
	} {
		if got := tc.caps.ReasoningEnabled(); got != tc.want {
			t.Errorf("%s: ReasoningEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// RequestEffortCapable: a row that does not reason takes no effort; a
// budget-shaped row sizes its budget from one; a row with no controls and
// no verdict is an unknown row that still accepts a level.
func TestRequestEffortCapable(t *testing.T) {
	t.Parallel()
	off := false
	unsupported := true
	for _, tc := range []struct {
		name string
		caps Caps
		want bool
	}{
		{"reasoning off", Caps{Reasoning: &off}, false},
		{"budget shaped", Caps{ReasoningControls: []string{"budget_tokens"}}, true},
		{"effort control", Caps{ReasoningControls: []string{"effort"}}, true},
		{"no effort parameter", Caps{EffortParameterUnsupported: &unsupported, ReasoningControls: []string{"toggle"}}, false},
		{"unknown row", Caps{}, true},
	} {
		if got := tc.caps.RequestEffortCapable(); got != tc.want {
			t.Errorf("%s: RequestEffortCapable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

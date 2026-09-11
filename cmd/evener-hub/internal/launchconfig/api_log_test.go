package launchconfig

import (
	"reflect"
	"testing"
)

// checkToArgs_APILog: api_log is a tri-state — nil emits nothing (evener's own
// default, off), true emits on, and false emits off explicitly so a higher
// layer can override a global default back to off.
func checkToArgs_APILog(t *testing.T) {
	cases := []struct {
		name  string
		layer Layer
		want  []string
	}{
		{"unset", Layer{}, nil},
		{"on", Layer{APILog: new(true)}, []string{"--api-log", "on"}},
		{"explicit off", Layer{APILog: new(false)}, []string{"--api-log", "off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToArgs(Resolved{Effective: tc.layer})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ToArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// checkMerge_APILog: higher layers replace lower ones in both directions, and
// an unset layer leaves a lower setting intact.
func checkMerge_APILog(t *testing.T) {
	globalOn := true
	launchOff := false
	launchOn := true

	resolved, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal: {APILog: &globalOn},
		LayerLaunch: {APILog: &launchOff},
	})
	if resolved.Effective.APILog == nil || *resolved.Effective.APILog {
		t.Fatalf("launch off over global on = %v, want false", resolved.Effective.APILog)
	}
	if resolved.Provenance["api_log"] != LayerLaunch {
		t.Fatalf("api_log provenance = %v, want launch", resolved.Provenance["api_log"])
	}

	resolved, _ = mergeLayers(map[LayerName]Layer{
		LayerGlobal: {APILog: &globalOn},
	})
	if resolved.Effective.APILog == nil || !*resolved.Effective.APILog {
		t.Fatalf("global on alone = %v, want true", resolved.Effective.APILog)
	}

	resolved, _ = mergeLayers(map[LayerName]Layer{
		LayerGlobal: {APILog: &globalOn},
		LayerLaunch: {APILog: &launchOn},
	})
	if resolved.Effective.APILog == nil || !*resolved.Effective.APILog {
		t.Fatalf("launch on over global on = %v, want true", resolved.Effective.APILog)
	}

	resolved, _ = mergeLayers(map[LayerName]Layer{})
	if resolved.Effective.APILog != nil {
		t.Fatalf("no layers set api_log = %v, want nil", resolved.Effective.APILog)
	}
}

// checkWire_APILogRoundTrips: all three states (unset, true, false) survive
// FromWire∘ToWire — an explicit false must not collapse into unset.
func checkWire_APILogRoundTrips(t *testing.T) {
	for _, value := range []*bool{nil, new(false), new(true)} {
		in := Layer{APILog: value}
		got := FromWire(ToWire(in))
		if !boolPtrEq(got.APILog, value) {
			t.Errorf("APILog round trip = %v, want %v", got.APILog, value)
		}
	}
}

// checkApplyRuntimeDefaults_APILog pins the builtin default: an unset api_log
// resolves to false (logging is opt-in).
func checkApplyRuntimeDefaults_APILog(t *testing.T) {
	resolved := ApplyRuntimeDefaults(Resolved{}, func(string) string { return "" }, LaunchOptionSchema())
	if resolved.Effective.APILog == nil || *resolved.Effective.APILog {
		t.Fatalf("builtin api_log = %v, want false", resolved.Effective.APILog)
	}
	if resolved.Provenance["api_log"] != LayerBuiltin {
		t.Fatalf("api_log provenance = %v, want builtin", resolved.Provenance["api_log"])
	}
}

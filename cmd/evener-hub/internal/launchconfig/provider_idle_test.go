package launchconfig

import (
	"encoding/json"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestProviderIdleLaunchConfig(t *testing.T) {
	var wire appwire.LaunchConfigLayer
	if err := json.Unmarshal([]byte(`{"providerIdleTimeout":"15m"}`), &wire); err != nil {
		t.Fatal(err)
	}
	layer := FromWire(wire)
	resolved, _ := mergeLayers(map[LayerName]Layer{LayerLaunch: layer})
	args := ToArgs(resolved)
	index := slices.Index(args, "--provider-idle-timeout")
	if index < 0 || index+1 >= len(args) || args[index+1] != "15m" {
		t.Fatalf("launch args=%v", args)
	}
	back := ToWire(resolved.Effective)
	raw, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	json.Unmarshal(raw, &values)
	if values["providerIdleTimeout"] != "15m" {
		t.Fatalf("wire roundtrip=%s", raw)
	}
	for _, option := range LaunchOptionSchema() {
		if option.Field == "provider_idle_timeout" {
			if option.WireField != "providerIdleTimeout" || option.BuiltinDefault != "10m" {
				t.Fatalf("schema=%+v", option)
			}
			return
		}
	}
	t.Fatal("provider idle duration missing from launch settings schema")
}

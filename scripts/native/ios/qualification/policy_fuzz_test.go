//go:build linux || darwin

package main

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// FuzzSimulatorSelection varies a candidate before a known-good fallback.
// The oracle requires the eligible candidate, or the fallback when any required
// field is absent. Disabling every runtime must leave no selectable device.
// This exercises parsing and selection only, without invoking Mac tools.
func FuzzSimulatorSelection(f *testing.F) {
	f.Add("runtime", "device", "iPhone", true)
	f.Add("runtime", "device", "iPhone", false)
	f.Add("", "device", "iPhone", true)
	f.Add("runtime", "", "iPhone", true)
	f.Add("runtime", "device", "iPad", true)
	f.Add("runtime", "device", "", true)
	f.Add("运行时", "设备", "iPhone", true)
	f.Fuzz(func(t *testing.T, runtimeID, deviceID, family string, available bool) {
		for _, value := range []string{runtimeID, deviceID, family} {
			if len(value) > 4096 || !utf8.ValidString(value) {
				t.Skip()
			}
		}
		runtimes := []map[string]any{
			{
				"identifier": runtimeID, "isAvailable": available,
				"supportedDeviceTypes": []map[string]string{{"identifier": deviceID, "productFamily": family}},
			},
			{
				"identifier": "fallback-runtime", "isAvailable": true,
				"supportedDeviceTypes": []map[string]string{{"identifier": "fallback-device", "productFamily": "iPhone"}},
			},
		}
		listing := map[string]any{"runtimes": runtimes}
		data, err := json.Marshal(listing)
		if err != nil {
			t.Fatal(err)
		}
		wantRuntime, wantDevice := "fallback-runtime", "fallback-device"
		if available && runtimeID != "" && deviceID != "" && family == "iPhone" {
			wantRuntime, wantDevice = runtimeID, deviceID
		}
		gotRuntime, gotDevice, err := selectSimulator(data)
		if err != nil || gotRuntime != wantRuntime || gotDevice != wantDevice {
			t.Fatalf("selection = (%q, %q, %v), want (%q, %q)", gotRuntime, gotDevice, err, wantRuntime, wantDevice)
		}
		for _, runtime := range runtimes {
			runtime["isAvailable"] = false
		}
		data, err = json.Marshal(listing)
		if err != nil {
			t.Fatal(err)
		}
		gotRuntime, gotDevice, err = selectSimulator(data)
		if err == nil || gotRuntime != "" || gotDevice != "" {
			t.Fatalf("unavailable runtimes selected (%q, %q, %v)", gotRuntime, gotDevice, err)
		}
	})
}

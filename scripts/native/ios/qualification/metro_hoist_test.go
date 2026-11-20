//go:build linux || darwin

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The staging directory must sit inside the run output: a failed experiment
// keeps its bundle there, and a path outside --output would escape cleanup.
func TestMetroHoistStagingStaysInOutput(t *testing.T) {
	output := "/tmp/qualification-run"
	staging := metroHoistStaging(output)
	if staging != filepath.Join(output, "metro-hoist") {
		t.Fatalf("staging = %q, want %q", staging, filepath.Join(output, "metro-hoist"))
	}
	if filepath.Dir(staging) != output {
		t.Fatalf("staging parent = %q, want %q", filepath.Dir(staging), output)
	}
}

// The patch must name the project, the host target, and absolute staging and
// emit paths, because the script aborts on a missing flag or a relative path.
func TestMetroHoistArgsCarryThePatchContract(t *testing.T) {
	args := metroHoistArgs("/repo", "/tmp/run")
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			flags[args[i]] = args[i+1]
		}
	}
	staging := filepath.Join("/tmp/run", "metro-hoist")
	want := map[string]string{
		"--app-project": "ios/Evener.xcodeproj",
		"--host-target": "Evener",
		"--staging":     staging,
		"--emit":        "/tmp/run/metro-hoist.sh",
	}
	for flag, value := range want {
		if flags[flag] != value {
			t.Errorf("%s = %q, want %q", flag, flags[flag], value)
		}
	}
	if len(args) < 4 || args[0] != "bundle" || args[1] != "exec" || args[2] != "ruby" {
		t.Fatalf("args prefix = %v, want bundle exec ruby", args)
	}
	if want := filepath.Join("/repo", "scripts", "native", "ios", "qualification", "metro_hoist.rb"); args[3] != want {
		t.Fatalf("script = %q, want %q", args[3], want)
	}
}

// The hoisted script runs outside xcodebuild, so the environment must carry the
// app target's own settings and redirect only the three hoist values.
func TestMetroHoistEnvUsesHostSettings(t *testing.T) {
	settings := map[string]string{
		"SRCROOT":           "/repo/mobile-native/ios",
		"PODS_ROOT":         "/repo/mobile-native/ios/Pods",
		"CONFIGURATION":     "Release",
		"PLATFORM_NAME":     "iphonesimulator",
		"PRODUCT_NAME":      "Evener",
		"UNRELATED_SETTING": "ignored",
	}
	env := metroHoistEnv(settings, "/tmp/run/metro-hoist")
	got := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", entry)
		}
		got[key] = value
	}
	for key, value := range settings {
		if key == "UNRELATED_SETTING" {
			if _, found := got[key]; found {
				t.Errorf("%s leaked into the hoist environment", key)
			}
			continue
		}
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if got["CONFIGURATION_BUILD_DIR"] != "/tmp/run/metro-hoist" {
		t.Errorf("CONFIGURATION_BUILD_DIR = %q, want the staging directory", got["CONFIGURATION_BUILD_DIR"])
	}
	if value, found := got["UNLOCALIZED_RESOURCES_FOLDER_PATH"]; !found || value != "" {
		t.Errorf("UNLOCALIZED_RESOURCES_FOLDER_PATH = %q (found %v), want an empty value", value, found)
	}
}

// mergeEnv must drop inherited values the hoist replaces: getenv takes the
// first match, so a stale PODS_ROOT would win otherwise.
func TestMergeEnvReplacesInheritedValues(t *testing.T) {
	base := []string{"PATH=/usr/bin", "PODS_ROOT=/stale", "HOME=/home/runner"}
	merged := mergeEnv(base, []string{"PODS_ROOT=/fresh", "CONFIGURATION_BUILD_DIR=/tmp/stage"})
	if len(merged) != 4 {
		t.Fatalf("merged = %v, want 4 entries", merged)
	}
	if merged[0] != "PATH=/usr/bin" || merged[1] != "HOME=/home/runner" {
		t.Fatalf("merged base = %v, want PATH and HOME preserved", merged[:2])
	}
	if merged[2] != "PODS_ROOT=/fresh" || merged[3] != "CONFIGURATION_BUILD_DIR=/tmp/stage" {
		t.Fatalf("merged overrides = %v, want the replacements last", merged[2:])
	}
}

// The hoist window must print with its elapsed seconds and skip every line the
// hoist did not write, so the job log carries the tail comparison directly.
func TestPrintHoistWindowEchoesMarkers(t *testing.T) {
	log := strings.Join([]string{
		"1.000\tBuild settings",
		"3.500\tE16 metro-hoist: begin 14:28:43",
		"705.400\tE16 metro-hoist: done 14:40:25",
		"770.000\tE16 metro-copy: waiting for /tmp/x/.complete",
		"770.100\tE16 metro-copy: copied 18 entries",
	}, "\n")
	var buf bytes.Buffer
	printHoistWindow(&buf, []byte(log))
	got := buf.String()
	for _, want := range []string{
		"build-timings: hoist 3.500 E16 metro-hoist: begin 14:28:43",
		"build-timings: hoist 705.400 E16 metro-hoist: done 14:40:25",
		"build-timings: hoist 770.000 E16 metro-copy: waiting for /tmp/x/.complete",
		"build-timings: hoist 770.100 E16 metro-copy: copied 18 entries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hoist window missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Build settings") {
		t.Errorf("hoist window echoed an unrelated line; got:\n%s", got)
	}
}

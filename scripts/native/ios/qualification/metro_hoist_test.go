//go:build linux || darwin

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// testRepoRoot and testRunDir keep the absolute paths out of the call sites:
// gocritic's filepathJoin flags a separator inside a literal Join argument.
const (
	testRepoRoot = "/repo"
	testRunDir   = "/tmp/run"
)

// The staging directory must sit inside the run output: a failed run keeps its
// bundle there, and a path outside --output would escape cleanup.
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
	args := metroHoistArgs(testRepoRoot, testRunDir)
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			flags[args[i]] = args[i+1]
		}
	}
	staging := filepath.Join(testRunDir, "metro-hoist")
	want := map[string]string{
		"--app-project": "ios/Evener.xcodeproj",
		"--host-target": "Evener",
		"--staging":     staging,
		"--emit":        filepath.Join(testRunDir, "metro-hoist.sh"),
	}
	for flag, value := range want {
		if flags[flag] != value {
			t.Errorf("%s = %q, want %q", flag, flags[flag], value)
		}
	}
	if len(args) < 4 || args[0] != "bundle" || args[1] != "exec" || args[2] != "ruby" {
		t.Fatalf("args prefix = %v, want bundle exec ruby", args)
	}
	if want := filepath.Join(testRepoRoot, "scripts", "native", "ios", "qualification", "metro_hoist.rb"); args[3] != want {
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
	got := env
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

// The overlay must replace inherited values the hoist sets: getenv takes the
// first match, so a stale PODS_ROOT would win otherwise.
func TestHoistEnvOverridesInheritedValues(t *testing.T) {
	base := []string{"PATH=/usr/bin", "PODS_ROOT=/stale", "HOME=/home/runner"}
	merged := envvars.ApplyOverlay(base, metroHoistEnv(map[string]string{"PODS_ROOT": "/fresh"}, "/tmp/stage"))
	if len(merged) != 6 {
		t.Fatalf("merged = %v, want 6 entries", merged)
	}
	if merged[0] != "PATH=/usr/bin" || merged[2] != "HOME=/home/runner" {
		t.Fatalf("merged base = %v, want PATH and HOME preserved in place", merged[:3])
	}
	if merged[1] != "PODS_ROOT=/fresh" {
		t.Fatalf("merged[1] = %q, want the fresh PODS_ROOT in place", merged[1])
	}
	for _, want := range []string{"CONFIGURATION_BUILD_DIR=/tmp/stage", "DERIVED_FILE_DIR=/tmp/metro-hoist-derived", "UNLOCALIZED_RESOURCES_FOLDER_PATH="} {
		if !slices.Contains(merged, want) {
			t.Errorf("merged = %v, want %q", merged, want)
		}
	}
}

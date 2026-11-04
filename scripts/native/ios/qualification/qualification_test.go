//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// These tests catch accepting a changed/missing lock, selecting an unavailable
// or non-iPhone runtime, and accepting a bundle without its shared capability.
// They exercise real files and our policy, not replacement Apple executables.
func TestLockComparisonPreservesCandidateAndOriginal(t *testing.T) {
	t.Parallel()
	for _, candidate := range []string{"PODS:\n  - Expo (58.0.23)\n", ""} {
		t.Run(candidate, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			old := filepath.Join(dir, "committed.lock")
			resolved := filepath.Join(dir, "resolved.lock")
			out := filepath.Join(dir, "evidence")
			original := []byte("PODS:\n  - Expo (57.0.23)\n")
			writeTestFile(t, old, original)
			if candidate != "" {
				writeTestFile(t, resolved, []byte(candidate))
			}
			if err := checkLock(context.Background(), old, resolved, out); !errors.Is(err, errLockMismatch) {
				t.Fatalf("expected lock mismatch, got %v", err)
			}
			got, err := os.ReadFile(old)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("committed input changed: %q, %v", got, err)
			}
			if candidate != "" {
				got, err = os.ReadFile(filepath.Join(out, "Podfile.lock.candidate"))
				if err != nil || string(got) != candidate {
					t.Fatalf("candidate lost: %q, %v", got, err)
				}
				diff, err := os.ReadFile(filepath.Join(out, "Podfile.lock.diff"))
				if err != nil || len(diff) == 0 {
					t.Fatalf("missing review diff: %v", err)
				}
			}
		})
	}
}

func TestMatchingLockStillRetainsEvidence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old, resolved := filepath.Join(dir, "old"), filepath.Join(dir, "resolved")
	data := []byte("PODS:\n  - Expo (58.0.23)\n")
	writeTestFile(t, old, data)
	writeTestFile(t, resolved, data)
	if err := checkLock(context.Background(), old, resolved, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "Podfile.lock.candidate"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("matching resolution evidence lost: %q, %v", got, err)
	}
}

func TestSimulatorRequiresAvailableCompatibleIPhone(t *testing.T) {
	t.Parallel()
	// The structured runtime fields come from simctl list --json, including
	// supportedDeviceTypes. IDs are opaque fixture tokens, not device names.
	input := []byte(`{"runtimes":[
		{"identifier":"old","isAvailable":false,"supportedDeviceTypes":[{"identifier":"phone-old","productFamily":"iPhone"}]},
		{"identifier":"watch","isAvailable":true,"supportedDeviceTypes":[{"identifier":"watch-type","productFamily":"Apple Watch"}]},
		{"identifier":"ios","isAvailable":true,"supportedDeviceTypes":[{"identifier":"tablet","productFamily":"iPad"},{"identifier":"phone","productFamily":"iPhone"}]}
	]}`)
	runtimeID, deviceID, err := selectSimulator(input)
	if err != nil || runtimeID != "ios" || deviceID != "phone" {
		t.Fatalf("selected %q/%q: %v", runtimeID, deviceID, err)
	}
	for _, bad := range [][]byte{[]byte(`{"runtimes":[]}`), []byte(`{"runtimes":[{"identifier":"ios","isAvailable":true}]}`), []byte(`{`)} {
		if _, _, err := selectSimulator(bad); err == nil {
			t.Fatalf("accepted unusable runtime data %s", bad)
		}
	}
}

func TestBundleMetadataRequiresIdentityAndSharedGroup(t *testing.T) {
	t.Parallel()
	info := map[string]any{"CFBundleIdentifier": "com.primeradiant.evener.native", "MinimumOSVersion": "16.4", "UIDeviceFamily": []any{float64(1)}, "CFBundleURLTypes": []any{map[string]any{"CFBundleURLSchemes": []any{"evener-native"}}}}
	entitlements := map[string]any{"com.apple.security.application-groups": []any{"group.com.primeradiant.evener.native"}}
	if err := validateBundleMetadata(info, entitlements, false); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any, map[string]any){
		func(i, _ map[string]any) { i["CFBundleIdentifier"] = "other.app" },
		func(i, _ map[string]any) { i["UIDeviceFamily"] = []any{float64(1), float64(2)} },
		func(i, _ map[string]any) { i["MinimumOSVersion"] = "17.0" },
		func(i, _ map[string]any) { delete(i, "CFBundleURLTypes") },
		func(_, e map[string]any) { delete(e, "com.apple.security.application-groups") },
		func(_, e map[string]any) { e["com.apple.security.application-groups"] = []any{"other.group"} },
	} {
		i, e := cloneTestMap(t, info), cloneTestMap(t, entitlements)
		mutate(i, e)
		if err := validateBundleMetadata(i, e, false); !errors.Is(err, errIdentity) {
			t.Fatalf("accepted invalid metadata: %v, %v, %v", i, e, err)
		}
	}
	share := map[string]any{"CFBundleIdentifier": "com.primeradiant.evener.native.ShareExtension", "MinimumOSVersion": "16.4", "UIDeviceFamily": []any{float64(1)}, "NSExtension": map[string]any{"NSExtensionPointIdentifier": "com.apple.share-services"}}
	if err := validateBundleMetadata(share, entitlements, true); err != nil {
		t.Fatal(err)
	}
	delete(share, "NSExtension")
	if err := validateBundleMetadata(share, entitlements, true); !errors.Is(err, errIdentity) {
		t.Fatalf("accepted extension without share service: %v", err)
	}
}

func TestLinuxRefusesBeforeCreatingOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux capability refusal")
	}
	t.Parallel()
	out := filepath.Join(t.TempDir(), "not-created")
	if err := qualify(context.Background(), out); !errors.Is(err, errMacRequired) {
		t.Fatalf("expected Mac prerequisite refusal, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation/output began on Linux: %v", err)
	}
}

func TestGeneratedSettingsDoNotHideConflictingPlist(t *testing.T) {
	t.Parallel()
	settings := map[string]string{
		"PRODUCT_BUNDLE_IDENTIFIER":  "com.primeradiant.evener.native",
		"IPHONEOS_DEPLOYMENT_TARGET": "16.4", "TARGETED_DEVICE_FAMILY": "1", "DEVELOPMENT_TEAM": "EVENERCI01",
	}
	entitlements := map[string]any{"com.apple.security.application-groups": []any{"group.com.primeradiant.evener.native"}}
	info := map[string]any{"CFBundleIdentifier": "$(PRODUCT_BUNDLE_IDENTIFIER)", "CFBundleURLTypes": []any{map[string]any{"CFBundleURLSchemes": []any{"evener-native"}}}}
	if err := validateGeneratedSettings(cloneTestMap(t, info), entitlements, settings, false); err != nil {
		t.Fatal(err)
	}
	for _, conflict := range []map[string]any{
		{"CFBundleIdentifier": "other.app"},
		{"CFBundleIdentifier": nil},
		{"MinimumOSVersion": "17.0"},
		{"UIDeviceFamily": []any{float64(2)}},
	} {
		changed := cloneTestMap(t, info)
		maps.Copy(changed, conflict)
		if err := validateGeneratedSettings(changed, entitlements, settings, false); !errors.Is(err, errIdentity) {
			t.Fatalf("generated plist conflict was hidden: %v, %v", conflict, err)
		}
	}
}

func TestGeneratedShareSettingsUseBuildIdentifier(t *testing.T) {
	t.Parallel()
	info := map[string]any{"NSExtension": map[string]any{"NSExtensionPointIdentifier": "com.apple.share-services"}}
	entitlements := map[string]any{"com.apple.security.application-groups": []any{"group.com.primeradiant.evener.native"}}
	for _, identifier := range []string{"com.primeradiant.evener.native.ShareExtension", "other.app", ""} {
		settings := map[string]string{
			"PRODUCT_BUNDLE_IDENTIFIER":  identifier,
			"IPHONEOS_DEPLOYMENT_TARGET": "16.4", "TARGETED_DEVICE_FAMILY": "1", "DEVELOPMENT_TEAM": "EVENERCI01",
		}
		err := validateGeneratedSettings(cloneTestMap(t, info), entitlements, settings, true)
		if identifier == "com.primeradiant.evener.native.ShareExtension" {
			if err != nil {
				t.Fatalf("rejected build-owned share identifier: %v", err)
			}
		} else if !errors.Is(err, errIdentity) {
			t.Fatalf("accepted invalid build identifier %q: %v", identifier, err)
		}
	}
	info["MinimumOSVersion"] = "16.4"
	info["UIDeviceFamily"] = []any{float64(1)}
	if err := validateBundleMetadata(info, entitlements, true); !errors.Is(err, errIdentity) {
		t.Fatalf("accepted built share bundle with missing identity: %v", err)
	}
}

func TestGeneratedShareSourcePlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires the real macOS plutil")
	}
	t.Parallel()
	path, err := filepath.Abs("../../../../mobile-native/targets/share/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{native: t.TempDir(), output: t.TempDir()}
	info, err := r.plist(context.Background(), "source-share", path)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{
		"PRODUCT_BUNDLE_IDENTIFIER":  "com.primeradiant.evener.native.ShareExtension",
		"IPHONEOS_DEPLOYMENT_TARGET": "16.4", "TARGETED_DEVICE_FAMILY": "1", "DEVELOPMENT_TEAM": "EVENERCI01",
	}
	entitlements := map[string]any{"com.apple.security.application-groups": []any{"group.com.primeradiant.evener.native"}}
	if err := validateGeneratedSettings(info, entitlements, settings, true); err != nil {
		t.Fatal(err)
	}
}

func TestLogOnlyCommandDoesNotRetainStdout(t *testing.T) {
	t.Parallel()
	r := &runner{native: t.TempDir(), output: t.TempDir()}
	output, err := r.commandOutput(context.Background(), "git-version", false, "git", "--version")
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(r.output, "git-version.log"))
	if err != nil || len(log) == 0 {
		t.Fatalf("command output was not retained on disk: %v", err)
	}
	if len(output) != 0 {
		t.Fatalf("log-only command retained %d stdout bytes", len(output))
	}
}

func TestSourceProvenanceExcludesInstalledGems(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ignore, err := os.ReadFile("../../../../mobile-native/.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	native := filepath.Join(root, "mobile-native")
	if err := os.MkdirAll(filepath.Join(native, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(native, ".gitignore"), ignore)
	for _, path := range []string{"Gemfile.lock", "package-lock.json", "Podfile.lock", "src/App.ts"} {
		writeTestFile(t, filepath.Join(native, path), []byte("fixture\n"))
	}
	r := &runner{root: root, native: native, output: t.TempDir()}
	for i, args := range [][]string{
		{"init", "-q"},
		{"add", "--", "mobile-native/.gitignore", "mobile-native/Gemfile.lock", "mobile-native/package-lock.json", "mobile-native/Podfile.lock", "mobile-native/src/App.ts"},
		{"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", "commit", "--no-gpg-sign", "-qm", "fixture"},
	} {
		if _, err := r.command(context.Background(), fmt.Sprintf("git-fixture-%d", i), append([]string{"git", "-C", root}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.provenanceUnchanged(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".bundle/config", "vendor/bundle/fixture.gemspec"} {
		full := filepath.Join(native, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, full, []byte("installed\n"))
	}
	if err := r.provenanceUnchanged(context.Background()); err != nil {
		t.Fatalf("installed gems prevented source qualification: %v", err)
	}
	for _, path := range []string{"Gemfile.lock", "package-lock.json", "Podfile.lock", "src/App.ts"} {
		full := filepath.Join(native, path)
		writeTestFile(t, full, []byte("changed\n"))
		if err := r.provenanceUnchanged(context.Background()); err == nil {
			t.Errorf("accepted changed source %s", path)
		}
		writeTestFile(t, full, []byte("fixture\n"))
	}
	writeTestFile(t, filepath.Join(native, "vendor", "source.swift"), []byte("untracked\n"))
	if err := r.provenanceUnchanged(context.Background()); err == nil {
		t.Fatal("accepted untracked source beside the installed gems")
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneTestMap(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

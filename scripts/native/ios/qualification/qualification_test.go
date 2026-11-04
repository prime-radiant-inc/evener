//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

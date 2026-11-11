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
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
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
	diff, err := os.ReadFile(filepath.Join(dir, "Podfile.lock.diff"))
	if err != nil || len(diff) != 0 {
		t.Fatalf("matching resolution diff = %q, %v; want empty", diff, err)
	}
}

// A byte-identical, nonempty lock is proved without Git: the equal result must
// not depend on an installed executable or a live context, and the retained
// diff is the empty one a real diff would have produced.
func TestMatchingNonemptyLockNeedsNoGitOrLiveContext(t *testing.T) {
	dir := t.TempDir()
	old, resolved := filepath.Join(dir, "old"), filepath.Join(dir, "resolved")
	data := []byte("PODS:\n  - Expo (58.0.23)\n")
	writeTestFile(t, old, data)
	writeTestFile(t, resolved, data)
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "git-ran")
	script := "#!/bin/sh\necho ran > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(dir, "evidence")
	if err := checkLock(ctx, old, resolved, out); err != nil {
		t.Fatalf("equal lock with a cancelled context: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("equal lock launched git: %v", err)
	}
	diff, err := os.ReadFile(filepath.Join(out, "Podfile.lock.diff"))
	if err != nil || len(diff) != 0 {
		t.Fatalf("equal lock diff = %q, %v; want empty", diff, err)
	}
}

// An empty committed lock is never accepted, even when the candidate is also
// empty, and its review diff stays Git's: the empty-input rejection and its
// evidence are unchanged.
func TestEmptyCommittedLockStillRejectedWithDiff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old, resolved := filepath.Join(dir, "old"), filepath.Join(dir, "resolved")
	writeTestFile(t, old, nil)
	writeTestFile(t, resolved, []byte("PODS:\n  - Expo (58.0.23)\n"))
	out := filepath.Join(dir, "evidence")
	if err := checkLock(context.Background(), old, resolved, out); !errors.Is(err, errLockMismatch) {
		t.Fatalf("empty committed lock: got %v, want lock mismatch", err)
	}
	diff, err := os.ReadFile(filepath.Join(out, "Podfile.lock.diff"))
	if err != nil || len(diff) == 0 {
		t.Fatalf("empty committed lock lost its review diff: %q, %v", diff, err)
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

// The real Mac control: verification of an owned copy must fail once the
// share extension is removed from it, while the untouched build keeps
// verifying.
func TestVerifyAppRejectsCopiedBuildWithoutExtension(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires the real macOS signing tools")
	}
	t.Parallel()
	r := &runner{native: t.TempDir(), output: t.TempDir()}
	ctx := context.Background()
	build := buildControlAppFixture(t, r)
	if err := r.verifyApp(ctx, build, "control-original"); err != nil {
		t.Fatalf("untouched build did not verify: %v", err)
	}
	owned := filepath.Join(t.TempDir(), "Evener-copy.app")
	if _, err := r.command(ctx, "control-copy-app", "cp", "-R", build, owned); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyApp(ctx, owned, "control-copy"); err != nil {
		t.Fatalf("owned copy did not verify before the control: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(owned, "PlugIns", "EvenerShareExtension.appex")); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyApp(ctx, owned, "control-missing-extension"); err == nil {
		t.Fatal("accepted a copied build whose share extension was removed")
	}
	if err := r.verifyApp(ctx, build, "control-original-recheck"); err != nil {
		t.Fatalf("the control altered the untouched build: %v", err)
	}
}

// buildControlAppFixture assembles the smallest app that verifyApp accepts:
// a host bundle and its share extension, each with a compiled binary and a
// real ad-hoc signature carrying the shared capability's entitlements.
func buildControlAppFixture(t *testing.T, r *runner) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	build := filepath.Join(root, "Evener.app")
	extension := filepath.Join(build, "PlugIns", "EvenerShareExtension.appex")
	if err := os.MkdirAll(extension, 0o700); err != nil {
		t.Fatal(err)
	}
	const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>%s</string>
	<key>CFBundleExecutable</key>
	<string>%s</string>
	<key>MinimumOSVersion</key>
	<string>16.4</string>
	<key>UIDeviceFamily</key>
	<array>
		<integer>1</integer>
	</array>
`
	hostInfo := plistHeader + `	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>evener-native</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`
	shareInfo := plistHeader + `	<key>NSExtension</key>
	<dict>
		<key>NSExtensionPointIdentifier</key>
		<string>com.apple.share-services</string>
	</dict>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(build, "Info.plist"), []byte(fmt.Sprintf(hostInfo, "com.primeradiant.evener.native", "evener")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension, "Info.plist"), []byte(fmt.Sprintf(shareInfo, "com.primeradiant.evener.native.ShareExtension", "evener-share")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "main.jsbundle"), []byte("// fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	entitlements := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.security.application-groups</key>
	<array>
		<string>group.com.primeradiant.evener.native</string>
	</array>
</dict>
</plist>
`
	entitlementPath := filepath.Join(root, "entitlements.plist")
	if err := os.WriteFile(entitlementPath, []byte(entitlements), 0o600); err != nil {
		t.Fatal(err)
	}
	compile := func(dir, name string) {
		t.Helper()
		source := filepath.Join(root, name+".c")
		if err := os.WriteFile(source, []byte("int main(void) { return 0; }"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.command(ctx, "control-compile-"+name, "clang", source, "-o", filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	sign := func(dir, name string) {
		t.Helper()
		if _, err := r.command(ctx, "control-sign-"+name, "codesign", "--force", "--sign", "-", "--entitlements", entitlementPath, dir); err != nil {
			t.Fatal(err)
		}
	}
	compile(build, "evener")
	compile(extension, "evener-share")
	sign(extension, "share")
	sign(build, "host")
	return build
}

func TestLinuxRefusesBeforeCreatingOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux capability refusal")
	}
	t.Parallel()
	out := filepath.Join(t.TempDir(), "not-created")
	if err := qualify(context.Background(), out, false); !errors.Is(err, errMacRequired) {
		t.Fatalf("expected Mac prerequisite refusal, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation/output began on Linux: %v", err)
	}
}

// The UI child runs with the identity this run wrote, inside an output
// directory the parent prepared, and its exit becomes the run's own result.
// A stub journey proves the wiring without a simulator.
func TestUIJourneyPropagatesChildExitAfterIdentity(t *testing.T) {
	t.Parallel()
	stub := filepath.Join(t.TempDir(), "stub-journey.sh")
	const script = `#!/bin/sh
identity="" output=""
while [ $# -gt 0 ]; do
	case "$1" in
	--build-identity) identity="$2"; shift 2 ;;
	--output) output="$2"; shift 2 ;;
	*) exit 9 ;;
	esac
done
[ -f "$identity" ] || exit 5
grep -q '"appPath"' "$identity" || exit 6
grep -q '"simulatorUDID"' "$identity" || exit 6
[ -d "$output" ] || exit 7
exit 23
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &runner{native: t.TempDir(), output: t.TempDir(), uiRunner: stub}
	r.identity.AppPath = "/tmp/Evener.app"
	r.identity.SimulatorUDID = "0A01AE4B-7278-47C0-997B-30258F2AF066"
	if err := r.writeJSON("build-identity.json", r.identity); err != nil {
		t.Fatal(err)
	}
	err := r.uiJourney(context.Background())
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("child failure did not surface an exit error: %v", err)
	}
	if exit.ExitCode() != 23 {
		t.Fatalf("child exit 23 did not propagate: %v", err)
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

func TestRepeatPrebuildUpdatesInsteadOfRecreating(t *testing.T) {
	t.Parallel()
	first := prebuildCommand("first")
	repeat := prebuildCommand("repeat")
	if !slices.Equal(first[:4], []string{"npx", "--no-install", "expo", "prebuild"}) {
		t.Fatalf("prebuild must run the real Expo CLI: %v", first)
	}
	if slices.Contains(first, "--no-clean") {
		t.Fatal("the first prebuild must create the project from the committed template")
	}
	if !slices.Contains(repeat, "--no-clean") {
		t.Fatal("the repeat prebuild must apply the supported update mode to the existing project")
	}
}

func TestProjectCanonicalizationRunsTheInstalledParser(t *testing.T) {
	t.Parallel()
	const script, native, project, canonical = "/repo/scripts/native/ios/qualification/project-canonicalize.mjs",
		"/repo/mobile-native", "/repo/mobile-native/ios/Evener.xcodeproj/project.pbxproj",
		"/out/prebuild-project-first.json"
	want := []string{"node", script, native, project, canonical}
	if got := canonicalProjectCommand(script, native, project, canonical); !slices.Equal(got, want) {
		t.Fatalf("canonicalization argv = %v, want %v", got, want)
	}
}

func TestPodUnlockTriggersOnlyOnChangedVersionRefusal(t *testing.T) {
	t.Parallel()
	// Verbatim CocoaPods 1.16.2 refusal captured from Mac run 41969592127.
	refusal := "[!] CocoaPods could not find compatible versions for pod \"ReactNativeDependencies\":\n" +
		"  In snapshot (Podfile.lock):\n" +
		"    ReactNativeDependencies (from `../node_modules/react-native/third-party-podspecs/ReactNativeDependencies.podspec`)\n" +
		"\n" +
		"  In Podfile:\n" +
		"    ReactNativeDependencies (from `../node_modules/react-native/third-party-podspecs/ReactNativeDependencies.podspec`)\n" +
		"\n" +
		"It seems like you've changed the version of the dependency `ReactNativeDependencies` and it differs from the version stored in `Pods/Local Podspecs`.\n" +
		"You should run `pod update ReactNativeDependencies --no-repo-update` to apply changes made locally.\n"
	if !podUnlockNeeded([]byte(refusal)) {
		t.Fatal("the changed-local-podspec refusal must trigger the SDK unlock")
	}
	for name, failure := range map[string]string{
		"missing podspec":  "No podspec for `ReactNativeDependencies` found in the specified paths.\n",
		"resolution error": "[!] CocoaPods could not find compatible versions for pod \"Expo\":\n  In snapshot (Podfile.lock): Expo (= 57.0.23)\n",
		"empty log":        "",
	} {
		if podUnlockNeeded([]byte(failure)) {
			t.Fatalf("%s must not trigger the unlock", name)
		}
	}
}

func TestLocalPodNamesCoverSdkOwnedSourcesOnly(t *testing.T) {
	t.Parallel()
	// Registry pods resolve from SPEC REPOS and have no EXTERNAL SOURCES
	// entry; local development pods appear there with :path: or a local
	// :podspec:. The fixture is CRLF so the parser is proven against either
	// line ending; the committed lock is LF.
	lock := strings.ReplaceAll(`PODS:
  - EXApplication (58.1.2):
    - ExpoModulesCore

SPEC REPOS:
  trunk:
    - libwebp
    - SDWebImageWebPCoder

EXTERNAL SOURCES:
  EXApplication:
    :path: "../node_modules/expo-application/ios"
  hermes-engine:
    :podspec: "../node_modules/react-native/sdks/hermes-engine/hermes-engine.podspec"
    :tag: hermes-v250829098.0.17
  ReactNativeDependencies:
    :podspec: "../node_modules/react-native/third-party-podspecs/ReactNativeDependencies.podspec"

SPEC CHECKSUMS:
  EXApplication: 36d54f24eb98a1d63ee9e840e7bd8e949c6f598591c1ad19b2a18cd0d80428d1

PODFILE CHECKSUM: 2d90de0738a5a44d8e6ee4b30a9577c8020d12b4a

COCOAPODS: 1.16.2
`, "\n", "\r\n")
	want := []string{"EXApplication", "hermes-engine", "ReactNativeDependencies"}
	if got := localPodNames([]byte(lock)); !slices.Equal(got, want) {
		t.Fatalf("local pods = %v, want %v", got, want)
	}
}

func TestPodUnlockKeepsRepoFlagOrder(t *testing.T) {
	t.Parallel()
	want := []string{"bundle", "exec", "pod", "update", "Expo", "hermes-engine", "--no-repo-update", "--project-directory=ios"}
	if got := podUnlockCommand([]string{"Expo", "hermes-engine"}); !slices.Equal(got, want) {
		t.Fatalf("unlock argv = %v, want %v", got, want)
	}
}

// The invocation is captured through a stub xcodebuild on PATH so this checks
// what the runner executes, not what a constant says.
func TestReleaseBuildUsesOnlyActiveArchitecture(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$XCODEBUILD_ARGV_RECORD\"\n"
	if err := os.WriteFile(filepath.Join(bin, "xcodebuild"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XCODEBUILD_ARGV_RECORD", record)
	r := &runner{native: dir, output: dir}
	udid := "00000000-0000-0000-0000-000000000000"
	derived := filepath.Join(dir, "DerivedData")
	if _, err := r.commandOutput(context.Background(), "xcodebuild-release", false, releaseBuildCommand(udid, derived)...); err != nil {
		t.Fatalf("release build invocation: %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("stub xcodebuild recorded no argv: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{
		"-workspace", "ios/Evener.xcworkspace", "-scheme", "Evener",
		"-configuration", "Release", "-sdk", "iphonesimulator",
		"-destination", "platform=iOS Simulator,id=" + udid,
		"-derivedDataPath", derived, "build", "ONLY_ACTIVE_ARCH=YES",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("release build argv = %v, want %v", got, want)
	}
}

func TestEmbeddedSectionDumpDecodes(t *testing.T) {
	t.Parallel()
	// otool -X -s __TEXT __entitlements prints the section as lines of hex
	// byte pairs. Tokens of any other shape (an address column, a section
	// header) are skipped rather than parsed.
	dump := []byte("Contents of (__TEXT,__entitlements) section\n" +
		"3c 3f 78 6d 6c 20 76 65\n" +
		"000000010000f000 72 73 69 6f 6e 3d 22 31\n")
	want := []byte(`<?xml version="1`)
	got, err := decodeSectionDump(dump)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("decodeSectionDump = %q, %v; want %q", got, err, want)
	}
	if _, err := decodeSectionDump([]byte("not hex")); err == nil {
		t.Fatal("a dump with no valid bytes must error, not pass silently")
	}
}

func TestEmbeddedSectionDumpDecodesArm64Words(t *testing.T) {
	t.Parallel()
	// A thin arm64 binary makes otool print little-endian four-byte words
	// instead of byte pairs. Run 42769642647 failed verification because the
	// decoder read no bytes from this shape.
	dump := []byte("Contents of (__TEXT,__entitlements) section\n" +
		"000000010212a142\t6d783f3c 6576206c 6f697372 31223d6e \n")
	want := []byte(`<?xml version="1`)
	got, err := decodeSectionDump(dump)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("decodeSectionDump = %q, %v; want %q", got, err, want)
	}
}

func TestProductEntitlementsPreferSignatureThenEmbeddedSection(t *testing.T) {
	t.Parallel()
	appGroup := map[string]any{"com.apple.security.application-groups": []any{"group.com.primeradiant.evener.native"}}
	signature := map[string]any{"other": true}
	if got := productEntitlements(signature, appGroup); !reflect.DeepEqual(got, signature) {
		t.Fatalf("non-empty signature entitlements must win, got %v", got)
	}
	if got := productEntitlements(map[string]any{}, appGroup); !reflect.DeepEqual(got, appGroup) {
		t.Fatalf("an empty signature must fall back to the embedded section, got %v", got)
	}
	if got := productEntitlements(map[string]any{}, map[string]any{}); len(got) != 0 {
		t.Fatalf("entitlements nowhere in the product must stay empty, got %v", got)
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

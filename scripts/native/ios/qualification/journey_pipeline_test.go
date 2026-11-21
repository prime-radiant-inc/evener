//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const journeyFixtureUDID = "0A01AE4B-7278-47C0-997B-30258F2AF066"

// journeyProductsFixture assembles the products tree the build job uploads:
// the Release app with its embedded extension, the generated UI test runner
// and test bundle, and the run configuration beside them.
func journeyProductsFixture(t *testing.T) (derived string, layout productsLayout) {
	t.Helper()
	derived = t.TempDir()
	simulator := filepath.Join(derived, "Build", "Products", "Release-iphonesimulator")
	for _, dir := range []string{
		filepath.Join(simulator, "Evener.app", "PlugIns", "EvenerShareExtension.appex"),
		filepath.Join(simulator, "EvenerQualificationUITests-Runner.app"),
		filepath.Join(simulator, "EvenerQualificationUITests.xctest"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(derived, "Build", "Products", "Evener_iphonesimulator26.6-arm64.xctestrun"), []byte("fixture"))
	layout, err := journeyProducts(derived)
	if err != nil {
		t.Fatalf("complete products tree rejected: %v", err)
	}
	return derived, layout
}

// The build job must never upload an empty or partial artifact: every product
// the journeys install or run is required, and each missing one is named.
func TestJourneyProductsRequireEveryBuiltProduct(t *testing.T) {
	t.Parallel()
	derived, layout := journeyProductsFixture(t)
	wantApp := filepath.Join(derived, "Build", "Products", "Release-iphonesimulator", "Evener.app")
	if layout.App != wantApp {
		t.Fatalf("app path = %q, want %q", layout.App, wantApp)
	}
	if len(layout.XCTestRuns) != 1 {
		t.Fatalf("xctestrun count = %d, want 1", len(layout.XCTestRuns))
	}

	// Some Xcode layouts embed the UI test bundle in its runner app instead of
	// placing it beside the app; the runner and the bundle are both required.
	embeddedDerived, _ := journeyProductsFixture(t)
	embedded := filepath.Join(embeddedDerived, "Build", "Products", "Release-iphonesimulator", "EvenerQualificationUITests-Runner.app", "PlugIns", "EvenerQualificationUITests.xctest")
	if err := os.MkdirAll(embedded, 0o700); err != nil {
		t.Fatal(err)
	}
	topLevel := filepath.Join(embeddedDerived, "Build", "Products", "Release-iphonesimulator", "EvenerQualificationUITests.xctest")
	if err := os.RemoveAll(topLevel); err != nil {
		t.Fatal(err)
	}
	embeddedLayout, err := journeyProducts(embeddedDerived)
	if err != nil {
		t.Fatalf("runner-embedded test bundle rejected: %v", err)
	}
	if embeddedLayout.TestBundle != embedded {
		t.Fatalf("test bundle = %q, want the runner-embedded %q", embeddedLayout.TestBundle, embedded)
	}

	for name, selectPath := range map[string]func(productsLayout) string{
		"app":               func(l productsLayout) string { return l.App },
		"extension":         func(l productsLayout) string { return l.Extension },
		"runner":            func(l productsLayout) string { return l.TestRunner },
		"test bundle":       func(l productsLayout) string { return l.TestBundle },
		"run configuration": func(l productsLayout) string { return l.XCTestRuns[0] },
	} {
		t.Run(name, func(t *testing.T) {
			derived, layout := journeyProductsFixture(t)
			if err := os.RemoveAll(selectPath(layout)); err != nil {
				t.Fatal(err)
			}
			if _, err := journeyProducts(derived); !errors.Is(err, errIncompleteProducts) {
				t.Fatalf("missing %s accepted: %v", name, err)
			}
		})
	}

	if err := os.RemoveAll(filepath.Join(derived, "Build")); err != nil {
		t.Fatal(err)
	}
	if _, err := journeyProducts(derived); !errors.Is(err, errIncompleteProducts) {
		t.Fatalf("empty products tree accepted: %v", err)
	}
}

// The journeys entry validates the downloaded layout before it creates any
// state: an incomplete download fails the run without an output directory.
func TestJourneysValidateDownloadedProductsBeforeCreatingOutput(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "not-created")
	err := journeys(context.Background(), out, t.TempDir(), "")
	if !errors.Is(err, errIncompleteProducts) {
		t.Fatalf("incomplete downloaded products: %v", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("journey output began before the products were validated: %v", statErr)
	}
}

// A complete download still refuses on Linux before creating output.
func TestJourneysRefuseLinuxAfterValidatingProducts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux capability refusal")
	}
	t.Parallel()
	derived, _ := journeyProductsFixture(t)
	out := filepath.Join(t.TempDir(), "not-created")
	if err := journeys(context.Background(), out, derived, ""); !errors.Is(err, errMacRequired) {
		t.Fatalf("expected Mac prerequisite refusal, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journey output began on Linux: %v", err)
	}
}

// fakeXcrun puts a stub xcrun on PATH that answers the simulator calls the
// runner makes and records every invocation to the returned file.
func fakeXcrun(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "xcrun-calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$XCRUN_CALLS"
case "$*" in
"simctl list --json runtimes")
	printf '%s\n' '{"runtimes":[{"identifier":"ios-26-6","isAvailable":true,"supportedDeviceTypes":[{"identifier":"iphone-17","productFamily":"iPhone"}]}]}'
	;;
"simctl create "*)
	if [ "${XCRUN_CREATE_EXIT:-0}" -ne 0 ]; then exit "$XCRUN_CREATE_EXIT"; fi
	printf '%s\n' '` + journeyFixtureUDID + `'
	;;
"simctl list --json devices")
	printf '%s\n' '{"devices":{"ios-26-6":[{"udid":"` + journeyFixtureUDID + `","state":"Booted"}]}}'
	;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XCRUN_CALLS", record)
	return record
}

func readCalls(t *testing.T, record string) []string {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// The UI job owns its simulator the same way the build job does: one runner
// creates the device and the same runner deletes it on every exit, including
// the failed journey path.
func TestOwnedSimulatorIsDeletedWhenTheJourneyFails(t *testing.T) {
	dir := t.TempDir()
	record := fakeXcrun(t, dir)

	r := &runner{native: dir, output: dir}
	sentinel := errors.New("journey failed")
	started := false
	err := r.withOwnedSimulator(context.Background(), "Evener-qualification-fixture", func(udid string) error {
		started = true
		if udid != journeyFixtureUDID {
			t.Fatalf("owned simulator = %q, want %q", udid, journeyFixtureUDID)
		}
		return sentinel
	})
	if !started {
		t.Fatal("the simulator was created but the run never started")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("the journey failure did not surface: %v", err)
	}
	calls := readCalls(t, record)
	if !slices.Contains(calls, "simctl delete "+journeyFixtureUDID) {
		t.Fatalf("the owning runner left its simulator behind: %v", calls)
	}
	identity, readErr := os.ReadFile(filepath.Join(dir, "simulator-identity.json"))
	if readErr != nil || !strings.Contains(string(identity), journeyFixtureUDID) {
		t.Fatalf("owned simulator identity = %q, %v", identity, readErr)
	}

	// A device that was never created has no cleanup owner and no run.
	r = &runner{native: dir, output: dir}
	started = false
	t.Setenv("XCRUN_CREATE_EXIT", "2")
	if err := r.withOwnedSimulator(context.Background(), "Evener-qualification-fixture", func(string) error {
		started = true
		return nil
	}); err == nil {
		t.Fatal("a failed simulator create was accepted")
	}
	if started {
		t.Fatal("the run started without an owned simulator")
	}
}

// The pre-boot mode refuses a non-Mac host before it creates any state, the
// same way the journeys mode does.
func TestSimulatorModeRefusesLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux capability refusal")
	}
	out := filepath.Join(t.TempDir(), "not-created")
	if err := prepareSimulator(context.Background(), out); !errors.Is(err, errMacRequired) {
		t.Fatalf("expected Mac prerequisite refusal, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the pre-boot began on Linux: %v", err)
	}
}

// The pre-boot mode creates the run's device and starts booting it, then
// exits: waiting for the boot belongs to the journey script's bootstatus.
func TestSimulatorModeCreatesAndStartsItsDevice(t *testing.T) {
	dir := t.TempDir()
	record := fakeXcrun(t, dir)
	r := &runner{native: dir, output: dir}
	if err := r.bootSimulator(context.Background()); err != nil {
		t.Fatalf("boot the run simulator: %v", err)
	}
	calls := readCalls(t, record)
	if !slices.Contains(calls, "simctl boot "+journeyFixtureUDID) {
		t.Fatalf("the pre-boot step never started the device: %v", calls)
	}
	identity, err := os.ReadFile(filepath.Join(dir, "simulator-identity.json"))
	if err != nil || !strings.Contains(string(identity), journeyFixtureUDID) {
		t.Fatalf("pre-booted simulator identity = %q, %v", identity, err)
	}
}

// The journeys mode adopts the device the pre-boot step started and deletes it
// on the way out exactly as it deletes one it created: one run, one owner.
func TestAdoptedSimulatorIsStillDeleted(t *testing.T) {
	dir := t.TempDir()
	record := fakeXcrun(t, dir)
	simulatorDir := filepath.Join(dir, "simulator")
	if err := os.Mkdir(simulatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(simulatorDir, "simulator-identity.json"),
		[]byte(`{"udid":"`+journeyFixtureUDID+`","runtime":"ios-26-6","deviceType":"iphone-17"}`))
	writeTestFile(t, filepath.Join(simulatorDir, "simulator-boot.log"), []byte("fixture boot"))
	output := filepath.Join(dir, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &runner{native: dir, output: output}
	if err := r.adoptSimulator(context.Background(), simulatorDir); err != nil {
		t.Fatalf("adopt the pre-booted simulator: %v", err)
	}
	started := false
	if err := r.withSimulator(context.Background(), "Evener-qualification-fixture", true, func(udid string) error {
		started = true
		if udid != journeyFixtureUDID {
			t.Fatalf("adopted simulator = %q, want %q", udid, journeyFixtureUDID)
		}
		return nil
	}); err != nil {
		t.Fatalf("the adopted run failed: %v", err)
	}
	if !started {
		t.Fatal("the adopted simulator never reached the run")
	}
	calls := readCalls(t, record)
	if slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, "simctl create ") }) {
		t.Fatalf("the adopting run created a second device: %v", calls)
	}
	if !slices.Contains(calls, "simctl delete "+journeyFixtureUDID) {
		t.Fatalf("the adopting run left its device behind: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(output, "simulator-boot.log")); err != nil {
		t.Fatalf("the pre-boot log did not reach the run output: %v", err)
	}
}

// A pre-boot that did not survive fails before the journey child starts, and
// names the contract that broke rather than the journey that trips over it.
func TestAdoptSimulatorRefusesAPreBootThatDidNotSurvive(t *testing.T) {
	dir := t.TempDir()
	_ = fakeXcrun(t, dir)
	output := filepath.Join(dir, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &runner{native: dir, output: output}
	if err := r.adoptSimulator(context.Background(), filepath.Join(dir, "absent")); err == nil {
		t.Fatal("an absent pre-boot directory was accepted")
	}
	simulatorDir := filepath.Join(dir, "simulator")
	if err := os.Mkdir(simulatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(simulatorDir, "simulator-identity.json"),
		[]byte(`{"udid":"11111111-2222-3333-4444-555555555555","runtime":"ios-26-6","deviceType":"iphone-17"}`))
	if err := r.adoptSimulator(context.Background(), simulatorDir); !errors.Is(err, errSimulatorGone) {
		t.Fatalf("a device that is gone was adopted: %v", err)
	}
}

// The build job proves the red side before it registers the generated target:
// the intended journey must fail against the as-generated project, and only
// then may the target exist and the test products be built.
func TestBuildForJourneysProvesRedBeforeRegisteringTarget(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "calls")
	xcode := `#!/bin/sh
printf 'xcodebuild %s\n' "$*" >> "$CALLS"
if [ "$1" = "test" ]; then exit "${XCODE_TEST_EXIT:-65}"; fi
exit 0
`
	bundle := `#!/bin/sh
printf 'bundle %s\n' "$*" >> "$CALLS"
exit 0
`
	for name, script := range map[string]string{"xcodebuild": xcode, "bundle": bundle} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", record)

	// The workflow restores the build cache into an external stable root, so
	// both xcodebuild invocations and the product validation must use it.
	derived := t.TempDir()
	r := &runner{native: dir, output: dir, derived: derived}
	r.identity.SimulatorUDID = journeyFixtureUDID
	if err := r.buildForJourneys(context.Background()); err != nil {
		t.Fatalf("red-then-target build: %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"xcodebuild test ",
		"bundle exec ruby ../scripts/native/ios/add-ui-test-target.rb ios/Evener.xcodeproj",
		"xcodebuild build-for-testing ",
	}
	calls := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(calls) != len(want) {
		t.Fatalf("build-for-journeys calls = %v, want %d", calls, len(want))
	}
	for i := range want {
		if !strings.HasPrefix(calls[i], want[i]) {
			t.Fatalf("build-for-journeys call %d = %q, want prefix %q", i, calls[i], want[i])
		}
	}
	if !strings.Contains(calls[0], "-derivedDataPath "+derived) {
		t.Errorf("red pass does not build in the external DerivedData root:\n%s", calls[0])
	}
	if !strings.Contains(calls[2], "-derivedDataPath "+derived) {
		t.Errorf("build-for-testing does not build in the external DerivedData root:\n%s", calls[2])
	}
	exitData, err := os.ReadFile(filepath.Join(dir, "absent-target-red.exit"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(exitData)); got != "65" {
		t.Fatalf("red pass exit evidence = %q, want the failing exit 65", got)
	}

	simulator := filepath.Join(derived, "Build", "Products", "Release-iphonesimulator")
	for _, sub := range []string{
		filepath.Join(simulator, "Evener.app", "PlugIns", "EvenerShareExtension.appex"),
		filepath.Join(simulator, "EvenerQualificationUITests-Runner.app"),
		filepath.Join(simulator, "EvenerQualificationUITests.xctest"),
	} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(derived, "Build", "Products", "Evener_iphonesimulator26.6-arm64.xctestrun"), []byte("fixture"))
	layout, err := journeyProducts(r.derivedDataDir())
	if err != nil {
		t.Fatalf("external products: %v", err)
	}
	if want := filepath.Join(derived, "Build", "Products"); layout.ProductsRoot != want {
		t.Fatalf("product validation root = %q, want %q", layout.ProductsRoot, want)
	}
}

// A red pass that succeeds means the target already existed or the harness is
// lying about what it runs; either way the build must stop before registering
// anything.
func TestBuildForJourneysRejectsAGreenRedPass(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf 'xcodebuild %s\n' "$1" >> "$CALLS"
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "xcodebuild"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", record)
	r := &runner{native: dir, output: dir}
	r.identity.SimulatorUDID = journeyFixtureUDID
	if err := r.buildForJourneys(context.Background()); err == nil {
		t.Fatal("a red pass that succeeded did not fail the build")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !slices.Equal(got, []string{"xcodebuild test"}) {
		t.Fatalf("a green red pass continued into touches it must not reach: %v", got)
	}
}

// A later caching change needs the real sizes: the products tree and every
// top-level DerivedData directory are recorded in the build job's output.
func TestDerivedDataSizeLogCoversProductsAndTopLevelDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	derived := filepath.Join(dir, "DerivedData")
	for _, sub := range []string{filepath.Join("Build", "Products", "Release-iphonesimulator"), "Intermediates.noindex"} {
		if err := os.MkdirAll(filepath.Join(derived, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(derived, "Build", "Products", "fixture.bin"), []byte("fixture"))
	r := &runner{native: dir, output: dir}
	if err := r.logDerivedDataSizes(context.Background()); err != nil {
		t.Fatalf("size log: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "derived-data-sizes.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Build/Products", "Intermediates.noindex"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("size log does not name %s:\n%s", want, log)
		}
	}
}

//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
)

var (
	errLockMismatch = errors.New("CocoaPods resolution differs from the committed lock")
	errIdentity     = errors.New("native bundle identity or capability is invalid")
	errMacRequired  = errors.New("iOS qualification requires macOS, Xcode, simctl, codesign and CocoaPods")
)

// generatedProjectFile is the one generated file whose identity is its complete
// parsed content rather than its bytes: Expo's writer may reformat an unchanged
// empty PBX dictionary between generations while every object, key, UUID and
// reference stays identical.
const generatedProjectFile = "Evener.xcodeproj/project.pbxproj"

// prebuildCommand returns the real Expo prebuild invocation for a phase. The
// repeat applies the supported update mode to the existing project instead of
// recreating it with fresh template identifiers.
func prebuildCommand(phase string) []string {
	args := []string{"npx", "--no-install", "expo", "prebuild", "--platform", "ios", "--no-install"}
	if phase == "repeat" {
		args = append(args, "--no-clean")
	}
	return args
}

// canonicalProjectCommand runs the repository canonicalizer, which parses a
// generated Xcode project with the real @bacons/xcode parser resolved from the
// installed native graph and writes its complete sorted-key JSON form.
func canonicalProjectCommand(script, native, project, canonical string) []string {
	return []string{"node", script, native, project, canonical}
}

// podUnlockNeeded reports whether CocoaPods refused installation because a
// local podspec's recorded version changed — the only failure the runner
// unlocks. Any other failure stays a surfaced error for review.
func podUnlockNeeded(installLog []byte) bool {
	return strings.Contains(string(installLog),
		"It seems like you've changed the version of the dependency")
}

// localPodNames returns the local development pods the committed lock declares
// under EXTERNAL SOURCES (a :path: or a local :podspec:). These are the SDK-
// and app-owned roots a `pod update` may unlock; registry pods never appear
// here and keep their locked versions.
func localPodNames(lock []byte) []string {
	var names []string
	inSources := false
	entry := ""
	for line := range strings.SplitSeq(string(lock), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "EXTERNAL SOURCES:":
			inSources = true
		case !inSources:
		case line == "":
		case !strings.HasPrefix(line, " "):
			return names
		case strings.HasPrefix(line, "    :path:"):
			if entry != "" {
				names = append(names, entry)
				entry = ""
			}
		case strings.HasPrefix(line, "    :podspec:") && !strings.Contains(line, "://"):
			if entry != "" {
				names = append(names, entry)
				entry = ""
			}
		case strings.HasPrefix(line, "  ") && strings.HasSuffix(line, ":"):
			entry = strings.TrimSuffix(strings.TrimSpace(line), ":")
		}
	}
	return names
}

// podUnlockCommand unlocks the named local pods and only those: the registry
// pins the lock also carries are never named and keep their locked versions.
func podUnlockCommand(names []string) []string {
	args := append([]string{"bundle", "exec", "pod", "update"}, names...)
	return append(args, "--no-repo-update", "--project-directory=ios")
}

func checkLock(ctx context.Context, committed, resolved, output string) error {
	candidate, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("%w: resolved lock: %w", errLockMismatch, err)
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	candidatePath := filepath.Join(output, "Podfile.lock.candidate")
	if err := os.WriteFile(candidatePath, candidate, 0o600); err != nil {
		return err
	}
	original, err := os.ReadFile(committed)
	if err != nil {
		return fmt.Errorf("%w: committed lock: %w", errLockMismatch, err)
	}
	diff, err := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-ext-diff", "--text", "--", committed, candidatePath).CombinedOutput()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return fmt.Errorf("lock diff: %w", err)
	}
	if err := os.WriteFile(filepath.Join(output, "Podfile.lock.diff"), diff, 0o600); err != nil {
		return err
	}
	if len(original) == 0 || !bytes.Equal(original, candidate) {
		return errLockMismatch
	}
	return nil
}

func selectSimulator(data []byte) (string, string, error) {
	var listing struct {
		Runtimes []struct {
			Identifier           string
			IsAvailable          bool
			SupportedDeviceTypes []struct {
				Identifier    string
				ProductFamily string
			}
		}
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		return "", "", err
	}
	for _, runtime := range listing.Runtimes {
		if !runtime.IsAvailable || runtime.Identifier == "" {
			continue
		}
		for _, device := range runtime.SupportedDeviceTypes {
			if device.ProductFamily == "iPhone" && device.Identifier != "" {
				return runtime.Identifier, device.Identifier, nil
			}
		}
	}
	return "", "", errors.New("simctl reports no available runtime with a supported iPhone device type")
}

func validateBundleMetadata(info, entitlements map[string]any, extension bool) error {
	id := "com.primeradiant.evener.native"
	if extension {
		id += ".ShareExtension"
	}
	if info["CFBundleIdentifier"] != id || info["MinimumOSVersion"] != "16.4" ||
		!reflect.DeepEqual(info["UIDeviceFamily"], []any{float64(1)}) ||
		!reflect.DeepEqual(entitlements["com.apple.security.application-groups"], []any{"group.com.primeradiant.evener.native"}) {
		return fmt.Errorf("%w: %s", errIdentity, id)
	}
	if extension {
		service, ok := info["NSExtension"].(map[string]any)
		if !ok || service["NSExtensionPointIdentifier"] != "com.apple.share-services" {
			return fmt.Errorf("%w: share service", errIdentity)
		}
		return nil
	}
	urlTypes, _ := info["CFBundleURLTypes"].([]any)
	for _, value := range urlTypes {
		urlType, _ := value.(map[string]any)
		schemes, _ := urlType["CFBundleURLSchemes"].([]any)
		for _, scheme := range schemes {
			if scheme == "evener-native" {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: activation scheme", errIdentity)
}

func validateGeneratedSettings(info, entitlements map[string]any, settings map[string]string, extension bool) error {
	if identifier, exists := info["CFBundleIdentifier"]; exists && identifier != "$(PRODUCT_BUNDLE_IDENTIFIER)" && identifier != settings["PRODUCT_BUNDLE_IDENTIFIER"] {
		return fmt.Errorf("%w: generated plist identifier conflicts with build settings", errIdentity)
	}
	if minimum, exists := info["MinimumOSVersion"]; exists && minimum != "$(IPHONEOS_DEPLOYMENT_TARGET)" && minimum != settings["IPHONEOS_DEPLOYMENT_TARGET"] {
		return fmt.Errorf("%w: generated plist deployment minimum conflicts with build settings", errIdentity)
	}
	if family, exists := info["UIDeviceFamily"]; exists && !reflect.DeepEqual(family, []any{float64(1)}) {
		return fmt.Errorf("%w: generated plist device family conflicts with build settings", errIdentity)
	}
	info["CFBundleIdentifier"] = settings["PRODUCT_BUNDLE_IDENTIFIER"]
	info["MinimumOSVersion"] = settings["IPHONEOS_DEPLOYMENT_TARGET"]
	if settings["TARGETED_DEVICE_FAMILY"] != "1" || settings["DEVELOPMENT_TEAM"] != "EVENERCI01" {
		return fmt.Errorf("%w: generated device family or fixture team", errIdentity)
	}
	info["UIDeviceFamily"] = []any{float64(1)}
	return validateBundleMetadata(info, entitlements, extension)
}

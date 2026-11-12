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
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	errLockMismatch = errors.New("CocoaPods resolution differs from the committed lock")
	errIdentity     = errors.New("native bundle identity or capability is invalid")
	errMacRequired  = errors.New("iOS qualification requires macOS, Xcode, simctl, codesign and CocoaPods")
	// errPodRefusalName is a changed-local-podspec refusal naming a pod the
	// committed lock does not declare under EXTERNAL SOURCES. Unlocking it
	// would move a registry pod, so the resolver surfaces it instead.
	errPodRefusalName  = errors.New("CocoaPods named a pod the committed lock does not declare as local")
	errPodRefusalStuck = errors.New("CocoaPods repeated a changed-local-podspec refusal with no new pod")
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

// changedPodRefusal names the pod in one refusal sentence. CocoaPods prints the
// pod name in backticks on the "changed the version of the dependency" line;
// the surrounding compatibility block is not a reliable source because it also
// names the pod in a "could not find compatible versions" heading.
var changedPodRefusal = regexp.MustCompile("changed the version of the dependency `([^`\n]+)`")

// podRefusalNames returns the local pods CocoaPods named as changed, in the
// order it named them, without duplicates. CocoaPods names one pod per refusal
// in practice and refuses again for the next one after an update, so the
// caller repeats until a refusal names nothing new.
func podRefusalNames(installLog []byte) []string {
	var names []string
	for _, match := range changedPodRefusal.FindAllSubmatch(installLog, -1) {
		name := strings.TrimSpace(string(match[1]))
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// localPodNames returns the local development pods the committed lock declares
// under EXTERNAL SOURCES (a :path: or a local :podspec:). These are the pods a
// `pod update` re-resolves. Registry pods never appear here, but those
// reachable only through these local pods may move with them, which is why
// the candidate is compared against the committed lock before review.
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

// podUnlockCommand re-resolves the local development pods the refusal named.
// Registry pins reachable only through those local pods may move with their
// parents, so the candidate diff is reviewed before it is applied.
func podUnlockCommand(names []string) []string {
	args := append([]string{"bundle", "exec", "pod", "update"}, names...)
	return append(args, "--no-repo-update", "--project-directory=ios")
}

// decodeSectionDump turns `otool -X -s __TEXT __entitlements` output into the
// section's raw bytes. otool prints two-character byte tokens for a universal
// binary and little-endian four-byte words for a thin arm64 one, so both shapes
// decode; anything else (a section header, an address column) is skipped.
func decodeSectionDump(dump []byte) ([]byte, error) {
	var out []byte
	for token := range strings.FieldsSeq(string(dump)) {
		if (len(token) != 2 && len(token) != 4 && len(token) != 8) || !isHexPair(token) {
			continue
		}
		value, err := strconv.ParseUint(token, 16, 32)
		if err != nil {
			return nil, err
		}
		// A word prints its byte pairs in reverse: the token's last pair is
		// the section's first byte.
		for shift := 0; shift < 8*(len(token)/2); shift += 8 {
			out = append(out, byte(value>>shift))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("entitlements section dump has no bytes")
	}
	return out, nil
}

func isHexPair(token string) bool {
	for _, c := range token {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// productEntitlements picks where the built product's entitlements live. On
// this toolchain's simulator builds Apple's simulated-entitlements model
// leaves the ad-hoc signature's entitlement blob empty and embeds the real
// entitlements in the binary's __TEXT,__entitlements section, so an empty
// signature falls back to that embedded section. Entitlements in neither
// place stay empty and fail the capability check.
func productEntitlements(signature, section map[string]any) map[string]any {
	if len(signature) > 0 {
		return signature
	}
	return section
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
	diffPath := filepath.Join(output, "Podfile.lock.diff")
	// An equal nonempty lock needs no external diff: write the empty diff Git
	// would have produced and return without launching it. This also makes an
	// equal lock's outcome independent of whether Git is installed or the
	// context was cancelled. An empty committed lock is never equal evidence:
	// it still falls through to rejection below, with Git's diff retained.
	equal := len(original) > 0 && bytes.Equal(original, candidate)
	var diff []byte
	if !equal {
		diff, err = exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-ext-diff", "--text", "--", committed, candidatePath).CombinedOutput()
		var exit *exec.ExitError
		if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
			return fmt.Errorf("lock diff: %w", err)
		}
	}
	if err := os.WriteFile(diffPath, diff, 0o600); err != nil {
		return err
	}
	if equal {
		return nil
	}
	return errLockMismatch
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

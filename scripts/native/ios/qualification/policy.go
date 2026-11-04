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
)

var (
	errLockMismatch = errors.New("CocoaPods resolution differs from the committed lock")
	errIdentity     = errors.New("native bundle identity or capability is invalid")
	errMacRequired  = errors.New("iOS qualification requires macOS, Xcode, simctl, codesign and CocoaPods")
)

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

package evener_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// builtDistChunk is the path gitleaks reported for the observed false positive
// in #3199, and trackedHubSource is a tracked source beside it that must keep
// being scanned.
const (
	builtDistChunk   = "cmd/evener-hub/frontend/dist/webassets/Settings-BUbGPRbt.js"
	trackedHubSource = "cmd/evener-hub/frontend/src/leak.js"
)

// TestGitleaksAllowlistsBuiltFrontendDist pins #3199. `make lint`'s secret-scan
// walks the whole working tree, so a web build's minified chunks under the
// hub's gitignored frontend/dist/ made gitleaks' generic-api-key rule fire on
// minified property assignments (observed at builtDistChunk:
// `r.token,s=r.operationId;`). The gate's lint-before-build order never sees
// the chunks, so CI stayed green while a bare `make lint` after local testing
// failed.
//
// The check reads the committed ruleset directly rather than shelling out to
// gitleaks, so it stays deterministic and needs no tool installation; the
// engine itself is verified by `make secret-scan`.
func TestGitleaksAllowlistsBuiltFrontendDist(t *testing.T) {
	t.Parallel()

	paths := gitleaksAllowlistPaths(t)
	if !anyGitleaksPathMatches(t, paths, builtDistChunk) {
		t.Fatalf(".gitleaks.toml allowlists no path matching the built frontend dist chunk %q, "+
			"so a bare `make lint` after a web build fails on gitleaks' generic-api-key rule; "+
			"add the built output path to [allowlist].paths", builtDistChunk)
	}
	// Negative control: the exclusion must stay scoped to build output. A regex
	// broad enough to cover the whole frontend would hide a real secret in a
	// tracked source beside the dist.
	if anyGitleaksPathMatches(t, paths, trackedHubSource) {
		t.Errorf(".gitleaks.toml allowlists %q, but that is a tracked frontend source, not "+
			"build output; scope the dist exclusion so sources stay scanned", trackedHubSource)
	}
}

type gitleaksConfig struct {
	Allowlist struct {
		Paths []string `toml:"paths"`
	} `toml:"allowlist"`
	Rules []struct {
		ID         string `toml:"id"`
		Allowlists []struct {
			Condition   string `toml:"condition"`
			RegexTarget string
			Paths       []string `toml:"paths"`
			Regexes     []string `toml:"regexes"`
		} `toml:"allowlists"`
	} `toml:"rules"`
}

// gitleaksAllowlistPaths returns the [allowlist].paths regexes from the
// committed ruleset, the same list gitleaks matches a file path against.
func gitleaksAllowlistPaths(t *testing.T) []string {
	t.Helper()
	var cfg gitleaksConfig
	if _, err := toml.DecodeFile(".gitleaks.toml", &cfg); err != nil {
		t.Fatalf("parse .gitleaks.toml: %v", err)
	}
	if len(cfg.Allowlist.Paths) == 0 {
		t.Fatal(".gitleaks.toml has no [allowlist].paths entries")
	}
	return cfg.Allowlist.Paths
}

func anyGitleaksPathMatches(t *testing.T, paths []string, target string) bool {
	t.Helper()
	for _, p := range paths {
		re, err := regexp.Compile(p)
		if err != nil {
			t.Fatalf("allowlist path %q does not compile: %v", p, err)
		}
		if re.MatchString(target) {
			return true
		}
	}
	return false
}

const (
	mermaidGeneratedPath = "mobile-native/src/generated/mermaidPage.ts"
	mermaidVendorCounts  = "p.keyCount,v=p.atlasCount;"
	wakeTrialLedgerPath  = "mobile-native/modules/evener-voice/core/WakeTrialLedger.swift"
	wakeTrialFixturePath = "mobile-native/modules/evener-voice/tests/WakeTrialLedgerTests.swift"
	wakeTrialStorageKey  = "evener.voice.wakeTrials.v1"
)

func TestGitleaksMermaidExceptionScope(t *testing.T) {
	t.Parallel()
	var cfg gitleaksConfig
	if _, err := toml.DecodeFile(".gitleaks.toml", &cfg); err != nil {
		t.Fatal(err)
	}
	for _, rule := range cfg.Rules {
		if rule.ID != "generic-api-key" {
			continue
		}
		for _, allowlist := range rule.Allowlists {
			if !anyGitleaksPathMatches(t, allowlist.Paths, mermaidGeneratedPath) {
				continue
			}
			if allowlist.Condition != "AND" || allowlist.RegexTarget != "match" {
				t.Fatal("vendor exception must require both the path and the individual match")
			}
			for _, path := range []string{
				"mobile-native/src/MermaidDiagram.tsx",
				"other/" + mermaidGeneratedPath,
				mermaidGeneratedPath + ".backup",
			} {
				if anyGitleaksPathMatches(t, allowlist.Paths, path) {
					t.Errorf("vendor exception also matches %q", path)
				}
			}
			for _, tc := range []struct {
				match string
				allow bool
			}{
				{mermaidVendorCounts, true},
				{mermaidVendorCounts + "api_key=another_value;", false},
				{"api_key=another_value;" + mermaidVendorCounts, false},
				{"p.keyCount,v=someOtherValue;", false},
			} {
				if got := anyGitleaksPathMatches(t, allowlist.Regexes, tc.match); got != tc.allow {
					t.Errorf("match %q: allow=%v, want %v", tc.match, got, tc.allow)
				}
			}
			return
		}
	}
	t.Fatal("no scoped generic-api-key exception for the generated Mermaid vendor expression")
}

func TestGitleaksWakeTrialExceptionScope(t *testing.T) {
	t.Parallel()
	assertWakeTrialExceptionScope(t, wakeTrialLedgerPath)
}

func TestGitleaksWakeTrialFixtureExceptionScope(t *testing.T) {
	t.Parallel()
	assertWakeTrialExceptionScope(t, wakeTrialFixturePath)
}

func assertWakeTrialExceptionScope(t *testing.T, targetPath string) {
	t.Helper()
	var cfg gitleaksConfig
	if _, err := toml.DecodeFile(".gitleaks.toml", &cfg); err != nil {
		t.Fatal(err)
	}
	if anyGitleaksPathMatches(t, cfg.Allowlist.Paths, targetPath) {
		t.Fatal("wake-trial source must not be globally path-allowlisted")
	}
	for _, rule := range cfg.Rules {
		if rule.ID != "generic-api-key" {
			continue
		}
		for _, allowlist := range rule.Allowlists {
			if !anyGitleaksPathMatches(t, allowlist.Paths, targetPath) {
				continue
			}
			if allowlist.Condition != "AND" || allowlist.RegexTarget != "secret" {
				t.Fatal("wake-trial exception must require both the path and the exact extracted value")
			}
			for _, path := range []string{
				"mobile-native/modules/evener-voice/core/VoiceCore.swift",
				"mobile-native/modules/evener-voice/tests/WakeWordTriggerTests.swift",
				"other/" + targetPath,
				targetPath + ".backup",
			} {
				if anyGitleaksPathMatches(t, allowlist.Paths, path) {
					t.Errorf("wake-trial exception also matches path %q", path)
				}
			}
			for _, tc := range []struct {
				value string
				allow bool
			}{
				{wakeTrialStorageKey, true},
				{wakeTrialStorageKey + "-other", false},
				{"prefix-" + wakeTrialStorageKey, false},
				{"evener.voice.wakeTrials.v2", false},
				{"evenerXvoiceXwakeTrialsXv1", false},
			} {
				if got := anyGitleaksPathMatches(t, allowlist.Regexes, tc.value); got != tc.allow {
					t.Errorf("value %q: allow=%v, want %v", tc.value, got, tc.allow)
				}
			}
			return
		}
	}
	t.Fatal("no scoped generic-api-key exception for the wake-trial UserDefaults key")
}

// The real engine is opt-in so default tests need no ambient tool installation.
func TestGitleaksScopedSecretDetection(t *testing.T) {
	if os.Getenv("EVENER_GITLEAKS_TESTS") != "1" {
		t.Skip("set EVENER_GITLEAKS_TESTS=1 with gitleaks installed")
	}
	t.Parallel()
	bin, err := exec.LookPath("gitleaks")
	if err != nil {
		t.Fatal(err)
	}
	config, err := filepath.Abs(".gitleaks.toml")
	if err != nil {
		t.Fatal(err)
	}
	// A deterministic synthetic credential exercises the actual generic rule.
	credential := `api_key="` + strings.Repeat("aB3dE7gH9kL2", 3) + `";`
	persistedWakeKey := `private let key = "` + wakeTrialStorageKey + `"`
	fixtureWakeKey := `defaults.set(data, forKey: "` + wakeTrialStorageKey + `")`
	for _, tc := range []struct {
		name     string
		path     string
		content  string
		findings int
	}{
		{"vendor-generated", mermaidGeneratedPath, mermaidVendorCounts, 0},
		{"vendor-other-path", "mobile-native/src/vendor.ts", mermaidVendorCounts, 1},
		{"credential-same-line", mermaidGeneratedPath, mermaidVendorCounts + credential, 1},
		{"credential-other-path", "mobile-native/src/vendor.ts", mermaidVendorCounts + credential, 2},
		{"wake-trial-key", wakeTrialLedgerPath, persistedWakeKey, 0},
		{"wake-trial-key-other-path", "mobile-native/modules/evener-voice/core/VoiceCore.swift", persistedWakeKey, 1},
		{"wake-trial-changed-key", wakeTrialLedgerPath, strings.ReplaceAll(persistedWakeKey, ".v1", ".v2"), 1},
		{"wake-trial-credential-same-line", wakeTrialLedgerPath, persistedWakeKey + "; " + credential, 1},
		{"wake-trial-fixture-key", wakeTrialFixturePath, fixtureWakeKey, 0},
		{"wake-trial-fixture-other-path", "mobile-native/modules/evener-voice/tests/WakeWordTriggerTests.swift", fixtureWakeKey, 1},
		{"wake-trial-fixture-changed-key", wakeTrialFixturePath, strings.ReplaceAll(fixtureWakeKey, ".v1", ".v2"), 1},
		{"wake-trial-fixture-credential-same-line", wakeTrialFixturePath, fixtureWakeKey + "; " + credential, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(t.TempDir(), "findings.json")
			cmd := exec.CommandContext(t.Context(), bin, "detect", "--no-git", "--redact",
				"--config", config, "--source", ".", "--report-format", "json", "--report-path", report)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if tc.findings == 0 {
				if err != nil {
					t.Fatalf("gitleaks: %v\n%s", err, out)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("gitleaks exit: %v\n%s", err, out)
				}
			}
			data, err := os.ReadFile(report)
			if err != nil {
				t.Fatal(err)
			}
			var findings []struct{ RuleID, File string }
			if err := json.Unmarshal(data, &findings); err != nil {
				t.Fatal(err)
			}
			if len(findings) != tc.findings {
				t.Fatalf("findings=%d, want %d", len(findings), tc.findings)
			}
			for _, finding := range findings {
				if finding.RuleID != "generic-api-key" || finding.File != tc.path {
					t.Errorf("unexpected finding: %+v", finding)
				}
			}
		})
	}
}

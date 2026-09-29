package evener_test

import (
	"regexp"
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

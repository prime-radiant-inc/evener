package evener_test

import (
	"os/exec"
	"path/filepath"
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
func TestGitleaksAllowlistsBuiltFrontendDist(t *testing.T) {
	t.Parallel()

	paths := gitleaksAllowlistPaths(t)
	if !anyGitleaksPathMatches(paths, builtDistChunk) {
		t.Fatalf(".gitleaks.toml allowlists no path matching the built frontend dist chunk %q, "+
			"so a bare `make lint` after a web build fails on gitleaks' generic-api-key rule; "+
			"add the built output path to [allowlist].paths", builtDistChunk)
	}
	// Negative control: the exclusion must stay scoped to build output. A regex
	// broad enough to cover the whole frontend would hide a real secret in a
	// tracked source beside the dist.
	if anyGitleaksPathMatches(paths, trackedHubSource) {
		t.Errorf(".gitleaks.toml allowlists %q, but that is a tracked frontend source, not "+
			"build output; scope the dist exclusion so sources stay scanned", trackedHubSource)
	}

	// When gitleaks is installed, confirm the engine agrees: the scan it runs
	// for `make secret-scan` finds nothing in the dist but still flags a key in
	// a tracked source. Absent locally it skips, exactly as the gate does.
	gitleaks, err := exec.LookPath("gitleaks")
	if err != nil {
		t.Logf("gitleaks is not on PATH (%v); skipping the end-to-end scan, as the gate does", err)
		return
	}
	config, err := filepath.Abs(".gitleaks.toml")
	if err != nil {
		t.Fatalf("resolve .gitleaks.toml: %v", err)
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(builtDistChunk)),
		[]byte("var a=1,r=0,s=0;const x={token:r};r.token,s=r.operationId;console.log(x);\n"), 0o644)
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(trackedHubSource)),
		[]byte("const cfg={api_key:\"9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f00\"};\n"), 0o644)

	if out, err := runGitleaks(gitleaks, config, root, filepath.Join("cmd", "evener-hub", "frontend", "dist")); err != nil {
		t.Errorf("gitleaks flagged the built dist chunk despite the allowlist: %v\n%s", err, out)
	}
	if _, err := runGitleaks(gitleaks, config, root, filepath.Join("cmd", "evener-hub", "frontend", "src")); err == nil {
		t.Errorf("gitleaks found nothing in a tracked source holding an unallowlisted key; " +
			"the dist exclusion must not blind the scan to sources")
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

func anyGitleaksPathMatches(paths []string, target string) bool {
	for _, p := range paths {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(target) {
			return true
		}
	}
	return false
}

func runGitleaks(binary, config, dir, source string) ([]byte, error) {
	cmd := exec.Command(binary, "detect", "--no-git", "--redact", "--config", config, "--source", source)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

package promoter

import (
	"os"
	"path/filepath"
	"strings"
)

// persistEnv switches a rapid fuzz target from throwaway temp artifacts to
// committed, durable ones. It is read directly from the process environment
// (NOT via serf's envvars registry) because this package imports only the
// standard library — the no-serf-deps boundary that is the toolkit's
// portability guarantee. The variable is documented in fuzz/README.md and is
// set only by scripts/fuzz-triage.sh / scripts/run-fuzz.sh during a triage run.
const persistEnv = "SERF_FUZZ_PERSIST"

// PersistPaths reports where a rapid fuzz target should write its promoter
// artifacts (the emitted regression test, and the dedup bucket store).
//
// By default — persistEnv unset or non-truthy, which is every gate run
// (`make fuzz`, `make test`, `make test-race`) — it returns the caller's
// fallback temp paths unchanged and persist=false, so a fuzz run NEVER dirties
// the working tree.
//
// When persistEnv is truthy (set only by the local triage tool) AND a repo root
// (the directory holding go.work) is found by walking up from pkgDir, it returns
// pkgDir as the emit directory (so the generated *_test.go compiles in-package)
// and <root>/fuzz/state/buckets.json as the shared, committed bucket store path
// (one store across all targets, for cross-target dedup), with persist=true. If
// no repo root is found it declines persistence and returns the fallbacks, so a
// misconfigured environment can never write outside the repo.
func PersistPaths(pkgDir, fallbackEmitDir, fallbackBuckets string) (emitDir, bucketsPath string, persist bool) {
	if !persistTruthy(os.Getenv(persistEnv)) {
		return fallbackEmitDir, fallbackBuckets, false
	}
	root, ok := findRepoRoot(pkgDir)
	if !ok {
		return fallbackEmitDir, fallbackBuckets, false
	}
	return pkgDir, filepath.Join(root, "fuzz", "state", "buckets.json"), true
}

// persistTruthy reports whether v enables persistence. Only the canonical truthy
// spellings count; anything else (including "0", "false", "") leaves the gate's
// default-off behavior intact.
func persistTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// findRepoRoot walks up from start until it finds the directory containing
// go.work, returning it. It reports false if the filesystem root is reached
// without finding one.
func findRepoRoot(start string) (string, bool) {
	dir := start
	for dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

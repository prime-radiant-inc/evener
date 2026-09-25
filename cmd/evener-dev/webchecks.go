package dev

// web-checks is the frontend's single gate (make test-web): typecheck, unit
// tests and lint, through the shared web gate (webgate.go). The three are
// independent readers of the same sources, so they run at once and the wall
// time is the slowest one (vitest) rather than the sum. scripts/web/test-web.sh
// execs it.

import (
	"fmt"
	"os"
	"path/filepath"
)

// webChecks are the checks in verdict order; each is `npm run <check>`.
var webChecks = []string{"typecheck", "test", "lint"}

// newWebChecksGate is the web checks gate; the caller supplies where it runs
// (see runWebGate).
func newWebChecksGate() *webGate {
	return &webGate{
		name:   "web-checks",
		checks: webChecks,
		spec:   webCheckSpec,
		slots:  len(webChecks),
	}
}

// webCheckSpec runs check in the frontend with private HOME, TMPDIR and XDG
// roots under root, its own scratch directory, and Node's compile cache off.
func webCheckSpec(check, root string) guardSpec {
	return guardSpec{
		name: check,
		argv: []string{"npm", "run", check},
		dir:  frontendDir,
		env: []string{
			"HOME=" + filepath.Join(root, "home"),
			"TMPDIR=" + filepath.Join(root, "tmp"),
			"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"),
			"XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"),
			"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
			"NODE_DISABLE_COMPILE_CACHE=1",
		},
		// npm exits on TERM without stopping the script's children (tsc,
		// vitest and its workers, biome), which an interrupt would orphan.
		group: true,
	}
}

// runWebChecks is `evener dev web-checks`, run from the repository root.
func runWebChecks(args []string) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: evener dev web-checks")
		return 2
	}
	return runWebGate(newWebChecksGate(), "evener-test-web")
}

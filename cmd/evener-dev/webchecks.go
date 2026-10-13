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
// roots under root, its own scratch directory, Vite's dep cache inside that
// root, and Node's compile cache off.
func webCheckSpec(check, root string) guardSpec {
	argv := []string{"npm", "run", check}
	// The timing ratchet reads the frontend's durations from the same vitest
	// run the gate already pays for (issue #144): EVENER_WEB_VITEST_JSON names
	// a file to receive vitest's JSON reporter, while the default reporter
	// keeps the human log. `npm run test -- ...` forwards the flags to vitest
	// (the script hands "$@" through), and the test check's own package.json
	// script only ever adds them to vitest, never to the node helper tests.
	if check == "test" {
		if out := os.Getenv("EVENER_WEB_VITEST_JSON"); out != "" {
			argv = append(argv, "--", "--reporter=default", "--reporter=json", "--outputFile.json="+out)
		}
	}
	return guardSpec{
		name: check,
		argv: argv,
		dir:  frontendDir,
		env: []string{
			"HOME=" + filepath.Join(root, "home"),
			"TMPDIR=" + filepath.Join(root, "tmp"),
			"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"),
			"XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"),
			"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
			// Vite's dep-optimizer cache would otherwise land in the checkout's
			// .vite-cache, which a read-only checkout denies (issue #4193). The
			// root already holds a "vite-cache" dir (guardDirs); the browser
			// guards set BROWSER_GUARD_VITE_CACHE_DIR the same way.
			"EVENER_FRONTEND_VITE_CACHE_DIR=" + filepath.Join(root, "vite-cache"),
			"NODE_DISABLE_COMPILE_CACHE=1",
		},
		// npm exits on TERM without stopping the script's children (tsc,
		// vitest and its workers, biome), which an interrupt would orphan.
		group: true,
	}
}

// runWebChecks is `evener-dev dev web-checks`, run from the repository root.
func runWebChecks(args []string) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: evener-dev dev web-checks")
		return 2
	}
	return runWebGate(newWebChecksGate(), "evener-test-web")
}

package dev

// web-browser-guards runs the real browser-only frontend guards (make
// test-web-browser) through the shared web gate (webgate.go). They stay out of
// test-web because jsdom cannot evaluate the CSS cascade or browser geometry.
// scripts/web/test-web-browser.sh keeps only the load-aware default for the
// slot count and hands off here.
//
// The skill and retirement guards are the gate's never-signalled checks: each
// is a go test (the retirement guard's behind `npm run`) whose driver, Chrome
// and helper daemons are cleaned up by the test binary's own t.Cleanup, which a
// TERM to go test would skip and a TERM to npm alone would orphan; an interrupt
// waits for them instead. The skill guard is also the check that needs the
// built frontend (the hub serves the embedded dist), which the gate builds
// first when it is missing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// browserGuards is every guard in verdict order.
var browserGuards = []string{"layoutguard", "overflowguard", "shellguard", "spawnguard", "transcriptscrollguard", "retirementguard", "skillguard"}

const (
	skillGuard      = "skillguard"
	retirementGuard = "retirementguard"
)

// newBrowserGate is the browser gate over its seven guards; the caller supplies
// where it runs (see runWebGate).
func newBrowserGate(slots int, buildFrontend bool) *webGate {
	return &webGate{
		name:          "web-browser-guards",
		checks:        browserGuards,
		spec:          browserGuardSpec,
		needsBuild:    skillGuard,
		unsignalled:   []string{retirementGuard, skillGuard},
		slots:         slots,
		buildFrontend: buildFrontend,
	}
}

// browserGuardSpec is guard's launch contract under root, its own scratch
// directory. Each guard's Vite gets a dep cache of its own: two Vite processes
// optimizing into one cache race (issue #1586), and the guards run side by
// side. Node never writes a compile cache into the shared home.
func browserGuardSpec(guard, root string) guardSpec {
	vite := "BROWSER_GUARD_VITE_CACHE_DIR=" + filepath.Join(root, "vite-cache")
	switch guard {
	case skillGuard:
		// web-skillguard is the full-stack guard: cmd/evener-hub's
		// TestSkillComposerBrowser (browserguard build tag) drives the
		// production web app in real Chrome through a real hub against two
		// real `evener serve` daemons. The TestSkillGuard* unit tests ride
		// along: they need no browser and only this tag compiles them.
		return guardSpec{
			name: guard,
			argv: []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestSkillComposerBrowser$|^TestSkillGuard", "-count=1"},
			dir:  ".",
			env:  []string{vite},
		}
	case retirementGuard:
		// retirementguard's contract is `npm run retirementguard`: it runs the
		// isolated Go fixture (TestRetirementBrowser), which starts the fixture
		// Hub and drives scripts/retirementguard/run.mjs against it.
		return guardSpec{
			name:          guard,
			argv:          []string{"npm", "run", "retirementguard"},
			dir:           frontendDir,
			env:           []string{"TMPDIR=" + filepath.Join(root, "tmp"), "NODE_DISABLE_COMPILE_CACHE=1", vite},
			privateGoHome: true,
		}
	default:
		return guardSpec{
			name: guard,
			argv: []string{"node", "scripts/" + guard + "/run.mjs"},
			dir:  frontendDir,
			env: []string{
				"HOME=" + filepath.Join(root, "home"),
				"TMPDIR=" + filepath.Join(root, "tmp"),
				"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"),
				"XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"),
				"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
				"NODE_DISABLE_COMPILE_CACHE=1",
				vite,
			},
		}
	}
}

// browserGuardSlots reads BROWSER_GUARD_CONCURRENCY: digits only, read as
// decimal (08 is eight, 00 is zero), and at least one, so no value can leave
// the gate with no slot to start a guard in.
func browserGuardSlots(value string) int {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 1
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// runWebBrowserGuards is `evener-dev dev web-browser-guards`, run from the
// repository root.
func runWebBrowserGuards(args []string) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: evener-dev dev web-browser-guards (BROWSER_GUARD_CONCURRENCY sets the slots)")
		return 2
	}
	gate := newBrowserGate(browserGuardSlots(os.Getenv("BROWSER_GUARD_CONCURRENCY")), !frontendBuilt(frontendDir))
	return runWebGate(gate, "evener-test-web-browser")
}

// frontendBuilt reports whether frontend holds a built dist/index.html: a
// regular file, so nothing else at that path passes for a build.
func frontendBuilt(frontend string) bool {
	info, err := os.Stat(filepath.Join(frontend, "dist", "index.html"))
	return err == nil && info.Mode().IsRegular()
}

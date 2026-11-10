package dev

// web-browser-guards runs the real browser-only frontend guards (make
// test-web-browser) through the shared web gate (webgate.go). They stay out of
// test-web because jsdom cannot evaluate the CSS cascade or browser geometry.
// scripts/web/test-web-browser.sh keeps only the load-aware default for the
// slot count and hands off here.
//
// The skill/onboarding, cascade, background Jobs and retirement guards are
// never-signalled checks: each is a go test (the retirement guard's behind
// `npm run`) whose driver, Chrome and helper daemons are cleaned up by the test
// binary's own t.Cleanup, which a TERM to go test would skip and a TERM to npm
// alone would orphan; an interrupt waits for them instead. The skill/onboarding
// and cascade guards also need the built frontend (the hub serves the embedded
// dist), which the gate builds first when it is missing.

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// browserGuards is every guard in verdict order. The last four
// (skillguard, cascadeguard, backgroundjobsguard, backgroundtranscriptguard) are the never-signalled
// checks that also need the built production frontend (needsBuild);
// retirementguard is never-signalled too, but needs no build. Keep the
// building four last.
var browserGuards = []string{"layoutguard", "overflowguard", "shellguard", "spawnguard", "transcriptscrollguard", "retirementguard", "mermaidguard", "memoryguard", "skillguard", "cascadeguard", "backgroundjobsguard", backgroundTranscriptGuard}

const (
	skillGuard      = "skillguard"
	cascadeGuard    = "cascadeguard"
	retirementGuard = "retirementguard"
	// backgroundTranscriptGuard is the whole-journey guard: cmd/evener-hub's
	// TestBackgroundTranscriptsEntrySliceBrowser (browserguard build tag) drives
	// the real background-to-disk-to-pane journey in real Chrome through a real
	// hub and two real `evener serve` daemons, on the production entry route.
	backgroundTranscriptGuard = "backgroundtranscriptguard"
	backgroundJobsGuard       = "backgroundjobsguard"
)

// newBrowserGate is the browser gate over its guards; the caller supplies
// where it runs (see runWebGate).
func newBrowserGate(slots int, buildFrontend bool) *webGate {
	gate := &webGate{
		name:          "web-browser-guards",
		checks:        browserGuards,
		spec:          browserGuardSpec,
		slots:         slots,
		buildFrontend: buildFrontend,
	}
	// skillguard is the longest guard: with more than one slot, starting it
	// first overlaps it with the short guards instead of queueing it behind
	// every one of them (#3634). One slot saves no wall time and would make an
	// interrupt wait for the longest guard, so it keeps verdict order; on a
	// multi-slot lane an interrupt now waits for skillguard, as it would once
	// that guard had started in any slot. Verdicts still print in
	// browserGuards order, and the build fence still gates it.
	if slots > 1 {
		gate.launchFirst = skillGuard
	}
	return gate
}

// browserGuardSpec is guard's launch contract under root, its own scratch
// directory. Each guard's Vite gets a dep cache of its own: two Vite processes
// optimizing into one cache race (issue #1586), and the guards run side by
// side. Node never writes a compile cache into the shared home.
func browserGuardSpec(guard, root string) guardSpec {
	vite := "BROWSER_GUARD_VITE_CACHE_DIR=" + filepath.Join(root, "vite-cache")
	switch guard {
	case backgroundJobsGuard:
		return guardSpec{
			name:            guard,
			argv:            []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestBackgroundJobsBrowser$", "-count=1"},
			dir:             ".",
			env:             []string{vite},
			needsBuild:      true,
			waitOnInterrupt: true,
		}
	case cascadeGuard:
		return guardSpec{
			name:            guard,
			argv:            []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestAgentCascadeBrowser$", "-count=1"},
			dir:             ".",
			env:             []string{vite},
			needsBuild:      true,
			waitOnInterrupt: true,
		}
	case skillGuard:
		// web-skillguard carries the full-stack skill and onboarding guards:
		// cmd/evener-hub's TestSkillComposerBrowser, TestOnboardingBrowser and
		// TestOnboardingOAuthBrowser (browserguard build tag) drive the
		// production web app in real Chrome through a real hub against real
		// `evener serve` daemons. Their tagged TestSkillGuard* and
		// TestOnboardingEvidence* rejection tests ride along.
		return guardSpec{
			name:            guard,
			argv:            []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestSkillComposerBrowser$|^TestSkillGuard|^TestOnboardingBrowser$|^TestOnboardingOAuthBrowser$|^TestOnboardingEvidence", "-count=1"},
			dir:             ".",
			env:             []string{vite},
			needsBuild:      true,
			waitOnInterrupt: true,
		}
	case retirementGuard:
		// retirementguard's contract is `npm run retirementguard`: it runs the
		// isolated Go fixture (TestRetirementBrowser), which starts the fixture
		// Hub and drives scripts/retirementguard/run.mjs against it.
		return guardSpec{
			name:            guard,
			argv:            []string{"npm", "run", "retirementguard"},
			dir:             frontendDir,
			env:             []string{"TMPDIR=" + filepath.Join(root, "tmp"), "NODE_DISABLE_COMPILE_CACHE=1", vite},
			privateGoHome:   true,
			waitOnInterrupt: true,
		}
	case backgroundTranscriptGuard:
		// backgroundtranscriptguard's contract is cmd/evener-hub's
		// TestBackgroundTranscriptsEntrySliceBrowser (browserguard build tag):
		// the real background-to-disk-to-pane journey on the production entry
		// route in real Chrome through a real hub and two real `evener serve`
		// daemons. Like the skill guard it is a go test, so its driver, Chrome
		// and helper daemons are cleaned up by the test binary's own t.Cleanup.
		return guardSpec{
			name:            guard,
			argv:            []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestBackgroundTranscriptsEntrySliceBrowser$", "-count=1"},
			needsBuild:      true,
			waitOnInterrupt: true,
			dir:             ".",
			env:             []string{vite},
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

// browserGuardShards splits the gate across CI's web-browser runners, one
// shard per runner, so the lane's wall time is its slowest shard instead of
// every guard on one runner's four cores. Each shard is built around one long
// guard (overflowguard, skillguard, cascadeguard) with shorter guards beside
// it. Every guard belongs to exactly one shard, and the workflow's matrix
// names every shard.
var browserGuardShards = map[string][]string{
	"overflow": {"layoutguard", "overflowguard", "spawnguard", "transcriptscrollguard", "retirementguard", "mermaidguard", "memoryguard"},
	"skill":    {"shellguard", "skillguard"},
	"cascade":  {"cascadeguard", "backgroundjobsguard", backgroundTranscriptGuard},
}

// browserShardGuards is shard's guards in verdict order (BROWSER_GUARD_SHARD),
// or every guard when shard is empty. A name that is no shard is an error.
func browserShardGuards(shard string) ([]string, error) {
	if shard == "" {
		return browserGuards, nil
	}
	members, ok := browserGuardShards[shard]
	if !ok {
		return nil, fmt.Errorf("no browser guard shard %q (shards: %s)", shard, strings.Join(slices.Sorted(maps.Keys(browserGuardShards)), ", "))
	}
	var guards []string
	for _, guard := range browserGuards {
		if slices.Contains(members, guard) {
			guards = append(guards, guard)
		}
	}
	return guards, nil
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
		_, _ = fmt.Fprintln(os.Stderr, "usage: evener-dev dev web-browser-guards (BROWSER_GUARD_CONCURRENCY sets the slots, BROWSER_GUARD_SHARD runs one shard)")
		return 2
	}
	guards, err := browserShardGuards(os.Getenv("BROWSER_GUARD_SHARD"))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "evener-dev dev web-browser-guards: %v\n", err)
		return 2
	}
	gate := newBrowserGate(browserGuardSlots(os.Getenv("BROWSER_GUARD_CONCURRENCY")), !frontendBuilt(frontendDir))
	gate.checks = guards
	return runWebGate(gate, "evener-test-web-browser")
}

// frontendBuilt reports whether frontend holds a built dist/index.html: a
// regular file, so nothing else at that path passes for a build.
func frontendBuilt(frontend string) bool {
	info, err := os.Stat(filepath.Join(frontend, "dist", "index.html"))
	return err == nil && info.Mode().IsRegular()
}

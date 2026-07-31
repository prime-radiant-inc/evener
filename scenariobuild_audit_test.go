package serf_test

import (
	"os"
	"strings"
	"testing"
)

// TestSetupChecklistBuildsTheFrontendBeforeTheHub pins the one build ordering
// the runbook's Setup checklist cannot get wrong. cmd/serf-hub/frontend/dist
// is untracked except for a one-line PLACEHOLDER
// (cmd/serf-hub/frontend/.gitignore), and cmd/serf-hub/webnext.go embeds that
// directory at compile time (`//go:embed all:frontend/dist`), so a bare
// `go build ./cmd/serf-hub` in a fresh checkout or worktree bakes in an empty
// app: /api/* keeps working and every page route answers `503 serf-hub web
// app not built` (serveSPAIndex). Scenario cards inherit this checklist by
// default — test/scenarios/README.md: "a card that says nothing about the hub
// inherits that default" — so dropping the frontend build from the copyable
// recipe breaks every browser-driven card at once, and only at the first
// browser step, minutes in. Kata a6k8.
func TestSetupChecklistBuildsTheFrontendBeforeTheHub(t *testing.T) {
	const runbook = "docs/agentic-testing.md"
	raw, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatalf("reading %s: %v", runbook, err)
	}
	const hubBuild = `go build -o "$run/serf-hub" ./cmd/serf-hub`
	// Commands only: the surrounding comments name `make build-web` several
	// times, and a comment that mentions the frontend build is exactly the
	// state this audit exists to reject.
	commands := shellCommandLines(setupChecklistBlockContaining(t, string(raw), hubBuild))

	hub := indexOfLineContaining(commands, hubBuild)
	web := indexOfLineContaining(commands, "make build-web")
	switch {
	case web < 0:
		t.Fatalf("%s: the Setup checklist builds the hub with %q but never runs "+
			"`make build-web`, so the recipe produces a hub that 503s on every "+
			"page route (cmd/serf-hub/webnext.go serveSPAIndex). Build the "+
			"frontend in the same block, before the hub.", runbook, hubBuild)
	case web > hub:
		t.Fatalf("%s: the Setup checklist runs `make build-web` AFTER %q. The "+
			"hub embeds frontend/dist at compile time, so the frontend must be "+
			"built first or the binary carries the previous (or placeholder) "+
			"app.", runbook, hubBuild)
	}
}

// shellCommandLines returns block's lines with blanks and whole-line `#`
// comments dropped, so an audit reasons about what the recipe RUNS rather
// than what its prose says.
func shellCommandLines(block string) []string {
	var commands []string
	for line := range strings.SplitSeq(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		commands = append(commands, trimmed)
	}
	return commands
}

// indexOfLineContaining reports the position of the first line containing
// needle, or -1.
func indexOfLineContaining(lines []string, needle string) int {
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

// setupChecklistBlockContaining returns the body of the fenced code block in
// the runbook's "## Setup checklist" section that contains needle. It fails
// the test when the section or that block is gone, so a restructured
// checklist has to re-state the ordering rather than silently losing its
// guard.
func setupChecklistBlockContaining(t *testing.T, runbook, needle string) string {
	t.Helper()
	const heading = "## Setup checklist"
	_, section, found := strings.Cut(runbook, heading)
	if !found {
		t.Fatalf("docs/agentic-testing.md: %q section is gone; the scenario "+
			"cards that inherit it (test/scenarios/README.md) point here by name", heading)
	}
	if before, _, cut := strings.Cut(section, "\n## "); cut {
		section = before
	}
	// Splitting on the fence puts prose at even indices and block bodies at
	// odd ones, so only the odd parts are candidate recipes.
	parts := strings.Split(section, "```")
	for i := 1; i < len(parts); i += 2 {
		if strings.Contains(parts[i], needle) {
			return parts[i]
		}
	}
	t.Fatalf("docs/agentic-testing.md: no fenced block in %q contains %q; if the "+
		"checklist stopped building the hub itself, re-point this audit at "+
		"whatever now produces the binary a scenario runs", heading, needle)
	return ""
}

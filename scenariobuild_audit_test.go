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

// shellCommandInvocations returns shell command words split at unquoted
// operators, newlines, and comments. It deliberately models only the lexical
// boundary needed by repository audits: quoted text stays one word, so a
// command such as `echo "make lint"` cannot impersonate an invocation, while
// `true && make lint` produces a second command beginning with `make`.
func shellCommandInvocations(script string) [][]string {
	var commands [][]string
	var command []string
	var word strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	wordStarted := false

	flushWord := func() {
		if !wordStarted {
			return
		}
		command = append(command, word.String())
		word.Reset()
		wordStarted = false
	}
	flushCommand := func() {
		flushWord()
		if len(command) == 0 {
			return
		}
		command = shellStripControlWords(command)
		if len(command) > 0 {
			commands = append(commands, command)
		}
		command = nil
	}

	for i := 0; i < len(script); i++ {
		ch := script[i]
		switch {
		case inSingleQuote:
			if ch == '\'' {
				inSingleQuote = false
			} else {
				word.WriteByte(ch)
			}
			wordStarted = true
		case inDoubleQuote:
			switch ch {
			case '"':
				inDoubleQuote = false
			case '\\':
				if i+1 < len(script) {
					i++
					if script[i] != '\n' {
						word.WriteByte(script[i])
					}
				}
			default:
				word.WriteByte(ch)
			}
			wordStarted = true
		case ch == '\'':
			inSingleQuote = true
			wordStarted = true
		case ch == '"':
			inDoubleQuote = true
			wordStarted = true
		case ch == '\\':
			if i+1 < len(script) {
				i++
				if script[i] != '\n' {
					word.WriteByte(script[i])
				}
				wordStarted = true
			}
		case ch == '#':
			if !wordStarted {
				for i+1 < len(script) && script[i+1] != '\n' {
					i++
				}
				continue
			}
			word.WriteByte(ch)
		case ch == '\n':
			flushCommand()
		case ch == ';' || ch == '|' || ch == '&':
			flushCommand()
			for i+1 < len(script) && (script[i+1] == ';' || script[i+1] == '|' || script[i+1] == '&') {
				i++
			}
		case ch == ' ' || ch == '\t' || ch == '\r':
			flushWord()
		default:
			word.WriteByte(ch)
			wordStarted = true
		}
	}
	flushCommand()
	return commands
}

// shellStripControlWords removes shell syntax that can precede the command
// being audited. Assignments are environmental setup, and these keywords are
// grammar rather than executables; neither should hide a following `make`.
func shellStripControlWords(command []string) []string {
	for len(command) > 0 {
		if isShellControlWord(command[0]) || isShellAssignment(command[0]) {
			command = command[1:]
			continue
		}
		break
	}
	return command
}

func isShellControlWord(word string) bool {
	switch word {
	case "!", "if", "then", "elif", "else", "fi", "while", "until", "do", "done":
		return true
	default:
		return false
	}
}

func isShellAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	for i, ch := range name {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && ch != '_' && (i == 0 || ch < '0' || ch > '9') {
			return false
		}
	}
	return true
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

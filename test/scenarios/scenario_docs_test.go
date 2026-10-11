package scenarios

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staleStateRE matches any form of state/status being set or compared to the
// stale "processing" value (e.g. state=processing, "state": "processing",
// state='processing', status: processing). Use the canonical "active" value instead.
var staleStateRE = regexp.MustCompile(`(?i)(state|status)[=: '"]+processing`)

// promptProseGrepRE matches a card that tells the operator to confirm the
// running binary by reading assembled system-prompt prose — a `grep` for the
// prompt's words. Prompt prose is not a test oracle (see
// docs/developing-evener/testing.md "Prompt Prose Is Not a Test Oracle"): the
// sentence such a grep looks for is pinned by nothing, so a prose rewrite
// leaves the needle matching no text and the precondition fails for a reason
// unrelated to the scenario. The line must carry both tokens — `grep` and
// `system prompt` — because a bare `grep` is ordinary tooling (a `ps aux | grep`
// liveness probe, a transcript scan) and only the pair names this anti-pattern.
var promptProseGrepRE = regexp.MustCompile(`(?i)grep[^\n]{0,80}system prompt`)

// TestScenarioPreStateDoesNotGrepPromptProse keeps a card's build-provenance
// precondition structural. Issue #2540: `job-delegate-wait-no-poll.md` told the
// operator to "grep the assembled system prompt for" a sentence that only the
// now-deleted TestBuildSystemPrompt_PinsAntiPollGuidance pinned, so the grep was
// a second copy of the prose oracle the system-prompt collapse removed. A card
// that wants to prove it is running the branch under test must assert something
// durable — the binary's build metadata — not prompt copy that any rewrite
// silently invalidates.
func TestScenarioPreStateDoesNotGrepPromptProse(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(".", entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for i, line := range strings.Split(string(body), "\n") {
			if m := promptProseGrepRE.FindString(line); m != "" {
				t.Fatalf("%s:%d greps the assembled system prompt for prose (%q); "+
					"prompt prose is not a test oracle — confirm the binary is the "+
					"checked-out build instead (its reported build commit vs "+
					"`git rev-parse --short HEAD`)",
					path, i+1, strings.TrimSpace(m))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no scenario cards were read — the audit is checking nothing")
	}
}

func TestScenarioDocsUseCanonicalActiveState(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(".", entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := staleStateRE.FindString(string(body)); m != "" {
			t.Fatalf("%s contains stale state form %q; use canonical active state", entry.Name(), m)
		}
	}
}

func TestInlineOutputImageScenarioCardsExist(t *testing.T) {
	for _, id := range []string{
		"read-image-tool-result-inline",
		"written-image-inline-after-reload",
		"shell-generated-image-path-inline",
		"unsafe-image-path-ignored",
		"output-image-lightbox-and-pane",
	} {
		path := id + ".md"
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("required scenario card %q missing at %s: %v", id, path, err)
		}
		if !strings.HasPrefix(string(body), "# "+id+":") {
			t.Fatalf("%s must start with canonical scenario heading %q", path, "# "+id+":")
		}
	}
}

// TestNativeMermaidOnDeviceScenarioCardExists pins the manual runbook the
// mermaid design spec names for the behavior vitest cannot observe on a
// device (docs/superpowers/specs/2026-09-29-mermaid-inline-diagrams-design.md
// "Testing", "Native real render: manual runbook on device"). Issue #3197: the
// native inline-diagram work merged without its runbook, leaving the device
// class unverified; this card is that runbook.
func TestNativeMermaidOnDeviceScenarioCardExists(t *testing.T) {
	const id = "native-mermaid-on-device"
	path := id + ".md"
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("required scenario card %q missing at %s: %v", id, path, err)
	}
	if !strings.HasPrefix(string(body), "# "+id+":") {
		t.Fatalf("%s must start with canonical scenario heading %q", path, "# "+id+":")
	}
}

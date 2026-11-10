package evener_test

import (
	"os"
	"strings"
	"testing"
)

// The hooks lifecycle spec (docs/superpowers/specs/2026-11-09-claude-hooks-lifecycle-compat-design.md)
// promised two amendments it did not carry, and the shipped code contradicted it
// where a reader would look for the contract. These audits pin the amended prose
// to what the code does, so the contradiction cannot return.
//
// The oracle is the production code each claim describes:
//   - agent/internal/hooks/matcher.go's isExactMatcher takes no event, so the
//     widened exact set applies to every event (finding: §4.1 claimed otherwise).
//   - agent/session_lifecycle.go's CloseForShutdown sends
//     prompt_input_exit when sessionEndPromptInputExit/idleForPromptExit prove
//     the session idle (finding: §4.7 said it is never sent).

const hooksLifecycleSpec = "docs/superpowers/specs/2026-11-09-claude-hooks-lifecycle-compat-design.md"

// TestHooksLifecycleSpec_MatcherSetIsWidenedForEveryEvent fails while §4.1 still
// claims FileChanged and StopFailure keep Claude's narrower exact set. The
// divergence itself stays named — it is real — but the section must say the
// shipped set covers every event.
func TestHooksLifecycleSpec_MatcherSetIsWidenedForEveryEvent(t *testing.T) {
	t.Parallel()
	section := safeAuditMarkdownSection(t, hooksLifecycleSpec, "### 4.1 Shared plumbing")
	if strings.Contains(section, "keep Claude's narrower set") {
		t.Fatalf("§4.1 still says FileChanged and StopFailure keep Claude's narrower exact set; "+
			"agent/internal/hooks/matcher.go's isExactMatcher takes no event, so the widened set applies to every event:\n%s", section)
	}
	if !strings.Contains(section, "applies to **every** event") {
		t.Errorf("§4.1 no longer states that the widened exact set applies to every event:\n%s", section)
	}
	// The divergence stays a documented difference, not a deleted topic.
	for _, want := range []string{"FileChanged", "StopFailure"} {
		if !strings.Contains(section, want) {
			t.Errorf("§4.1 dropped %s entirely; the accepted divergence must stay named", want)
		}
	}
}

// TestHooksLifecycleSpec_PromptInputExitIsSent fails while §4.7 still says
// prompt_input_exit is never sent. CloseForShutdown sends it for a provably idle
// session.
func TestHooksLifecycleSpec_PromptInputExitIsSent(t *testing.T) {
	t.Parallel()
	section := safeAuditMarkdownSection(t, hooksLifecycleSpec, "### 4.7 Corrections to already-fired events")
	if strings.Contains(section, "`prompt_input_exit` are never sent") {
		t.Fatalf("§4.7 still says prompt_input_exit is never sent; "+
			"agent/session_lifecycle.go's CloseForShutdown sends it for a provably idle session:\n%s", section)
	}
	if !strings.Contains(section, "CloseForShutdown") || !strings.Contains(section, "prompt_input_exit") {
		t.Errorf("§4.7 no longer names both CloseForShutdown and prompt_input_exit, so the sent reason is undocumented:\n%s", section)
	}
	if !strings.Contains(section, "Amendment") {
		t.Errorf("§4.7 lost the amendment that records what the shipped code does:\n%s", section)
	}
}

// safeAuditMarkdownSection returns the body of the first section whose trimmed
// line equals heading, up to the next ATX heading at the same or a higher level.
// It fails when the heading is missing, so a renamed section trips the audit
// rather than silently scanning nothing.
func safeAuditMarkdownSection(t *testing.T, path, heading string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	level := safeAuditHeadingLevel(heading)
	if level == 0 {
		t.Fatalf("heading %q is not an ATX heading", heading)
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s no longer carries the heading %q; the audit would otherwise pass vacuously", path, heading)
	}
	var b strings.Builder
	for _, line := range lines[start+1:] {
		if l := safeAuditHeadingLevel(line); l != 0 && l <= level {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// safeAuditHeadingLevel reports the ATX heading level of a line (1-6), or 0 when
// the line is not a heading.
func safeAuditHeadingLevel(line string) int {
	line = strings.TrimSpace(line)
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0
	}
	if n == len(line) || line[n] == ' ' {
		return n
	}
	return 0
}

package evener_test

import (
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

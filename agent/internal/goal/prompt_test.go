package goal

import (
	"strings"
	"testing"
)

func TestRenderPrompt(t *testing.T) {
	out := Render("fix <bug> & ship")

	// The objective must be XML-escaped inside the <objective> tags.
	if !strings.Contains(out, "fix &lt;bug&gt; &amp; ship") {
		t.Fatal("objective must be XML-escaped inside <objective>")
	}

	// One distinctive phrase from each §4 paragraph must be present.
	phrases := []struct {
		phrase    string
		paragraph string
	}{
		{"do not redefine success", "Continuation behavior"},
		{"merely-compatible", "Fidelity"},
		{"the loop ends on", "How this loop ends"},
		{"do not count as progress", "How this loop ends"},
		{`status "blocked"`, "When to call update_goal(blocked)"},
		{"Completion audit:", "Completion audit"},
		{"does NOT end the goal", "How this loop ends"},
	}
	for _, p := range phrases {
		if !strings.Contains(out, p.phrase) {
			t.Fatalf("prompt missing phrase %q (from paragraph %q)", p.phrase, p.paragraph)
		}
	}

	// Completion audit must appear exactly once (not duplicated).
	if count := strings.Count(out, "Completion audit:"); count != 1 {
		t.Fatalf("expected 'Completion audit:' exactly once, got %d", count)
	}

	// The <objective> placeholder must be replaced (no literal {{objective}}).
	if strings.Contains(out, "{{objective}}") {
		t.Fatal("rendered prompt still contains unreplaced {{objective}} placeholder")
	}

	// The objective XML tags must appear.
	if !strings.Contains(out, "<objective>") || !strings.Contains(out, "</objective>") {
		t.Fatal("rendered prompt must contain <objective>...</objective> tags")
	}
}

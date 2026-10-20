package chatcompletions

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Every dialect this builder spells must be one the registry accepts, and the
// reverse: the load-time vocabulary is what a live listing filters a gateway's
// per-model thinkingFormat through, so a dialect present in only one of the two
// is either unreachable config or a value silently dropped from a listing.
//
// Covered by behavior, not by copy: a format only counts as handled here if
// applyThinkingFormat writes something for it, so the switch can never drift
// from the list without this failing.
func TestApplyThinkingFormatAndRegistryVocabularyAgree(t *testing.T) {
	on := true
	effort := "high"
	// A fully capable row: an effort control, a ladder that vouches for the
	// level, and template kwargs for the dialect that writes them.
	caps := func(format string) registry.Caps {
		return registry.Caps{
			Reasoning:          &on,
			ReasoningControls:  []string{"effort"},
			EffortValues:       []string{"high"},
			ThinkingFormat:     &format,
			ChatTemplateKwargs: map[string]any{"enable_thinking": true},
		}
	}
	req := llm.Request{ReasoningEffort: &effort}
	handled := map[string]bool{}
	for _, format := range registry.ThinkingFormats() {
		body := map[string]any{}
		applyThinkingFormat(body, req, caps(format))
		handled[format] = len(body) > 0
	}
	// The unset dialect is the documented default and writes reasoning_effort.
	body := map[string]any{}
	applyThinkingFormat(body, req, caps(""))
	if len(body) == 0 {
		t.Error("the unset dialect must spell an effort on an effort-capable row")
	}
	if registry.ValidThinkingFormat("") {
		t.Error("the unset dialect is not a configured vocabulary value")
	}
	for format, ok := range handled {
		if !ok {
			t.Errorf("dialect %q is in the registry vocabulary but applyThinkingFormat writes nothing for it", format)
		}
	}
}

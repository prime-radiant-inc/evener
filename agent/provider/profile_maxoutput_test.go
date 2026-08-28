package provider

import (
	"testing"
)

// The row's max_output_tokens is the whole answer; a row without one reports
// 0 so the protocol's own default governs.
func TestProfileMaxOutputTokens(t *testing.T) {
	r := fixtureRegistry(t)
	known := mustResolve(t, r, "anthropic/claude-sonnet-4-5")
	if known.Resolved().Caps.MaxOutputTokens == nil {
		t.Fatal("the catalog carries an output cap for claude-sonnet-4-5; pick a model it covers")
	}
	if got, want := known.MaxOutputTokens(), *known.Resolved().Caps.MaxOutputTokens; got != want {
		t.Errorf("MaxOutputTokens() = %d, want the row's %d", got, want)
	}

	res := known.Resolved()
	res.Caps.MaxOutputTokens = nil
	if got := known.WithResolved(res).MaxOutputTokens(); got != 0 {
		t.Errorf("a row without an output cap reports %d, want 0", got)
	}
}

func TestProfileMaxInputTokens(t *testing.T) {
	r := fixtureRegistry(t)
	known := mustResolve(t, r, "openai/gpt-5.5")
	if known.Resolved().Caps.MaxInputTokens == nil {
		t.Fatal("the catalog carries an input cap for gpt-5.5; pick a model it covers")
	}
	if got, want := known.MaxInputTokens(), *known.Resolved().Caps.MaxInputTokens; got != want {
		t.Errorf("MaxInputTokens() = %d, want the row's %d", got, want)
	}

	res := known.Resolved()
	res.Caps.MaxInputTokens = nil
	if got := known.WithResolved(res).MaxInputTokens(); got != 0 {
		t.Errorf("a row without an input cap reports %d, want 0", got)
	}
}

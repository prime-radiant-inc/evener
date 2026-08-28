package provider

import (
	"reflect"
	"testing"
)

func TestW2Tail_cloneStringSlice_Nil(t *testing.T) {
	if cloneStringSlice(nil) != nil {
		t.Errorf("cloneStringSlice(nil) = non-nil")
	}
	got := cloneStringSlice([]string{"a", "b"})
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("cloneStringSlice = %v", got)
	}
}

// CheapModel falls through to the main model when nothing is configured and
// the instance carries no cheap_model; an instance that does carries it.
func TestW2Tail_CheapModel_DefaultFallthrough(t *testing.T) {
	r := fixtureRegistry(t)
	p := mustResolve(t, r, "ollama/llama3.1")
	if got := p.CheapModel(); got != p.Model() {
		t.Errorf("CheapModel default fallthrough = %q, want main model %q", got, p.Model())
	}
	a := mustResolve(t, r, "anthropic/claude-opus-5")
	if got := a.CheapModel(); got != "claude-haiku-4-5" {
		t.Errorf("anthropic CheapModel = %q", got)
	}
	if got := WithCheapModel(a, "  spaced  ").CheapModel(); got != "spaced" {
		t.Errorf("configured cheap model wins: %q", got)
	}
}

func TestW2Tail_ConfiguredCheapModel_Nil(t *testing.T) {
	var p *Profile
	if got := p.ConfiguredCheapModel(); got != "" {
		t.Errorf("nil ConfiguredCheapModel = %q, want empty", got)
	}
	if got := p.CheapProvider(); got != "" {
		t.Errorf("nil CheapProvider = %q, want empty", got)
	}
	if got := p.CheapModelRefString(); got != "" {
		t.Errorf("nil CheapModelRefString = %q, want empty", got)
	}
}

func TestW2Tail_withCheapModelFrom_Nil(t *testing.T) {
	p := mustResolve(t, fixtureRegistry(t), "anthropic/claude-opus-5")
	if p.withCheapModelFrom(nil) != p {
		t.Errorf("withCheapModelFrom(nil) should return receiver unchanged")
	}
	var nilP *Profile
	if nilP.withCheapModelFrom(p) != nil {
		t.Errorf("nil.withCheapModelFrom != nil")
	}
}

// WithCommunicateOverridesFrom appends the communicate def when the target
// profile lacks one, and is a no-op when the original has none.
func TestW2Tail_WithCommunicateOverridesFrom_AppendAndNoop(t *testing.T) {
	r := fixtureRegistry(t)
	orig := mustResolve(t, r, "anthropic/claude-opus-5")
	// Target profile with no communicate tool.
	target := mustResolve(t, r, "anthropic/claude-opus-5")
	target.toolDefs = nil
	got := target.WithCommunicateOverridesFrom(orig)
	if findToolDef(got, "communicate") == nil {
		t.Errorf("communicate not appended when target lacked it")
	}

	// Original with no communicate tool: no-op.
	noComm := mustResolve(t, r, "anthropic/claude-opus-5")
	noComm.toolDefs = nil
	before := len(orig.toolDefs)
	res := orig.WithCommunicateOverridesFrom(noComm)
	if len(res.toolDefs) != before {
		t.Errorf("no-op path changed tool defs: %d -> %d", before, len(res.toolDefs))
	}
	var nilP *Profile
	if nilP.WithCommunicateOverridesFrom(orig) != nil || orig.WithCommunicateOverridesFrom(nil) != orig {
		t.Errorf("nil communicate carry-over guard failed")
	}
}

// WithModel exercises the self-prefix strip, the re-resolve, the
// cross-instance fallthrough, and the unresolvable-id clone.
func TestW2Tail_WithModel_Paths(t *testing.T) {
	r := fixtureRegistry(t)
	gw := mustResolve(t, r, "work/glm-5")

	// Empty model keeps the current model.
	if got := gw.WithModel("  "); got.Model() != gw.Model() {
		t.Errorf("empty model changed model to %q", got.Model())
	}

	// Same-instance switch re-resolves.
	sw := gw.WithModel("glm-5-flash")
	if sw.Model() != "glm-5-flash" || sw.ID() != "work" {
		t.Errorf("same-instance switch = %s/%s", sw.ID(), sw.Model())
	}

	// Self-prefix strip: "work/model" -> "model".
	if stripped := gw.WithModel("work/glm-5-flash"); stripped.Model() != "glm-5-flash" {
		t.Errorf("self-prefix not stripped: %q", stripped.Model())
	}

	// A cross-instance ref passes through unchanged (the resolver's job).
	if cross := gw.WithModel("google/gemini-3-pro"); cross.Model() != "google/gemini-3-pro" || cross.ID() != "work" {
		t.Errorf("cross-instance ref should pass through unchanged, got %s/%s", cross.ID(), cross.Model())
	}

	// An id the instance cannot resolve at all (the Codex allowlist) is kept
	// verbatim so the membership check reports it.
	codex := mustResolve(t, r, "openai-codex/gpt-5.6")
	off := codex.WithModel("not-on-the-allowlist")
	if off.ID() != "openai-codex" || off.Model() != "not-on-the-allowlist" {
		t.Errorf("unresolvable id = %s/%s", off.ID(), off.Model())
	}
}

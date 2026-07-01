package provider

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
)

// cloneAnyValue must recurse through every supported container shape and
// pass scalars through unchanged.
func TestW2Tail_cloneAnyValue_AllShapes(t *testing.T) {
	src := map[string]any{
		"nested": map[string]any{"k": "v"},
		"maps":   []map[string]any{{"a": 1}, {"b": 2}},
		"anys":   []any{"x", map[string]any{"y": "z"}},
		"strs":   []string{"one", "two"},
		"scalar": 42,
	}
	got := cloneAnyMap(src)

	if !reflect.DeepEqual(got, src) {
		t.Fatalf("cloneAnyMap round-trip mismatch\n got=%#v\nwant=%#v", got, src)
	}
	// Mutating the clone's nested containers must not touch the source.
	got["maps"].([]map[string]any)[0]["a"] = 999
	if src["maps"].([]map[string]any)[0]["a"] != 1 {
		t.Errorf("cloneAnyValue []map[string]any not deep-copied")
	}
	got["anys"].([]any)[1].(map[string]any)["y"] = "mutated"
	if src["anys"].([]any)[1].(map[string]any)["y"] != "z" {
		t.Errorf("cloneAnyValue []any not deep-copied")
	}
	got["strs"].([]string)[0] = "changed"
	if src["strs"].([]string)[0] != "one" {
		t.Errorf("cloneAnyValue []string not deep-copied")
	}
	if cloneAnyValue(nil) != nil {
		t.Errorf("cloneAnyValue(nil) = non-nil")
	}
	if cloneAnyMap(nil) != nil {
		t.Errorf("cloneAnyMap(nil) = non-nil")
	}
}

func TestW2Tail_cloneStringSlice_Nil(t *testing.T) {
	if cloneStringSlice(nil) != nil {
		t.Errorf("cloneStringSlice(nil) = non-nil")
	}
	got := cloneStringSlice([]string{"a", "b"})
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("cloneStringSlice = %v", got)
	}
}

// CheapModel falls through to the main model when no cheap model is configured
// and the provider has no default (openai-compat "kimi").
func TestW2Tail_CheapModel_DefaultFallthrough(t *testing.T) {
	p := newOpenAICompatProfile("kimi", "kimi-k2", 0)
	if got := p.CheapModel(); got != p.Model() {
		t.Errorf("CheapModel default fallthrough = %q, want main model %q", got, p.Model())
	}
	// Anthropic has an explicit default.
	a := newAnthropicProfile("claude-opus-4-6")
	if got := a.CheapModel(); got != "claude-haiku-4-5-20251001" {
		t.Errorf("anthropic CheapModel = %q", got)
	}
}

func TestW2Tail_ConfiguredCheapModel_Nil(t *testing.T) {
	var p *Profile
	if got := p.ConfiguredCheapModel(); got != "" {
		t.Errorf("nil ConfiguredCheapModel = %q, want empty", got)
	}
}

// WithLiveModelInfo overrides each field only when the info field is present,
// and returns nil for a nil receiver.
func TestW2Tail_WithLiveModelInfo_Branches(t *testing.T) {
	var nilP *Profile
	if nilP.WithLiveModelInfo(llm.ModelInfo{}) != nil {
		t.Errorf("nil.WithLiveModelInfo != nil")
	}

	base := newOpenAICompatProfile("kimi", "kimi-k2", 100_000)
	yes := true
	q := base.WithLiveModelInfo(llm.ModelInfo{
		ContextWindow:     222_222,
		SupportsReasoning: true,
		SupportsWebSearch: &yes,
	})
	if q.ContextWindowSize() != 222_222 {
		t.Errorf("context window not overridden: %d", q.ContextWindowSize())
	}
	if !q.SupportsWebSearch() {
		t.Errorf("web search not enabled from live info")
	}

	// Empty info leaves the profile unchanged (no field overrides).
	same := base.WithLiveModelInfo(llm.ModelInfo{})
	if same.ContextWindowSize() != base.ContextWindowSize() {
		t.Errorf("empty info changed context window")
	}
}

func TestW2Tail_withCheapModelFrom_Nil(t *testing.T) {
	p := newAnthropicProfile("claude-opus-4-6")
	if p.withCheapModelFrom(nil) != p {
		t.Errorf("withCheapModelFrom(nil) should return receiver unchanged")
	}
	var nilP *Profile
	if nilP.withCheapModelFrom(p) != nil {
		t.Errorf("nil.withCheapModelFrom != nil")
	}
}

func TestW2Tail_restampInstanceIdentity_Nil(t *testing.T) {
	if restampInstanceIdentity(nil, "x", "y") != nil {
		t.Errorf("restampInstanceIdentity(nil) != nil")
	}
}

// WithCommunicateOverridesFrom appends the communicate def when the target
// profile lacks one, and is a no-op when the original has none.
func TestW2Tail_WithCommunicateOverridesFrom_AppendAndNoop(t *testing.T) {
	orig := newAnthropicProfile("claude-opus-4-6")
	// Target profile with no communicate tool.
	target := newAnthropicProfile("claude-opus-4-6")
	target.toolDefs = nil
	got := target.WithCommunicateOverridesFrom(orig)
	if findToolDef(got, "communicate") == nil {
		t.Errorf("communicate not appended when target lacked it")
	}

	// Original with no communicate tool: no-op.
	noComm := newAnthropicProfile("claude-opus-4-6")
	noComm.toolDefs = nil
	before := len(orig.toolDefs)
	res := orig.WithCommunicateOverridesFrom(noComm)
	if len(res.toolDefs) != before {
		t.Errorf("no-op path changed tool defs: %d -> %d", before, len(res.toolDefs))
	}
}

// WithModel exercises the same-provider rebuild path (kimi/openrouter-anthropic),
// the self-prefix strip, the cross-provider fallthrough, and the empty-model
// default.
func TestW2Tail_WithModel_Paths(t *testing.T) {
	kimi := newOpenAICompatProfile("kimi", "kimi-k2", 0)

	// Empty model keeps the current model.
	if got := kimi.WithModel("  "); got.Model() != kimi.Model() {
		t.Errorf("empty model changed model to %q", got.Model())
	}

	// Same-provider rebuild for a rebuild-on-change provider.
	sw := kimi.WithModel("kimi-k2-instruct")
	if sw.Model() != "kimi-k2-instruct" {
		t.Errorf("kimi same-provider switch model = %q", sw.Model())
	}
	if sw.ID() != "kimi" || sw.BehaviorTag() != kimi.BehaviorTag() {
		t.Errorf("identity not preserved: id=%q tag=%q", sw.ID(), sw.BehaviorTag())
	}

	// Self-prefix strip: "kimi/model" -> "model".
	stripped := kimi.WithModel("kimi/kimi-k2-0905")
	if stripped.Model() != "kimi-k2-0905" {
		t.Errorf("self-prefix not stripped: %q", stripped.Model())
	}

	// Cross-provider ref falls through unchanged (resolver's job).
	cross := kimi.WithModel("google/gemini-2.5-flash")
	if cross.Model() != "google/gemini-2.5-flash" {
		t.Errorf("cross-provider ref should pass through unchanged, got %q", cross.Model())
	}

	// openrouter-anthropic rebuild path.
	ora := newOpenRouterAnthropicProfile("anthropic/claude-opus-4-5-20251101")
	ora2 := ora.WithModel("anthropic/claude-sonnet-4-5-20250929")
	if ora2.Model() != "anthropic/claude-sonnet-4-5-20250929" {
		t.Errorf("openrouter-anthropic rebuild model = %q", ora2.Model())
	}
}

// The anthropic WithModel branch strips a redundant "anthropic/" self-prefix
// but leaves cross-provider prefixes intact.
func TestW2Tail_WithModel_AnthropicSelfPrefix(t *testing.T) {
	p := newAnthropicProfile("claude-opus-4-6")
	got := p.WithModel("anthropic/claude-opus-4-5-20251101")
	if got.Model() != "claude-opus-4-5-20251101" {
		t.Errorf("anthropic self-prefix not stripped: %q", got.Model())
	}
}

// resolveOpenRouterAnthropicCtxAndEfforts prefers the prefixed catalog hit,
// falls back to the bare lookup, and returns defaults when neither hits.
func TestW2Tail_resolveOpenRouterAnthropicCtxAndEfforts(t *testing.T) {
	defEfforts := []string{"low", "medium", "high"}

	// Prefixed hit provides context + efforts.
	lookupPrefixed := func(key string) *llm.ModelInfo {
		if key == "openrouter/m" {
			return &llm.ModelInfo{ContextWindow: 500_000, ReasoningEffortLevels: []string{"medium", "high"}}
		}
		return nil
	}
	ctx, eff := resolveOpenRouterAnthropicCtxAndEfforts(lookupPrefixed, "m", 1000, defEfforts)
	if ctx != 500_000 || !reflect.DeepEqual(eff, []string{"medium", "high"}) {
		t.Errorf("prefixed hit: ctx=%d eff=%v", ctx, eff)
	}

	// No prefixed hit; bare lookup supplies context only.
	lookupBare := func(key string) *llm.ModelInfo {
		if key == "m" {
			return &llm.ModelInfo{ContextWindow: 42_000}
		}
		return nil
	}
	ctx, eff = resolveOpenRouterAnthropicCtxAndEfforts(lookupBare, "m", 1000, defEfforts)
	if ctx != 42_000 || !reflect.DeepEqual(eff, defEfforts) {
		t.Errorf("bare hit: ctx=%d eff=%v", ctx, eff)
	}

	// No hit at all: defaults.
	ctx, eff = resolveOpenRouterAnthropicCtxAndEfforts(func(string) *llm.ModelInfo { return nil }, "m", 1000, defEfforts)
	if ctx != 1000 || !reflect.DeepEqual(eff, defEfforts) {
		t.Errorf("no hit: ctx=%d eff=%v", ctx, eff)
	}
}

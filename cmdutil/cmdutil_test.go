package cmdutil

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestMaxRoundsToConfig(t *testing.T) {
	tests := []struct {
		name     string
		cli      int
		wantConf int
	}{
		{"not specified", -1, 0},   // 0 → applyDefaults sets to 200
		{"unlimited", 0, -1},       // -1 → no limit in session loop
		{"explicit limit", 50, 50}, // pass through
		{"explicit limit 1", 1, 1}, // edge case
		{"very negative", -999, 0}, // any negative → not specified
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxRoundsToConfig(tt.cli)
			if got != tt.wantConf {
				t.Fatalf("MaxRoundsToConfig(%d) = %d, want %d", tt.cli, got, tt.wantConf)
			}
		})
	}
}

func TestResolveReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		cli     string
		env     string
		wantSet bool
		wantVal string
		wantErr bool
	}{
		{name: "unset", cli: "", env: "", wantSet: false},
		{name: "env medium", cli: "", env: "medium", wantSet: true, wantVal: "medium"},
		{name: "cli overrides env", cli: "HIGH", env: "low", wantSet: true, wantVal: "high"},
		{name: "cli none clears", cli: "none", env: "high", wantSet: true, wantVal: ""},
		{name: "env none clears", cli: "", env: "none", wantSet: true, wantVal: ""},
		{name: "xhigh", cli: "xhigh", env: "", wantSet: true, wantVal: "xhigh"},
		{name: "minimal", cli: "minimal", env: "", wantSet: true, wantVal: "minimal"},
		{name: "off alias clears", cli: "off", env: "", wantSet: true, wantVal: ""},
		{name: "invalid", cli: "banana", env: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveReasoningEffort(tt.cli, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Set != tt.wantSet {
				t.Fatalf("Set=%v want %v", got.Set, tt.wantSet)
			}
			if got.Value != tt.wantVal {
				t.Fatalf("Value=%q want %q", got.Value, tt.wantVal)
			}
		})
	}
}

func TestResolveModelRef_QualifiedModelSuppliesProvider(t *testing.T) {
	got, err := ResolveModelRef("openai/gpt-5.2", "", "", "")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.2" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-5.2", got.Provider, got.Model)
	}
	if got.Qualified() != "openai/gpt-5.2" {
		t.Fatalf("Qualified()=%q", got.Qualified())
	}
}

func TestResolveModelRef_EnvModelSuppliesProvider(t *testing.T) {
	got, err := ResolveModelRef("", "Anthropic/claude-opus-4-6", "", "")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveModelRef_RejectsBareStartupModel(t *testing.T) {
	_, err := ResolveModelRef("gpt-5.2", "", "", "")
	if err == nil {
		t.Fatal("expected error for bare startup model")
	}
	if !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("error=%q, want provider/model guidance", err.Error())
	}
}

func TestResolveModelRef_ResumeMetaSuppliesBareModelProvider(t *testing.T) {
	got, err := ResolveModelRef("", "", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveResumeModelRef_PersistedMetaBeatsEnv(t *testing.T) {
	got, err := ResolveResumeModelRef("", "openai/gpt-env", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveResumeModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveResumeModelRef_CLIOverridesPersistedMeta(t *testing.T) {
	got, err := ResolveResumeModelRef("openai/gpt-cli", "openai/gpt-env", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveResumeModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-cli" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-cli", got.Provider, got.Model)
	}
}

func TestResolveResumeModelRef_UsesEnvWhenMetaMissing(t *testing.T) {
	got, err := ResolveResumeModelRef("", "openai/gpt-env", "", "")
	if err != nil {
		t.Fatalf("ResolveResumeModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-env" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-env", got.Provider, got.Model)
	}
}

func TestParseAllowedDecisions_CommaSeparated(t *testing.T) {
	got := parseAllowedDecisions("approved,changes_requested")
	if len(got) != 2 || got[0] != "approved" || got[1] != "changes_requested" {
		t.Fatalf("got %v, want [approved changes_requested]", got)
	}
}

func TestParseAllowedDecisions_JSONArray(t *testing.T) {
	got := parseAllowedDecisions(`["pass","fail"]`)
	if len(got) != 2 || got[0] != "pass" || got[1] != "fail" {
		t.Fatalf("got %v, want [pass fail]", got)
	}
}

func TestParseAllowedDecisions_Empty(t *testing.T) {
	if got := parseAllowedDecisions(""); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestParseAllowedDecisions_Whitespace(t *testing.T) {
	got := parseAllowedDecisions(" approved , changes_requested ")
	if len(got) != 2 || got[0] != "approved" || got[1] != "changes_requested" {
		t.Fatalf("got %v, want [approved changes_requested]", got)
	}
}

func TestIsOpenAICompatTag(t *testing.T) {
	compat := []string{"openai-compatible", "kimi", "glm", "openrouter", "ollama"}
	for _, tag := range compat {
		if !isOpenAICompatTag(tag) {
			t.Errorf("isOpenAICompatTag(%q) = false, want true", tag)
		}
	}
	notCompat := []string{"openai", "anthropic", "google", "minimax", "openrouter-anthropic", ""}
	for _, tag := range notCompat {
		if isOpenAICompatTag(tag) {
			t.Errorf("isOpenAICompatTag(%q) = true, want false", tag)
		}
	}
}

// stubQueryModelContextWindow replaces the live /models lookup for the duration
// of a test and restores it on cleanup. It records the (provider, model) the
// resolver passed so the test can assert the lookup keys on the behavior tag.
func stubQueryModelContextWindow(t *testing.T, fn func(provider, model string) int) *struct {
	called  bool
	gotProv string
	gotMod  string
} {
	t.Helper()
	rec := &struct {
		called  bool
		gotProv string
		gotMod  string
	}{}
	orig := queryModelContextWindow
	queryModelContextWindow = func(provider, model string) int {
		rec.called = true
		rec.gotProv = provider
		rec.gotMod = model
		return fn(provider, model)
	}
	t.Cleanup(func() { queryModelContextWindow = orig })
	return rec
}

// TestResolveProfileWithLiveWindow_OpenAICompatAppliesLiveWindow verifies the
// app-layer default: for an openai-compat instance the resolver refines the
// context window with the live lookup, keyed on the behavior tag (the provider
// type), not on the instance-name segment of the ref.
func TestResolveProfileWithLiveWindow_OpenAICompatAppliesLiveWindow(t *testing.T) {
	const liveWindow = 262_144
	rec := stubQueryModelContextWindow(t, func(provider, model string) int {
		return liveWindow
	})

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			// Instance name "kc" differs from the kimi behavior tag.
			{Name: "kc", Type: "kimi"},
		},
	}
	p, err := ResolveProfileWithLiveWindow(cfg, "kc/kimi-k2")
	if err != nil {
		t.Fatalf("ResolveProfileWithLiveWindow: %v", err)
	}
	if got := p.ContextWindowSize(); got != liveWindow {
		t.Fatalf("ContextWindowSize() = %d, want %d (live window must be applied)", got, liveWindow)
	}
	if !rec.called {
		t.Fatal("expected live lookup to be called for openai-compat provider")
	}
	if rec.gotProv != "kimi" {
		t.Fatalf("live lookup provider = %q, want %q (must key on behavior tag, not instance name)", rec.gotProv, "kimi")
	}
	if rec.gotMod != "kimi-k2" {
		t.Fatalf("live lookup model = %q, want %q", rec.gotMod, "kimi-k2")
	}
	if p.ID() != "kc" {
		t.Fatalf("profile ID = %q, want %q", p.ID(), "kc")
	}
}

// TestResolveProfileWithLiveWindow_FallsBackWhenLookupZero verifies that a
// zero result from the live lookup (no creds / offline) preserves the
// catalog-derived window rather than zeroing it out.
func TestResolveProfileWithLiveWindow_FallsBackWhenLookupZero(t *testing.T) {
	stubQueryModelContextWindow(t, func(provider, model string) int { return 0 })

	const catalogModel = "moonshot/kimi-latest-128k"
	cat := llm.EmbeddedModelCatalog()
	mi := cat.GetModelInfo(catalogModel)
	if mi == nil || mi.ContextWindow == 0 {
		t.Fatalf("catalog model %q missing or zero window; pick another", catalogModel)
	}
	wantCtx := mi.ContextWindow

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "kc", Type: "kimi"},
		},
	}
	p, err := ResolveProfileWithLiveWindow(cfg, "kc/"+catalogModel)
	if err != nil {
		t.Fatalf("ResolveProfileWithLiveWindow: %v", err)
	}
	if got := p.ContextWindowSize(); got != wantCtx {
		t.Fatalf("ContextWindowSize() = %d, want %d (catalog fallback when lookup returns 0)", got, wantCtx)
	}
}

// TestResolveProfileWithLiveWindow_NonCompatSkipsLookup verifies the live
// lookup is NOT invoked for non-openai-compat providers (e.g. anthropic), so
// those profiles keep their constructor-derived window untouched.
func TestResolveProfileWithLiveWindow_NonCompatSkipsLookup(t *testing.T) {
	rec := stubQueryModelContextWindow(t, func(provider, model string) int {
		t.Errorf("live lookup must not be called for non-openai-compat provider; got (%q,%q)", provider, model)
		return 999
	})

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "ant", Type: "anthropic"},
		},
	}
	p, err := ResolveProfileWithLiveWindow(cfg, "ant/claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveProfileWithLiveWindow: %v", err)
	}
	if rec.called {
		t.Fatal("live lookup was called for anthropic; expected skip")
	}
	if got := p.ContextWindowSize(); got != 200_000 {
		t.Fatalf("ContextWindowSize() = %d, want 200000 (anthropic default untouched)", got)
	}
}

// TestResolveProfileForProvider_NetworkFree verifies the no-config probe helper
// resolves common providers without invoking the live lookup, and maps the
// openai/openai-compat roster the same way the seeded path does.
func TestResolveProfileForProvider_NetworkFree(t *testing.T) {
	stubQueryModelContextWindow(t, func(provider, model string) int {
		t.Errorf("ResolveProfileForProvider must be network-free; live lookup called with (%q,%q)", provider, model)
		return 999
	})

	cases := []struct {
		provider string
		model    string
		wantID   string
		wantTag  string
	}{
		{"openai", "gpt-5.2", "openai", "openai"},
		{"anthropic", "claude-opus-4-6", "anthropic", "anthropic"},
		{"kimi", "kimi-k2", "kimi", "kimi"},
		{"openrouter", "free", "openrouter", "openrouter"},
		{"ollama", "llama3.1", "ollama", "ollama"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			p, err := ResolveProfileForProvider(tc.provider, tc.model)
			if err != nil {
				t.Fatalf("ResolveProfileForProvider(%q): %v", tc.provider, err)
			}
			if p.ID() != tc.wantID {
				t.Errorf("ID() = %q, want %q", p.ID(), tc.wantID)
			}
			if p.BehaviorTag() != tc.wantTag {
				t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), tc.wantTag)
			}
			if p.Model() != tc.model {
				t.Errorf("Model() = %q, want %q", p.Model(), tc.model)
			}
		})
	}
}

func TestResolveProfileForProvider_UnknownProvider(t *testing.T) {
	_, err := ResolveProfileForProvider("missing", "free")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error = %v, want to contain 'unknown provider'", err)
	}
}

func TestStringSliceFlag(t *testing.T) {
	var f StringSliceFlag
	if err := f.Set("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("b"); err != nil {
		t.Fatal(err)
	}
	if f.String() != "a,b" {
		t.Fatalf("String() = %q, want %q", f.String(), "a,b")
	}
	if len(f) != 2 || f[0] != "a" || f[1] != "b" {
		t.Fatalf("values = %v, want [a b]", []string(f))
	}
}

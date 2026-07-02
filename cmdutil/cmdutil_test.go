package cmdutil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/kimicoding"
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
		{name: "max alias of top tier", cli: "max", env: "", wantSet: true, wantVal: "max"},
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
	queryModelContextWindow = func(provider, model, _, _ string) int {
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
	const model = "moonshot/kimi-latest-128k"

	// Capture the catalog-derived window via the network-free resolver before
	// the stub is installed. This uses the same code path the resolver itself
	// uses internally, avoiding a fragile direct catalog key lookup.
	baseline, err := ResolveProfileForProvider("kimi", model)
	if err != nil {
		t.Fatalf("ResolveProfileForProvider: %v", err)
	}
	wantCtx := baseline.ContextWindowSize()
	if wantCtx == 0 {
		t.Fatal("baseline catalog window is 0; cannot verify fallback")
	}

	stubQueryModelContextWindow(t, func(provider, model string) int { return 0 })

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "kc", Type: "kimi"},
		},
	}
	p, err := ResolveProfileWithLiveWindow(cfg, "kc/"+model)
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

// The real queryModelContextWindow must query the instance's base URL (passed
// explicitly) rather than the provider-type default — so a Kimi coding-plan
// instance (custom base_url in providers.toml) is sized from its own /models.
func TestQueryModelContextWindow_UsesInstanceBaseURL(t *testing.T) {
	var gotPath, gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-for-coding","context_length":262144}]}`))
	}))
	t.Cleanup(srv.Close)

	got := queryModelContextWindow("kimi", "kimi-for-coding", srv.URL, "inst-key")
	if got != 262144 {
		t.Fatalf("queryModelContextWindow = %d, want 262144 (must hit the instance base URL)", got)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer inst-key" {
		t.Fatalf("auth = %q, want Bearer inst-key (instance api key)", gotAuth)
	}
	// Kimi For Coding gates on a coding-agent User-Agent allowlist, so the
	// /models probe must announce it just like the chat adapters do.
	if gotUA != kimicoding.UserAgent {
		t.Fatalf("User-Agent = %q, want %q (Kimi coding-agent allowlist)", gotUA, kimicoding.UserAgent)
	}
}

// Only the Kimi coding plan gates on the coding-agent User-Agent, so other
// openai-compat providers must not have it spoofed onto their /models probe.
func TestQueryModelContextWindow_NonKimiOmitsCodingUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-4.6","context_length":200000}]}`))
	}))
	t.Cleanup(srv.Close)

	got := queryModelContextWindow("glm", "glm-4.6", srv.URL, "inst-key")
	if got != 200000 {
		t.Fatalf("queryModelContextWindow = %d, want 200000", got)
	}
	// Assert the exact expected UA: no explicit header set → Go default.
	// Asserting the exact value catches any mutation that injects a wrong
	// explicit UA (e.g. "anthropic-agent/1.0") which the negative-only
	// check would silently pass.
	if gotUA != "Go-http-client/1.1" {
		t.Fatalf("User-Agent = %q, want %q (must not inject any explicit UA for non-kimi provider)", gotUA, "Go-http-client/1.1")
	}
}

// TestResolveProfileWithLiveWindow_ConfiguredWindowSkipsLiveOverride verifies
// that an explicit models.<id>.context_window in providers.toml beats the live
// /models probe — user config is authoritative over provider metadata.
func TestResolveProfileWithLiveWindow_ConfiguredWindowSkipsLiveOverride(t *testing.T) {
	rec := stubQueryModelContextWindow(t, func(provider, model string) int {
		return 999_999
	})

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name:     "gw",
				Type:     "openai",
				APIStyle: providercfg.StyleChatCompletions,
				Models: map[string]providercfg.ModelConfig{
					"glm-5.2-nvfp4": {ContextWindow: 1_048_576},
				},
			},
		},
	}
	p, err := ResolveProfileWithLiveWindow(cfg, "gw/glm-5.2-nvfp4")
	if err != nil {
		t.Fatalf("ResolveProfileWithLiveWindow: %v", err)
	}
	if got := p.ContextWindowSize(); got != 1_048_576 {
		t.Fatalf("ContextWindowSize() = %d, want configured 1048576 (live probe must not override)", got)
	}
	if rec.called {
		t.Fatal("live lookup ran despite a configured context_window")
	}

	// A model on the same instance WITHOUT a configured window still refines
	// from the live probe.
	q, err := ResolveProfileWithLiveWindow(cfg, "gw/other-model")
	if err != nil {
		t.Fatalf("ResolveProfileWithLiveWindow(other): %v", err)
	}
	if got := q.ContextWindowSize(); got != 999_999 {
		t.Fatalf("ContextWindowSize(other) = %d, want live 999999", got)
	}
}

// An openai + api_style=chat-completions instance (behavior tag
// "openai-compatible") is a custom gateway: the live window probe must query
// its configured base_url — including keyless local gateways — and must bail
// without one (there is no meaningful type-level default endpoint).
func TestQueryModelContextWindow_OpenAICompatibleInstance(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.2-nvfp4","context_length":204800}]}`))
	}))
	t.Cleanup(srv.Close)

	if got := queryModelContextWindow("openai-compatible", "glm-5.2-nvfp4", srv.URL, "gw-key"); got != 204800 {
		t.Fatalf("queryModelContextWindow = %d, want 204800", got)
	}
	if gotAuth != "Bearer gw-key" {
		t.Fatalf("auth = %q, want Bearer gw-key", gotAuth)
	}

	// Keyless gateways (local proxies) still probe — without an Authorization
	// header rather than a bogus "Bearer ".
	gotAuth = "unset"
	if got := queryModelContextWindow("openai-compatible", "glm-5.2-nvfp4", srv.URL, ""); got != 204800 {
		t.Fatalf("keyless queryModelContextWindow = %d, want 204800", got)
	}
	if gotAuth != "" {
		t.Fatalf("keyless auth header = %q, want absent", gotAuth)
	}

	// No configured base_url → no probe (0 keeps the catalog window).
	if got := queryModelContextWindow("openai-compatible", "glm-5.2-nvfp4", "", "gw-key"); got != 0 {
		t.Fatalf("no-base-url queryModelContextWindow = %d, want 0", got)
	}
}

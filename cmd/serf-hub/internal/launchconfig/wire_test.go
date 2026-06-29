package launchconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestFromWire(t *testing.T) {
	noProjectPrompts := true
	appReplaySize := 42
	in := appwire.LaunchConfigLayer{
		Model:                       "openai/gpt-5",
		FastCheapModel:              "openai/gpt-5-mini",
		OpenAIResponsesContinuation: "auto",
		Schema:                      ptrInt(1),
		MCPs:                        []appwire.MCPServerSpec{{Name: "x", Command: "y", Args: []string{"z"}}},
		MaxRounds:                   ptrInt(50),
		Agent:                       "myagent",
		ReasoningEffort:             "high",
		ContextStrategy:             "compact",
		SkillsDirs:                  []string{"/skills"},
		PluginDirs:                  []string{"/plugins"},
		MCPConfigs:                  []string{"/mcp.toml"},
		NoProjectPrompts:            &noProjectPrompts,
		AppReplaySize:               &appReplaySize,
		Env:                         map[string]string{"FOO": "bar"},
	}
	got := FromWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Errorf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
	if got.Schema != 1 {
		t.Errorf("Schema = %d, want 1", got.Schema)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v, want 50", got.MaxRounds)
	}
	if got.OpenAIResponsesContinuation != "auto" {
		t.Errorf("OpenAIResponsesContinuation = %q, want auto", got.OpenAIResponsesContinuation)
	}
	if len(got.MCPs) != 1 || got.MCPs[0].Name != "x" {
		t.Errorf("MCPs = %v", got.MCPs)
	}
	if got.Agent != "myagent" {
		t.Errorf("Agent = %q, want myagent", got.Agent)
	}
	if got.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high", got.ReasoningEffort)
	}
	if got.ContextStrategy != "compact" {
		t.Errorf("ContextStrategy = %q, want compact", got.ContextStrategy)
	}
	if !reflect.DeepEqual(got.SkillsDirs, []string{"/skills"}) {
		t.Errorf("SkillsDirs = %v, want [/skills]", got.SkillsDirs)
	}
	if !reflect.DeepEqual(got.PluginDirs, []string{"/plugins"}) {
		t.Errorf("PluginDirs = %v, want [/plugins]", got.PluginDirs)
	}
	if !reflect.DeepEqual(got.MCPConfigs, []string{"/mcp.toml"}) {
		t.Errorf("MCPConfigs = %v, want [/mcp.toml]", got.MCPConfigs)
	}
	if got.NoProjectPrompts == nil || !*got.NoProjectPrompts {
		t.Errorf("NoProjectPrompts = %v, want true", got.NoProjectPrompts)
	}
	if got.AppReplaySize == nil || *got.AppReplaySize != 42 {
		t.Errorf("AppReplaySize = %v, want 42", got.AppReplaySize)
	}
	if got.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want bar", got.Env["FOO"])
	}
}

func TestToWire(t *testing.T) {
	in := Layer{Model: "openai/gpt-5", FastCheapModel: "openai/gpt-5-mini", OpenAIResponsesContinuation: "auto", MaxRounds: ptrInt(50)}
	got := ToWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Errorf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v", got.MaxRounds)
	}
	if got.OpenAIResponsesContinuation != "auto" {
		t.Errorf("OpenAIResponsesContinuation = %q, want auto", got.OpenAIResponsesContinuation)
	}
}

func TestToWirePreservesExplicitEmptyModelFallbacks(t *testing.T) {
	got := ToWire(Layer{ModelFallbacks: &[]string{}})
	if got.ModelFallbacks == nil || len(got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %#v, want explicit empty slice", got.ModelFallbacks)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !jsonContains(raw, `"modelFallbacks":[]`) {
		t.Fatalf("json = %s, want explicit empty modelFallbacks array", raw)
	}
}

func TestWire_SystemPromptAndDebugFieldsRoundTrip(t *testing.T) {
	verbose := true
	rawHTTPLogging := true
	nonInteractive := true
	in := Layer{
		SystemPromptMode:          "inline",
		SystemPromptText:          "base",
		SystemPromptAppendMode:    "file",
		SystemPromptAppendFile:    "/append.md",
		NonInteractive:            &nonInteractive,
		Verbose:                   &verbose,
		RawHTTPLogging:            &rawHTTPLogging,
		TraceFile:                 "/trace",
		CPUProfile:                "/cpu",
		ExportATIFPath:            "/atif",
		ExportATIFProviderHandles: "raw-local",
	}
	got := FromWire(ToWire(in))
	if got.SystemPromptMode != in.SystemPromptMode || got.SystemPromptText != in.SystemPromptText {
		t.Fatalf("system prompt round trip = %#v", got)
	}
	if got.NonInteractive == nil || *got.NonInteractive != true {
		t.Fatalf("non_interactive round trip = %#v", got.NonInteractive)
	}
	if got.Verbose == nil || *got.Verbose != true || got.TraceFile != "/trace" || got.CPUProfile != "/cpu" || got.ExportATIFPath != "/atif" || got.ExportATIFProviderHandles != "raw-local" {
		t.Fatalf("debug round trip = %#v", got)
	}
	if got.RawHTTPLogging == nil || *got.RawHTTPLogging != true {
		t.Fatalf("raw_http_logging round trip = %#v", got.RawHTTPLogging)
	}
}

func TestWireOmitsUnsetModelFallbacks(t *testing.T) {
	raw, err := json.Marshal(appwire.LaunchConfigLayer{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if jsonContains(raw, `"modelFallbacks"`) {
		t.Fatalf("json = %s, want modelFallbacks omitted when unset", raw)
	}
}

func TestResolvedToWire(t *testing.T) {
	r := Resolved{
		Effective:   Layer{Model: "m"},
		Layers:      map[LayerName]Layer{LayerGlobal: {Model: "m"}},
		Provenance:  map[string]LayerName{"model": LayerGlobal},
		Repo:        &RepoStatus{Path: "/p", Trust: TrustTrusted, Hash: "sha256:abc"},
		Diagnostics: []Diagnostic{{Layer: LayerGlobal, Field: "model", Message: "overridden"}},
	}
	got := ResolvedToWire(r)
	if got.Effective.Model != "m" {
		t.Errorf("Effective.Model")
	}
	if got.Layers["global"].Model != "m" {
		t.Errorf("Layers[global]")
	}
	if got.Provenance["model"] != "global" {
		t.Errorf("Provenance[model] = %q", got.Provenance["model"])
	}
	if got.Repo == nil || got.Repo.Trust != "trusted" {
		t.Errorf("Repo = %v", got.Repo)
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].Field != "model" || got.Diagnostics[0].Message != "overridden" {
		t.Errorf("Diagnostics = %v", got.Diagnostics)
	}
}

// TestLaunchConfigLayer_ConfigPlumbingRoundtrip: ModelFallbacks survives the
// wire (appwire.LaunchConfigLayer ⇄ launchconfig.Layer) and merges correctly
// across layers (launch overrides global).
func TestLaunchConfigLayer_ConfigPlumbingRoundtrip(t *testing.T) {
	// Wire → internal.
	wireLayer := appwire.LaunchConfigLayer{
		ModelFallbacks: []string{"openai/gpt-5.4", "anthropic/claude-haiku-4-5"},
	}
	internal := FromWire(wireLayer)
	if internal.ModelFallbacks == nil || !reflect.DeepEqual(*internal.ModelFallbacks, wireLayer.ModelFallbacks) {
		t.Errorf("FromWire ModelFallbacks: got %v want %v", internal.ModelFallbacks, wireLayer.ModelFallbacks)
	}

	// Internal → wire roundtrip.
	roundtrip := ToWire(internal)
	if got, want := roundtrip.ModelFallbacks, wireLayer.ModelFallbacks; !reflect.DeepEqual(got, want) {
		t.Errorf("ToWire ModelFallbacks: got %v want %v", got, want)
	}

	// Verify the JSON tag is snake_case (project convention for launch
	// config-adjacent surfaces; appwire is camelCase per codex requirement).
	// The appwire side uses camelCase: encode and check the key.
	enc, err := json.Marshal(wireLayer)
	if err != nil {
		t.Fatalf("marshal appwire: %v", err)
	}
	if !strings.Contains(string(enc), `"modelFallbacks"`) {
		t.Errorf("appwire JSON tag for ModelFallbacks: expected camelCase 'modelFallbacks', got: %s", enc)
	}
}

func jsonContains(raw []byte, needle string) bool {
	return strings.Contains(string(raw), needle)
}

func TestToWire_WithSchemaAndMCPs(t *testing.T) {
	in := Layer{
		Model:  "m",
		Schema: 2,
		MCPs:   []MCPServerSpec{{Name: "a", Command: "b", Args: []string{"x", "y"}}},
	}
	got := ToWire(in)
	if got.Schema == nil || *got.Schema != 2 {
		t.Fatalf("Schema = %v, want 2", got.Schema)
	}
	if len(got.MCPs) != 1 {
		t.Fatalf("MCPs = %v, want 1 entry", got.MCPs)
	}
	if got.MCPs[0].Name != "a" || got.MCPs[0].Command != "b" {
		t.Errorf("MCPs[0] = %+v, want Name=a Command=b", got.MCPs[0])
	}
	if !reflect.DeepEqual(got.MCPs[0].Args, []string{"x", "y"}) {
		t.Errorf("MCPs[0].Args = %#v, want [x y]", got.MCPs[0].Args)
	}
}

func TestToWire_NonNilPtrToNilSliceClears(t *testing.T) {
	// A non-nil pointer to a nil slice is still the explicit-clear state:
	// ToWire must surface it as a non-nil empty array on the wire.
	mf := []string(nil)
	in := Layer{ModelFallbacks: &mf}
	got := ToWire(in)
	if got.ModelFallbacks == nil || len(got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %#v, want explicit empty slice", got.ModelFallbacks)
	}
}

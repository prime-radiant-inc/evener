package registry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writerFixtureLayer() *Layer {
	return &Layer{
		Tag:     LayerConfig,
		Default: "work",
		TopGlobs: map[string]Model{
			"*gemini-3*": {ID: "*gemini-3*", Caps: Caps{MultimodalToolResults: new(true)}},
		},
		Providers: map[string]Provider{
			"work": {
				ID: "work", Base: "openai", Protocol: ProtocolOpenAIChat, Surface: SurfaceGeneric,
				Headers:           map[string]string{"X-Portkey-Provider": "openai"},
				CredentialHeaders: map[string]string{"Authorization": "Bearer $PORTKEY_KEY"},
				APIKeyEnv:         []string{},
				DefaultModel:      "glm-5.2-nvfp4",
				Transport:         Transport{BaseURL: "https://gw.example.com/v1"},
				Caps:              Caps{Fields: map[string]bool{"stream_options": false}, ContextWindow: new(131072)},
				Models: map[string]Model{
					"glm-5.2-nvfp4": {ID: "glm-5.2-nvfp4", Caps: Caps{
						ContextWindow: new(1048576), MaxOutputTokens: new(131072),
						EffortValues: []string{"high", "max"}, ThinkingFormat: new("zai"),
						Cost: &Cost{Input: 0.5, Output: 1.5, Tiers: []CostTier{{InputTokensAbove: 200000, Input: 1, Output: 3}}},
					}},
				},
			},
			"bedrock": {ID: "bedrock", Base: "amazon-bedrock", Transport: Transport{Vars: map[string]string{"AWS_REGION": "us-east-1"}}, Models: map[string]Model{}},
			"local":   {ID: "local", Base: "openai-compatible", Transport: Transport{BaseURL: "http://localhost:8080/v1", Auth: AuthNone}, Models: map[string]Model{}},
		},
	}
}

func TestMarshalConfigRoundTrips(t *testing.T) {
	want := writerFixtureLayer()
	data, err := MarshalConfig(want)
	if err != nil {
		t.Fatalf("MarshalConfig: %v", err)
	}
	got, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("ParseConfig of marshaled output: %v\n%s", err, data)
	}
	if got.Default != want.Default {
		t.Fatalf("default: %q vs %q", got.Default, want.Default)
	}
	if !reflect.DeepEqual(got.TopGlobs, want.TopGlobs) {
		t.Fatalf("top globs differ:\n got %+v\nwant %+v", got.TopGlobs, want.TopGlobs)
	}
	for name, wp := range want.Providers {
		gp, ok := got.Providers[name]
		if !ok {
			t.Fatalf("provider %s missing after round trip:\n%s", name, data)
		}
		if !reflect.DeepEqual(gp, wp) {
			t.Fatalf("provider %s differs:\n got %+v\nwant %+v\n%s", name, gp, wp, data)
		}
	}
	if strings.Contains(string(data), `surface = ""`) || strings.Contains(string(data), `protocol = ""`) {
		t.Fatalf("unset scalars must not be written:\n%s", data)
	}
}

func TestMarshalConfigWritesExplicitEmptyAPIKeyEnvOnly(t *testing.T) {
	data, err := MarshalConfig(&Layer{Providers: map[string]Provider{
		"a": {ID: "a", Base: "openai", APIKeyEnv: []string{}},
		"b": {ID: "b", Base: "openai"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "api_key_env = []") {
		t.Fatalf("an explicit empty list is meaningful (spec §6.2) and must be written:\n%s", s)
	}
	if strings.Count(s, "api_key_env") != 1 {
		t.Fatalf("a nil list must not be written:\n%s", s)
	}
}

func TestReadWriteConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "providers.toml")
	l, exists, err := ReadConfigFile(path)
	if err != nil || exists || l == nil || l.Providers == nil {
		t.Fatalf("absent file must read as an empty layer: %v %v %+v", err, exists, l)
	}
	l.Default = "local"
	l.Providers["local"] = Provider{ID: "local", Base: "openai-compatible", Transport: Transport{BaseURL: "http://localhost:8080/v1", Auth: AuthNone}}
	if err := WriteConfigFile(path, l); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode: %v %v", err, info)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temp file must be renamed away")
	}
	back, exists, err := ReadConfigFile(path)
	if err != nil || !exists || back.Default != "local" || back.Providers["local"].Base != "openai-compatible" {
		t.Fatalf("read back: %v %v %+v", err, exists, back)
	}
}

func TestValidInstanceName(t *testing.T) {
	for name, want := range map[string]bool{"work": true, "kimi-for-coding": true, "a.b_c": true, "Work": false, "a/b": false, "": false, "-x": false} {
		if got := ValidInstanceName(name); got != want {
			t.Fatalf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestReadConfigFileReportsOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadConfigFile(path)
	if !errors.Is(err, ErrOldSchema) {
		t.Fatalf("want ErrOldSchema, got %v", err)
	}
}

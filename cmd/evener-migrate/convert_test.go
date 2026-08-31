package migrate

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestIsOldProvidersSchema(t *testing.T) {
	if !isOldProvidersSchema([]byte("[instances.kimi]\ntype = \"kimi\"\n")) {
		t.Fatal("old-schema file must be detected")
	}
	if isOldProvidersSchema([]byte("schema = 2\n\n[providers.openai]\n")) {
		t.Fatal("new-schema file must not be detected as old")
	}
	if isOldProvidersSchema([]byte("not toml at all = = =")) {
		t.Fatal("unparseable file is not convertible")
	}
}

func TestConvertOldConfig_MapsTypesAndDefault(t *testing.T) {
	src := []byte(`default = "kimi"

[instances.kimi]
type = "kimi"

[instances.glm]
type = "glm"

[instances.km]
type = "kimi-anthropic"

[instances.ora]
type = "openrouter-anthropic"

[instances.lunaroute]
type = "openai"
api_style = "chat-completions"
base_url = "http://localhost:8892/v1"

[instances.openrouter]
type = "openrouter"
`)
	out, _, err := convertProvidersConfig(src)
	if err != nil {
		t.Fatal(err)
	}
	l, err := registry.ParseConfig(out)
	if err != nil {
		t.Fatalf("converted file must load through the real parser: %v\n%s", err, out)
	}
	if l.Default != "kimi" {
		t.Fatalf("default = %q, want kimi", l.Default)
	}
	want := map[string]struct{ base, protocol, baseURL string }{
		"kimi":       {base: "moonshotai"},
		"glm":        {base: "zai"},
		"km":         {base: "kimi-for-coding"},
		"ora":        {base: "openrouter", protocol: "anthropic"},
		"lunaroute":  {base: "openai", protocol: "openai-chat", baseURL: "http://localhost:8892/v1"},
		"openrouter": {}, // name is already the registry id: no base needed
	}
	for name, w := range want {
		p, ok := l.Providers[name]
		if !ok {
			t.Fatalf("instance %q missing from converted config", name)
		}
		if p.Base != w.base || p.Protocol != w.protocol || p.Transport.BaseURL != w.baseURL {
			t.Fatalf("%s: base=%q protocol=%q base_url=%q, want %+v", name, p.Base, p.Protocol, p.Transport.BaseURL, w)
		}
	}
}

func TestConvertOldConfig_CredentialForms(t *testing.T) {
	src := []byte(`[instances.gw]
type = "openrouter"
api_key = "$MY_GATEWAY_KEY"

[instances.lit]
type = "openrouter"
api_key = "sk-or-literal"

[instances.anthropic]
type = "anthropic"
base_url = "https://gw.example.com/anthropic"

[instances.hdr]
type = "openrouter"
headers = { "X-Route" = "a" }
credential_headers = { "X-Secret" = "$SECRET_VAR" }
`)
	out, _, err := convertProvidersConfig(src)
	if err != nil {
		t.Fatal(err)
	}
	l, err := registry.ParseConfig(out)
	if err != nil {
		t.Fatalf("converted file must load: %v\n%s", err, out)
	}
	if got := l.Providers["gw"].APIKeyEnv; len(got) != 1 || got[0] != "MY_GATEWAY_KEY" {
		t.Fatalf("gw api_key_env = %v, want [MY_GATEWAY_KEY]", got)
	}
	if got := l.Providers["lit"].APIKey; got != "sk-or-literal" {
		t.Fatalf("lit api_key = %q, want the literal carried", got)
	}
	// The old schema let a vendor instance with base_url inherit the vendor
	// key; the registry deliberately does not, so the converter writes the
	// standard variable explicitly.
	if got := l.Providers["anthropic"].APIKeyEnv; len(got) != 1 || got[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic api_key_env = %v, want [ANTHROPIC_API_KEY]", got)
	}
	if got := l.Providers["hdr"].Headers["X-Route"]; got != "a" {
		t.Fatalf("headers must carry: %v", l.Providers["hdr"].Headers)
	}
	if got := l.Providers["hdr"].CredentialHeaders["X-Secret"]; got != "$SECRET_VAR" {
		t.Fatalf("credential_headers must carry: %v", l.Providers["hdr"].CredentialHeaders)
	}
}

func TestConvertOldConfig_DropsCompatQuirksModelsWithNote(t *testing.T) {
	src := []byte(`[instances.kimi]
type = "kimi"
quirks = "kimi"

[instances.kimi.compat]
thinking_format = "openrouter"

[instances.kimi.models."kimi-k3"]
context_window = 262144
`)
	out, notes, err := convertProvidersConfig(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ParseConfig(out); err != nil {
		t.Fatalf("converted file must load: %v\n%s", err, out)
	}
	text := string(out)
	for _, dropped := range []string{"quirks", "compat", "models"} {
		if !strings.Contains(text, "# migrate: ") || !strings.Contains(text, dropped) {
			t.Fatalf("dropped %q must be recorded as a comment:\n%s", dropped, text)
		}
	}
	if len(notes) == 0 {
		t.Fatal("dropping fields must surface notes for the report")
	}
}

func TestConvertOldConfig_WritesExplicitDefault(t *testing.T) {
	src := []byte("[instances.zeta]\ntype = \"openrouter\"\n\n[instances.alpha]\ntype = \"ollama\"\n")
	out, _, err := convertProvidersConfig(src)
	if err != nil {
		t.Fatal(err)
	}
	l, err := registry.ParseConfig(out)
	if err != nil {
		t.Fatalf("converted file must load: %v\n%s", err, out)
	}
	// The old loader picked the first sorted instance name when the file set
	// no default; the ranking rule changed, so the converter pins the old pick.
	if l.Default != "alpha" {
		t.Fatalf("default = %q, want alpha (old sorted-name pick made explicit)", l.Default)
	}
}

func TestConvertOldConfig_OutputDeclaresSchemaTwo(t *testing.T) {
	out, _, err := convertProvidersConfig([]byte("[instances.ollama]\ntype = \"ollama\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "schema = 2") {
		t.Fatalf("converted file must declare schema = 2:\n%s", out)
	}
}

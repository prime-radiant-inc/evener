package providercfg

import (
	"os"
	"path/filepath"
	"testing"
)

const fixtureToml = `
schema = 1
default = "work"

[instances.work]
type = "openai"
api_style = "responses"
api_key = "sk-work"

[instances.kimi-corp]
type = "kimi"
base_url = "https://kimi.example.com"
quirks = "kimi-k2.5"
`

func TestLoadParsesTwoInstances(t *testing.T) {
	cfg, err := Load([]byte(fixtureToml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Default != "work" {
		t.Errorf("Default = %q, want %q", cfg.Default, "work")
	}
	if len(cfg.Instances) != 2 {
		t.Fatalf("len(Instances) = %d, want 2", len(cfg.Instances))
	}

	// Instances are sorted by name: kimi-corp < work
	kimi := cfg.Instances[0]
	work := cfg.Instances[1]

	if kimi.Name != "kimi-corp" {
		t.Errorf("Instances[0].Name = %q, want %q", kimi.Name, "kimi-corp")
	}
	if kimi.Type != "kimi" {
		t.Errorf("kimi.Type = %q, want %q", kimi.Type, "kimi")
	}
	if kimi.BaseURL != "https://kimi.example.com" {
		t.Errorf("kimi.BaseURL = %q, want %q", kimi.BaseURL, "https://kimi.example.com")
	}
	if kimi.Quirks != "kimi-k2.5" {
		t.Errorf("kimi.Quirks = %q, want %q", kimi.Quirks, "kimi-k2.5")
	}
	if kimi.APIStyle != "" {
		t.Errorf("kimi.APIStyle = %q, want empty", kimi.APIStyle)
	}

	if work.Name != "work" {
		t.Errorf("Instances[1].Name = %q, want %q", work.Name, "work")
	}
	if work.Type != "openai" {
		t.Errorf("work.Type = %q, want %q", work.Type, "openai")
	}
	if work.APIStyle != StyleResponses {
		t.Errorf("work.APIStyle = %q, want %q", work.APIStyle, StyleResponses)
	}
	if work.APIKey != "sk-work" {
		t.Errorf("work.APIKey = %q, want %q", work.APIKey, "sk-work")
	}
}

func TestLoadDefaultFallsToFirstSorted(t *testing.T) {
	const noDefault = `
schema = 1

[instances.zebra]
type = "anthropic"

[instances.alpha]
type = "google"
`
	cfg, err := Load([]byte(noDefault))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// alpha < zebra, so alpha is first
	if cfg.Default != "alpha" {
		t.Errorf("Default = %q, want %q", cfg.Default, "alpha")
	}
}

func TestLoadRejectsZeroInstances(t *testing.T) {
	const empty = `schema = 1`
	_, err := Load([]byte(empty))
	if err == nil {
		t.Error("Load with zero instances: expected error, got nil")
	}
}

func TestLoadRejectsUpperCaseName(t *testing.T) {
	const upper = `
schema = 1

[instances.Work]
type = "openai"
`
	_, err := Load([]byte(upper))
	if err == nil {
		t.Error("expected error for upper-case instance name, got nil")
	}
}

func TestLoadRejectsSlashInName(t *testing.T) {
	const slashed = `
schema = 1

[instances."my/provider"]
type = "openai"
`
	_, err := Load([]byte(slashed))
	if err == nil {
		t.Error("expected error for '/' in instance name, got nil")
	}
}

func TestLoadRejectsUnknownType(t *testing.T) {
	const unknown = `
schema = 1

[instances.foo]
type = "nope"
`
	_, err := Load([]byte(unknown))
	if err == nil {
		t.Error("expected error for unknown type, got nil")
	}
}

func TestLoadRejectsAPIStyleOnNonOpenAI(t *testing.T) {
	const badStyle = `
schema = 1

[instances.foo]
type = "anthropic"
api_style = "responses"
`
	_, err := Load([]byte(badStyle))
	if err == nil {
		t.Error("expected error for api_style on non-openai type, got nil")
	}
}

func TestLoadAcceptsOpenAIAutoAPIStyle(t *testing.T) {
	const adaptive = `
schema = 1

[instances.adaptive]
type = "openai"
api_style = "auto"
api_key = "sk-adaptive"
`
	cfg, err := Load([]byte(adaptive))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("len(Instances) = %d, want 1", len(cfg.Instances))
	}
	if got := cfg.Instances[0].APIStyle; got != StyleAuto {
		t.Fatalf("APIStyle = %q, want %q", got, StyleAuto)
	}
}

func TestLoadRejectsDefaultNamingAbsentInstance(t *testing.T) {
	const badDefault = `
schema = 1
default = "nobody"

[instances.foo]
type = "anthropic"
`
	_, err := Load([]byte(badDefault))
	if err == nil {
		t.Error("expected error for default naming absent instance, got nil")
	}
}

func TestLoadRejectsUnknownAPIStyle(t *testing.T) {
	const badStyle = `
schema = 1

[instances.foo]
type = "openai"
api_style = "grpc"
`
	_, err := Load([]byte(badStyle))
	if err == nil {
		t.Error("expected error for unknown api_style, got nil")
	}
}

func TestLoadFileAbsentReturnsExistsFalse(t *testing.T) {
	_, exists, err := LoadFile("/nonexistent/path/providers.toml")
	if err != nil {
		t.Errorf("LoadFile missing: expected nil err, got %v", err)
	}
	if exists {
		t.Error("LoadFile missing: expected exists=false")
	}
}

func TestLoadFilePresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(fixtureToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, exists, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if !exists {
		t.Fatal("LoadFile: expected exists=true")
	}
	if len(cfg.Instances) != 2 {
		t.Errorf("len(Instances) = %d, want 2", len(cfg.Instances))
	}
}

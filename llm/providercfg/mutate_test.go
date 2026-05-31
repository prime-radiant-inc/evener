package providercfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// WriteFile
// ---------------------------------------------------------------------------

func TestWriteFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")

	cfg := Config{
		Default: "work",
		Instances: []InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: StyleResponses, APIKey: "sk-secret"},
			{Name: "kimi-corp", Type: "kimi", BaseURL: "https://kimi.example.com"},
		},
	}

	if err := WriteFile(path, cfg); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// File must exist and be 0644.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}

	// File must not contain api_key.
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "api_key") || strings.Contains(string(data), "sk-secret") {
		t.Fatalf("WriteFile leaked a secret:\n%s", data)
	}

	// Round-trip via LoadFile.
	got, exists, err := LoadFile(path)
	if err != nil || !exists {
		t.Fatalf("LoadFile: exists=%v err=%v", exists, err)
	}
	if got.Default != cfg.Default {
		t.Errorf("Default = %q, want %q", got.Default, cfg.Default)
	}
	if len(got.Instances) != 2 {
		t.Errorf("len(Instances) = %d, want 2", len(got.Instances))
	}
}

func TestWriteFileCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "providers.toml")

	cfg := Config{
		Default: "foo",
		Instances: []InstanceConfig{
			{Name: "foo", Type: "anthropic"},
		},
	}

	if err := WriteFile(path, cfg); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Upsert
// ---------------------------------------------------------------------------

func TestUpsertAppendsNewInstance(t *testing.T) {
	orig := Config{
		Default: "a",
		Instances: []InstanceConfig{
			{Name: "a", Type: "anthropic"},
		},
	}
	updated := orig.Upsert(InstanceConfig{Name: "b", Type: "google"})
	if len(updated.Instances) != 2 {
		t.Fatalf("len = %d, want 2", len(updated.Instances))
	}
}

func TestUpsertReplacesExistingInstance(t *testing.T) {
	orig := Config{
		Default: "work",
		Instances: []InstanceConfig{
			{Name: "work", Type: "openai", BaseURL: "old"},
		},
	}
	updated := orig.Upsert(InstanceConfig{Name: "work", Type: "openai", BaseURL: "new"})
	if len(updated.Instances) != 1 {
		t.Fatalf("len = %d, want 1", len(updated.Instances))
	}
	if updated.Instances[0].BaseURL != "new" {
		t.Errorf("BaseURL = %q, want %q", updated.Instances[0].BaseURL, "new")
	}
}

func TestUpsertReturnsSortedByName(t *testing.T) {
	orig := Config{
		Default: "b",
		Instances: []InstanceConfig{
			{Name: "b", Type: "anthropic"},
			{Name: "c", Type: "google"},
		},
	}
	updated := orig.Upsert(InstanceConfig{Name: "a", Type: "kimi"})
	if len(updated.Instances) != 3 {
		t.Fatalf("len = %d, want 3", len(updated.Instances))
	}
	names := []string{updated.Instances[0].Name, updated.Instances[1].Name, updated.Instances[2].Name}
	want := []string{"a", "b", "c"}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("Instances[%d].Name = %q, want %q", i, n, want[i])
		}
	}
}

func TestUpsertDoesNotMutateInput(t *testing.T) {
	orig := Config{
		Default: "a",
		Instances: []InstanceConfig{
			{Name: "a", Type: "anthropic"},
		},
	}
	_ = orig.Upsert(InstanceConfig{Name: "b", Type: "google"})
	if len(orig.Instances) != 1 {
		t.Errorf("input mutated: len = %d, want 1", len(orig.Instances))
	}
}

// ---------------------------------------------------------------------------
// RemoveInstance
// ---------------------------------------------------------------------------

func TestRemoveInstanceDropsNamed(t *testing.T) {
	orig := Config{
		Default: "a",
		Instances: []InstanceConfig{
			{Name: "a", Type: "anthropic"},
			{Name: "b", Type: "google"},
		},
	}
	updated := orig.RemoveInstance("a")
	if len(updated.Instances) != 1 {
		t.Fatalf("len = %d, want 1", len(updated.Instances))
	}
	if updated.Instances[0].Name != "b" {
		t.Errorf("remaining instance = %q, want %q", updated.Instances[0].Name, "b")
	}
}

func TestRemoveInstanceNoOpForMissingName(t *testing.T) {
	orig := Config{
		Default: "a",
		Instances: []InstanceConfig{
			{Name: "a", Type: "anthropic"},
		},
	}
	updated := orig.RemoveInstance("nope")
	if len(updated.Instances) != 1 {
		t.Errorf("len = %d, want 1", len(updated.Instances))
	}
}

// ---------------------------------------------------------------------------
// WithDefault
// ---------------------------------------------------------------------------

func TestWithDefaultSetsDefault(t *testing.T) {
	orig := Config{Default: "a", Instances: []InstanceConfig{{Name: "a", Type: "anthropic"}}}
	updated := orig.WithDefault("b")
	if updated.Default != "b" {
		t.Errorf("Default = %q, want %q", updated.Default, "b")
	}
	// original unchanged
	if orig.Default != "a" {
		t.Errorf("orig.Default mutated: %q", orig.Default)
	}
}

// ---------------------------------------------------------------------------
// ValidateInstanceName
// ---------------------------------------------------------------------------

func TestValidateInstanceNameRejectsEmpty(t *testing.T) {
	if err := ValidateInstanceName(""); err == nil {
		t.Error("expected error for empty name, got nil")
	}
}

func TestValidateInstanceNameRejectsUppercase(t *testing.T) {
	if err := ValidateInstanceName("Work"); err == nil {
		t.Error("expected error for uppercase name, got nil")
	}
}

func TestValidateInstanceNameRejectsSlash(t *testing.T) {
	if err := ValidateInstanceName("a/b"); err == nil {
		t.Error("expected error for slash in name, got nil")
	}
}

func TestValidateInstanceNameAcceptsValid(t *testing.T) {
	if err := ValidateInstanceName("work2"); err != nil {
		t.Errorf("expected nil for valid name, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ValidateAPIStyle
// ---------------------------------------------------------------------------

func TestValidateAPIStyleAcceptsEmpty(t *testing.T) {
	if err := ValidateAPIStyle("anthropic", ""); err != nil {
		t.Errorf("expected nil for empty style, got %v", err)
	}
}

func TestValidateAPIStyleAcceptsValidOpenAIStyles(t *testing.T) {
	for _, style := range []APIStyle{StyleResponses, StyleChatCompletions} {
		if err := ValidateAPIStyle("openai", style); err != nil {
			t.Errorf("expected nil for openai/%s, got %v", style, err)
		}
	}
}

func TestValidateAPIStyleRejectsStyleOnNonOpenAI(t *testing.T) {
	if err := ValidateAPIStyle("anthropic", StyleResponses); err == nil {
		t.Error("expected error for api_style on anthropic, got nil")
	}
}

func TestValidateAPIStyleRejectsUnknownStyle(t *testing.T) {
	if err := ValidateAPIStyle("openai", "grpc"); err == nil {
		t.Error("expected error for unknown api_style, got nil")
	}
}

func TestValidateType(t *testing.T) {
	for _, typ := range []Type{"openai", "anthropic", "google", "kimi", "glm", "minimax", "openrouter", "openrouter-anthropic", "ollama"} {
		if err := ValidateType(typ); err != nil {
			t.Errorf("ValidateType(%q) = %v, want nil", typ, err)
		}
	}
	for _, typ := range []Type{"", "bogus", "openai-compatible", "OpenAI"} {
		if err := ValidateType(typ); err == nil {
			t.Errorf("ValidateType(%q) = nil, want error", typ)
		}
	}
}

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
		t.Fatalf("len(Instances) = %d, want 2", len(got.Instances))
	}

	// Verify field-level serialization fidelity for each instance.
	// APIKey is intentionally excluded from the written file.
	wantByName := make(map[string]InstanceConfig, len(cfg.Instances))
	for _, inst := range cfg.Instances {
		wantByName[inst.Name] = inst
	}
	for _, gotInst := range got.Instances {
		want, ok := wantByName[gotInst.Name]
		if !ok {
			t.Errorf("unexpected instance %q in round-trip output", gotInst.Name)
			continue
		}
		if gotInst.Type != want.Type {
			t.Errorf("Instances[%q].Type = %q, want %q", gotInst.Name, gotInst.Type, want.Type)
		}
		if gotInst.APIStyle != want.APIStyle {
			t.Errorf("Instances[%q].APIStyle = %q, want %q", gotInst.Name, gotInst.APIStyle, want.APIStyle)
		}
		if gotInst.BaseURL != want.BaseURL {
			t.Errorf("Instances[%q].BaseURL = %q, want %q", gotInst.Name, gotInst.BaseURL, want.BaseURL)
		}
		if gotInst.Quirks != want.Quirks {
			t.Errorf("Instances[%q].Quirks = %q, want %q", gotInst.Name, gotInst.Quirks, want.Quirks)
		}
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
	// Instances are sorted: "a" < "b", so the appended instance is at index 1.
	if updated.Instances[1].Name != "b" {
		t.Errorf("Instances[1].Name = %q, want %q", updated.Instances[1].Name, "b")
	}
	if updated.Instances[1].Type != "google" {
		t.Errorf("Instances[1].Type = %q, want %q", updated.Instances[1].Type, "google")
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
	for _, style := range []APIStyle{StyleResponses, StyleChatCompletions, StyleAuto} {
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
	for _, typ := range []Type{"openai", "anthropic", "google", "kimi", "kimi-anthropic", "glm", "minimax", "openrouter", "openrouter-anthropic", "ollama"} {
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

func TestKnownTypeNamesGolden(t *testing.T) {
	// Golden list: any addition or removal from knownTypes in load.go requires a
	// deliberate update here. This catches mutations that knownTypes cross-checks
	// alone cannot detect (e.g. removing a type that no positive test exercises).
	want := []string{
		"anthropic",
		"glm",
		"google",
		"kimi",
		"kimi-anthropic",
		"minimax",
		"ollama",
		"openai",
		"openrouter",
		"openrouter-anthropic",
	}
	got := KnownTypeNames()
	if len(got) != len(want) {
		t.Fatalf("KnownTypeNames() = %v, want %v", got, want)
	}
	for i, name := range got {
		if name != want[i] {
			t.Errorf("KnownTypeNames()[%d] = %q, want %q", i, name, want[i])
		}
	}
}

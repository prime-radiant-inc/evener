package envvars

import (
	"reflect"
	"testing"
)

// TestProvider resolves providers case-insensitively and reports misses.
func TestProvider(t *testing.T) {
	p, ok := Provider("OpenAI")
	if !ok {
		t.Fatal("Provider(OpenAI) not found (case-insensitive lookup)")
	}
	if p.Name != "openai" {
		t.Errorf("Provider(OpenAI).Name = %q, want openai", p.Name)
	}

	if got, ok := Provider("nope"); ok || got.Name != "" {
		t.Errorf("Provider(nope) = %+v, %v; want zero, false", got, ok)
	}
}

func TestProviders(t *testing.T) {
	got := Providers()
	if len(got) != len(providers) {
		t.Fatalf("Providers() len = %d, want %d", len(got), len(providers))
	}
	got[0] = ProviderEnv{Name: "MUTATED"}
	if providers[0].Name == "MUTATED" {
		t.Error("Providers() aliased the backing slice; mutation leaked")
	}
}

func TestAPIKeyVars(t *testing.T) {
	got := APIKeyVars("google")
	want := []Var{GeminiAPIKey, GoogleAPIKey}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("APIKeyVars(google) = %+v, want %+v", got, want)
	}

	// Returned slice is a copy, not the backing array.
	got[0] = Var{Name: "MUTATED"}
	if APIKeyVars("google")[0].Name == "MUTATED" {
		t.Error("APIKeyVars aliased the backing slice")
	}

	if got := APIKeyVars("unknown"); got != nil {
		t.Errorf("APIKeyVars(unknown) = %+v, want nil", got)
	}
}

func TestInjectAPIKeyVar(t *testing.T) {
	v, ok := InjectAPIKeyVar("kimi")
	if !ok || v != KimiAPIKey {
		t.Errorf("InjectAPIKeyVar(kimi) = %+v, %v; want %+v, true", v, ok, KimiAPIKey)
	}

	// ollama has no inject var (empty Name) -> not ok.
	if got, ok := InjectAPIKeyVar("ollama"); ok || got != (Var{}) {
		t.Errorf("InjectAPIKeyVar(ollama) = %+v, %v; want zero, false", got, ok)
	}

	if got, ok := InjectAPIKeyVar("unknown"); ok || got != (Var{}) {
		t.Errorf("InjectAPIKeyVar(unknown) = %+v, %v; want zero, false", got, ok)
	}
}

func TestBaseURLVar(t *testing.T) {
	v, ok := BaseURLVar("ollama")
	if !ok || v != OllamaBaseURL {
		t.Errorf("BaseURLVar(ollama) = %+v, %v; want %+v (first of list), true", v, ok, OllamaBaseURL)
	}

	if got, ok := BaseURLVar("unknown"); ok || got != (Var{}) {
		t.Errorf("BaseURLVar(unknown) = %+v, %v; want zero, false", got, ok)
	}
}

func TestAuthModes(t *testing.T) {
	got := AuthModes("openai")
	want := []string{"apiKey", "oauth"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthModes(openai) = %v, want %v", got, want)
	}

	got[0] = "MUTATED"
	if AuthModes("openai")[0] == "MUTATED" {
		t.Error("AuthModes aliased the backing slice")
	}

	if got := AuthModes("unknown"); got != nil {
		t.Errorf("AuthModes(unknown) = %v, want nil", got)
	}
}

package sandbox

import (
	"slices"
	"testing"
)

func TestIsSecretEnvName(t *testing.T) {
	secret := []string{
		"OPENAI_API_KEY", "openai_api_key", "MY_SECRET", "GITHUB_TOKEN",
		"DB_PASSWORD", "AWS_CREDENTIAL_FILE", "ANTHROPIC_API_KEY",
	}
	for _, name := range secret {
		if !IsSecretEnvName(name) {
			t.Errorf("IsSecretEnvName(%q) = false, want true", name)
		}
	}
	safe := []string{"PATH", "HOME", "TERM", "LANG", "GOCACHE", "TMPDIR", "EDITOR"}
	for _, name := range safe {
		if IsSecretEnvName(name) {
			t.Errorf("IsSecretEnvName(%q) = true, want false", name)
		}
	}
}

func TestScrubSecretEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"OPENAI_API_KEY=sk-leak",
		"HOME=/home/u",
		"MY_SECRET=nope",
		"MALFORMED_NO_EQUALS",
	}
	got := ScrubSecretEnv(in)
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "MALFORMED_NO_EQUALS"}
	if !slices.Equal(got, want) {
		t.Errorf("ScrubSecretEnv(%v) = %v, want %v", in, got, want)
	}
	// The input slice must not be mutated.
	if len(in) != 5 || in[1] != "OPENAI_API_KEY=sk-leak" {
		t.Errorf("ScrubSecretEnv mutated its input: %v", in)
	}
}

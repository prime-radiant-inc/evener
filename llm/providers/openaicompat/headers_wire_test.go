package openaicompat

import "testing"

func TestNewForInstance_HeadersMergedIntoDefaultHeaders(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "gw",
		BaseURL: "https://x/v1",
		Headers: map[string]string{"X-Gateway": "portkey"},
	})
	if a.DefaultHeaders["X-Gateway"] != "portkey" {
		t.Errorf("DefaultHeaders = %#v, want X-Gateway=portkey", a.DefaultHeaders)
	}
}

func TestNewForInstance_NoHeaders_LeavesDefaultHeadersNil(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{Name: "x", BaseURL: "https://x/v1"})
	if a.DefaultHeaders != nil {
		t.Errorf("DefaultHeaders = %#v, want nil when no headers configured", a.DefaultHeaders)
	}
}

func TestNewForInstance_UserHeaderOverridesProviderDefault(t *testing.T) {
	// User headers win over provider-set defaults, but a provider default
	// survives when the user configures no header of that name.
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:            "kimi",
		BaseURL:         "https://x/v1",
		ProviderHeaders: map[string]string{"User-Agent": "serf-coding", "X-Provider": "keep"},
		Headers:         map[string]string{"User-Agent": "user-wins"},
	})
	if a.DefaultHeaders["User-Agent"] != "user-wins" {
		t.Errorf("User-Agent = %q, want user-wins (user overrides default)", a.DefaultHeaders["User-Agent"])
	}
	if a.DefaultHeaders["X-Provider"] != "keep" {
		t.Errorf("X-Provider = %q, want keep (default survives)", a.DefaultHeaders["X-Provider"])
	}
}

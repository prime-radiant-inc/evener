package providers_test

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/test/e2e/providers"
)

// TestRenderParsesAsTheRegistrySchema pins the one writer's output to the
// schema the hub loads it with. The harnesses' own audit
// (e2eprovidersschema_audit_test.go) runs the full load/resolve boundary; this
// one keeps the package's optional-field behaviour honest.
func TestRenderParsesAsTheRegistrySchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    providers.Config
		key    string
		window int
	}{
		{
			name: "fakellm",
			cfg: providers.Config{
				Name: "fake", BaseURL: "http://127.0.0.1:54321/v1",
				APIKey: "fakellm-not-a-secret", SessionAffinityHeaders: true,
			},
			key: "fakellm-not-a-secret",
		},
		{
			name: "fake429",
			cfg: providers.Config{
				Name: "ratelimited", BaseURL: "http://127.0.0.1:54321/v1",
			},
		},
		{
			name: "turncpu-with-window",
			cfg: providers.Config{
				Name: "fake", BaseURL: "http://127.0.0.1:54321/v1",
				APIKey: "turncpu-not-a-secret", SessionAffinityHeaders: true,
				ContextWindow: 131072,
			},
			key: "turncpu-not-a-secret", window: 131072,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := providers.Render(tc.cfg)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			body := string(rendered)
			layer, err := registry.ParseConfig(rendered)
			if err != nil {
				t.Fatalf("Render output does not parse: %v\n%s", err, body)
			}
			if layer.Default != tc.cfg.Name {
				t.Errorf("default = %q, want %q", layer.Default, tc.cfg.Name)
			}
			provider, ok := layer.Providers[tc.cfg.Name]
			if !ok {
				t.Fatalf("no [providers.%s] in %q", tc.cfg.Name, body)
			}
			if provider.Transport.BaseURL != tc.cfg.BaseURL {
				t.Errorf("base_url = %q, want %q", provider.Transport.BaseURL, tc.cfg.BaseURL)
			}
			if provider.APIKey != tc.key {
				t.Errorf("api_key = %q, want %q", provider.APIKey, tc.key)
			}
			gotAffinity := provider.Caps.SessionAffinityHeaders != nil && *provider.Caps.SessionAffinityHeaders
			if gotAffinity != tc.cfg.SessionAffinityHeaders {
				t.Errorf("session_affinity_headers = %v, want %v", gotAffinity, tc.cfg.SessionAffinityHeaders)
			}
			gotWindow := 0
			if provider.Caps.ContextWindow != nil {
				gotWindow = *provider.Caps.ContextWindow
			}
			if gotWindow != tc.window {
				t.Errorf("context_window = %d, want %d", gotWindow, tc.window)
			}
		})
	}
}

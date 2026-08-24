package envvars

import "testing"

// TestNormalizeOllamaHost and TestResolveOllamaBaseURL pin the small set of
// cases that matter for callers outside this package: the exhaustive
// resolution-order table lives in llm/providers/ollama's adapter_test.go,
// which exercises this same logic through the adapter's thin wrapper.
func TestNormalizeOllamaHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{name: "empty falls back to default", host: "", want: "http://localhost:11434/v1"},
		{name: "bare host gets default port and /v1", host: "localhost", want: "http://localhost:11434/v1"},
		{name: "host:port gets /v1", host: "192.168.1.5:11434", want: "http://192.168.1.5:11434/v1"},
		{name: "full URL gets /v1 appended", host: "https://ollama.example.com", want: "https://ollama.example.com/v1"},
		{name: "full URL already ending /v1 preserved", host: "https://proxy.example/ollama/v1", want: "https://proxy.example/ollama/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeOllamaHost(tc.host); got != tc.want {
				t.Fatalf("NormalizeOllamaHost(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestResolveOllamaBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		host    string
		want    string
	}{
		{name: "nothing set uses default", want: "http://localhost:11434/v1"},
		{name: "base URL wins over host", baseURL: "http://example.com:9000/v1", host: "ignored", want: "http://example.com:9000/v1"},
		{name: "host alone gets normalized, not passed through raw", host: "localhost", want: "http://localhost:11434/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveOllamaBaseURL(tc.baseURL, tc.host); got != tc.want {
				t.Fatalf("ResolveOllamaBaseURL(%q, %q) = %q, want %q", tc.baseURL, tc.host, got, tc.want)
			}
		})
	}
}

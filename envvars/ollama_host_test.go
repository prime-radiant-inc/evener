package envvars

import "testing"

func TestNormalizeOllamaHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{name: "empty falls back to default", host: "", want: "http://localhost:11434/v1"},
		{name: "bare host gets default port and /v1", host: "localhost", want: "http://localhost:11434/v1"},
		{name: "host:port gets /v1", host: "192.168.1.5:11434", want: "http://192.168.1.5:11434/v1"},
		{name: "http URL", host: "http://localhost:11434", want: "http://localhost:11434/v1"},
		{name: "https URL", host: "https://ollama.example.com", want: "https://ollama.example.com/v1"},
		{name: "URL path", host: "https://proxy.example/ollama", want: "https://proxy.example/ollama/v1"},
		{name: "path already v1", host: "https://proxy.example/ollama/v1/", want: "https://proxy.example/ollama/v1"},
		{name: "bare cloud host", host: "ollama.com", want: "https://ollama.com:443/v1"},
		{name: "explicit cloud host", host: "https://ollama.com", want: "https://ollama.com:443/v1"},
		{name: "bare IPv6", host: "::1", want: "http://[::1]:11434/v1"},
		{name: "bracketed IPv6", host: "[::1]", want: "http://[::1]:11434/v1"},
		{name: "bracketed IPv6 with port", host: "[::1]:8080", want: "http://[::1]:8080/v1"},
		{name: "IPv6 URL", host: "http://[::1]:11434/v1", want: "http://[::1]:11434/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeOllamaHost(tc.host)
			if err != nil {
				t.Fatalf("NormalizeOllamaHost(%q): %v", tc.host, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeOllamaHost(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestNormalizeOllamaHostRejectsInvalidEndpoints(t *testing.T) {
	for _, host := range []string{
		"http://", "http:/", "host:66000", "[::1]:66000", "host:a:b",
		"ftp://ollama.example", "http://user@example", "http://example/?q=1",
		"http://example/#fragment", "http:example",
	} {
		t.Run(host, func(t *testing.T) {
			if got, err := NormalizeOllamaHost(host); err == nil || got != "" {
				t.Fatalf("NormalizeOllamaHost(%q) = %q, err %v; want rejection", host, got, err)
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
		{name: "base URL trailing slash trimmed", baseURL: "https://proxy.example/ollama/v1/", want: "https://proxy.example/ollama/v1"},
		{name: "host alone gets normalized", host: "localhost", want: "http://localhost:11434/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveOllamaBaseURL(tc.baseURL, tc.host)
			if err != nil {
				t.Fatalf("ResolveOllamaBaseURL(%q, %q): %v", tc.baseURL, tc.host, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveOllamaBaseURL(%q, %q) = %q, want %q", tc.baseURL, tc.host, got, tc.want)
			}
		})
	}
}

func TestResolveOllamaBaseURLRejectsInvalidBase(t *testing.T) {
	if got, err := ResolveOllamaBaseURL("http://host:66000/v1", "localhost"); err == nil || got != "" {
		t.Fatalf("invalid base URL resolved to %q without error: %v", got, err)
	}
}

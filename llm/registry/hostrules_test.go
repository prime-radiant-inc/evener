package registry

import (
	"strings"
	"testing"
)

func TestResolveOllamaHost(t *testing.T) {
	cases := []struct{ baseURL, host, want string }{
		{"", "localhost", "http://localhost:11434/v1"},
		{"", "::1", "http://[::1]:11434/v1"},
		{"", "ollama.com", "https://ollama.com:443/v1"},
		{"", "https://ollama.com", "https://ollama.com:443/v1"},
		{"", "http://proxy.example/ollama/v1", "http://proxy.example/ollama/v1"},
		{"", "gpu-box:11435", "http://gpu-box:11435/v1"},
		{"http://proxy.example/ollama/v1/", "localhost", "http://proxy.example/ollama/v1"},
		{"", "", "http://localhost:11434/v1"},
	}
	for _, c := range cases {
		got, err := resolveOllamaHost(c.baseURL, c.host)
		if err != nil || got != c.want {
			t.Errorf("resolveOllamaHost(%q, %q) = %q, %v; want %q", c.baseURL, c.host, got, err, c.want)
		}
	}
	for _, bad := range []string{"ftp://x", "http://user@x", "http://x?y=1", "http://x#frag", "http://x:"} {
		if _, err := resolveOllamaHost("", bad); err == nil {
			t.Errorf("host %q must be rejected", bad)
		}
	}
	if _, err := resolveOllamaHost("ftp://x", "localhost"); err == nil {
		t.Error("invalid OLLAMA_BASE_URL must be rejected")
	}
	for _, bad := range []string{"http://user:pw@host", "http://user:pw@host/v1"} {
		_, err := resolveOllamaHost("", bad)
		if err == nil || strings.Contains(err.Error(), "pw") {
			t.Errorf("userinfo must never be echoed: %v", err)
		}
		_, err = resolveOllamaHost(bad, "localhost")
		if err == nil || strings.Contains(err.Error(), "pw") {
			t.Errorf("userinfo in OLLAMA_BASE_URL must never be echoed: %v", err)
		}
	}
}

func TestVertexHost(t *testing.T) {
	cases := map[string]string{
		"global":       "https://aiplatform.googleapis.com",
		"us":           "https://aiplatform.us.rep.googleapis.com",
		"eu":           "https://aiplatform.eu.rep.googleapis.com",
		"europe-west1": "https://europe-west1-aiplatform.googleapis.com",
	}
	for loc, want := range cases {
		if got := vertexHost(loc); got != want {
			t.Errorf("vertexHost(%q) = %q, want %q", loc, got, want)
		}
	}
}

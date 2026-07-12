package httpguard

import (
	"net/http"
	"testing"
)

func FuzzPolicy(f *testing.F) {
	for _, seed := range []struct{ allowed, host, origin string }{
		{"", "evil", "http://evil"},
		{"127.0.0.1:8080", "127.0.0.1:8080", ""},
		{"127.0.0.1:8080", "localhost:8080", "http://localhost:8080"},
		{"localhost:8080", "127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"[::1]:8080", "localhost:8080", "http://localhost:8080"},
		{"example.com", "other.example", ""},
		{"example.com", "example.com", "http://other.example"},
		{":8080", ":8080", "http://:8080"},
	} {
		f.Add(seed.allowed, seed.host, seed.origin)
	}
	f.Fuzz(func(t *testing.T, allowed, host, origin string) {
		p := NewSameOriginPolicy(allowed)
		r := &http.Request{Host: host, Header: make(http.Header)}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		reason := p.Rejection(r)
		if len(p.hosts) == 0 && reason != "" {
			t.Fatalf("zero policy rejected request: %q", reason)
		}
		if reason != "" && reason != "forbidden host" && reason != "forbidden origin" {
			t.Fatalf("unexpected rejection %q", reason)
		}
	})
}

func FuzzHubTokenAuthorized(f *testing.F) {
	for _, seed := range []struct{ expected, header string }{
		{"", ""}, {"secret", ""}, {"secret", "Basic secret"},
		{"secret", "Bearer secret"}, {"secret", "Bearer  secret  "}, {"secret", "Bearer wrong"},
	} {
		f.Add(seed.expected, seed.header)
	}
	f.Fuzz(func(t *testing.T, expected, header string) {
		got := HubTokenAuthorized(expected, header)
		if expected == "" && !got {
			t.Fatal("empty expected token must authorize")
		}
	})
}

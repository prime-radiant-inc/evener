package hubendpoint

import (
	"errors"
	"testing"
)

func TestParseListenAddr(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name string
		raw  string
		want string
	}{
		{name: "IPv4 wildcard and ephemeral port", raw: "0.0.0.0:0", want: "0.0.0.0:0"},
		{name: "empty wildcard host", raw: ":9180", want: ":9180"},
		{name: "IPv6 wildcard and ephemeral port", raw: "[::]:0", want: "[::]:0"},
		{name: "DNS name", raw: "hub.example.test:9180", want: "hub.example.test:9180"},
		{name: "scoped IPv6", raw: "[fe80::1%eth0]:9180", want: "[fe80::1%eth0]:9180"},
		{name: "surrounding whitespace", raw: " \t127.0.0.1:65535\n", want: "127.0.0.1:65535"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseListenAddr(tc.raw)
			if err != nil {
				t.Fatalf("ParseListenAddr() error = %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("ParseListenAddr().String() = %q, want %q", got.String(), tc.want)
			}
		})
	}

	rejected := []struct {
		name string
		raw  string
		code string
	}{
		{name: "empty", raw: "", code: "empty"},
		{name: "missing port", raw: "127.0.0.1", code: "port"},
		{name: "service port", raw: "127.0.0.1:http", code: "port"},
		{name: "negative port", raw: "127.0.0.1:-1", code: "port"},
		{name: "port above range", raw: "127.0.0.1:65536", code: "port"},
		{name: "URL", raw: "http://127.0.0.1:9180", code: "host"},
		{name: "credentials", raw: "user@example.test:9180", code: "host"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseListenAddr(tc.raw)
			if err == nil {
				t.Fatal("ParseListenAddr() accepted invalid listener")
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("ParseListenAddr() error type = %T, want *ValidationError", err)
			}
			if validationErr.Code != tc.code || validationErr.Field != "listen_addr" {
				t.Fatalf("ParseListenAddr() validation = {%q, %q}, want {%q, %q}", validationErr.Code, validationErr.Field, tc.code, "listen_addr")
			}
		})
	}
}

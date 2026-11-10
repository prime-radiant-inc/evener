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

func TestParsePortGrammar(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		raw         string
		port        uint16
		withZero    bool
		withoutZero bool
	}{
		{raw: ""},
		{raw: "+1"},
		{raw: "-1"},
		{raw: "1_0"},
		{raw: " 1"},
		{raw: "1 "},
		{raw: "1\t0"},
		{raw: "١"},
		{raw: "１"},
		{raw: "65536"},
		{raw: "18446744073709551616"},
		{raw: "65535", port: 65535, withZero: true, withoutZero: true},
		{raw: "1", port: 1, withZero: true, withoutZero: true},
		{raw: "00080", port: 80, withZero: true, withoutZero: true},
		{raw: "0", withZero: true},
		{raw: "000", withZero: true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			for _, allowZero := range []bool{false, true} {
				wantOK := tc.withoutZero
				if allowZero {
					wantOK = tc.withZero
				}
				got, ok := parsePort(tc.raw, allowZero)
				if got != tc.port || ok != wantOK {
					t.Fatalf("parsePort(%q, %v) = (%d, %v), want (%d, %v)", tc.raw, allowZero, got, ok, tc.port, wantOK)
				}
			}
		})
	}
}

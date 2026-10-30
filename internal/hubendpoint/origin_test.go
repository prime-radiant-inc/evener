package hubendpoint

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type originCorpusRow struct {
	Name     string             `json:"name"`
	Raw      string             `json:"raw"`
	Origin   string             `json:"origin"`
	RPCURL   string             `json:"rpc_url"`
	Hostname string             `json:"hostname"`
	Port     uint16             `json:"port"`
	Scheme   string             `json:"scheme"`
	Error    *originCorpusError `json:"error"`
}

type originCorpusError struct {
	Code  string `json:"code"`
	Field string `json:"field"`
}

func readOriginCorpus(t testing.TB) []originCorpusRow {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/hubendpoint/origins.json")
	if err != nil {
		t.Fatalf("read origin corpus: %v", err)
	}
	var rows []originCorpusRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode origin corpus: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("origin corpus is empty")
	}
	return rows
}

func TestOriginCorpus(t *testing.T) {
	t.Parallel()

	for _, row := range readOriginCorpus(t) {
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseOrigin(row.Raw)
			if row.Error != nil {
				if err == nil {
					t.Fatalf("ParseOrigin() accepted invalid origin as %q", got.String())
				}
				var validationErr *ValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("ParseOrigin() error type = %T, want *ValidationError", err)
				}
				if validationErr.Code != row.Error.Code || validationErr.Field != row.Error.Field {
					t.Fatalf("ParseOrigin() validation = {%q, %q}, want {%q, %q}", validationErr.Code, validationErr.Field, row.Error.Code, row.Error.Field)
				}
				if strings.Contains(err.Error(), "secret-value") {
					t.Fatal("ParseOrigin() error retained secret-bearing input")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOrigin() error = %v", err)
			}
			if got.String() != row.Origin {
				t.Errorf("String() = %q, want %q", got.String(), row.Origin)
			}
			if got.RPCURL() != row.RPCURL {
				t.Errorf("RPCURL() = %q, want %q", got.RPCURL(), row.RPCURL)
			}
			if got.Hostname() != row.Hostname {
				t.Errorf("Hostname() = %q, want %q", got.Hostname(), row.Hostname)
			}
			if got.Port() != row.Port {
				t.Errorf("Port() = %d, want %d", got.Port(), row.Port)
			}
			if got.Scheme() != row.Scheme {
				t.Errorf("Scheme() = %q, want %q", got.Scheme(), row.Scheme)
			}
		})
	}
}

func TestLocalOrigin(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name      string
		boundAddr string
		origin    string
		hostname  string
		port      uint16
	}{
		{name: "IPv4 wildcard", boundAddr: "0.0.0.0:9180", origin: "http://127.0.0.1:9180", hostname: "127.0.0.1", port: 9180},
		{name: "IPv6 wildcard", boundAddr: "[::]:9180", origin: "http://[::1]:9180", hostname: "::1", port: 9180},
		{name: "IPv4-mapped wildcard", boundAddr: "[::ffff:0.0.0.0]:9180", origin: "http://127.0.0.1:9180", hostname: "127.0.0.1", port: 9180},
		{name: "specific IPv4", boundAddr: "192.0.2.10:8080", origin: "http://192.0.2.10:8080", hostname: "192.0.2.10", port: 8080},
		{name: "specific IPv6", boundAddr: "[2001:db8::1]:443", origin: "http://[2001:db8::1]:443", hostname: "2001:db8::1", port: 443},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := LocalOrigin(tc.boundAddr)
			if err != nil {
				t.Fatalf("LocalOrigin() error = %v", err)
			}
			if got.String() != tc.origin || got.Hostname() != tc.hostname || got.Port() != tc.port || got.Scheme() != "http" {
				t.Fatalf("LocalOrigin() = {%q, %q, %d, %q}, want {%q, %q, %d, %q}", got.String(), got.Hostname(), got.Port(), got.Scheme(), tc.origin, tc.hostname, tc.port, "http")
			}
		})
	}

	rejected := []struct {
		name      string
		boundAddr string
		code      string
		field     string
	}{
		{name: "unresolved IPv4 port zero", boundAddr: "0.0.0.0:0", code: "port", field: "port"},
		{name: "unresolved IPv6 port zero", boundAddr: "[::]:0", code: "port", field: "port"},
		{name: "DNS is not an actual bound interface", boundAddr: "hub.example.test:9180", code: "host", field: "host"},
		{name: "scoped IPv6 cannot form an origin", boundAddr: "[fe80::1%eth0]:9180", code: "scoped_host", field: "host"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := LocalOrigin(tc.boundAddr)
			if err == nil {
				t.Fatal("LocalOrigin() accepted unusable bound address")
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("LocalOrigin() error type = %T, want *ValidationError", err)
			}
			if validationErr.Code != tc.code || validationErr.Field != tc.field {
				t.Fatalf("LocalOrigin() validation = {%q, %q}, want {%q, %q}", validationErr.Code, validationErr.Field, tc.code, tc.field)
			}
		})
	}
}

// Registry: native:.:./internal/hubendpoint:FuzzParseOrigin
func FuzzParseOrigin(f *testing.F) {
	for _, row := range readOriginCorpus(f) {
		if row.Error != nil {
			f.Add(row.Raw)
		}
	}
	f.Fuzz(func(t *testing.T, raw string) {
		origin, err := ParseOrigin(raw)
		if err != nil {
			if _, ok := errors.AsType[*ValidationError](err); !ok {
				t.Fatalf("ParseOrigin(%q) error type = %T, want *ValidationError", raw, err)
			}
			return
		}
		reparsed, err := ParseOrigin(origin.String())
		if err != nil {
			t.Fatalf("ParseOrigin(String()) error = %v", err)
		}
		if reparsed.String() != origin.String() || reparsed.RPCURL() != origin.RPCURL() || reparsed.Hostname() != origin.Hostname() || reparsed.Port() != origin.Port() || reparsed.Scheme() != origin.Scheme() {
			t.Fatalf("ParseOrigin() is not stable: first = {%q, %q, %q, %d, %q}, second = {%q, %q, %q, %d, %q}", origin.String(), origin.RPCURL(), origin.Hostname(), origin.Port(), origin.Scheme(), reparsed.String(), reparsed.RPCURL(), reparsed.Hostname(), reparsed.Port(), reparsed.Scheme())
		}
	})
}

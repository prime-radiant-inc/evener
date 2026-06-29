package hubstart

import (
	"strings"
	"testing"
)

// FuzzParseStartup drives hubstart's two real parsers: ParseTUIStartupOptions
// (the serf-tui flag parser, with a stubbed getenv so it is env-independent) and
// NormalizeHubAddress (the hub URL parser). The flag selector bit picks which.
// Oracle: no-panic floor plus, for NormalizeHubAddress, an idempotence
// invariant — re-normalizing a successfully-normalized BaseURL is a fixed point.
func FuzzParseStartup(f *testing.F) {
	seeds := []struct {
		which int
		s     string
	}{
		{0, "--hub-addr=127.0.0.1:9180 --debug"},
		{0, "--no-auto-start-hub --state-dir=/tmp/s"},
		{0, "--auth-token tok --log-file=/tmp/l --hub-bin=/x/serf-hub"},
		{0, "--unknown-flag"},
		{0, "--hub-addr"},
		{0, ""},
		{1, "127.0.0.1:9180"},
		{1, "http://localhost:9180/rpc/"},
		{1, "https://hub.example.com"},
		{1, "ftp://bad"},
		{1, "://nohost"},
		{1, ""},
		{1, "http://[::1]:9180"},
	}
	for _, s := range seeds {
		f.Add(s.which, s.s)
	}

	getenv := func(string) string { return "" }

	f.Fuzz(func(t *testing.T, which int, raw string) {
		if which&1 == 0 {
			args := strings.Split(raw, " ")
			// flag parsing must never panic, only return an error.
			_, _ = ParseTUIStartupOptions(args, getenv)
			return
		}

		addr, err := NormalizeHubAddress(raw)
		if err != nil {
			return
		}
		// A normalized address must re-normalize to itself.
		again, err2 := NormalizeHubAddress(addr.BaseURL)
		if err2 != nil {
			t.Fatalf("re-normalize of %q (from %q) failed: %v", addr.BaseURL, raw, err2)
		}
		if again.BaseURL != addr.BaseURL {
			t.Fatalf("NormalizeHubAddress not idempotent:\n in=%q\n once=%q\n twice=%q", raw, addr.BaseURL, again.BaseURL)
		}
	})
}

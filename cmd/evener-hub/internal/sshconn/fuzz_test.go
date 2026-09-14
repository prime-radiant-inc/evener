package sshconn

import (
	"errors"
	"strings"
	"testing"
)

// FuzzParseLaunchCheck drives the decode of a host's `evener launch-check
// --json` response. Those bytes come from a remote host and are untrusted, and
// parseLaunchCheck is the package's only json decode surface (preflight.go).
// Two contract properties hold for every input: a failure is always the named
// ErrPreflightDecode class, and an accepted response always carries the
// non-empty protocol the function promises (it must never hand an
// unlaunchable host through).
func FuzzParseLaunchCheck(f *testing.F) {
	for _, seed := range []string{
		`{"protocol":"evener-appwire-v5","version":"1.2.3","launch_flags":["api-log"]}`,
		`{"protocol":"evener-appwire-v5","launch_flags":["api-log","api-log"]}`,
		`{"protocol":"","version":"1.2.3","launch_flags":[]}`,
		`{}`,
		`null`,
		`[]`,
		`"a string"`,
		`{"protocol":42}`,
		`{`,
		``,
		"\xff\xfe",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		lc, err := parseLaunchCheck(raw)
		if err != nil {
			if !errors.Is(err, ErrPreflightDecode) {
				t.Fatalf("parseLaunchCheck(%q) error %v is not ErrPreflightDecode", raw, err)
			}
			return
		}
		if strings.TrimSpace(lc.Protocol) == "" {
			t.Fatalf("parseLaunchCheck(%q) accepted a protocol-less response: %+v", raw, lc)
		}
	})
}

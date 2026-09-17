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
		`{"protocol":"evener-appwire-v6","version":"1.2.3","launch_flags":["api-log"]}`,
		`{"protocol":"evener-appwire-v6","launch_flags":["api-log","api-log"]}`,
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

// FuzzParseEnvProbe drives the decode of the host environment probe, the other
// untrusted remote-host surface in preflight.go. One invariant holds for every
// input: a rejection is always the named ErrPreflightDecode class, and an
// accepted map always answers HOME, which is what resolveRoots depends on.
func FuzzParseEnvProbe(f *testing.F) {
	for _, seed := range []string{
		"HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=/xdg/config\n",
		"HOME=\n",
		"HOME=/home/dev",
		"HOME /home/dev\n",
		"=value\n",
		"HOME=a\x00b\n",
		"\n\n\n",
		"",
		"\xff\xfe",
		"HOME=/home/dev\nHOME=/other\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		env, err := parseEnvProbe(raw)
		if err != nil {
			if !errors.Is(err, ErrPreflightDecode) {
				t.Fatalf("parseEnvProbe(%q) error %v is not ErrPreflightDecode", raw, err)
			}
			return
		}
		if _, ok := env["HOME"]; !ok {
			t.Fatalf("parseEnvProbe(%q) accepted a probe without HOME: %v", raw, env)
		}
	})
}

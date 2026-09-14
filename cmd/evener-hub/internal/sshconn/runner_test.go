package sshconn

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestComposeDest(t *testing.T) {
	cases := []struct {
		name string
		host hostreg.Host
		want string
	}{
		{"no user", hostreg.Host{SSH: "alpha.example"}, "alpha.example"},
		{"user composes", hostreg.Host{SSH: "alpha.example", User: "bob"}, "bob@alpha.example"},
		{"ssh already has user", hostreg.Host{SSH: "bob@alpha.example"}, "bob@alpha.example"},
		{"no double at", hostreg.Host{SSH: "bob@alpha.example", User: "alice"}, "bob@alpha.example"},
		{"trimmed", hostreg.Host{SSH: "  alpha.example  ", User: "bob"}, "bob@alpha.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeDest(tc.host); got != tc.want {
				t.Fatalf("composeDest = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvenerCommand(t *testing.T) {
	cases := map[string]string{
		"":                       "evener",
		"   ":                    "evener",
		"evener":                 "evener",
		" /usr/local/bin/evener": "/usr/local/bin/evener",
		"/opt/evener/bin/evener": "/opt/evener/bin/evener",
	}
	for in, want := range cases {
		if got := evenerCommand(in); got != want {
			t.Fatalf("evenerCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChannelArgv(t *testing.T) {
	opts := Options{
		ConnectTimeout:      10 * time.Second,
		ServerAliveInterval: 15 * time.Second,
		ServerAliveCountMax: 3,
	}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", User: "bob", EvenerPath: "/opt/evener/bin/evener"}
	got := channelArgv(opts, host)
	want := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"bob@alpha.example",
		"/opt/evener/bin/evener",
		"hub", "attach", "--stdio",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelArgv:\n got %v\nwant %v", got, want)
	}
}

func TestChannelArgvDefaultsAndEmptyPath(t *testing.T) {
	got := channelArgv(Options{}, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	want := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"alpha.example",
		"evener",
		"hub", "attach", "--stdio",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelArgv defaults:\n got %v\nwant %v", got, want)
	}
}

func TestRawAndEvenerCommandArgv(t *testing.T) {
	opts := Options{ConnectTimeout: 7 * time.Second, ServerAliveInterval: 9 * time.Second, ServerAliveCountMax: 2}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	raw := rawCommandArgv(opts, host, "uname -s")
	if raw[0] != "ssh" || raw[len(raw)-2] != "alpha.example" || raw[len(raw)-1] != "uname -s" {
		t.Fatalf("rawCommandArgv = %v", raw)
	}
	lc := evenerCommandArgv(opts, host, "launch-check", "--protocol", appwire.ProtocolVersion, "--json")
	if lc[len(lc)-5] != "evener" || lc[len(lc)-4] != "launch-check" {
		t.Fatalf("evenerCommandArgv = %v", lc)
	}
	if lc[len(lc)-3] != "--protocol" || lc[len(lc)-2] != appwire.ProtocolVersion {
		t.Fatalf("protocol argv tail = %v", lc)
	}
}

func TestSSHSecondsClampsToMinimumOne(t *testing.T) {
	if got := sshSeconds(0); got != "1" {
		t.Fatalf("sshSeconds(0) = %q, want 1", got)
	}
	if got := sshSeconds(1500 * time.Millisecond); got != "1" {
		t.Fatalf("sshSeconds(1.5s) = %q, want 1", got)
	}
	if got := sshSeconds(10 * time.Second); got != "10" {
		t.Fatalf("sshSeconds(10s) = %q, want 10", got)
	}
}

func TestAppendTailKeepsTrailingBytes(t *testing.T) {
	buf := appendTail(nil, []byte("abcdef"), 4)
	if string(buf) != "cdef" {
		t.Fatalf("appendTail = %q, want cdef", buf)
	}
	buf = appendTail(buf, []byte("ghijkl"), 4)
	if string(buf) != "ijkl" {
		t.Fatalf("appendTail second = %q, want ijkl", buf)
	}
}

func TestDiagSinkForwardsAndTails(t *testing.T) {
	var sb strings.Builder
	sink := newDiagSink(&sb)
	if _, err := sink.Write([]byte("ssh: connect failed\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(sb.String(), "connect failed") {
		t.Fatalf("sink did not forward to writer: %q", sb.String())
	}
	if !strings.Contains(sink.tail(), "connect failed") {
		t.Fatalf("tail = %q", sink.tail())
	}
}
